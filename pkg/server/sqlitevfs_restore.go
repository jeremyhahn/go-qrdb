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
	"errors"
	"fmt"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
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
}

var (
	_ service.MVCCKVServicer          = (*sqliteRestoreMVCC)(nil)
	_ service.PrefixMover             = (*sqliteRestoreMVCC)(nil)
	_ service.RevisionHistoryReporter = (*sqliteRestoreMVCC)(nil)
	_ service.CompactionPolicyHolder  = (*sqliteRestoreMVCC)(nil)
)

// withSQLiteRestoreHook returns svcs with its MVCC service wrapped in the
// SQLite restore hook. svcs is returned as it is when it has no MVCC
// service or its services cannot back a page store.
func withSQLiteRestoreHook(svcs *DatabaseServices) *DatabaseServices {
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
	out.MVCC = &sqliteRestoreMVCC{MVCCKVServicer: svcs.MVCC, kv: kv, txn: txn}
	return &out
}

// RestoreToRevision runs the restore, then bumps the generation of each
// SQLite database whose keys it changed. A bump failure is returned wrapped
// together with the restore's result: the restore itself has been applied.
func (s *sqliteRestoreMVCC) RestoreToRevision(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	if req == nil || req.DryRun {
		return s.MVCCKVServicer.RestoreToRevision(ctx, req)
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
	result := &service.RestoreRevisionResult{
		Revision: plan.Revision,
		Changes:  plan.Changes,
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
	backend, err := sqlitevfs.NewServiceBackend(s.kv, s.txn)
	if err != nil {
		return nil, fmt.Errorf("sqlitevfs restore hook: %w", err)
	}
	conflicted := make(map[string]bool)
	for _, db := range dbs {
		store, err := sqlitevfs.NewStore(backend, db)
		if err == nil {
			_, err = store.RestoreToRevision(ctx, req.Revision)
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
	for _, c := range plan.Changes {
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

// MovePrefix forwards to the wrapped service.
func (s *sqliteRestoreMVCC) MovePrefix(ctx context.Context, req *service.MovePrefixRequest) (int64, error) {
	mover, ok := s.MVCCKVServicer.(service.PrefixMover)
	if !ok {
		return 0, &service.PrefixMoveUnsupportedError{}
	}
	return mover.MovePrefix(ctx, req)
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
