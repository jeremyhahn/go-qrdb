// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sqlitevfs

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// defaultKeysPageSize is the number of keys ServiceBackend.Keys asks for in
// one Range call.
const defaultKeysPageSize = 1000

// ServiceKV is the part of the server's MVCC key-value service a
// ServiceBackend reads and publishes through. *service.MVCCKVService and the
// namespaced, hard-confined and quota wrappers satisfy it.
type ServiceKV interface {
	GetAtRevision(ctx context.Context, key string, revision int64) (*service.KeyValue, error)
	Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error)
	CurrentRevision(ctx context.Context) (int64, error)
	service.PrefixMover
}

// ServiceTxn is the part of the server's transaction service a
// ServiceBackend commits through. *service.TransactionService satisfies it.
type ServiceTxn interface {
	Execute(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error)
}

// The server's lock service is what NewLockServiceWriter takes.
var _ LockServiceLocker = (service.LockServicer)(nil)

// ServiceBackend is a Backend over the server's replicated services: reads
// through ServiceKV, commits as one ServiceTxn transaction, and the staged
// publish as one server-side prefix move (OpMVCCMovePrefix). The caller
// supplies the database's services already wrapped for its namespace,
// hard confinement and quota; ServiceBackend adds no key rewriting.
//
// Each call takes the caller's context (owner point o3, decided by convention
// under ruling 109).
type ServiceBackend struct {
	kv           ServiceKV
	txn          ServiceTxn
	keysPageSize int64
}

var _ Backend = (*ServiceBackend)(nil)

// NewServiceBackend returns a ServiceBackend over kv and txn. A nil kv or
// txn is ErrNilBackend.
func NewServiceBackend(kv ServiceKV, txn ServiceTxn) (*ServiceBackend, error) {
	if kv == nil || txn == nil {
		return nil, ErrNilBackend
	}
	return &ServiceBackend{kv: kv, txn: txn, keysPageSize: defaultKeysPageSize}, nil
}

// Revision returns the service's current revision.
func (b *ServiceBackend) Revision(ctx context.Context) (int64, error) {
	rev, err := b.kv.CurrentRevision(ctx)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: current revision: %w", err)
	}
	return rev, nil
}

// GetAt returns key's value at revision rev. A *service.KeyNotFoundError (a
// tombstone), a *service.RevisionNotFoundError (no version of key at or
// before rev, which is how the server answers a key never written) or a nil
// KeyValue is found=false with a nil error.
func (b *ServiceBackend) GetAt(ctx context.Context, key []byte, rev int64) ([]byte, bool, error) {
	kv, err := b.kv.GetAtRevision(ctx, string(key), rev)
	if err != nil {
		var notFound *service.KeyNotFoundError
		var noRevision *service.RevisionNotFoundError
		if errors.As(err, &notFound) || errors.As(err, &noRevision) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("sqlitevfs: read %q at revision %d: %w", key, rev, err)
	}
	if kv == nil {
		return nil, false, nil
	}
	return kv.Value, true, nil
}

// Keys returns every key live at revision rev under prefix, in key order,
// paging the Range by keysPageSize keys.
func (b *ServiceBackend) Keys(ctx context.Context, prefix []byte, rev int64) ([][]byte, error) {
	if rev <= 0 {
		return nil, nil
	}
	end := prefixEnd(prefix)
	start := prefix
	var keys [][]byte
	for {
		kvs, _, _, err := b.kv.Range(ctx, start, end,
			lock.WithRevision(rev), lock.WithKeysOnly(), lock.WithLimit(b.keysPageSize))
		if err != nil {
			return nil, fmt.Errorf("sqlitevfs: list keys at revision %d: %w", rev, err)
		}
		for _, kv := range kvs {
			keys = append(keys, kv.Key)
		}
		if int64(len(kvs)) < b.keysPageSize {
			return keys, nil
		}
		last := kvs[len(kvs)-1].Key
		start = append(append(make([]byte, 0, len(last)+1), last...), 0x00)
	}
}

