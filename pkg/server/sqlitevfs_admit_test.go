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
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
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

// TestSQLiteAdmitTxn_OneTxnCreatingTwoPastTheCapIsRefused commits the meta
// records of two new databases in one transaction under a cap of one: each
// alone fits, together they do not, so the transaction is refused, writes
// nothing and leaves the usage at 0.
func TestSQLiteAdmitTxn_OneTxnCreatingTwoPastTheCapIsRefused(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			fakes := &admitFakes{allowed: true, max: 1}
			svcs := gatedServices(t, build(t).registry, lockTenantA, fakes)
			metaA, err := sqlitevfs.MetaKey("a.db")
			if err != nil {
				t.Fatalf("MetaKey: %v", err)
			}
			metaB, err := sqlitevfs.MetaKey("b.db")
			if err != nil {
				t.Fatalf("MetaKey: %v", err)
			}
			ops := []service.Op{
				{Type: service.OpPut, Key: metaA, Value: []byte("meta")},
				{Type: service.OpPut, Key: metaB, Value: []byte("meta")},
			}
			_, err = svcs.Transaction.Execute(t.Context(), nil, ops, nil)
			var exceeded *quota.ExceededError
			if !errors.As(err, &exceeded) || exceeded.Resource != quota.ResourceSQLiteDatabases {
				t.Fatalf("two creates under a cap of one = %v, want quota.ExceededError for %s", err, quota.ResourceSQLiteDatabases)
			}
			for _, key := range [][]byte{metaA, metaB} {
				if _, err := svcs.MVCC.Get(t.Context(), string(key)); err == nil {
					t.Fatalf("meta %q written by a refused transaction", key)
				}
			}
			if usage, _ := fakes.counts(); usage != 0 {
				t.Fatalf("usage after a refused transaction = %d, want 0", usage)
			}

			fakes.mu.Lock()
			fakes.max = 2
			fakes.mu.Unlock()
			if _, err := svcs.Transaction.Execute(t.Context(), nil, ops, nil); err != nil {
				t.Fatalf("two creates under a cap of two: %v", err)
			}
			if usage, _ := fakes.counts(); usage != 2 {
				t.Fatalf("usage after two creates = %d, want 2", usage)
			}
		})
	}
}

// failingUsage is an admitFakes whose usage recorder fails.
type failingUsage struct{ *admitFakes }

func (failingUsage) IncrementQuotaUsage(context.Context, string, quota.Resource, int64) (*tenant.TenantInfo, error) {
	return nil, errRecordUsage
}

var errRecordUsage = errors.New("record usage failed")

// TestSQLiteAdmitTxn_UsageErrorIsReturned proves a failed usage increment
// after a committed create reaches the caller instead of being discarded.
func TestSQLiteAdmitTxn_UsageErrorIsReturned(t *testing.T) {
	fakes := &admitFakes{allowed: true}
	gate := &sqliteAdmitGate{
		adm:      &sqliteAdmission{ents: fakes, lim: fakes, usage: failingUsage{fakes}},
		tenantID: lockTenantA,
	}
	metaKey, err := sqlitevfs.MetaKey("a.db")
	if err != nil {
		t.Fatalf("MetaKey: %v", err)
	}
	err = gate.record(t.Context(), map[string]bool{"a.db": false}, []service.Op{{Type: service.OpPut, Key: metaKey}})
	if !errors.Is(err, errRecordUsage) {
		t.Fatalf("record with a failing recorder = %v, want %v", err, errRecordUsage)
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

// swapAfterRangeKV runs swap once, right after the first Range it serves
// returns: the source changes between the gate's inspection and the apply.
type swapAfterRangeKV struct {
	sqlitevfs.ServiceKV
	swap func()
	once sync.Once
}

func (k *swapAfterRangeKV) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	kvs, count, rev, err := k.ServiceKV.Range(ctx, start, end, opts...)
	k.once.Do(k.swap)
	return kvs, count, rev, err
}

// A move admitted on seed/<db>/other must not apply after seed/<db>/other is
// swapped for seed/<db>/meta: the count still matches, but the key set the
// gate admitted is gone, so the move is refused and creates nothing.
func TestSQLiteAdmitMovePrefix_SwappedSourceIsRefused(t *testing.T) {
	for name, build := range lockScenes {
		t.Run(name, func(t *testing.T) {
			fakes := &admitFakes{allowed: false, max: 1}
			svcs := gatedServices(t, build(t).registry, lockTenantA, fakes)
			other := []byte("seed/" + wiringDB + "/other")
			meta := []byte("seed/" + wiringDB + "/meta")
			seed := []service.Op{{Type: service.OpPut, Key: other, Value: []byte("staged")}}
			if resp, err := svcs.Transaction.Execute(t.Context(), nil, seed, nil); err != nil || resp == nil || !resp.Succeeded {
				t.Fatalf("seed: %+v, %v", resp, err)
			}
			rm, ok := svcs.MVCC.(*sqliteRestoreMVCC)
			if !ok {
				t.Fatalf("gated MVCC %T is no *sqliteRestoreMVCC", svcs.MVCC)
			}
			swapped := *rm
			swapped.kv = &swapAfterRangeKV{ServiceKV: rm.kv, swap: func() {
				ops := []service.Op{{Type: service.OpDelete, Key: other}, {Type: service.OpPut, Key: meta, Value: []byte("staged")}}
				if resp, err := svcs.Transaction.Execute(t.Context(), nil, ops, nil); err != nil || resp == nil || !resp.Succeeded {
					t.Errorf("swap: %+v, %v", resp, err)
				}
			}}
			_, err := swapped.MovePrefix(t.Context(), &service.MovePrefixRequest{Src: "seed/", Dst: "sqlite/", ExpectedKeys: 1})
			var refused *service.WriteGuardRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("move after the source swap = %v, want service.WriteGuardRefusedError", err)
			}
			assertNoMeta(t, gatedStore(t, svcs))
			kvs, _, _, err := rm.kv.Range(t.Context(), []byte("sqlite/"), storage.PrefixEnd([]byte("sqlite/")), service.WithKeysOnly())
			if err != nil || len(kvs) != 0 {
				t.Fatalf("keys under sqlite/ after the refused move: %d, %v; want none", len(kvs), err)
			}
			if usage, _ := fakes.counts(); usage != 0 {
				t.Fatalf("usage after the refused move = %d, want 0", usage)
			}
		})
	}
}

