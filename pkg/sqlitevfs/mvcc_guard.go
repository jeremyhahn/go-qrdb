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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// RestoreLockTTL is the lease of the writer lock a guarded restore takes on
// each database it rewrites.
const RestoreLockTTL = 30 * time.Second

// LockResolver returns the lock service a guarded restore takes each
// rewritten database's writer lock through.
type LockResolver func() (LockServiceLocker, error)

// GuardedMVCC is an MVCC service over a store that holds SQLite databases.
// Its single-key writes refuse SQLite page and meta keys (RefuseKey): only a
// Store, through the transaction service or a prefix move, writes a
// database. Its RestoreToRevision restores each SQLite database the plan
// touches as one image through a Store, under the database's writer lock,
// with a generation bump, so an open connection never reads a half-restored
// database; every other key goes through the wrapped service.
type GuardedMVCC struct {
	service.MVCCKVServicer
	kv    ServiceKV
	txn   ServiceTxn
	locks LockResolver
}

// NewGuardedMVCC returns mvcc guarded for SQLite databases. kv and txn are
// the services a restore reads and rewrites the databases through (nil
// refuses a restore that touches a SQLite database); locks resolves the
// writer-lock service.
func NewGuardedMVCC(mvcc service.MVCCKVServicer, kv ServiceKV, txn ServiceTxn, locks LockResolver) *GuardedMVCC {
	return &GuardedMVCC{MVCCKVServicer: mvcc, kv: kv, txn: txn, locks: locks}
}

// GuardMVCC guards a composer's MVCC service for SQLite databases (see
// GuardedMVCC), reading and rewriting databases through mvcc itself and txn.
// A nil or already guarded mvcc is returned as is.
func GuardMVCC(mvcc service.MVCCKVServicer, txn service.TransactionServicer, locks LockResolver) service.MVCCKVServicer {
	if mvcc == nil {
		return nil
	}
	if _, ok := mvcc.(*GuardedMVCC); ok {
		return mvcc
	}
	kv, _ := mvcc.(ServiceKV)
	stxn, _ := txn.(ServiceTxn)
	return NewGuardedMVCC(mvcc, kv, stxn, locks)
}

// RestoreToRevision restores through Restore, rewriting databases through
// the guard's own services.
func (g *GuardedMVCC) RestoreToRevision(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	return g.Restore(ctx, req, g.kv, g.txn)
}

// Restore is RestoreToRevision with the services the rewritten Stores commit
// through: a caller that admits SQLite writes passes its gated ones.
func (g *GuardedMVCC) Restore(ctx context.Context, req *service.RestoreRevisionRequest, commitKV ServiceKV, commitTxn ServiceTxn) (*service.RestoreRevisionResult, error) {
	if req == nil {
		return g.MVCCKVServicer.RestoreToRevision(ctx, req)
	}
	if req.DryRun {
		plan, err := g.MVCCKVServicer.RestoreToRevision(ctx, req)
		if err != nil || plan == nil {
			return plan, err
		}
		dbs := restoredDatabases(plan.Changes)
		if len(dbs) == 0 {
			return plan, nil
		}
		changes, err := g.imageChanges(ctx, plan.Changes, dbs, req.Revision)
		if err != nil {
			return nil, err
		}
		priced := *plan
		priced.Changes = changes
		return &priced, nil
	}
	if g.kv == nil || g.txn == nil || commitKV == nil || commitTxn == nil {
		return g.restoreWithoutDatabases(ctx, req)
	}
	observed, err := g.kv.CurrentRevision(ctx)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs restore: current revision: %w", err)
	}
	planReq := *req
	planReq.DryRun = true
	plan, err := g.MVCCKVServicer.RestoreToRevision(ctx, &planReq)
	if err != nil || plan == nil {
		return plan, err
	}
	dbs := restoredDatabases(plan.Changes)
	if len(dbs) == 0 {
		return g.MVCCKVServicer.RestoreToRevision(ctx, req)
	}
	changes, err := g.imageChanges(ctx, plan.Changes, dbs, req.Revision)
	if err != nil {
		return nil, err
	}
	result := &service.RestoreRevisionResult{
		Revision: plan.Revision,
		Changes:  changes,
		Skipped:  plan.Skipped,
	}
	var otherKeys []string
	for _, c := range plan.Changes {
		if _, ok := DatabaseOfKey([]byte(c.Key)); !ok {
			otherKeys = append(otherKeys, c.Key)
		}
	}
	if len(otherKeys) > 0 {
		keysReq := *req
		keysReq.Prefix = ""
		keysReq.Keys = otherKeys
		other, err := g.MVCCKVServicer.RestoreToRevision(ctx, &keysReq)
		if err != nil {
			return nil, err
		}
		if other != nil {
			result.Applied += other.Applied
			result.Conflicts = append(result.Conflicts, other.Conflicts...)
		}
	}
	backend, err := NewServiceBackend(commitKV, commitTxn)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs restore: %w", err)
	}
	if g.locks == nil {
		return nil, errors.New("sqlitevfs restore: no lock service for the writer lock")
	}
	lockSvc, err := g.locks()
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs restore: lock service: %w", err)
	}
	holder, err := restoreHolder()
	if err != nil {
		return nil, err
	}
	writer, err := NewLockServiceWriter(lockSvc, holder, RestoreLockTTL)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs restore: writer lock: %w", err)
	}
	conflicted := make(map[string]bool)
	for _, db := range dbs {
		store, err := NewStore(backend, db, WithWriterLock(writer))
		switch {
		case err != nil:
		case req.Force:
			_, err = store.RestoreToRevision(ctx, req.Revision)
		default:
			_, err = store.RestoreToRevisionFrom(ctx, req.Revision, observed)
		}
		if err == nil {
			continue
		}
		if !req.Force && (errors.Is(err, ErrSnapshotChanged) || errors.Is(err, ErrBusy)) {
			conflicted[db] = true
			continue
		}
		return nil, fmt.Errorf("sqlitevfs restore: restore database %q to revision %d: %w", db, req.Revision, err)
	}
	for _, c := range changes {
		db, ok := DatabaseOfKey([]byte(c.Key))
		switch {
		case !ok:
		case conflicted[db]:
			result.Conflicts = append(result.Conflicts, c)
		default:
			result.Applied++
		}
	}
	result.Conflicted = len(result.Conflicts)
	return result, nil
}

