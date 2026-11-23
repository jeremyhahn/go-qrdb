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

// The sqlite-vfs page store over the services the server resolves for a
// (tenant, database) scope (row #926, slice 8): the MVCC and transaction
// servicers back a sqlitevfs.ServiceBackend, the lock servicer backs the
// writer lock, and a Store over them commits, publishes a staged commit
// through the prefix move, serializes its writers and stays inside its
// tenant on the prefix, volume and raft isolation types. The scenes are
// lock_tenancy_test.go's.

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const (
	wiringDB       = "app.db"
	wiringPageSize = 512
	// wiringStageThreshold sends a commit of more than two pages through
	// staging and the prefix-move publish.
	wiringStageThreshold = 2 * wiringPageSize
)

// wiringStore is a Store for tenantID's lockTestDB scope, over the services
// the scene's registry resolves for it, with its writer lock taken as
// holder.
type wiringStore struct {
	store   *sqlitevfs.Store
	backend *sqlitevfs.ServiceBackend
}

func newWiringStore(t *testing.T, reg service.Registry, tenantID, holder string) *wiringStore {
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
	w, err := sqlitevfs.NewLockServiceWriter(lockSvcFor(t, reg, tenantID), holder, lockTTL)
	if err != nil {
		t.Fatalf("NewLockServiceWriter: %v", err)
	}
	s, err := sqlitevfs.NewStore(backend, wiringDB,
		sqlitevfs.WithWriterLock(w), sqlitevfs.WithStageThreshold(wiringStageThreshold))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return &wiringStore{store: s, backend: backend}
}

func wiringPage(fill byte) []byte {
	return bytes.Repeat([]byte{fill}, wiringPageSize)
}

// reserved begins a transaction and takes the writer lock.
func (w *wiringStore) reserved(t *testing.T) *sqlitevfs.Txn {
	t.Helper()
	txn, err := w.store.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := txn.Reserve(t.Context()); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	return txn
}

func wiringWrite(t *testing.T, txn *sqlitevfs.Txn, fills ...byte) {
	t.Helper()
	for i, fill := range fills {
		if n, err := txn.WriteAt(wiringPage(fill), int64(i)*wiringPageSize); err != nil || n != wiringPageSize {
			t.Fatalf("WriteAt page %d = %d, %v", i+1, n, err)
		}
	}
}

// commitPages writes one page per fill byte in a reserved transaction and
// commits it, returning the commit's revision.
func (w *wiringStore) commitPages(t *testing.T, fills ...byte) int64 {
	t.Helper()
	txn := w.reserved(t)
	wiringWrite(t, txn, fills...)
	rev, err := txn.Commit(t.Context())
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return rev
}

// assertPages reads the database in a new transaction and wants one page
// per fill byte.
func (w *wiringStore) assertPages(t *testing.T, fills ...byte) {
	t.Helper()
	txn, err := w.store.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = txn.Rollback(t.Context()) }()
	if got, want := txn.Size(), int64(len(fills))*wiringPageSize; got != want {
		t.Fatalf("database size %d, want %d", got, want)
	}
	for i, fill := range fills {
		buf := make([]byte, wiringPageSize)
		if _, err := txn.ReadAt(buf, int64(i)*wiringPageSize); err != nil {
			t.Fatalf("ReadAt page %d: %v", i+1, err)
		}
		if !bytes.Equal(buf, wiringPage(fill)) {
			t.Fatalf("page %d does not read back the committed bytes", i+1)
		}
	}
}

func TestSQLiteVFSWiring_CommitReadsBack(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			a := newWiringStore(t, build(t).registry, lockTenantA, lockHolderA)
			a.commitPages(t, 0x11, 0x22)
			a.assertPages(t, 0x11, 0x22)
		})
	}
}