// A listing shorter than ExpectedKeys is refused before anything is
// proposed: compares on the listed keys alone would leave room for a key
// added after inspection.
func TestSQLiteAdmitMovePrefix_ShortListingIsRefused(t *testing.T) {
	fakes := &admitFakes{allowed: true, max: 1}
	svcs := gatedServices(t, lockScenes["prefix"](t).registry, lockTenantA, fakes)
	mover := svcs.MVCC.(service.PrefixMover)
	_, err := mover.MovePrefix(t.Context(), &service.MovePrefixRequest{Src: "seed/", Dst: "sqlite/", ExpectedKeys: 1})
	var conflict *service.CommandConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("move of an empty source = %v, want service.CommandConflictError", err)
	}
}

// TestSQLiteAdmitMovePrefix_StagedPublishIsBounded checks that a staged
// publish (stage prefix -> page prefix of one database) carries no per-key
// compare: the publish proposal must not grow with the staged page count,
// or a large bulk write or VACUUM exceeds the log entry ceiling (SPEC
// sqlite-vfs Q2). The moved pages still land.
func TestSQLiteAdmitMovePrefix_StagedPublishIsBounded(t *testing.T) {
	const pages = 2048
	fakes := &admitFakes{allowed: true, max: 1}
	svcs := gatedServices(t, lockScenes["prefix"](t).registry, lockTenantA, fakes)
	rm, ok := svcs.MVCC.(*sqliteRestoreMVCC)
	if !ok {
		t.Fatalf("gated MVCC %T is no *sqliteRestoreMVCC", svcs.MVCC)
	}
	const commitID = "0123456789abcdef0123456789abcdef"
	src, err := sqlitevfs.StagePrefix(wiringDB, commitID)
	if err != nil {
		t.Fatalf("StagePrefix: %v", err)
	}
	dst, err := sqlitevfs.PagePrefix(wiringDB)
	if err != nil {
		t.Fatalf("PagePrefix: %v", err)
	}
	for start := uint32(1); start <= pages; start += 256 {
		ops := make([]service.Op, 0, 256)
		for pgno := start; pgno < start+256 && pgno <= pages; pgno++ {
			key, err := sqlitevfs.StageKey(wiringDB, commitID, pgno)
			if err != nil {
				t.Fatalf("StageKey: %v", err)
			}
			ops = append(ops, service.Op{Type: service.OpPut, Key: key, Value: []byte("page")})
		}
		if resp, err := svcs.Transaction.Execute(t.Context(), nil, ops, nil); err != nil || resp == nil || !resp.Succeeded {
			t.Fatalf("seed stage pages: %+v, %v", resp, err)
		}
	}
	req := &service.MovePrefixRequest{Src: string(src), Dst: string(dst), ExpectedKeys: pages}
	moved, compares, err := rm.movedMeta(t.Context(), req)
	if err != nil {
		t.Fatalf("movedMeta: %v", err)
	}
	if len(moved) != 0 || len(compares) != 0 {
		t.Fatalf("staged publish of %d pages: %d admission ops, %d compares; want none (bounded proposal)", pages, len(moved), len(compares))
	}
	if _, err := rm.MovePrefix(t.Context(), req); err != nil {
		t.Fatalf("MovePrefix: %v", err)
	}
	kvs, _, _, err := rm.kv.Range(t.Context(), dst, storage.PrefixEnd(dst), service.WithKeysOnly())
	if err != nil || len(kvs) != pages {
		t.Fatalf("pages after the publish: %d, %v; want %d", len(kvs), err, pages)
	}
	if usage, _ := fakes.counts(); usage != 0 {
		t.Fatalf("usage after a publish that creates no database = %d, want 0", usage)
	}
}

func TestPrefixHoldsNoMeta(t *testing.T) {
	for prefix, want := range map[string]bool{
		"sqlite/" + wiringDB + "/page/":                                   true,
		"sqlite/" + wiringDB + "/stage/0123456789abcdef0123456789abcdef/": true,
		"sqlite/" + wiringDB + "/metadata":                                true,
		"sqlite/" + wiringDB + "/":                                        false,
		"sqlite/" + wiringDB + "/me":                                      false,
		"sqlite/" + wiringDB + "/meta":                                    false,
		"sqlite/":                                                         false,
		"seed/" + wiringDB + "/":                                          false,
		"":                                                                false,
	} {
		if got := prefixHoldsNoMeta(prefix); got != want {
			t.Errorf("prefixHoldsNoMeta(%q) = %v, want %v", prefix, got, want)
		}
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
