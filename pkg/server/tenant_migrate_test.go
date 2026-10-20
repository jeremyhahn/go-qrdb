// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// migrateTestTenant registers an UNENCRYPTED (StrategyNone) IsolationVolume
// tenant, wires a Host registry over it, and returns both plus the DataRoot.
func migrateTestTenant(t *testing.T, id string) (*seal.BarrierRegistry, *TenantHostRegistry, string) {
	t.Helper()
	reg := newVolumePooledRegistryWith(t, seal.NewNoneStrategy())
	dataRoot, _ := registerVolumeTenant(t, reg, id)

	source := &lookupDataRootSource{registry: reg}
	hostReg := NewTenantHostRegistry(reg, source, volTestLogger())
	t.Cleanup(func() { _ = hostReg.CloseAll() })
	return reg, hostReg, dataRoot
}

// TestTenantHostRegistry_MigrateEncryption_NoneToPassphrase proves the full
// per-tenant enable-encryption recycle: an unencrypted IsolationVolume tenant
// with live KV + MVCC data is migrated to the passphrase strategy in place, and
// every datum survives — both the state-machine Pebble and the Raft WAL are
// re-encrypted, the live tenant barrier transitions to keyed, and the Host is
// rebuilt and continues serving without data loss.
func TestTenantHostRegistry_MigrateEncryption_NoneToPassphrase(t *testing.T) {
	ctx := context.Background()
	reg, hostReg, _ := migrateTestTenant(t, "vt")

	// Seed data through the live (unencrypted) Host across both engines.
	svcs, err := hostReg.Services(ctx, "vt")
	if err != nil {
		t.Fatalf("Services(pre-migrate): %v", err)
	}
	if err := svcs.KV.Put(ctx, "kv-key", []byte("kv-value")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	if err := svcs.MVCC.Put(ctx, "mv-key", []byte("mv-value")); err != nil {
		t.Fatalf("MVCC.Put: %v", err)
	}

	// Sanity: the source barrier is genuinely unencrypted before migration.
	if got := tenantStrategy(t, reg, "vt"); got != seal.StrategyNone {
		t.Fatalf("pre-migrate tenant strategy = %q, want none", got)
	}

	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("tenant-migrate-pw")),
	}
	if err := hostReg.MigrateEncryption(ctx, "vt", opts); err != nil {
		t.Fatalf("MigrateEncryption: %v", err)
	}

	// The live tenant barrier is now keyed.
	if got := tenantStrategy(t, reg, "vt"); got != seal.StrategyPassphrase {
		t.Fatalf("post-migrate tenant strategy = %q, want passphrase", got)
	}

	// All data survives the recycle, readable through the rebuilt Host.
	svcs, err = hostReg.Services(ctx, "vt")
	if err != nil {
		t.Fatalf("Services(post-migrate): %v", err)
	}
	if got, err := getWhenReadable(ctx, t, svcs.KV.Get, "kv-key"); err != nil || string(got) != "kv-value" {
		t.Fatalf("KV.Get(post-migrate) = %q, %v; want kv-value", got, err)
	}
	if got, err := svcs.MVCC.Get(ctx, "mv-key"); err != nil || string(got) != "mv-value" {
		t.Fatalf("MVCC.Get(post-migrate) = %q, %v; want mv-value", got, err)
	}

	// Writes continue to work post-migration (the keyed barrier is fully live).
	if err := svcs.KV.Put(ctx, "kv-key2", []byte("after")); err != nil {
		t.Fatalf("KV.Put(post-migrate): %v", err)
	}
	if got, err := svcs.KV.Get(ctx, "kv-key2"); err != nil || string(got) != "after" {
		t.Fatalf("KV.Get(post-migrate new) = %q, %v; want after", got, err)
	}
}