// restoreWithoutDatabases restores through the wrapped service when the
// guard has no services to rewrite a database through, refusing a plan
// that touches one: restoring its pages key by key would let an open
// connection read a half-restored database.
func (g *GuardedMVCC) restoreWithoutDatabases(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	planReq := *req
	planReq.DryRun = true
	plan, err := g.MVCCKVServicer.RestoreToRevision(ctx, &planReq)
	if err != nil || plan == nil {
		return plan, err
	}
	if dbs := restoredDatabases(plan.Changes); len(dbs) > 0 {
		return nil, fmt.Errorf("sqlitevfs restore: database %q: %w", dbs[0], ErrRestoreUnsupported)
	}
	return g.MVCCKVServicer.RestoreToRevision(ctx, req)
}

// ErrRestoreUnsupported refuses a restore that touches a SQLite database on
// an MVCC service with no transaction service to rewrite it through.
var ErrRestoreUnsupported = errors.New("restoring a SQLite database needs a transaction service")

// restoreHolder returns a fresh writer-lock holder id for one restore.
func restoreHolder() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("sqlitevfs restore: holder id: %w", err)
	}
	return "sqlite-restore-" + hex.EncodeToString(raw[:]), nil
}

// restoredDatabases returns, in first-seen order, each SQLite database a
// restore plan touches.
func restoredDatabases(changes []service.RestoreKeyChange) []string {
	var dbs []string
	seen := make(map[string]struct{})
	for _, c := range changes {
		db, ok := DatabaseOfKey([]byte(c.Key))
		if !ok {
			continue
		}
		if _, dup := seen[db]; dup {
			continue
		}
		seen[db] = struct{}{}
		dbs = append(dbs, db)
	}
	return dbs
}

