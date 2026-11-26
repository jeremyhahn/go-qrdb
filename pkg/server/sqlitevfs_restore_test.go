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

// The Q4 A restore hook (row #926) on the prefix, volume and raft isolation
// types: a restore to a revision through a scope's MVCC service bumps the
// generation of every SQLite database whose keys it touched, so a
// transaction or a stock SQLite connection open across the restore is told
// (ErrSnapshotChanged, SQLITE_BUSY_SNAPSHOT) and the next one reads the
// restored pages. A dry run, a restore that touched no sqlite/ key and a
// failed restore bump nothing.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs/moderncvfs"
)

// restoreStageCommitID is the commit id of a stage key a test leaves behind.
const restoreStageCommitID = "0123456789abcdef0123456789abcdef"

// newRestoreStore returns a page store of database wiringDB over tenantID's
// services in reg, without a writer lock (the default), so two stores can
// hold open write transactions at once.
func newRestoreStore(t *testing.T, reg service.Registry, tenantID string) *wiringStore {
	t.Helper()
	mvccSvc, err := reg.MVCCService(tenantID, lockTestDB)
	if err != nil {
		t.Fatalf("MVCCService(%s): %v", tenantID, err)
	}
	txnSvc, err := reg.TransactionService(tenantID, lockTestDB)
	if err != nil {
		t.Fatalf("TransactionService(%s): %v", tenantID, err)
	}
	kv, ok := mvccSvc.(sqlitevfs.ServiceKV)
	if !ok {
		t.Fatalf("MVCC servicer %T is not a sqlitevfs.ServiceKV", mvccSvc)
	}
	txn, ok := txnSvc.(sqlitevfs.ServiceTxn)
	if !ok {
		t.Fatalf("transaction servicer %T is not a sqlitevfs.ServiceTxn", txnSvc)
	}
	backend, err := sqlitevfs.NewServiceBackend(kv, txn)
	if err != nil {
		t.Fatalf("NewServiceBackend: %v", err)
	}
	s, err := sqlitevfs.NewStore(backend, wiringDB)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return &wiringStore{store: s, backend: backend}
}

// restoreCommit writes one page per fill byte as the whole file and commits.
func (w *wiringStore) restoreCommit(t *testing.T, fills ...byte) int64 {
	t.Helper()
	txn, err := w.store.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	wiringWrite(t, txn, fills...)
	rev, err := txn.Commit(t.Context())
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return rev
}

// revision is the backend's current revision.
func (w *wiringStore) revision(t *testing.T) int64 {
	t.Helper()
	rev, err := w.backend.Revision(t.Context())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	return rev
}

// generation is the database's meta generation at the current revision.
func (w *wiringStore) generation(t *testing.T) uint64 {
	t.Helper()
	key, err := sqlitevfs.MetaKey(wiringDB)
	if err != nil {
		t.Fatal(err)
	}
	value, found, err := w.backend.GetAt(t.Context(), key, w.revision(t))
	if err != nil || !found {
		t.Fatalf("meta: found=%v err=%v", found, err)
	}
	var m sqlitevfs.Meta
	if err := m.UnmarshalBinary(value); err != nil {
		t.Fatal(err)
	}
	return m.Generation
}

// restoreScope restores the scope's keys under prefix to revision rev
// through tenantID's MVCC service in reg.
func restoreScope(t *testing.T, reg service.Registry, tenantID, prefix string, rev int64, dryRun bool) (*service.RestoreRevisionResult, error) {
	t.Helper()
	mvccSvc, err := reg.MVCCService(tenantID, lockTestDB)
	if err != nil {
		t.Fatalf("MVCCService(%s): %v", tenantID, err)
	}
	return mvccSvc.RestoreToRevision(t.Context(), &service.RestoreRevisionRequest{Revision: rev, Prefix: prefix, DryRun: dryRun})
}

// restoreLocks returns a restore-hook lock resolver over the lock service of
// tenantID's test database, the scope the registry's hook resolves.
func restoreLocks(t *testing.T, reg service.Registry, tenantID string) sqliteLockResolver {
	t.Helper()
	return func() (sqlitevfs.LockServiceLocker, error) {
		return lockSvcFor(t, reg, tenantID), nil
	}
}

