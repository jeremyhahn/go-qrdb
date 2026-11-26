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

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// sqliteRestoreMVCC is the SQLite VFS restore hook (row #926, Q4 A, o9) over
// a scope's MVCC service. A restore to a revision is planned first (a dry
// run of the same request); the keys of the plan outside sqlite/ are then
// restored by the inner service, scoped to exactly those keys, and each
// SQLite database the plan touches is restored through its page store
// (sqlitevfs.Store.RestoreToRevision): one page-store commit that writes the
// differing pages, the size and the meta record with the generation past
// every generation it has had. The restore and the bump are one revision, so
// no transaction reads a half-restored file, and a page store that saw the
// database before is told (ErrSnapshotChanged, SQLITE_BUSY_SNAPSHOT) instead
// of trusting a page cache whose header change counter the restore may have
// put back. A database whose restore commit kept meeting a concurrent writer
// is reported in Conflicts (all of its plan entries), as a key the CAS
// restore found moved; with Force its error is returned. A dry run is the
// inner service's.
type sqliteRestoreMVCC struct {
	service.MVCCKVServicer
	kv  sqlitevfs.ServiceKV
	txn sqlitevfs.ServiceTxn
	// gate, when set, admits the database a MovePrefix creates (o1, see
	// withSQLiteAdmission).
	gate *sqliteAdmitGate
	// locks resolves the lock service each rewritten Store takes the
	// database's writer lock through.
	locks sqliteLockResolver
}

var (
	_ service.MVCCKVServicer          = (*sqliteRestoreMVCC)(nil)
	_ service.PrefixMover             = (*sqliteRestoreMVCC)(nil)
	_ service.RevisionHistoryReporter = (*sqliteRestoreMVCC)(nil)
	_ service.CompactionPolicyHolder  = (*sqliteRestoreMVCC)(nil)
)

// withSQLiteRestoreHook returns svcs with its MVCC service wrapped in the
// SQLite restore hook. svcs is returned as it is when it has no MVCC
// service or its services cannot back a page store. locks resolves the
// database's lock service: each Store the restore rewrites takes the
// database's writer lock, so a database a writer holds is a conflict (or,
// forced, an ErrBusy error) and is left as it is.
func withSQLiteRestoreHook(svcs *DatabaseServices, locks sqliteLockResolver) *DatabaseServices {
	if svcs == nil || svcs.MVCC == nil {
		return svcs
	}
	if _, wrapped := svcs.MVCC.(*sqliteRestoreMVCC); wrapped {
		return svcs
	}
	kv, ok := svcs.MVCC.(sqlitevfs.ServiceKV)
	if !ok {
		return svcs
	}
	txn, ok := svcs.Transaction.(sqlitevfs.ServiceTxn)
	if !ok {
		return svcs
	}
	out := *svcs
	out.MVCC = &sqliteRestoreMVCC{MVCCKVServicer: svcs.MVCC, kv: kv, txn: txn, locks: locks}
	return &out
}

// sqliteLockResolver returns the lock service of the database whose SQLite
// stores a restore rewrites: the (tenant, database) scope `qrdb sqlite`
// takes its writer lock in.
type sqliteLockResolver func() (sqlitevfs.LockServiceLocker, error)

// sqliteRestoreLockTTL is the lease of the writer lock the restore hook
// holds on each database it rewrites; the Store renews it as it writes.
const sqliteRestoreLockTTL = 30 * time.Second

// sqliteRestoreHolder returns a fresh writer-lock holder id for one restore.
func sqliteRestoreHolder() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("sqlitevfs restore hook: holder id: %w", err)
	}
	return "sqlite-restore-" + hex.EncodeToString(raw[:]), nil
}