// TestTenantHostRegistry_MigrateEncryption_RecodesStandaloneHomeRecords pins
// that a standalone tenant's home store -- its IAM records, CA material and
// DEK keyring, kept in the barrier's own backend by tenant.LocalHomes --
// reads back under the adopted root key, and that the migration leaves no
// staged copy behind.
func TestTenantHostRegistry_MigrateEncryption_RecodesStandaloneHomeRecords(t *testing.T) {
	ctx := context.Background()
	reg, hostReg, _ := migrateTestTenant(t, "vt")
	// The migration recodes the live Host's stores, so the Host must exist.
	if _, err := hostReg.Services(ctx, "vt"); err != nil {
		t.Fatalf("Services(pre-migrate): %v", err)
	}
	homes, err := tenant.NewLocalHomes(reg)
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
		t.Fatalf("HomeStore(pre-migrate): %v", err)
	}
	for k, v := range records {
		if err := home.Put(ctx, k, []byte(v)); err != nil {
			t.Fatalf("home.Put(%q): %v", k, err)
		}
	}

	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(ctx, []byte("tenant-migrate-pw")),
	}
	if err := hostReg.MigrateEncryption(ctx, "vt", opts); err != nil {
		t.Fatalf("MigrateEncryption: %v", err)
	}

	home, err = homes.HomeStore(ctx, "vt")
	if err != nil {
		t.Fatalf("HomeStore(post-migrate): %v", err)
	}
	for k, v := range records {
		got, err := home.Get(ctx, k)
		if err != nil || string(got) != v {
			t.Fatalf("home.Get(%q) under the adopted key = %q, %v; want %q", k, got, err, v)
		}
	}
	tb, err := reg.GetTenant("vt")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	staged, err := tb.Barrier().List(ctx, tb.Barrier().RootKeyPath()+migrateStagingSuffix+"/records/")
	if err != nil {
		t.Fatalf("List(staging): %v", err)
	}
	if len(staged) != 0 {
		t.Fatalf("migration left %d staged records behind: %v", len(staged), staged)
	}
}