// Commit applies puts and deletes as one transaction with no compares and
// returns its revision. Nothing to write returns the current revision. A
// transaction past the tenant's storage quota (a *quota.ExceededError) is
// ErrFull, joined with the cause.
func (b *ServiceBackend) Commit(ctx context.Context, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	if len(puts) == 0 && len(deletes) == 0 {
		return b.Revision(ctx)
	}
	resp, err := b.txn.Execute(ctx, readCompares(reads), writeOps(puts, deletes), nil)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: commit transaction: %w", full(err))
	}
	if resp == nil {
		return 0, errors.New("sqlitevfs: commit transaction returned no response")
	}
	if !resp.Succeeded {
		if len(reads) > 0 {
			return 0, fmt.Errorf("%w: a read check failed at revision %d", ErrSnapshotChanged, resp.Revision)
		}
		return 0, errors.New("sqlitevfs: commit transaction did not succeed")
	}
	return resp.Revision, nil
}

// readCompares returns each read check as service compares: a key read
// live at Rev must still be live (ModRevision greater than 0) with
// ModRevision less than Rev+1; a key read absent must still read
// ModRevision 0, which a key never written or no longer live does.
func readCompares(reads []ReadCheck) []service.Compare {
	if len(reads) == 0 {
		return nil
	}
	cmps := make([]service.Compare, 0, 2*len(reads))
	for _, r := range reads {
		if r.CreateRev != 0 {
			// A fence: an absent record has create revision 0, never
			// a token, so a released or lost lock refuses too.
			cmps = append(cmps, service.Compare{Key: r.Key, Target: service.CompareCreateRevision, Result: service.CompareEqual, CreateRevision: r.CreateRev})
			continue
		}
		if !r.Found {
			cmps = append(cmps, service.Compare{Key: r.Key, Target: service.CompareModRevision, Result: service.CompareEqual, ModRevision: 0})
			continue
		}
		cmps = append(cmps,
			service.Compare{Key: r.Key, Target: service.CompareModRevision, Result: service.CompareGreater, ModRevision: 0},
			service.Compare{Key: r.Key, Target: service.CompareModRevision, Result: service.CompareLess, ModRevision: r.Rev + 1},
		)
	}
	return cmps
}

// Publish sends one server-side prefix move carrying the read set as its
// compares, which the engine checks at apply, atomically with the move. A
// failed compare (a *service.WriteGuardRefusedError: a key read by the
// staged transaction changed since its snapshot) is ErrSnapshotChanged and
// writes nothing. A *service.CommandConflictError (a live source count other
// than expectedKeys at apply) is ErrPublishConflict. A move past the
// tenant's storage quota (a *quota.ExceededError) is ErrFull, joined with
// the cause.
func (b *ServiceBackend) Publish(ctx context.Context, src, dst []byte, expectedKeys int64, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	if err := validatePublish(src, dst, expectedKeys); err != nil {
		return 0, err
	}
	rev, err := b.kv.MovePrefix(ctx, &service.MovePrefixRequest{
		Src:          string(src),
		Dst:          string(dst),
		ExpectedKeys: expectedKeys,
		Ops:          writeOps(puts, deletes),
		Compares:     readCompares(reads),
	})
	if err != nil {
		var refused *service.WriteGuardRefusedError
		if errors.As(err, &refused) {
			return 0, fmt.Errorf("%w: %w", ErrSnapshotChanged, err)
		}
		var conflict *service.CommandConflictError
		if errors.As(err, &conflict) {
			return 0, fmt.Errorf("%w: %w", ErrPublishConflict, err)
		}
		return 0, fmt.Errorf("sqlitevfs: publish %q to %q: %w", src, dst, full(err))
	}
	return rev, nil
}

// full joins ErrFull with err when err carries a *quota.ExceededError (the
// tenant's storage quota refused the write), so errors.Is(err, ErrFull) and
// errors.As(err, **quota.ExceededError) both hold; any other err is returned
// unchanged.
func full(err error) error {
	var exceeded *quota.ExceededError
	if errors.As(err, &exceeded) {
		return fmt.Errorf("%w: %w", ErrFull, err)
	}
	return err
}

// writeOps returns puts as OpPut ops in key order, then deletes as OpDelete
// ops.
func writeOps(puts map[string][]byte, deletes [][]byte) []service.Op {
	keys := make([]string, 0, len(puts))
	for k := range puts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ops := make([]service.Op, 0, len(keys)+len(deletes))
	for _, k := range keys {
		ops = append(ops, service.Op{Type: lock.OpPut, Key: []byte(k), Value: puts[k]})
	}
	for _, k := range deletes {
		ops = append(ops, service.Op{Type: lock.OpDelete, Key: k})
	}
	return ops
}