// TestSQLiteVFSRestore_HeldWriterLockIsConflict checks that the restore
// hook takes the database's writer lock: while a writer holds it, a restore
// reports the database as a conflict and leaves its pages, a forced restore
// fails with ErrBusy, and once it is released the restore applies.
func TestSQLiteVFSRestore_HeldWriterLockIsConflict(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			reg := build(t).registry
			r := newRestoreStore(t, reg, lockTenantA).restoreCommit(t, 0x11, 0x22)
			newRestoreStore(t, reg, lockTenantA).restoreCommit(t, 0x33, 0x44)
			writer, err := sqlitevfs.NewLockServiceWriter(lockSvcFor(t, reg, lockTenantA), "cli-writer", time.Minute)
			if err != nil {
				t.Fatalf("NewLockServiceWriter: %v", err)
			}
			key, err := sqlitevfs.WriterLockKey(wiringDB)
			if err != nil {
				t.Fatalf("WriterLockKey: %v", err)
			}
			token, err := writer.Acquire(ctx, key)
			if err != nil {
				t.Fatalf("Acquire: %v", err)
			}
			mvccSvc, err := reg.MVCCService(lockTenantA, lockTestDB)
			if err != nil {
				t.Fatalf("MVCCService: %v", err)
			}

			res, err := mvccSvc.RestoreToRevision(ctx, &service.RestoreRevisionRequest{Revision: r, Prefix: "sqlite/"})
			if err != nil {
				t.Fatalf("RestoreToRevision: %v", err)
			}
			if res.Conflicted == 0 || res.Applied != 0 {
				t.Fatalf("restore under a held writer lock conflicted %d applied %d, want >0 and 0", res.Conflicted, res.Applied)
			}
			newRestoreStore(t, reg, lockTenantA).assertPages(t, 0x33, 0x44)
			if _, err := mvccSvc.RestoreToRevision(ctx, &service.RestoreRevisionRequest{Revision: r, Prefix: "sqlite/", Force: true}); !errors.Is(err, sqlitevfs.ErrBusy) {
				t.Fatalf("forced restore under a held writer lock err = %v, want ErrBusy", err)
			}
			newRestoreStore(t, reg, lockTenantA).assertPages(t, 0x33, 0x44)

			if err := writer.Release(ctx, key, token); err != nil {
				t.Fatalf("Release: %v", err)
			}
			res, err = mvccSvc.RestoreToRevision(ctx, &service.RestoreRevisionRequest{Revision: r, Prefix: "sqlite/"})
			if err != nil {
				t.Fatalf("RestoreToRevision after release: %v", err)
			}
			if res.Conflicted != 0 || res.Applied == 0 {
				t.Fatalf("restore after release conflicted %d applied %d, want 0 and >0", res.Conflicted, res.Applied)
			}
			newRestoreStore(t, reg, lockTenantA).assertPages(t, 0x11, 0x22)
		})
	}
}

// mustRestore restores the scope's sqlite/ keys to rev and fails on error.
func mustRestore(t *testing.T, reg service.Registry, rev int64) *service.RestoreRevisionResult {
	t.Helper()
	result, err := restoreScope(t, reg, lockTenantA, "sqlite/", rev, false)
	if err != nil {
		t.Fatalf("RestoreToRevision(%d): %v", rev, err)
	}
	if len(result.Changes) == 0 {
		t.Fatalf("RestoreToRevision(%d) changed nothing", rev)
	}
	return result
}

func TestSQLiteVFSRestore_OpenTxnIsSnapshotChanged(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			reg := build(t).registry
			a := newRestoreStore(t, reg, lockTenantA)
			r := a.restoreCommit(t, 0x11, 0x22)

			// Pinned at R; another store moves on; the restore puts R's
			// pages and meta back, so only the generation tells them apart.
			open, err := a.store.Begin(t.Context())
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			wiringWrite(t, open, 0x99)
			newRestoreStore(t, reg, lockTenantA).restoreCommit(t, 0x33, 0x44, 0x55)
			mustRestore(t, reg, r)
			after := a.revision(t)

			if _, err := open.Commit(t.Context()); !errors.Is(err, sqlitevfs.ErrSnapshotChanged) {
				t.Fatalf("Commit across a restore err = %v, want ErrSnapshotChanged", err)
			}
			if got := a.revision(t); got != after {
				t.Fatalf("a refused Commit moved the revision from %d to %d", after, got)
			}
		})
	}
}

