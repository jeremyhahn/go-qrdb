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
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sqlitevfs"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// admitFakes is the tenant side of the gate: the sqlite_vfs entitlement, a
// sqlite_databases cap over a usage count, and the recorder that moves it.
type admitFakes struct {
	mu      sync.Mutex
	allowed bool
	max     int64
	usage   int64
	checks  int
}

func (f *admitFakes) CheckIsolation(context.Context, string, string) error { return nil }

func (f *admitFakes) CheckFeature(_ context.Context, tenantID, feature string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if feature == quota.FeatureSQLiteVFS && f.allowed {
		return nil
	}
	return &quota.EntitlementError{TenantID: tenantID, Feature: feature}
}

func (f *admitFakes) Check(ctx context.Context, tenantID string, resource quota.Resource) error {
	return f.CheckN(ctx, tenantID, resource, 1)
}

func (f *admitFakes) CheckN(_ context.Context, tenantID string, resource quota.Resource, n int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	if f.max > 0 && f.usage+n > f.max {
		return &quota.ExceededError{TenantID: tenantID, Resource: resource, Current: f.usage, Limit: f.max}
	}
	return nil
}

func (f *admitFakes) Usage(context.Context, string, quota.Resource) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usage, nil
}

func (f *admitFakes) IncrementQuotaUsage(_ context.Context, _ string, resource quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if resource == quota.ResourceSQLiteDatabases {
		f.usage += delta
	}
	return nil, nil
}

func (f *admitFakes) DecrementQuotaUsage(_ context.Context, _ string, resource quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if resource == quota.ResourceSQLiteDatabases {
		f.usage -= delta
	}
	return nil, nil
}

func (f *admitFakes) counts() (usage int64, checks int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usage, f.checks
}

func (f *admitFakes) admission() *sqliteAdmission {
	return &sqliteAdmission{ents: f, lim: f, usage: f}
}

// gatedServices returns the tenant's services of lockTestDB with the gate
// of fakes on them, built over the registry's ungated services.
func gatedServices(t *testing.T, reg service.Registry, tenantID string, fakes *admitFakes) *DatabaseServices {
	t.Helper()
	mvccSvc, err := reg.MVCCService(tenantID, lockTestDB)
	if err != nil {
		t.Fatalf("MVCCService(%s): %v", tenantID, err)
	}
	txnSvc, err := reg.TransactionService(tenantID, lockTestDB)
	if err != nil {
		t.Fatalf("TransactionService(%s): %v", tenantID, err)
	}
	if gated, ok := txnSvc.(*sqliteAdmitTxn); ok {
		txnSvc = gated.TransactionServicer
	}
	if rm, ok := mvccSvc.(*sqliteRestoreMVCC); ok {
		mvccSvc = rm.MVCCKVServicer
	}
	svcs := withSQLiteRestoreHook(&DatabaseServices{MVCC: mvccSvc, Transaction: txnSvc}, restoreLocks(t, reg, tenantID))
	gated := withSQLiteAdmission(svcs, tenantID, fakes.admission())
	if _, ok := gated.Transaction.(*sqliteAdmitTxn); !ok {
		t.Fatalf("withSQLiteAdmission left transaction servicer %T ungated", gated.Transaction)
	}
	return gated
}

// gatedStore returns a Store of wiringDB whose writes go through svcs.
func gatedStore(t *testing.T, svcs *DatabaseServices, opts ...sqlitevfs.Option) *wiringStore {
	t.Helper()
	backend, err := sqlitevfs.NewServiceBackend(svcs.MVCC.(sqlitevfs.ServiceKV), svcs.Transaction.(sqlitevfs.ServiceTxn))
	if err != nil {
		t.Fatalf("NewServiceBackend: %v", err)
	}
	s, err := sqlitevfs.NewStore(backend, wiringDB, opts...)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return &wiringStore{store: s, backend: backend}
}

// createDatabase writes one page in a fresh Store of wiringDB, the write
// that creates the database.
func createDatabase(t *testing.T, w *wiringStore, fill byte) error {
	t.Helper()
	txn, err := w.store.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	wiringWrite(t, txn, fill)
	_, err = txn.Commit(t.Context())
	if err != nil {
		_ = txn.Rollback(t.Context())
	}
	return err
}

// assertNoMeta wants no live meta record of wiringDB.
func assertNoMeta(t *testing.T, w *wiringStore) {
	t.Helper()
	metaKey, err := sqlitevfs.MetaKey(wiringDB)
	if err != nil {
		t.Fatalf("MetaKey: %v", err)
	}
	rev, err := w.backend.Revision(t.Context())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	if _, found, err := w.backend.GetAt(t.Context(), metaKey, rev); err != nil || found {
		t.Fatalf("meta record after a refused create: found %v, %v; want none", found, err)
	}
}

