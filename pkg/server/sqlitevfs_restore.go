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

// sqliteRestoreMVCC is the SQLite VFS restore hook (row #926, Q4 A) over a
// scope's MVCC service: a restore to a revision that wrote a key under
// sqlite/<db>/ bumps that database's generation once, past every generation
// it has had, so a page store that saw the database before the restore is
// told (ErrSnapshotChanged, SQLITE_BUSY_SNAPSHOT) instead of trusting a page
// cache whose header change counter the restore may have put back. A dry
// run, a restore that touched no sqlite/ key and a failed restore bump
// nothing.
//
// The bump is a second write after the restore's own. A commit that lands
// between them is never overwritten (the bump's read check refuses a moved
// meta and retries past it), but a transaction that begins in between is
// not told of the restore when the restored generation equals the one its
// store saw. Closing that window needs the
// restore op to write the bump in its own revision; docs/plans/926.md
// records it as o9 for the owner.
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
	before, err := s.MVCCKVServicer.CurrentRevision(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.MVCCKVServicer.RestoreToRevision(ctx, req)
	if err != nil || result == nil {
		return result, err
	}
	dbs := restoredDatabases(result.Changes)
	if len(dbs) == 0 {
		return result, nil
	}
	backend, err := sqlitevfs.NewServiceBackend(s.kv, s.txn)
	if err != nil {
		return result, fmt.Errorf("sqlitevfs restore hook: %w", err)
	}
	var errs []error
	for _, db := range dbs {
		store, err := sqlitevfs.NewStore(backend, db)
		if err == nil {
			_, err = store.BumpGenerationAfterRestore(ctx, before)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return result, fmt.Errorf("sqlitevfs restore hook: restore to revision %d applied, generation bump failed: %w", req.Revision, err)
	}
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