// TestTenantHostRegistry_MigrateEncryption_NoneToShamir proves the Shamir
// target migration produces a GENUINE M-of-N Shamir barrier — not a silently
// mislabeled passphrase barrier — and that it survives the full lifecycle: the
// migration completes, the barrier reports strategy=shamir with the correct
// threshold/total, the tenant Host correctly stays detached (Services reports
// seal.ErrSealed rather than erroring the migration) because Shamir never
// auto-unseals, and submitting a quorum of the REAL distributed shares
// unseals it and recovers the pre-migration data with zero loss.
func TestTenantHostRegistry_MigrateEncryption_NoneToShamir(t *testing.T) {
	ctx := context.Background()
	reg, hostReg, _ := migrateTestTenant(t, "vt-shamir")

	// Wire a ceremony distributor on the tenant's dedicated barrier BEFORE
	// migrating — the same operational precondition a fresh
	// BarrierShamirInitialize has.
	tb, err := reg.GetTenant("vt-shamir")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	custody := sealtest.NewCustody("h1", "h2", "h3")
	custody.Install(tb.Barrier())

	// Seed data through the live (unencrypted) Host before migrating.
	svcs, err := hostReg.Services(ctx, "vt-shamir")
	if err != nil {
		t.Fatalf("Services(pre-migrate): %v", err)
	}
	if err := svcs.KV.Put(ctx, "shamir-key", []byte("shamir-value")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}

	opts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyShamir,
		Mechanism:       seal.MechanismSwap,
		Availability:    seal.AvailabilityQuiesced,
		ShamirThreshold: 2,
		ShamirTotal:     3,
	}
	if err := hostReg.MigrateEncryption(ctx, "vt-shamir", opts); err != nil {
		t.Fatalf("MigrateEncryption(shamir): %v", err)
	}
	for _, h := range []string{"h1", "h2", "h3"} {
		if custody.Distributor.Share(seal.DomainUnseal, h) == "" {
			t.Fatalf("holder %s received no share from the migration", h)
		}
	}

	// The live tenant barrier is genuinely Shamir — correct label, correct
	// M/N — and SEALED (Shamir never auto-unseals from disk, migration or
	// not).
	status := tenantStatus(t, reg, "vt-shamir")
	if status.Strategy != seal.StrategyShamir {
		t.Fatalf("post-migrate strategy = %q, want shamir (not silently downgraded to passphrase)", status.Strategy)
	}
	if !status.Sealed {
		t.Fatal("post-migrate shamir barrier must be sealed pending a share-holder quorum")
	}
	if status.ShamirThreshold != 2 || status.ShamirTotal != 3 {
		t.Fatalf("post-migrate scheme = %d/%d, want 2/3", status.ShamirThreshold, status.ShamirTotal)
	}

	// The tenant Host correctly stays detached: Services must fail with the
	// typed seal.ErrSealed, not silently serve stale (pre-migration) state and
	// not have aborted the migration.
	if _, err := hostReg.Services(ctx, "vt-shamir"); !errors.Is(err, seal.ErrSealed) {
		t.Fatalf("Services on a sealed post-migration tenant = %v, want seal.ErrSealed", err)
	}

	// Submitting a quorum (2-of-3) of the REAL distributed shares unseals the
	// migrated barrier — the "shares are usable" proof.
	tb2, err := reg.GetTenant("vt-shamir")
	if err != nil {
		t.Fatalf("GetTenant(post-migrate): %v", err)
	}
	progress, uerr := custody.Quorum(ctx, tb2.Barrier(), seal.DomainUnseal, seal.OpUnseal, "h1", "h2")
	if uerr != nil {
		t.Fatalf("unseal quorum: %v", uerr)
	}
	if !progress.Complete || tb2.IsSealed() {
		t.Fatalf("barrier not unsealed after a 2-of-3 quorum: progress=%+v sealed=%v", progress, tb2.IsSealed())
	}

	// Every datum written before the migration survives, readable once the
	// Host reattaches under the now-unsealed keyed barrier.
	if err := hostReg.Attach(ctx, "vt-shamir"); err != nil {
		t.Fatalf("Attach(post-unseal): %v", err)
	}
	svcs, err = hostReg.Services(ctx, "vt-shamir")
	if err != nil {
		t.Fatalf("Services(post-unseal): %v", err)
	}
	if got, gerr := getWhenReadable(ctx, t, svcs.KV.Get, "shamir-key"); gerr != nil || string(got) != "shamir-value" {
		t.Fatalf("KV.Get(post-unseal) = %q, %v; want shamir-value (DATA LOSS)", got, gerr)
	}
}

// TestTenantHostRegistry_MigrateEncryption_ShamirNoDistributor proves a Shamir
// target migration fails CLOSED with the same typed error a fresh
// BarrierShamirInitialize would give when no CeremonyDistributor is wired —
// rather than silently completing as a mislabeled passphrase barrier.
func TestTenantHostRegistry_MigrateEncryption_ShamirNoDistributor(t *testing.T) {
	ctx := context.Background()
	reg, hostReg, _ := migrateTestTenant(t, "vt-shamir-nodist")
	// No SetCeremonyDistributor call.

	opts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyShamir,
		Mechanism:       seal.MechanismSwap,
		Availability:    seal.AvailabilityQuiesced,
		ShamirThreshold: 2,
		ShamirTotal:     3,
	}
	if err := hostReg.MigrateEncryption(ctx, "vt-shamir-nodist", opts); !errors.Is(err, seal.ErrCeremonyDistributorNotConfigured) {
		t.Fatalf("MigrateEncryption(shamir, no distributor) = %v, want ErrCeremonyDistributorNotConfigured", err)
	}
	// The barrier must be untouched by the failed attempt.
	if got := tenantStrategy(t, reg, "vt-shamir-nodist"); got != seal.StrategyNone {
		t.Fatalf("tenant strategy after a rejected shamir migration = %q, want none (unaffected)", got)
	}
}