// RestoreToRevision runs the restore, then bumps the generation of each
// SQLite database whose keys it changed. A bump failure is returned wrapped
// together with the restore's result: the restore itself has been applied.
func (s *sqliteRestoreMVCC) RestoreToRevision(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	if req == nil {
		return s.MVCCKVServicer.RestoreToRevision(ctx, req)
	}
	if req.DryRun {
		plan, err := s.MVCCKVServicer.RestoreToRevision(ctx, req)
		if err != nil || plan == nil {
			return plan, err
		}
		dbs := restoredDatabases(plan.Changes)
		if len(dbs) == 0 {
			return plan, nil
		}
		changes, err := s.imageChanges(ctx, plan.Changes, dbs, req.Revision)
		if err != nil {
			return nil, err
		}
		priced := *plan
		priced.Changes = changes
		return &priced, nil
	}
	observed, err := s.kv.CurrentRevision(ctx)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs restore hook: current revision: %w", err)
	}
	planReq := *req
	planReq.DryRun = true
	plan, err := s.MVCCKVServicer.RestoreToRevision(ctx, &planReq)
	if err != nil || plan == nil {
		return plan, err
	}
	dbs := restoredDatabases(plan.Changes)
	if len(dbs) == 0 {
		return s.MVCCKVServicer.RestoreToRevision(ctx, req)
	}
	changes, err := s.imageChanges(ctx, plan.Changes, dbs, req.Revision)
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
		if _, ok := sqlitevfs.DatabaseOfKey([]byte(c.Key)); !ok {
			otherKeys = append(otherKeys, c.Key)
		}
	}
	if len(otherKeys) > 0 {
		keysReq := *req
		keysReq.Prefix = ""
		keysReq.Keys = otherKeys
		other, err := s.MVCCKVServicer.RestoreToRevision(ctx, &keysReq)
		if err != nil {
			return nil, err
		}
		if other != nil {
			result.Applied += other.Applied
			result.Conflicts = append(result.Conflicts, other.Conflicts...)
		}
	}
	kv := s.kv
	txn := s.txn
	if s.gate != nil {
		kv = sqliteGatedKV{ServiceKV: s.kv, mover: s}
		txn = sqliteGatedServiceTxn{inner: s.txn, gate: s.gate}
	}
	backend, err := sqlitevfs.NewServiceBackend(kv, txn)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs restore hook: %w", err)
	}
	if s.locks == nil {
		return nil, errors.New("sqlitevfs restore hook: no lock service for the writer lock")
	}
	lockSvc, err := s.locks()
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs restore hook: lock service: %w", err)
	}
	holder, err := sqliteRestoreHolder()
	if err != nil {
		return nil, err
	}
	writer, err := sqlitevfs.NewLockServiceWriter(lockSvc, holder, sqliteRestoreLockTTL)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs restore hook: writer lock: %w", err)
	}
	conflicted := make(map[string]bool)
	for _, db := range dbs {
		store, err := sqlitevfs.NewStore(backend, db, sqlitevfs.WithWriterLock(writer))
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
		if !req.Force && (errors.Is(err, sqlitevfs.ErrSnapshotChanged) || errors.Is(err, sqlitevfs.ErrBusy)) {
			conflicted[db] = true
			continue
		}
		return nil, fmt.Errorf("sqlitevfs restore hook: restore database %q to revision %d: %w", db, req.Revision, err)
	}
	for _, c := range changes {
		db, ok := sqlitevfs.DatabaseOfKey([]byte(c.Key))
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

// restoredDatabases returns, once each and in first-seen order, the SQLite
// databases whose keys a restore changed.
func restoredDatabases(changes []service.RestoreKeyChange) []string {
	var dbs []string
	seen := make(map[string]struct{})
	for _, c := range changes {
		db, ok := sqlitevfs.DatabaseOfKey([]byte(c.Key))
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

// imageChanges returns changes with every SQLite key replaced by the
// complete image of its database at rev: the metadata record and every
// page, each priced at the bytes the restore may write. The revision diff
// names only the keys that differ (restoring sqlite/<db>/meta alone is one
// small change), but the Store rewrites the pages the restored metadata
// points at, so a quota check pricing the diff would admit a restore far
// past MaxStorageBytes. Non-SQLite changes are kept as they are.
func (s *sqliteRestoreMVCC) imageChanges(ctx context.Context, changes []service.RestoreKeyChange, dbs []string, rev int64) ([]service.RestoreKeyChange, error) {
	backend, err := sqlitevfs.NewServiceBackend(s.kv, s.txn)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs restore hook: %w", err)
	}
	out := make([]service.RestoreKeyChange, 0, len(changes))
	for _, c := range changes {
		if _, ok := sqlitevfs.DatabaseOfKey([]byte(c.Key)); !ok {
			out = append(out, c)
		}
	}
	for _, db := range dbs {
		store, err := sqlitevfs.NewStore(backend, db)
		if err != nil {
			return nil, fmt.Errorf("sqlitevfs restore hook: database %q: %w", db, err)
		}
		image, err := store.ImageAt(ctx, rev)
		if err != nil {
			return nil, fmt.Errorf("sqlitevfs restore hook: image of %q at revision %d: %w", db, rev, err)
		}
		for _, k := range image {
			out = append(out, service.RestoreKeyChange{Key: k.Key, Action: service.RestoreActionRevert, ToRevision: rev, ValueBytes: k.Bytes})
		}
	}
	return out, nil
}

// MovePrefix forwards to the wrapped service.
func (s *sqliteRestoreMVCC) MovePrefix(ctx context.Context, req *service.MovePrefixRequest) (int64, error) {
	mover, ok := s.MVCCKVServicer.(service.PrefixMover)
	if !ok {
		return 0, &service.PrefixMoveUnsupportedError{}
	}
	if s.gate == nil || req == nil {
		return mover.MovePrefix(ctx, req)
	}
	moved, err := s.movedMeta(ctx, req)
	if err != nil {
		return 0, err
	}
	live, err := s.gate.admit(ctx, moved, req.Ops)
	if err != nil {
		return 0, err
	}
	rev, err := mover.MovePrefix(ctx, req)
	if err == nil {
		s.gate.record(ctx, live, append(moved, req.Ops...))
	}
	return rev, err
}

// movedMeta returns the meta-record writes the move itself makes: a delete
// of each SQLite meta record under req.Src and a put of each source key that
// lands on a SQLite meta record under req.Dst.
func (s *sqliteRestoreMVCC) movedMeta(ctx context.Context, req *service.MovePrefixRequest) ([]service.Op, error) {
	if req.Src == "" || req.ExpectedKeys < 1 {
		return nil, nil
	}
	src := []byte(req.Src)
	kvs, _, _, err := s.kv.Range(ctx, src, storage.PrefixEnd(src), service.WithKeysOnly(), service.WithLimit(req.ExpectedKeys+1))
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs move gate: list %q: %w", req.Src, err)
	}
	var ops []service.Op
	for _, kv := range kvs {
		if _, ok := metaDatabase(kv.Key); ok {
			ops = append(ops, service.Op{Type: service.OpDelete, Key: kv.Key})
		}
		mapped := append([]byte(req.Dst), kv.Key[len(src):]...)
		if _, ok := metaDatabase(mapped); ok {
			ops = append(ops, service.Op{Type: service.OpPut, Key: mapped})
		}
	}
	return ops, nil
}

// KeepsRevisionHistory forwards to the wrapped service.
func (s *sqliteRestoreMVCC) KeepsRevisionHistory() bool {
	r, ok := s.MVCCKVServicer.(service.RevisionHistoryReporter)
	return !ok || r.KeepsRevisionHistory()
}

// CompactionPolicy forwards to the wrapped service.
func (s *sqliteRestoreMVCC) CompactionPolicy(ctx context.Context) (*compaction.Policy, error) {
	h, ok := s.MVCCKVServicer.(service.CompactionPolicyHolder)
	if !ok {
		return nil, nil
	}
	return h.CompactionPolicy(ctx)
}

// SetCompactionPolicy forwards to the wrapped service.
func (s *sqliteRestoreMVCC) SetCompactionPolicy(ctx context.Context, policy *compaction.Policy) error {
	h, ok := s.MVCCKVServicer.(service.CompactionPolicyHolder)
	if !ok {
		return &service.CompactionPolicyUnavailableError{}
	}
	return h.SetCompactionPolicy(ctx, policy)
}
