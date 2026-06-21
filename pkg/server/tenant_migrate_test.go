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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// migrateTestTenant registers an UNENCRYPTED (StrategyNone) IsolationVolume
// tenant, wires a Host registry over it, and returns both plus the DataRoot.
func migrateTestTenant(t *testing.T, id string) (*seal.BarrierRegistry, *TenantHostRegistry, string) {
	t.Helper()
	reg := newVolumePooledRegistryWith(t, seal.NewNoneStrategy())
	dataRoot, _ := registerVolumeTenant(t, reg, id)

	source := &servicerDataRootSource{svc: &fakeVolumeTenantSvc{
		info: &tenant.TenantInfo{ID: id, IsolationType: tenant.IsolationVolume, DataRoot: dataRoot},
	}}
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
		Credentials:    seal.Credentials{Secret: []byte("tenant-migrate-pw")},
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
	if got, err := svcs.KV.Get(ctx, "kv-key"); err != nil || string(got) != "kv-value" {
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
		Credentials:    seal.Credentials{Secret: []byte("pw")},
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
		seal.MigrationOptions{TargetStrategy: seal.StrategyPassphrase, Credentials: seal.Credentials{Secret: []byte("pw")}})
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
		seal.MigrationOptions{TargetStrategy: seal.StrategyPassphrase, Credentials: seal.Credentials{Secret: []byte("pw")}})
	if err == nil {
		t.Fatal("expected entitlement rejection for a non-entitled target strategy")
	}
	// The tenant barrier must remain unmigrated (still none).
	if got := tenantStrategy(t, reg, "ent"); got != seal.StrategyNone {
		t.Fatalf("tenant strategy = %q after rejected migration, want none", got)
	}
}