// TestTenantHostRegistry_MigrateEncryption_Idempotency proves a second migration
// attempt against an already-keyed tenant barrier is rejected (the source is no
// longer StrategyNone), preventing accidental double-migration.
func TestTenantHostRegistry_MigrateEncryption_AlreadyKeyedRejected(t *testing.T) {
	ctx := context.Background()
	_, hostReg, _ := migrateTestTenant(t, "vt2")
	if err := hostReg.Attach(ctx, "vt2"); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("pw")),
	}
	if err := hostReg.MigrateEncryption(ctx, "vt2", opts); err != nil {
		t.Fatalf("first MigrateEncryption: %v", err)
	}
	// Second attempt must fail: the barrier is already keyed (source != none).
	if err := hostReg.MigrateEncryption(ctx, "vt2", opts); err == nil {
		t.Fatal("second MigrateEncryption on a keyed barrier must be rejected")
	}
}

// TestTenantHostRegistry_MigrateEncryption_InvalidOpts proves option validation
// runs before any side effect (no target minted, Host untouched).
func TestTenantHostRegistry_MigrateEncryption_InvalidOpts(t *testing.T) {
	ctx := context.Background()
	_, hostReg, _ := migrateTestTenant(t, "vt3")

	// StrategyNone is not a valid migration target.
	err := hostReg.MigrateEncryption(ctx, "vt3", seal.MigrationOptions{
		TargetStrategy: seal.StrategyNone,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
	})
	if err == nil {
		t.Fatal("MigrateEncryption with StrategyNone target must be rejected")
	}
}

// TestScopedEncryptionMigrator_Routing exercises the server-level scope router
// that fronts the transport handlers: tenant scope routes to the Host registry,
// system scope to the configured system migrator, and missing handlers fail
// closed with ErrMigrationProviderUnavailable.
func TestScopedEncryptionMigrator_Routing(t *testing.T) {
	ctx := context.Background()

	// System scope with a wired system migrator delegates to it.
	sysCalled := false
	sysMig := encMigratorFunc(func(_ context.Context, scope service.BarrierScope, _ seal.MigrationOptions) error {
		if scope.Kind != service.ScopeSystem {
			t.Errorf("system migrator got scope %q", scope.Kind)
		}
		sysCalled = true
		return nil
	})
	m := &scopedEncryptionMigrator{system: sysMig}
	if err := m.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{}); err != nil {
		t.Fatalf("system route: %v", err)
	}
	if !sysCalled {
		t.Fatal("system migrator was not invoked")
	}

	// System scope without a system migrator fails closed.
	empty := &scopedEncryptionMigrator{}
	if err := empty.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{}); !errors.Is(err, service.ErrMigrationProviderUnavailable) {
		t.Fatalf("system route (unwired) = %v, want ErrMigrationProviderUnavailable", err)
	}

	// Tenant scope without a tenant registry fails closed.
	if err := (&scopedEncryptionMigrator{system: sysMig}).EnableEncryption(ctx,
		service.BarrierScope{Kind: service.ScopeTenant, TenantID: "x"}, seal.MigrationOptions{}); !errors.Is(err, service.ErrMigrationProviderUnavailable) {
		t.Fatalf("tenant route (unwired) = %v, want ErrMigrationProviderUnavailable", err)
	}

	// Unknown scope fails closed.
	if err := m.EnableEncryption(ctx, service.BarrierScope{Kind: "bogus"}, seal.MigrationOptions{}); !errors.Is(err, service.ErrMigrationProviderUnavailable) {
		t.Fatalf("unknown scope = %v, want ErrMigrationProviderUnavailable", err)
	}
}

// TestScopedEncryptionMigrator_SharedBarrierImmutable proves a tenant with no
// dedicated barrier (i.e. on the shared system barrier) is rejected with
// ErrSharedBarrierImmutable rather than reaching the recycle.
func TestScopedEncryptionMigrator_SharedBarrierImmutable(t *testing.T) {
	ctx := context.Background()
	reg, hostReg, _ := migrateTestTenant(t, "registered")

	m := &scopedEncryptionMigrator{tenants: hostReg, barrierReg: reg}
	err := m.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeTenant, TenantID: "not-a-tenant"},
		seal.MigrationOptions{TargetStrategy: seal.StrategyPassphrase, Credentials: seal.MustCredentials(context.Background(), []byte("pw"))})
	if !errors.Is(err, service.ErrSharedBarrierImmutable) {
		t.Fatalf("shared-barrier tenant = %v, want ErrSharedBarrierImmutable", err)
	}
}