func TestSQLiteAdmitTxn_EntitlementOffRefusesCreate(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			fakes := &admitFakes{allowed: false}
			w := gatedStore(t, gatedServices(t, build(t).registry, lockTenantA, fakes))
			err := createDatabase(t, w, 0x11)
			var ent *quota.EntitlementError
			if !errors.As(err, &ent) || ent.Feature != quota.FeatureSQLiteVFS {
				t.Fatalf("create with sqlite_vfs off = %v, want quota.EntitlementError for %s", err, quota.FeatureSQLiteVFS)
			}
			assertNoMeta(t, w)
			if usage, _ := fakes.counts(); usage != 0 {
				t.Fatalf("usage after a refused create = %d, want 0", usage)
			}
		})
	}
}

func TestSQLiteAdmitTxn_CapReachedRefusesCreate(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			fakes := &admitFakes{allowed: true, max: 1, usage: 1}
			w := gatedStore(t, gatedServices(t, build(t).registry, lockTenantA, fakes))
			err := createDatabase(t, w, 0x11)
			var exceeded *quota.ExceededError
			if !errors.As(err, &exceeded) || exceeded.Resource != quota.ResourceSQLiteDatabases {
				t.Fatalf("create at the cap = %v, want quota.ExceededError for %s", err, quota.ResourceSQLiteDatabases)
			}
			assertNoMeta(t, w)
			if usage, _ := fakes.counts(); usage != 1 {
				t.Fatalf("usage after a refused create = %d, want 1", usage)
			}
		})
	}
}

func TestSQLiteAdmitTxn_ExistingDatabaseOpensWithoutChecks(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			fakes := &admitFakes{allowed: true, max: 1}
			reg := build(t).registry
			w := gatedStore(t, gatedServices(t, reg, lockTenantA, fakes))
			if err := createDatabase(t, w, 0x11); err != nil {
				t.Fatalf("create: %v", err)
			}
			_, checksAfterCreate := fakes.counts()

			// The entitlement is now off and the cap is reached: the
			// existing database still opens, is written and reads back.
			fakes.mu.Lock()
			fakes.allowed = false
			fakes.mu.Unlock()
			again := gatedStore(t, gatedServices(t, reg, lockTenantA, fakes))
			txn, err := again.store.Begin(t.Context())
			if err != nil {
				t.Fatalf("Begin: %v", err)
			}
			wiringWrite(t, txn, 0x21, 0x22)
			if _, err := txn.Commit(t.Context()); err != nil {
				t.Fatalf("Commit to the existing database: %v", err)
			}
			again.assertPages(t, 0x21, 0x22)
			usage, checks := fakes.counts()
			if checks != checksAfterCreate {
				t.Fatalf("writes to an existing database ran %d checks, want none", checks-checksAfterCreate)
			}
			if usage != 1 {
				t.Fatalf("usage = %d, want 1 (the one create)", usage)
			}
		})
	}
}

func TestSQLiteAdmitTxn_CreateAndDropRecordUsage(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			fakes := &admitFakes{allowed: true, max: 1}
			svcs := gatedServices(t, build(t).registry, lockTenantA, fakes)
			w := gatedStore(t, svcs)
			if err := createDatabase(t, w, 0x11); err != nil {
				t.Fatalf("create: %v", err)
			}
			if usage, _ := fakes.counts(); usage != 1 {
				t.Fatalf("usage after the create = %d, want 1", usage)
			}

			metaKey, err := sqlitevfs.MetaKey(wiringDB)
			if err != nil {
				t.Fatalf("MetaKey: %v", err)
			}
			drop := []service.Op{{Type: service.OpDelete, Key: metaKey}}
			resp, err := svcs.Transaction.Execute(t.Context(), nil, drop, nil)
			if err != nil || resp == nil || !resp.Succeeded {
				t.Fatalf("drop: %+v, %v", resp, err)
			}
			if usage, _ := fakes.counts(); usage != 0 {
				t.Fatalf("usage after the drop = %d, want 0", usage)
			}
			// A delete of a meta record that is not live is no drop.
			if _, err := svcs.Transaction.Execute(t.Context(), nil, drop, nil); err != nil {
				t.Fatalf("second drop: %v", err)
			}
			if usage, _ := fakes.counts(); usage != 0 {
				t.Fatalf("usage after a second drop = %d, want 0", usage)
			}

			// The freed slot admits the database again.
			if err := createDatabase(t, gatedStore(t, svcs), 0x12); err != nil {
				t.Fatalf("re-create: %v", err)
			}
			if usage, _ := fakes.counts(); usage != 1 {
				t.Fatalf("usage after the re-create = %d, want 1", usage)
			}
		})
	}
}

func TestSQLiteAdmitMovePrefix_StagedCreateIsGated(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			fakes := &admitFakes{allowed: false}
			svcs := gatedServices(t, build(t).registry, lockTenantA, fakes)
			fills := []byte{0x31, 0x32, 0x33, 0x34}
			staged := func() error {
				w := gatedStore(t, svcs, sqlitevfs.WithStageThreshold(wiringStageThreshold))
				txn, err := w.store.Begin(t.Context())
				if err != nil {
					t.Fatalf("Begin: %v", err)
				}
				wiringWrite(t, txn, fills...)
				_, err = txn.Commit(t.Context())
				if err != nil {
					_ = txn.Rollback(t.Context())
				}
				return err
			}
			var ent *quota.EntitlementError
			if err := staged(); !errors.As(err, &ent) {
				t.Fatalf("staged create with sqlite_vfs off = %v, want quota.EntitlementError", err)
			}
			w := gatedStore(t, svcs)
			assertNoMeta(t, w)

			fakes.mu.Lock()
			fakes.allowed = true
			fakes.mu.Unlock()
			if err := staged(); err != nil {
				t.Fatalf("staged create with sqlite_vfs on: %v", err)
			}
			w.assertPages(t, fills...)
			if usage, _ := fakes.counts(); usage != 1 {
				t.Fatalf("usage after the staged create = %d, want 1", usage)
			}
		})
	}
}

