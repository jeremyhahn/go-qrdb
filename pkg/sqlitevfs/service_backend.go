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
// The context is taken at construction and used for every call, because the
// page store's ReadAt and WriteAt are io-shaped and carry no context. Whether
// Backend should carry a per-call context instead is an open point for the
// owner.
type ServiceBackend struct {
	ctx          context.Context
	kv           ServiceKV
	txn          ServiceTxn
	keysPageSize int64
}

var _ Backend = (*ServiceBackend)(nil)

// NewServiceBackend returns a ServiceBackend over kv and txn that uses ctx
// for every call. A nil kv or txn is ErrNilBackend; a nil ctx is
// context.Background().
func NewServiceBackend(ctx context.Context, kv ServiceKV, txn ServiceTxn) (*ServiceBackend, error) {
	if kv == nil || txn == nil {
		return nil, ErrNilBackend
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &ServiceBackend{ctx: ctx, kv: kv, txn: txn, keysPageSize: defaultKeysPageSize}, nil
}

// Revision returns the service's current revision.
func (b *ServiceBackend) Revision() (int64, error) {
	rev, err := b.kv.CurrentRevision(b.ctx)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: current revision: %w", err)
	}
	return rev, nil
}

// GetAt returns key's value at revision rev. A *service.KeyNotFoundError (a
// tombstone), a *service.RevisionNotFoundError (no version of key at or
// before rev, which is how the server answers a key never written) or a nil
// KeyValue is found=false with a nil error.
func (b *ServiceBackend) GetAt(key []byte, rev int64) ([]byte, bool, error) {
	kv, err := b.kv.GetAtRevision(b.ctx, string(key), rev)
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
func (b *ServiceBackend) Keys(prefix []byte, rev int64) ([][]byte, error) {
	if rev <= 0 {
		return nil, nil
	}
	end := prefixEnd(prefix)
	start := prefix
	var keys [][]byte
	for {
		kvs, _, _, err := b.kv.Range(b.ctx, start, end,
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
// returns its revision. Nothing to write returns the current revision.
func (b *ServiceBackend) Commit(puts map[string][]byte, deletes [][]byte) (int64, error) {
	if len(puts) == 0 && len(deletes) == 0 {
		return b.Revision()
	}
	resp, err := b.txn.Execute(b.ctx, nil, writeOps(puts, deletes), nil)
	if err != nil {
		return 0, fmt.Errorf("sqlitevfs: commit transaction: %w", err)
	}
	if resp == nil || !resp.Succeeded {
		return 0, errors.New("sqlitevfs: commit transaction did not succeed")
	}
	return resp.Revision, nil
}

// Publish sends one server-side prefix move. A *service.CommandConflictError
// (a live source count other than expectedKeys at apply) is
// ErrPublishConflict.
func (b *ServiceBackend) Publish(src, dst []byte, expectedKeys int64, puts map[string][]byte, deletes [][]byte) (int64, error) {
	if err := validatePublish(src, dst, expectedKeys); err != nil {
		return 0, err
	}
	rev, err := b.kv.MovePrefix(b.ctx, &service.MovePrefixRequest{
		Src:          string(src),
		Dst:          string(dst),
		ExpectedKeys: expectedKeys,
		Ops:          writeOps(puts, deletes),
	})
	if err != nil {
		var conflict *service.CommandConflictError
		if errors.As(err, &conflict) {
			return 0, fmt.Errorf("%w: %w", ErrPublishConflict, err)
		}
		return 0, fmt.Errorf("sqlitevfs: publish %q to %q: %w", src, dst, err)
	}
	return rev, nil
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