// TestTenantHostRegistry_MigratingGuard proves that while a tenant is marked
// migrating, Services refuses to (re)build its Host with the retryable
// ErrTenantMigrationInProgress — preventing a concurrent build from wrapping the
// host with the stale source barrier during the recycle — and that clearing the
// guard restores normal builds.
func TestTenantHostRegistry_MigratingGuard(t *testing.T) {
	ctx := context.Background()
	reg, hostReg, _ := migrateTestTenant(t, "guarded")

	hostReg.migrating.Store("guarded", struct{}{})
	if _, err := hostReg.Services(ctx, "guarded"); !errors.Is(err, ErrTenantMigrationInProgress) {
		t.Fatalf("Services while migrating = %v, want ErrTenantMigrationInProgress", err)
	}

	hostReg.migrating.Delete("guarded")
	if _, err := hostReg.Services(ctx, "guarded"); err != nil {
		t.Fatalf("Services after clearing guard: %v", err)
	}
	_ = reg
}

// tenantStrategy returns the active sealing strategy of a tenant's barrier.
func tenantStrategy(t *testing.T, reg *seal.BarrierRegistry, id string) seal.StrategyID {
	t.Helper()
	tb, err := reg.GetTenant(id)
	if err != nil {
		t.Fatalf("GetTenant(%s): %v", id, err)
	}
	return tb.Status().Strategy
}

// tenantStatus returns the full barrier status of a tenant's barrier.
func tenantStatus(t *testing.T, reg *seal.BarrierRegistry, id string) *seal.BarrierStatus {
	t.Helper()
	tb, err := reg.GetTenant(id)
	if err != nil {
		t.Fatalf("GetTenant(%s): %v", id, err)
	}
	return tb.Status()
}

// encMigratorFunc adapts a function to service.EncryptionMigrator for tests.
type encMigratorFunc func(context.Context, service.BarrierScope, seal.MigrationOptions) error

func (f encMigratorFunc) EnableEncryption(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	return f(ctx, scope, opts)
}

// denyBarrierModeChecker denies a specific target strategy, modeling a tenant
// not entitled to migrate to it.
type denyBarrierModeChecker struct{ deny seal.StrategyID }

func (d denyBarrierModeChecker) CheckBarrierMode(_ context.Context, _ string, mode seal.StrategyID) error {
	if mode == d.deny {
		return errors.New("not entitled to barrier mode")
	}
	return nil
}

// TestScopedEncryptionMigrator_EntitlementGate proves a tenant-scope migration
// is rejected when the tenant is not entitled to the target strategy, BEFORE the
// recycle runs (the barrier mode is governed identically to barrier-init).
func TestScopedEncryptionMigrator_EntitlementGate(t *testing.T) {
	ctx := context.Background()
	reg, hostReg, _ := migrateTestTenant(t, "ent")

	m := &scopedEncryptionMigrator{
		tenants:    hostReg,
		barrierReg: reg,
		tenantSvc:  denyBarrierModeChecker{deny: seal.StrategyPassphrase},
	}
	err := m.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeTenant, TenantID: "ent"},
		seal.MigrationOptions{TargetStrategy: seal.StrategyPassphrase, Credentials: seal.MustCredentials(context.Background(), []byte("pw"))})
	if err == nil {
		t.Fatal("expected entitlement rejection for a non-entitled target strategy")
	}
	// The tenant barrier must remain unmigrated (still none).
	if got := tenantStrategy(t, reg, "ent"); got != seal.StrategyNone {
		t.Fatalf("tenant strategy = %q after rejected migration, want none", got)
	}
}
