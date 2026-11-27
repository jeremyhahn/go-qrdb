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
	"fmt"
	"strings"

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
	// GuardedMVCC refuses single-key writes of SQLite keys and restores
	// each database whole, under its writer lock.
	*sqlitevfs.GuardedMVCC
	kv  sqlitevfs.ServiceKV
	txn sqlitevfs.ServiceTxn
	// gate, when set, admits the database a MovePrefix creates (o1, see
	// withSQLiteAdmission).
	gate *sqliteAdmitGate
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
	out.MVCC = &sqliteRestoreMVCC{GuardedMVCC: sqlitevfs.NewGuardedMVCC(svcs.MVCC, kv, txn, sqlitevfs.LockResolver(locks)), kv: kv, txn: txn}
	return &out
}

// sqliteLockResolver returns the lock service of the database whose SQLite
// stores a restore rewrites: the (tenant, database) scope `qrdb sqlite`
// takes its writer lock in.
type sqliteLockResolver func() (sqlitevfs.LockServiceLocker, error)

// sqliteRestoreHolder returns a fresh writer-lock holder id for one restore.
// RestoreToRevision runs the restore, then bumps the generation of each
// SQLite database whose keys it changed. A bump failure is returned wrapped
// together with the restore's result: the restore itself has been applied.
func (s *sqliteRestoreMVCC) RestoreToRevision(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	if s.gate == nil {
		return s.GuardedMVCC.RestoreToRevision(ctx, req)
	}
	return s.Restore(ctx, req, sqliteGatedKV{ServiceKV: s.kv, mover: s}, sqliteGatedServiceTxn{inner: s.txn, gate: s.gate})
}

// restoredDatabases returns, once each and in first-seen order, the SQLite
// databases whose keys a restore changed.
// imageChanges returns changes with every SQLite key replaced by the
// complete image of its database at rev: the metadata record and every
// page, each priced at the bytes the restore may write. The revision diff
// names only the keys that differ (restoring sqlite/<db>/meta alone is one
// small change), but the Store rewrites the pages the restored metadata
// points at, so a quota check pricing the diff would admit a restore far
// past MaxStorageBytes. Non-SQLite changes are kept as they are.
// MovePrefix forwards to the wrapped service.
func (s *sqliteRestoreMVCC) MovePrefix(ctx context.Context, req *service.MovePrefixRequest) (int64, error) {
	mover, ok := s.MVCCKVServicer.(service.PrefixMover)
	if !ok {
		return 0, &service.PrefixMoveUnsupportedError{}
	}
	if s.gate == nil || req == nil {
		return mover.MovePrefix(ctx, req)
	}
	moved, pinned, err := s.movedMeta(ctx, req)
	if err != nil {
		return 0, err
	}
	live, err := s.gate.admit(ctx, moved, req.Ops)
	if err != nil {
		return 0, err
	}
	bound := *req
	bound.Compares = append(append(make([]service.Compare, 0, len(req.Compares)+len(pinned)), req.Compares...), pinned...)
	rev, err := mover.MovePrefix(ctx, &bound)
	if err == nil {
		s.gate.record(ctx, live, append(moved, req.Ops...))
	}
	return rev, err
}

// movedMeta returns the meta-record writes the move itself makes: a delete
// of each SQLite meta record under req.Src and a put of each source key that
// lands on a SQLite meta record under req.Dst. It also returns one
// ModRevision compare per inspected source key, which MovePrefix adds to the
// proposal so the move applies only to the key set admitted here. A listing
// whose count is not req.ExpectedKeys is a *service.CommandConflictError.
func (s *sqliteRestoreMVCC) movedMeta(ctx context.Context, req *service.MovePrefixRequest) ([]service.Op, []service.Compare, error) {
	if req.Src == "" || req.ExpectedKeys < 1 {
		return nil, nil, nil
	}
	if prefixHoldsNoMeta(req.Src) && prefixHoldsNoMeta(req.Dst) {
		// No key under either prefix can be a SQLite meta record (a staged
		// publish: sqlite/<db>/stage/<id>/page/ -> sqlite/<db>/page/), so
		// the move creates or removes no database whatever keys it moves:
		// nothing to admit, and apply's ExpectedKeys count check is the
		// whole guard. Per-key compares here would grow the publish
		// proposal with the page count (SPEC sqlite-vfs Q2).
		return nil, nil, nil
	}
	src := []byte(req.Src)
	kvs, _, _, err := s.kv.Range(ctx, src, storage.PrefixEnd(src), service.WithKeysOnly(), service.WithLimit(req.ExpectedKeys+1))
	if err != nil {
		return nil, nil, fmt.Errorf("sqlitevfs move gate: list %q: %w", req.Src, err)
	}
	if int64(len(kvs)) != req.ExpectedKeys {
		// Apply refuses a count other than ExpectedKeys anyway; refusing here
		// keeps a short listing from leaving room for keys nobody admitted.
		return nil, nil, &service.CommandConflictError{Message: fmt.Sprintf(
			"sqlitevfs move gate: %d live keys under %q, expected %d", len(kvs), req.Src, req.ExpectedKeys)}
	}
	var ops []service.Op
	compares := make([]service.Compare, 0, len(kvs))
	for _, kv := range kvs {
		// Every inspected key must still be the revision admitted here when
		// the move applies: with apply's count check this pins the exact
		// source key set, so a key swapped in after inspection (seed/other
		// replaced by seed/meta) refuses the move instead of creating an
		// unadmitted database.
		compares = append(compares, service.Compare{
			Key:         append([]byte(nil), kv.Key...),
			Target:      service.CompareModRevision,
			Result:      service.CompareEqual,
			ModRevision: kv.ModRevision,
		})
		if _, ok := metaDatabase(kv.Key); ok {
			ops = append(ops, service.Op{Type: service.OpDelete, Key: kv.Key})
		}
		mapped := append([]byte(req.Dst), kv.Key[len(src):]...)
		if _, ok := metaDatabase(mapped); ok {
			ops = append(ops, service.Op{Type: service.OpPut, Key: mapped})
		}
	}
	return ops, compares, nil
}

// prefixHoldsNoMeta reports whether no key beginning with prefix can be a
// SQLite meta record (sqlite/<db>/meta): the prefix names one database,
// sqlite/<db>/, and continues with a segment that is not a prefix of
// "meta", so every key under it belongs to that database and differs from
// its meta key.
func prefixHoldsNoMeta(prefix string) bool {
	db, ok := sqlitevfs.DatabaseOfKey([]byte(prefix))
	if !ok {
		return false
	}
	metaKey, err := sqlitevfs.MetaKey(db)
	if err != nil {
		return false
	}
	root := len(metaKey) - len("meta")
	rest := prefix[root:]
	metaSegment := string(metaKey[root:])
	return rest != "" && !strings.HasPrefix(metaSegment, rest)
}