func TestSQLiteVFSRestore_ReadsRestoredPages(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			reg := build(t).registry
			a := newRestoreStore(t, reg, lockTenantA)
			r := a.restoreCommit(t, 0x11, 0x22)
			a.restoreCommit(t, 0x33, 0x44, 0x55)
			mustRestore(t, reg, r)

			if _, err := a.store.Begin(t.Context()); !errors.Is(err, sqlitevfs.ErrSnapshotChanged) {
				t.Fatalf("first Begin after a restore err = %v, want ErrSnapshotChanged", err)
			}
			a.assertPages(t, 0x11, 0x22)
			newRestoreStore(t, reg, lockTenantA).assertPages(t, 0x11, 0x22)
		})
	}
}

func TestSQLiteVFSRestore_StageKeysAfterRestoreGone(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			reg := build(t).registry
			a := newRestoreStore(t, reg, lockTenantA)
			r := a.restoreCommit(t, 0x11)
			stageKey, err := sqlitevfs.StageKey(wiringDB, restoreStageCommitID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.backend.Commit(t.Context(), nil, map[string][]byte{string(stageKey): wiringPage(0x77)}, nil); err != nil {
				t.Fatalf("Commit stage key: %v", err)
			}
			genBefore := a.generation(t)
			mustRestore(t, reg, r)

			stageRoot, err := sqlitevfs.StageRoot(wiringDB)
			if err != nil {
				t.Fatal(err)
			}
			if live, err := a.backend.Keys(t.Context(), stageRoot, a.revision(t)); err != nil || len(live) != 0 {
				t.Fatalf("live stage keys after the restore = %q, %v, want none", live, err)
			}
			// The hook's Store holds the writer lock, so its Reserve sweeps
			// the abandoned stage; the image is unchanged, so the restore
			// writes no meta and the generation stays.
			if got := a.generation(t); got != genBefore {
				t.Fatalf("generation after a restore that only swept a stage key = %d, want %d", got, genBefore)
			}
			newRestoreStore(t, reg, lockTenantA).assertPages(t, 0x11)
		})
	}
}

func TestSQLiteVFSRestore_DryRunBumpsNothing(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			reg := build(t).registry
			a := newRestoreStore(t, reg, lockTenantA)
			r := a.restoreCommit(t, 0x11)
			a.restoreCommit(t, 0x22)
			before := a.revision(t)

			// A dry run plans the restore and writes nothing.
			result, err := restoreScope(t, reg, lockTenantA, "sqlite/", r, true)
			if err != nil || len(result.Changes) == 0 {
				t.Fatalf("dry run = %+v, %v, want a plan", result, err)
			}
			if got := a.revision(t); got != before {
				t.Fatalf("a dry run moved the revision from %d to %d", before, got)
			}

			// A failed restore (a revision in the future) bumps nothing.
			if _, err := restoreScope(t, reg, lockTenantA, "sqlite/", before+100, false); err == nil {
				t.Fatal("a restore to a future revision succeeded")
			}
			if got := a.revision(t); got != before {
				t.Fatalf("a failed restore moved the revision from %d to %d", before, got)
			}

			// A restore that touched no sqlite/ key bumps nothing.
			mvccSvc, err := reg.MVCCService(lockTenantA, lockTestDB)
			if err != nil {
				t.Fatal(err)
			}
			if err := mvccSvc.Put(t.Context(), "other/k", []byte("v")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			result, err = restoreScope(t, reg, lockTenantA, "other/", before, false)
			if err != nil || len(result.Changes) != 1 {
				t.Fatalf("restore of other/ = %+v, %v, want one change", result, err)
			}
			if got := a.revision(t); got != before+2 {
				t.Fatalf("revision after a put and a restore of other/ = %d, want %d", got, before+2)
			}
			if got := a.generation(t); got != 0 {
				t.Fatalf("generation after restores that bumped nothing = %d, want 0", got)
			}
			a.assertPages(t, 0x22)
		})
	}
}