func TestSQLiteVFSWiring_StagedCommitPublishesThroughPrefixMove(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			a := newWiringStore(t, build(t).registry, lockTenantA, lockHolderA)
			fills := []byte{0x31, 0x32, 0x33, 0x34}
			rev := a.commitPages(t, fills...)
			a.assertPages(t, fills...)

			now, err := a.backend.Revision(t.Context())
			if err != nil {
				t.Fatalf("Revision: %v", err)
			}
			stageRoot, err := sqlitevfs.StageRoot(wiringDB)
			if err != nil {
				t.Fatalf("StageRoot: %v", err)
			}
			if live, err := a.backend.Keys(t.Context(), stageRoot, now); err != nil || len(live) != 0 {
				t.Fatalf("live stage keys after publish = %q, %v, want none", live, err)
			}
			pagePrefix, err := sqlitevfs.PagePrefix(wiringDB)
			if err != nil {
				t.Fatalf("PagePrefix: %v", err)
			}
			// The publish is one revision: every page is live at the
			// commit's revision and none is one revision earlier.
			if pages, err := a.backend.Keys(t.Context(), pagePrefix, rev); err != nil || len(pages) != len(fills) {
				t.Fatalf("page keys at the commit revision = %d, %v, want %d", len(pages), err, len(fills))
			}
			if pages, err := a.backend.Keys(t.Context(), pagePrefix, rev-1); err != nil || len(pages) != 0 {
				t.Fatalf("page keys one revision before the commit = %d, %v, want none", len(pages), err)
			}
		})
	}
}

// TestSQLiteVFSWiring_OptimisticStagedPublishChecksReads: an optimistic
// Store (no writer lock) publishes a staged commit through the prefix move
// with its read set as the move's compares. A staged commit whose read page
// another writer changed is ErrSnapshotChanged at the publish and writes
// nothing; an unchanged one publishes.
func TestSQLiteVFSWiring_OptimisticStagedPublishChecksReads(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			locked := newWiringStore(t, build(t).registry, lockTenantA, lockHolderA)
			s, err := sqlitevfs.NewStore(locked.backend, wiringDB,
				sqlitevfs.WithOptimistic(), sqlitevfs.WithStageThreshold(wiringStageThreshold))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			opt := &wiringStore{store: s, backend: locked.backend}
			begin := func() *sqlitevfs.Txn {
				t.Helper()
				txn, err := opt.store.Begin(t.Context())
				if err != nil {
					t.Fatalf("Begin: %v", err)
				}
				return txn
			}
			seedTxn := begin()
			wiringWrite(t, seedTxn, 0x71, 0x72)
			if _, err := seedTxn.Commit(t.Context()); err != nil {
				t.Fatalf("seed Commit: %v", err)
			}

			reader, writer := begin(), begin()
			if _, err := reader.ReadAt(make([]byte, wiringPageSize), 0); err != nil {
				t.Fatalf("ReadAt page 1: %v", err)
			}
			wiringWrite(t, reader, 0x81, 0x82, 0x83, 0x84)
			wiringWrite(t, writer, 0x91)
			if _, err := writer.Commit(t.Context()); err != nil {
				t.Fatalf("writer Commit: %v", err)
			}
			if _, err := reader.Commit(t.Context()); !errors.Is(err, sqlitevfs.ErrSnapshotChanged) {
				t.Fatalf("staged Commit over a changed read = %v, want ErrSnapshotChanged", err)
			}
			opt.assertPages(t, 0x91, 0x72)
			stageRoot, err := sqlitevfs.StageRoot(wiringDB)
			if err != nil {
				t.Fatalf("StageRoot: %v", err)
			}
			now, err := opt.backend.Revision(t.Context())
			if err != nil {
				t.Fatalf("Revision: %v", err)
			}
			if live, err := opt.backend.Keys(t.Context(), stageRoot, now); err != nil || len(live) != 0 {
				t.Fatalf("live stage keys after the refused publish = %q, %v, want none", live, err)
			}

			again := begin()
			if _, err := again.ReadAt(make([]byte, wiringPageSize), 0); err != nil {
				t.Fatalf("ReadAt page 1: %v", err)
			}
			wiringWrite(t, again, 0xa1, 0xa2, 0xa3, 0xa4)
			if _, err := again.Commit(t.Context()); err != nil {
				t.Fatalf("staged Commit with an unchanged read set: %v", err)
			}
			opt.assertPages(t, 0xa1, 0xa2, 0xa3, 0xa4)
		})
	}
}