func TestSQLiteAdmitMovePrefix_MovedMetaIsGated(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			fakes := &admitFakes{allowed: false, max: 1}
			svcs := gatedServices(t, build(t).registry, lockTenantA, fakes)
			seed := []service.Op{{Type: service.OpPut, Key: []byte("seed/" + wiringDB + "/meta"), Value: []byte("staged")}}
			if resp, err := svcs.Transaction.Execute(t.Context(), nil, seed, nil); err != nil || resp == nil || !resp.Succeeded {
				t.Fatalf("seed: %+v, %v", resp, err)
			}
			mover, ok := svcs.MVCC.(service.PrefixMover)
			if !ok {
				t.Fatalf("gated MVCC %T is no PrefixMover", svcs.MVCC)
			}
			moveIn := &service.MovePrefixRequest{Src: "seed/", Dst: "sqlite/", ExpectedKeys: 1}
			var ent *quota.EntitlementError
			if _, err := mover.MovePrefix(t.Context(), moveIn); !errors.As(err, &ent) {
				t.Fatalf("move creating a meta record with sqlite_vfs off = %v, want quota.EntitlementError", err)
			}
			assertNoMeta(t, gatedStore(t, svcs))

			fakes.mu.Lock()
			fakes.allowed = true
			fakes.mu.Unlock()
			if _, err := mover.MovePrefix(t.Context(), moveIn); err != nil {
				t.Fatalf("move with sqlite_vfs on: %v", err)
			}
			if usage, _ := fakes.counts(); usage != 1 {
				t.Fatalf("usage after the move in = %d, want 1", usage)
			}
			if _, err := mover.MovePrefix(t.Context(), &service.MovePrefixRequest{Src: "sqlite/", Dst: "gone/", ExpectedKeys: 1}); err != nil {
				t.Fatalf("move out: %v", err)
			}
			if usage, _ := fakes.counts(); usage != 0 {
				t.Fatalf("usage after the move out = %d, want 0", usage)
			}
		})
	}
}

func TestSQLiteAdmitRestore_CreateAndRemoveAreGated(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			fakes := &admitFakes{allowed: true, max: 1}
			svcs := gatedServices(t, build(t).registry, lockTenantA, fakes)
			other := []service.Op{{Type: service.OpPut, Key: []byte("other"), Value: []byte("v")}}
			if resp, err := svcs.Transaction.Execute(t.Context(), nil, other, nil); err != nil || resp == nil || !resp.Succeeded {
				t.Fatalf("seed: %+v, %v", resp, err)
			}
			w := gatedStore(t, svcs)
			before, err := w.backend.Revision(t.Context())
			if err != nil {
				t.Fatalf("Revision: %v", err)
			}
			if err := createDatabase(t, w, 0x21); err != nil {
				t.Fatalf("create: %v", err)
			}
			created, err := w.backend.Revision(t.Context())
			if err != nil {
				t.Fatalf("Revision: %v", err)
			}
			restorer, ok := svcs.MVCC.(*sqliteRestoreMVCC)
			if !ok {
				t.Fatalf("gated MVCC %T is no sqliteRestoreMVCC", svcs.MVCC)
			}
			restore := func(rev int64) error {
				_, err := restorer.RestoreToRevision(t.Context(), &service.RestoreRevisionRequest{Prefix: "sqlite/", Revision: rev, Force: true})
				return err
			}
			if err := restore(before); err != nil {
				t.Fatalf("restore to before the create: %v", err)
			}
			assertNoMeta(t, gatedStore(t, svcs))
			if usage, _ := fakes.counts(); usage != 0 {
				t.Fatalf("usage after a restore removed the database = %d, want 0", usage)
			}

			fakes.mu.Lock()
			fakes.allowed = false
			fakes.mu.Unlock()
			var ent *quota.EntitlementError
			if err := restore(created); !errors.As(err, &ent) {
				t.Fatalf("restore recreating the database with sqlite_vfs off = %v, want quota.EntitlementError", err)
			}
			assertNoMeta(t, gatedStore(t, svcs))

			fakes.mu.Lock()
			fakes.allowed = true
			fakes.mu.Unlock()
			if err := restore(created); err != nil {
				t.Fatalf("restore recreating the database: %v", err)
			}
			if usage, _ := fakes.counts(); usage != 1 {
				t.Fatalf("usage after a restore recreated the database = %d, want 1", usage)
			}
		})
	}
}