func TestSQLiteVFSRestore_DatabaseRestoredInOneRevision(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			reg := build(t).registry
			a := newRestoreStore(t, reg, lockTenantA)
			r := a.restoreCommit(t, 0x11, 0x22)
			newRestoreStore(t, reg, lockTenantA).restoreCommit(t, 0x33, 0x44, 0x55)
			before := a.revision(t)
			mustRestore(t, reg, r)

			// The pages, the size and the bumped meta are one revision: the
			// writer lock's own writes add revisions, but every revision
			// from before to after reads either the whole old image or the
			// whole restored one, and the switch happens once.
			after := a.revision(t)
			switches := 0
			restored := false
			for rev := before; rev <= after; rev++ {
				snap, err := newRestoreStore(t, reg, lockTenantA).store.BeginAt(t.Context(), rev)
				if err != nil {
					t.Fatalf("BeginAt(%d): %v", rev, err)
				}
				buf := make([]byte, wiringPageSize)
				if _, err := snap.ReadAt(buf, 0); err != nil {
					t.Fatalf("ReadAt at %d: %v", rev, err)
				}
				size := snap.Size()
				_ = snap.Rollback(t.Context())
				var isRestored bool
				switch {
				case size == 3*wiringPageSize && bytes.Equal(buf, wiringPage(0x33)):
				case size == 2*wiringPageSize && bytes.Equal(buf, wiringPage(0x11)):
					isRestored = true
				default:
					t.Fatalf("revision %d reads size %d and a page of neither image", rev, size)
				}
				if isRestored != restored {
					switches++
					restored = isRestored
				}
			}
			if switches != 1 || !restored {
				t.Fatalf("the image switched %d times between revisions %d and %d, want once", switches, before, after)
			}
			fresh := newRestoreStore(t, reg, lockTenantA)
			txn, err := fresh.store.BeginAt(t.Context(), after)
			if err != nil {
				t.Fatalf("BeginAt(%d): %v", after, err)
			}
			if got := txn.Size(); got != 2*wiringPageSize {
				t.Fatalf("size at %d = %d, want R's %d", after, got, 2*wiringPageSize)
			}
			_ = txn.Rollback(t.Context())
			if got := fresh.generation(t); got == 0 {
				t.Fatalf("generation at %d = 0, want bumped", after)
			}
			fresh.assertPages(t, 0x11, 0x22)
		})
	}
}

// planHookMVCC runs onPlan once, after the first dry-run restore it forwards.
type planHookMVCC struct {
	service.MVCCKVServicer
	onPlan func()
}

func (p *planHookMVCC) RestoreToRevision(ctx context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
	res, err := p.MVCCKVServicer.RestoreToRevision(ctx, req)
	if req != nil && req.DryRun && p.onPlan != nil {
		hook := p.onPlan
		p.onPlan = nil
		hook()
	}
	return res, err
}

func TestSQLiteVFSRestore_CommitAfterPlanIsConflict(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			reg := build(t).registry
			r := newRestoreStore(t, reg, lockTenantA).restoreCommit(t, 0x11, 0x22)
			newRestoreStore(t, reg, lockTenantA).restoreCommit(t, 0x33, 0x44)
			mvccSvc, err := reg.MVCCService(lockTenantA, lockTestDB)
			if err != nil {
				t.Fatalf("MVCCService: %v", err)
			}
			txnSvc, err := reg.TransactionService(lockTenantA, lockTestDB)
			if err != nil {
				t.Fatalf("TransactionService: %v", err)
			}
			hook := &planHookMVCC{MVCCKVServicer: mvccSvc}
			hook.onPlan = func() {
				newRestoreStore(t, reg, lockTenantA).restoreCommit(t, 0x55, 0x66)
			}
			w := &sqliteRestoreMVCC{
				MVCCKVServicer: hook,
				kv:             mvccSvc.(sqlitevfs.ServiceKV),
				txn:            txnSvc.(sqlitevfs.ServiceTxn),
				locks:          restoreLocks(t, reg, lockTenantA),
			}

			res, err := w.RestoreToRevision(ctx, &service.RestoreRevisionRequest{Revision: r, Prefix: "sqlite/"})
			if err != nil {
				t.Fatalf("RestoreToRevision: %v", err)
			}
			if len(res.Conflicts) == 0 || res.Conflicted != len(res.Conflicts) {
				t.Fatalf("conflicts = %d (Conflicted %d), want the database's keys", len(res.Conflicts), res.Conflicted)
			}
			for _, c := range res.Conflicts {
				if !strings.HasPrefix(c.Key, "sqlite/") {
					t.Fatalf("conflict on %q, want only the database's keys", c.Key)
				}
			}
			if res.Applied != 0 {
				t.Fatalf("applied = %d, want 0", res.Applied)
			}
			newRestoreStore(t, reg, lockTenantA).assertPages(t, 0x55, 0x66)

			res, err = w.RestoreToRevision(ctx, &service.RestoreRevisionRequest{Revision: r, Prefix: "sqlite/", Force: true})
			if err != nil {
				t.Fatalf("forced RestoreToRevision: %v", err)
			}
			if res.Conflicted != 0 || res.Applied == 0 {
				t.Fatalf("forced restore conflicted %d applied %d, want 0 and >0", res.Conflicted, res.Applied)
			}
			newRestoreStore(t, reg, lockTenantA).assertPages(t, 0x11, 0x22)
		})
	}
}