// imageChanges replaces the plan's per-key changes of each database in dbs
// with that database's image at rev: a database is restored whole.
func (g *GuardedMVCC) imageChanges(ctx context.Context, changes []service.RestoreKeyChange, dbs []string, rev int64) ([]service.RestoreKeyChange, error) {
	if g.kv == nil || g.txn == nil {
		return nil, fmt.Errorf("sqlitevfs restore: database %q: %w", dbs[0], ErrRestoreUnsupported)
	}
	backend, err := NewServiceBackend(g.kv, g.txn)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs restore: %w", err)
	}
	out := make([]service.RestoreKeyChange, 0, len(changes))
	for _, c := range changes {
		if _, ok := DatabaseOfKey([]byte(c.Key)); !ok {
			out = append(out, c)
		}
	}
	for _, db := range dbs {
		store, err := NewStore(backend, db)
		if err != nil {
			return nil, fmt.Errorf("sqlitevfs restore: database %q: %w", db, err)
		}
		image, err := store.ImageAt(ctx, rev)
		if err != nil {
			return nil, fmt.Errorf("sqlitevfs restore: image of %q at revision %d: %w", db, rev, err)
		}
		for _, k := range image {
			out = append(out, service.RestoreKeyChange{Key: k.Key, Action: service.RestoreActionRevert, ToRevision: rev, ValueBytes: k.Bytes})
		}
	}
	return out, nil
}

// MovePrefix forwards to the wrapped service's prefix move: a move is how a
// staged Store publishes, so it is not refused here.
func (g *GuardedMVCC) MovePrefix(ctx context.Context, req *service.MovePrefixRequest) (int64, error) {
	mover, ok := g.MVCCKVServicer.(service.PrefixMover)
	if !ok {
		return 0, &service.PrefixMoveUnsupportedError{}
	}
	return mover.MovePrefix(ctx, req)
}

// KeepsRevisionHistory forwards to the wrapped service.
func (g *GuardedMVCC) KeepsRevisionHistory() bool {
	r, ok := g.MVCCKVServicer.(service.RevisionHistoryReporter)
	return !ok || r.KeepsRevisionHistory()
}

// CompactionPolicy forwards to the wrapped service.
func (g *GuardedMVCC) CompactionPolicy(ctx context.Context) (*compaction.Policy, error) {
	h, ok := g.MVCCKVServicer.(service.CompactionPolicyHolder)
	if !ok {
		return nil, nil
	}
	return h.CompactionPolicy(ctx)
}

// SetCompactionPolicy forwards to the wrapped service.
func (g *GuardedMVCC) SetCompactionPolicy(ctx context.Context, policy *compaction.Policy) error {
	h, ok := g.MVCCKVServicer.(service.CompactionPolicyHolder)
	if !ok {
		return &service.CompactionPolicyUnavailableError{}
	}
	return h.SetCompactionPolicy(ctx, policy)
}

func (g *GuardedMVCC) Put(ctx context.Context, key string, value []byte) error {
	if err := RefuseKey([]byte(key)); err != nil {
		return err
	}
	return g.MVCCKVServicer.Put(ctx, key, value)
}

func (g *GuardedMVCC) PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error {
	if err := RefuseKey([]byte(key)); err != nil {
		return err
	}
	return g.MVCCKVServicer.PutWithMode(ctx, key, value, mode)
}

func (g *GuardedMVCC) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if err := RefuseKey([]byte(key)); err != nil {
		return false, err
	}
	return g.MVCCKVServicer.PutIfAbsent(ctx, key, value)
}

func (g *GuardedMVCC) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if err := RefuseKey([]byte(key)); err != nil {
		return false, err
	}
	return g.MVCCKVServicer.CompareAndSwap(ctx, key, expected, next)
}

func (g *GuardedMVCC) Delete(ctx context.Context, key string) error {
	if err := RefuseKey([]byte(key)); err != nil {
		return err
	}
	return g.MVCCKVServicer.Delete(ctx, key)
}

func (g *GuardedMVCC) DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error {
	if err := RefuseKey([]byte(key)); err != nil {
		return err
	}
	return g.MVCCKVServicer.DeleteWithMode(ctx, key, mode)
}

func (g *GuardedMVCC) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if err := RefuseKey([]byte(key)); err != nil {
		return false, err
	}
	return g.MVCCKVServicer.CompareAndDelete(ctx, key, expected)
}

func (g *GuardedMVCC) PutWithLease(ctx context.Context, key string, value []byte, leaseID int64) (*service.KeyValue, error) {
	if err := RefuseKey([]byte(key)); err != nil {
		return nil, err
	}
	return g.MVCCKVServicer.PutWithLease(ctx, key, value, leaseID)
}

func (g *GuardedMVCC) PutWithLeaseAndMode(ctx context.Context, key string, value []byte, leaseID int64, mode writemode.Mode) (*service.KeyValue, error) {
	if err := RefuseKey([]byte(key)); err != nil {
		return nil, err
	}
	return g.MVCCKVServicer.PutWithLeaseAndMode(ctx, key, value, leaseID, mode)
}