func TestSQLiteVFSWiring_WriterLockSerializesHolders(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			reg := build(t).registry
			a := newWiringStore(t, reg, lockTenantA, lockHolderA)
			b := newWiringStore(t, reg, lockTenantA, lockHolderB)

			txnA := a.reserved(t)
			wiringWrite(t, txnA, 0x41)
			txnB, err := b.store.Begin(t.Context())
			if err != nil {
				t.Fatalf("Begin B: %v", err)
			}
			err = txnB.Reserve(t.Context())
			if !errors.Is(err, sqlitevfs.ErrBusy) {
				t.Fatalf("Reserve while A holds the writer = %v, want ErrBusy", err)
			}
			if !strings.Contains(err.Error(), lockHolderA) {
				t.Fatalf("ErrBusy %q does not name the holder %q", err, lockHolderA)
			}
			_ = txnB.Rollback(t.Context())
			if _, err := txnA.Commit(t.Context()); err != nil {
				t.Fatalf("Commit A: %v", err)
			}

			b.commitPages(t, 0x42)
			a.assertPages(t, 0x42)
		})
	}
}

func TestSQLiteVFSWiring_TenantsDoNotShareADatabase(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			scene := build(t)
			a := newWiringStore(t, scene.registry, lockTenantA, lockHolderA)
			b := newWiringStore(t, scene.registry, lockTenantB, lockHolderB)
			a.commitPages(t, 0x51, 0x52)

			// B's database of the same name holds none of A's pages.
			b.assertPages(t)

			// B reserves its own writer while A holds A's.
			txnA := a.reserved(t)
			txnB := b.reserved(t)
			wiringWrite(t, txnB, 0x61)
			if _, err := txnB.Commit(t.Context()); err != nil {
				t.Fatalf("Commit B: %v", err)
			}
			if err := txnA.Rollback(t.Context()); err != nil {
				t.Fatalf("Rollback A: %v", err)
			}
			a.assertPages(t, 0x51, 0x52)
			b.assertPages(t, 0x61)

			assertWiringKeysSeparated(t, name, scene.rawKeys(t))
		})
	}
}

// assertWiringKeysSeparated checks where both tenants' sqlite/ keys landed.
// Under prefix isolation they share one store and every key carries its
// tenant's "<tenant>/<database>/" namespace; under volume each tenant has its
// own store and its keys still carry the namespace; under raft each tenant
// has its own shard store and the keys are bare, so the separation is the
// store (B reading none of A's pages is the proof).
func assertWiringKeysSeparated(t *testing.T, scene string, raw []string) {
	t.Helper()
	pageKey, err := sqlitevfs.PageKey(wiringDB, 1)
	if err != nil {
		t.Fatalf("PageKey: %v", err)
	}
	nsA := tenant.ComposeNamespace(lockTenantA, lockTestDB)
	nsB := tenant.ComposeNamespace(lockTenantB, lockTestDB)
	var sawA, sawB, bare int
	for _, k := range raw {
		if !strings.Contains(k, "sqlite/") {
			continue
		}
		switch {
		case strings.Contains(k, nsA+"sqlite/") || strings.Contains(k, nsA+"__sys:lock/sqlite/"):
			sawA++
		case strings.Contains(k, nsB+"sqlite/") || strings.Contains(k, nsB+"__sys:lock/sqlite/"):
			sawB++
		default:
			bare++
		}
	}
	if scene != "raft" {
		if sawA == 0 || sawB == 0 {
			t.Fatalf("%s isolation: namespaced sqlite keys missing: tenant A %d, tenant B %d", scene, sawA, sawB)
		}
		if bare != 0 {
			t.Fatalf("%s isolation: %d sqlite keys were written outside any tenant namespace", scene, bare)
		}
		return
	}
	if sawA != 0 || sawB != 0 {
		t.Fatalf("raft isolation: sqlite keys carry a redundant tenant namespace (A %d, B %d)", sawA, sawB)
	}
	if bare == 0 {
		t.Fatalf("%s isolation: no sqlite key reached the tenants' dedicated stores", scene)
	}
	found := false
	for _, k := range raw {
		if strings.Contains(k, string(pageKey)) {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s isolation: page 1 of %s is in no store", scene, wiringDB)
	}
}