func TestSQLiteVFSRestore_MixedKeysRestoresBoth(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			reg := build(t).registry
			mvccSvc, err := reg.MVCCService(lockTenantA, lockTestDB)
			if err != nil {
				t.Fatal(err)
			}
			if err := mvccSvc.Put(t.Context(), "s/plain", []byte("old")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			a := newRestoreStore(t, reg, lockTenantA)
			r := a.restoreCommit(t, 0x11)
			if err := mvccSvc.Put(t.Context(), "s/plain", []byte("new")); err != nil {
				t.Fatalf("Put: %v", err)
			}
			a.restoreCommit(t, 0x22, 0x33)

			result, err := restoreScope(t, reg, lockTenantA, "s", r, false)
			if err != nil {
				t.Fatalf("RestoreToRevision: %v", err)
			}
			var plain, pages bool
			for _, c := range result.Changes {
				if c.Key == "s/plain" {
					plain = true
				} else if _, ok := sqlitevfs.DatabaseOfKey([]byte(c.Key)); ok {
					pages = true
				}
			}
			if !plain || !pages || result.Applied != len(result.Changes) || result.Conflicted != 0 {
				t.Fatalf("restore result = %+v, want the plain key and the database applied", result)
			}
			if got, err := mvccSvc.Get(t.Context(), "s/plain"); err != nil || string(got) != "old" {
				t.Fatalf("s/plain after the restore = %q, %v, want old", got, err)
			}
			newRestoreStore(t, reg, lockTenantA).assertPages(t, 0x11)
		})
	}
}

// bindingExtendedCode is the extended SQLite result code of err, -1 if err
// is not a *sqlite.Error.
func bindingExtendedCode(err error) int {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	return -1
}

func TestSQLiteVFSRestore_StockSQLiteSeesRestore(t *testing.T) {
	stores := &bindingStores{}
	if err := moderncvfs.Register(stores.open); err != nil {
		t.Fatalf("Register: %v", err)
	}
	t.Cleanup(func() {
		if err := moderncvfs.Unregister(); err != nil {
			t.Errorf("Unregister: %v", err)
		}
	})
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			reg := build(t).registry
			a := newRestoreStore(t, reg, lockTenantA)
			dbA := bindingOpen(t, stores, name+"-restore-a", a.store)
			dbB := bindingOpen(t, stores, name+"-restore-b", newRestoreStore(t, reg, lockTenantA).store)

			bindingExec(t, dbA, "create table t(id integer primary key, v blob)")
			bindingExec(t, dbA, "insert into t(id, v) values(1, randomblob(300)), (2, randomblob(300))")
			r := a.revision(t)

			// dbA opens a write transaction at R; dbB commits past it; the
			// restore puts R back while dbA's transaction is open.
			conn, err := dbA.Conn(ctx)
			if err != nil {
				t.Fatalf("Conn: %v", err)
			}
			pinned := conn2db{ctx, conn}
			bindingExec(t, pinned, "begin immediate")
			bindingExec(t, pinned, "insert into t(id, v) values(10, randomblob(300))")
			bindingExec(t, dbB, "insert into t(id, v) values(20, randomblob(300))")
			mustRestore(t, reg, r)

			if _, err := pinned.Exec("commit"); bindingExtendedCode(err) != sqlite3.SQLITE_BUSY_SNAPSHOT {
				t.Fatalf("commit across a restore err = %v (code %d), want SQLITE_BUSY_SNAPSHOT", err, bindingExtendedCode(err))
			}
			_, _ = pinned.Exec("rollback")
			if err := conn.Close(); err != nil {
				t.Fatalf("Conn.Close: %v", err)
			}

			// The refused commit told the connection; it, and a fresh one,
			// read R's rows, and integrity_check (in bindingState) is ok.
			var n, sum int64
			fresh := bindingOpen(t, stores, name+"-restore-fresh", newRestoreStore(t, reg, lockTenantA).store)
			for label, db := range map[string]*sql.DB{"dbA": dbA, "fresh": fresh} {
				if n, sum = bindingState(t, db); n != 2 || sum != 3 {
					t.Fatalf("%s rows after the restore = %d (ids sum %d), want R's 2 (3)", label, n, sum)
				}
			}
		})
	}
}
