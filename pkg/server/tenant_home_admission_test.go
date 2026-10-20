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
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// requireMigrationRefusal fails t unless err is the refusal Services returns
// while tenantID migrates: a *TenantHostError wrapping the retryable
// ErrTenantMigrationInProgress.
func requireMigrationRefusal(t *testing.T, what string, err error) {
	t.Helper()
	var hostErr *TenantHostError
	if !errors.Is(err, ErrTenantMigrationInProgress) || !errors.As(err, &hostErr) {
		t.Errorf("%s = %v, want a *TenantHostError wrapping ErrTenantMigrationInProgress", what, err)
	}
}

// homeOpsInFlight reads tenantID's admitted home-store operation count.
func homeOpsInFlight(t *testing.T, r *TenantHostRegistry, tenantID string) int {
	t.Helper()
	g, err := r.homeOpGate(tenantID, "test")
	if err != nil {
		t.Fatalf("homeOpGate(%q): %v", tenantID, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inflight
}

// notAHomeOpGate is a value the registry never stores for a tenant: held in
// homeOps, it is a gate record that cannot be read.
type notAHomeOpGate struct{}

// requireGateInvalid fails t unless err is the TenantHostError for tenantID
// and op wrapping ErrHomeOpGateInvalid.
func requireGateInvalid(t *testing.T, err error, tenantID, op string) {
	t.Helper()
	var hostErr *TenantHostError
	if !errors.Is(err, ErrHomeOpGateInvalid) || !errors.As(err, &hostErr) || hostErr.TenantID != tenantID || hostErr.Op != op {
		t.Fatalf("err = %v, want the %s *TenantHostError for %s wrapping ErrHomeOpGateInvalid", err, op, tenantID)
	}
}

func TestTenantHostRegistry_homeOpGate_CreatesOncePerTenant(t *testing.T) {
	r := NewTenantHostRegistry(nil, nil, nil)
	first, err := r.homeOpGate("t1", "home")
	if err != nil {
		t.Fatalf("homeOpGate: %v", err)
	}
	again, err := r.homeOpGate("t1", "home")
	if err != nil || again != first {
		t.Fatalf("second homeOpGate(t1) = (%p, %v), want the first gate %p", again, err, first)
	}
	other, err := r.homeOpGate("t2", "home")
	if err != nil || other == first {
		t.Fatalf("homeOpGate(t2) = (%p, %v), want a gate of its own", other, err)
	}
}

func TestTenantHostRegistry_homeOpGate_InvalidRecordErrors(t *testing.T) {
	r := NewTenantHostRegistry(nil, nil, nil)
	r.homeOps.Store("t1", notAHomeOpGate{})
	g, err := r.homeOpGate("t1", "some-op")
	if g != nil {
		t.Fatalf("homeOpGate over an invalid record returned gate %p, want none", g)
	}
	requireGateInvalid(t, err, "t1", "some-op")
}

func TestTenantHostRegistry_AdmitHome_RefusesOverAnInvalidGate(t *testing.T) {
	r := NewTenantHostRegistry(nil, nil, nil)
	r.homeOps.Store("t1", notAHomeOpGate{})
	release, err := r.AdmitHome("t1")
	if release != nil {
		t.Fatal("a refused admission must return no release")
	}
	requireGateInvalid(t, err, "t1", "home")
	if release, err := r.AdmitHome("t2"); err != nil {
		t.Fatalf("AdmitHome(t2) = %v; another tenant's invalid gate must not refuse it", err)
	} else {
		release()
	}
}

func TestTenantHostRegistry_drainHomeOps_InvalidGateErrors(t *testing.T) {
	r := NewTenantHostRegistry(nil, nil, nil)
	r.homeOps.Store("t1", notAHomeOpGate{})
	r.onHomeDrainWait = func(string) { t.Error("a drain over an invalid gate must not wait") }
	requireGateInvalid(t, r.drainHomeOps(context.Background(), "t1"), "t1", "migrate-drain-home")
}

func TestTenantHostRegistry_AdmitHome_RefusesWhileMigrating(t *testing.T) {
	r := NewTenantHostRegistry(nil, nil, nil)
	r.migrating.Store("t1", struct{}{})
	release, err := r.AdmitHome("t1")
	requireMigrationRefusal(t, "AdmitHome during a migration", err)
	if release != nil {
		t.Fatal("a refused admission must return no release")
	}
	var hostErr *TenantHostError
	if errors.As(err, &hostErr) && (hostErr.TenantID != "t1" || hostErr.Op != "home") {
		t.Fatalf("refusal = %+v, want tenant t1, op home", hostErr)
	}
	if n := homeOpsInFlight(t, r, "t1"); n != 0 {
		t.Fatalf("in flight after a refusal = %d, want 0", n)
	}
	// Another tenant is not fenced by t1's migration.
	other, err := r.AdmitHome("t2")
	if err != nil {
		t.Fatalf("AdmitHome(t2) during t1's migration = %v, want admitted", err)
	}
	other()
}

func TestTenantHostRegistry_AdmitHome_CountsUntilReleasedOnce(t *testing.T) {
	r := NewTenantHostRegistry(nil, nil, nil)
	first, err := r.AdmitHome("t1")
	if err != nil {
		t.Fatalf("AdmitHome: %v", err)
	}
	second, err := r.AdmitHome("t1")
	if err != nil {
		t.Fatalf("AdmitHome: %v", err)
	}
	if n := homeOpsInFlight(t, r, "t1"); n != 2 {
		t.Fatalf("in flight = %d, want 2", n)
	}
	first()
	first() // a second release of the same admission is a no-op
	if n := homeOpsInFlight(t, r, "t1"); n != 1 {
		t.Fatalf("in flight after a doubled release = %d, want 1", n)
	}
	second()
	if n := homeOpsInFlight(t, r, "t1"); n != 0 {
		t.Fatalf("in flight after both releases = %d, want 0", n)
	}
}

func TestTenantHostRegistry_AdmitHome_NilRegistryAdmits(t *testing.T) {
	var r *TenantHostRegistry
	release, err := r.AdmitHome("t1")
	if err != nil || release == nil {
		t.Fatalf("nil registry AdmitHome = (%v, %v), want an admission", release != nil, err)
	}
	release()
}

func TestTenantHostRegistryHolder_AdmitHome(t *testing.T) {
	t.Run("nil holder admits", func(t *testing.T) {
		var h *TenantHostRegistryHolder
		release, err := h.AdmitHome("t1")
		if err != nil {
			t.Fatalf("AdmitHome = %v, want admitted", err)
		}
		release()
	})
	t.Run("empty holder admits", func(t *testing.T) {
		release, err := (&TenantHostRegistryHolder{}).AdmitHome("t1")
		if err != nil {
			t.Fatalf("AdmitHome = %v, want admitted", err)
		}
		release()
	})
	t.Run("loaded holder uses the registry's guard", func(t *testing.T) {
		r := NewTenantHostRegistry(nil, nil, nil)
		h := &TenantHostRegistryHolder{}
		h.Store(r)
		r.migrating.Store("t1", struct{}{})
		_, err := h.AdmitHome("t1")
		requireMigrationRefusal(t, "holder AdmitHome during a migration", err)
	})
}

func TestTenantHostRegistry_drainHomeOps_NoneInFlightReturns(t *testing.T) {
	r := NewTenantHostRegistry(nil, nil, nil)
	r.onHomeDrainWait = func(string) { t.Error("a drain with nothing in flight must not wait") }
	if err := r.drainHomeOps(context.Background(), "t1"); err != nil {
		t.Fatalf("drainHomeOps = %v, want nil", err)
	}
}

func TestTenantHostRegistry_drainHomeOps_WaitsForRelease(t *testing.T) {
	r := NewTenantHostRegistry(nil, nil, nil)
	held := make([]func(), 2)
	for i := range held {
		release, err := r.AdmitHome("t1")
		if err != nil {
			t.Fatalf("AdmitHome: %v", err)
		}
		held[i] = release
	}
	r.migrating.Store("t1", struct{}{})
	waiting := make(chan struct{})
	r.onHomeDrainWait = func(string) { close(waiting) }
	var released atomic.Int32
	done := make(chan error, 1)
	go func() { done <- r.drainHomeOps(context.Background(), "t1") }()
	<-waiting
	if _, err := r.AdmitHome("t1"); err == nil {
		t.Fatal("AdmitHome while a drain waits must be refused")
	}
	for _, release := range held {
		released.Add(1)
		release()
	}
	if err := <-done; err != nil {
		t.Fatalf("drainHomeOps = %v, want nil", err)
	}
	if n := released.Load(); n != int32(len(held)) {
		t.Fatalf("drain returned after %d of %d releases", n, len(held))
	}
}

func TestTenantHostRegistry_drainHomeOps_ContextEnds(t *testing.T) {
	r := NewTenantHostRegistry(nil, nil, nil)
	release, err := r.AdmitHome("t1")
	if err != nil {
		t.Fatalf("AdmitHome: %v", err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	r.onHomeDrainWait = func(string) { cancel() }
	err = r.drainHomeOps(ctx, "t1")
	var hostErr *TenantHostError
	if !errors.Is(err, context.Canceled) || !errors.As(err, &hostErr) || hostErr.Op != "migrate-drain-home" {
		t.Fatalf("drainHomeOps on an ended context = %v, want a migrate-drain-home *TenantHostError wrapping context.Canceled", err)
	}
}

// admittedHomesFixture returns a standalone tenant "vt" with a live Host,
// its home source gated by the host registry exactly as a standalone node
// wires it, and records already in the home store.
func admittedHomesFixture(t *testing.T) (*seal.BarrierRegistry, *TenantHostRegistry, *tenant.LocalHomes, map[string]string) {
	t.Helper()
	ctx := context.Background()
	reg, hostReg, _ := migrateTestTenant(t, "vt")
	if _, err := hostReg.Services(ctx, "vt"); err != nil {
		t.Fatalf("Services(pre-migrate): %v", err)
	}
	homes, err := tenant.NewLocalHomes(reg, tenant.WithHomeAdmission(hostReg))
	if err != nil {
		t.Fatalf("NewLocalHomes: %v", err)
	}
	records := map[string]string{
		"iam/users/alice": "alice-record",
		"ca/root":         "ca-material",
		"dek/keyring":     "dek-keyring",
	}
	home, err := homes.HomeStore(ctx, "vt")
	if err != nil {
		t.Fatalf("HomeStore: %v", err)
	}
	for k, v := range records {
		if err := home.Put(ctx, k, []byte(v)); err != nil {
			t.Fatalf("home.Put(%q): %v", k, err)
		}
	}
	return reg, hostReg, homes, records
}

func passphraseMigration(ctx context.Context) seal.MigrationOptions {
	return seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(ctx, []byte("tenant-migrate-pw")),
	}
}

// A write racing a standalone migration at each journal checkpoint -- the
// begin, staging, verify and commit -- through a store obtained before the
// migration or a fresh one, is refused with the retryable error Services
// returns, and changes nothing. After the commit, the same held store writes
// under the keyed barrier, and a store built afresh over it reads the write.
func TestTenantHostRegistry_MigrateEncryption_FencesStandaloneHomeWrites(t *testing.T) {
	ctx := context.Background()
	reg, hostReg, homes, records := admittedHomesFixture(t)
	held, err := homes.HomeStore(ctx, "vt")
	if err != nil {
		t.Fatalf("HomeStore: %v", err)
	}
	heldAtomic, ok := held.(kvstore.AtomicKVStore)
	if !ok {
		t.Fatal("the home store must keep the barrier's atomic capability")
	}

	var seen []seal.MigrationPhase
	hostReg.onMigrationPhase = func(tenantID string, phase seal.MigrationPhase) {
		seen = append(seen, phase)
		key := "iam/users/race-" + string(phase)
		requireMigrationRefusal(t, string(phase)+": held Put", held.Put(ctx, key, []byte("racer")))
		requireMigrationRefusal(t, string(phase)+": held overwrite", held.Put(ctx, "iam/users/alice", []byte("racer")))
		_, err := heldAtomic.CompareAndSwap(ctx, "ca/root", []byte("ca-material"), []byte("racer"))
		requireMigrationRefusal(t, string(phase)+": held CompareAndSwap", err)
		_, err = held.Get(ctx, "dek/keyring")
		requireMigrationRefusal(t, string(phase)+": held Get", err)
		fresh, err := homes.HomeStore(ctx, tenantID)
		if err != nil {
			t.Errorf("%s: HomeStore = %v", phase, err)
			return
		}
		requireMigrationRefusal(t, string(phase)+": fresh Put", fresh.Put(ctx, key, []byte("racer")))
	}
	if err := hostReg.MigrateEncryption(ctx, "vt", passphraseMigration(ctx)); err != nil {
		t.Fatalf("MigrateEncryption: %v", err)
	}
	want := []seal.MigrationPhase{seal.PhasePending, seal.PhaseCopying, seal.PhaseVerifying, seal.PhaseCommitting}
	if len(seen) != len(want) {
		t.Fatalf("observed checkpoints %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("observed checkpoints %v, want %v", seen, want)
		}
	}

	// A home source built after the migration has no cache from before it.
	after, err := tenant.NewLocalHomes(reg)
	if err != nil {
		t.Fatalf("NewLocalHomes: %v", err)
	}
	keyedHome, err := after.HomeStore(ctx, "vt")
	if err != nil {
		t.Fatalf("HomeStore(post-migrate): %v", err)
	}
	for k, v := range records {
		got, err := keyedHome.Get(ctx, k)
		if err != nil || string(got) != v {
			t.Fatalf("Get(%q) after the migration = (%q, %v), want the pre-migration %q", k, got, err, v)
		}
	}
	for _, phase := range want {
		key := "iam/users/race-" + string(phase)
		if ok, err := keyedHome.Exists(ctx, key); err != nil || ok {
			t.Fatalf("Exists(%q) = (%v, %v); a refused write must leave nothing", key, ok, err)
		}
	}

	if err := held.Put(ctx, "iam/users/post", []byte("post-record")); err != nil {
		t.Fatalf("held Put after the migration = %v, want admitted", err)
	}
	got, err := keyedHome.Get(ctx, "iam/users/post")
	if err != nil || string(got) != "post-record" {
		t.Fatalf("keyed Get of the held store's post-migration write = (%q, %v); it must land under the keyed barrier", got, err)
	}
	if n := homeOpsInFlight(t, hostReg, "vt"); n != 0 {
		t.Fatalf("home operations in flight after the migration = %d, want 0", n)
	}
}

// A home-store operation in flight when the migration begins -- a Scan
// whose callback is still running -- holds the migration before its first
// write to the barrier: the drain waits for it, a write it attempts inside
// the window is refused, and staging starts only after it returns.
func TestTenantHostRegistry_MigrateEncryption_DrainsInFlightHomeScan(t *testing.T) {
	ctx := context.Background()
	_, hostReg, homes, records := admittedHomesFixture(t)
	home, err := homes.HomeStore(ctx, "vt")
	if err != nil {
		t.Fatalf("HomeStore: %v", err)
	}

	draining := make(chan struct{})
	hostReg.onHomeDrainWait = func(string) { close(draining) }
	var callbackReturned atomic.Bool
	var phaseMu sync.Mutex
	var staged bool
	hostReg.onMigrationPhase = func(_ string, phase seal.MigrationPhase) {
		if phase != seal.PhaseCopying {
			return
		}
		phaseMu.Lock()
		staged = true
		phaseMu.Unlock()
		if !callbackReturned.Load() {
			t.Error("the migration reached staging while an admitted Scan was still running")
		}
	}

	migrated := make(chan error, 1)
	scanned := 0
	err = home.Scan(ctx, "iam/", func(string, []byte) error {
		scanned++
		go func() { migrated <- hostReg.MigrateEncryption(ctx, "vt", passphraseMigration(ctx)) }()
		select {
		case <-draining:
		case merr := <-migrated:
			t.Errorf("the migration returned (%v) without draining the in-flight Scan", merr)
			migrated <- merr
			return nil
		}
		requireMigrationRefusal(t, "Put inside the drained Scan", home.Put(ctx, "iam/users/inside", []byte("racer")))
		phaseMu.Lock()
		early := staged
		phaseMu.Unlock()
		if early {
			t.Error("staging started before the in-flight Scan returned")
		}
		callbackReturned.Store(true)
		return nil
	})
	if err != nil || scanned != 1 {
		t.Fatalf("Scan = %v after %d records, want one record", err, scanned)
	}
	if err := <-migrated; err != nil {
		t.Fatalf("MigrateEncryption: %v", err)
	}

	for k, v := range records {
		got, err := home.Get(ctx, k)
		if err != nil || string(got) != v {
			t.Fatalf("Get(%q) after the migration = (%q, %v), want %q", k, got, err, v)
		}
	}
	if ok, err := home.Exists(ctx, "iam/users/inside"); err != nil || ok {
		t.Fatalf("Exists(inside) = (%v, %v); the refused write must leave nothing", ok, err)
	}
}

// A drain that cannot finish fails the migration with the drain's error and
// rolls it back: the tenant keeps its records and its home store serves
// again once the in-flight operation releases.
func TestTenantHostRegistry_MigrateEncryption_DrainCanceledRollsBack(t *testing.T) {
	_, hostReg, homes, records := admittedHomesFixture(t)
	release, err := hostReg.AdmitHome("vt")
	if err != nil {
		t.Fatalf("AdmitHome: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hostReg.onHomeDrainWait = func(string) { cancel() }
	err = hostReg.MigrateEncryption(ctx, "vt", passphraseMigration(context.Background()))
	var hostErr *TenantHostError
	if !errors.Is(err, context.Canceled) || !errors.As(err, &hostErr) || hostErr.Op != "migrate-drain-home" {
		t.Fatalf("MigrateEncryption with an undrainable home store = %v, want the migrate-drain-home error", err)
	}
	release()

	bg := context.Background()
	home, err := homes.HomeStore(bg, "vt")
	if err != nil {
		t.Fatalf("HomeStore: %v", err)
	}
	for k, v := range records {
		got, err := home.Get(bg, k)
		if err != nil || string(got) != v {
			t.Fatalf("Get(%q) after the rolled-back migration = (%q, %v), want %q", k, got, err, v)
		}
	}
	if err := home.Put(bg, "iam/users/after", []byte("v")); err != nil {
		t.Fatalf("Put after the rolled-back migration = %v, want admitted", err)
	}
}

// A migration that cannot read the tenant's home-operation gate cannot know
// that no home-store operation is in flight, so it fails closed with the
// gate's error before staging and rolls back: the tenant keeps its records,
// under its original barrier, and Services serves it again.
func TestTenantHostRegistry_MigrateEncryption_InvalidHomeGateFailsClosed(t *testing.T) {
	ctx := context.Background()
	reg, hostReg, _, records := admittedHomesFixture(t)
	hostReg.homeOps.Store("vt", notAHomeOpGate{})
	hostReg.onMigrationPhase = func(_ string, phase seal.MigrationPhase) {
		if phase != seal.PhasePending {
			t.Errorf("the migration reached %s over an invalid home gate", phase)
		}
	}
	err := hostReg.MigrateEncryption(ctx, "vt", passphraseMigration(ctx))
	requireGateInvalid(t, err, "vt", "migrate-drain-home")

	if _, err := hostReg.Services(ctx, "vt"); err != nil {
		t.Fatalf("Services after the failed migration = %v; the migration guard must be lifted", err)
	}
	homes, err := tenant.NewLocalHomes(reg)
	if err != nil {
		t.Fatalf("NewLocalHomes: %v", err)
	}
	home, err := homes.HomeStore(ctx, "vt")
	if err != nil {
		t.Fatalf("HomeStore: %v", err)
	}
	for k, v := range records {
		got, err := home.Get(ctx, k)
		if err != nil || string(got) != v {
			t.Fatalf("Get(%q) after the failed migration = (%q, %v), want %q", k, got, err, v)
		}
	}
}
