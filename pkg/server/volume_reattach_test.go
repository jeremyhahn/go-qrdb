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
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// reattachTestLogger returns a logger that discards all output, used to keep
// reattach tests noise-free.
func reattachTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newReattachFixture builds a TenantService backed by a pooled,
// system-managed barrier registry whose system barrier is initialized and
// unsealed, suitable for exercising reattachVolumeTenants. Default
// entitlements permit volume isolation and the system-managed barrier so
// both modes can be created.
func newReattachFixture(t *testing.T) (*tenant.TenantService, *seal.BarrierRegistry) {
	t.Helper()

	dir := t.TempDir()
	c := local.New()
	if err := c.Configure(map[string]string{"path": dir}); err != nil {
		t.Fatalf("configure local backend: %v", err)
	}
	stateStorage, err := pebblestore.New(&pebblestore.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open state-store Pebble: %v", err)
	}
	t.Cleanup(func() { _ = stateStorage.Close() })
	state, err := qrdbpool.NewPebbleStateStore(stateStorage)
	if err != nil {
		t.Fatalf("NewPebbleStateStore: %v", err)
	}
	mgr := objpool.NewManager()
	if err := mgr.Register("default", []common.Storage{c}, &objpool.RoundRobinStrategy{State: state}); err != nil {
		t.Fatalf("pool Register: %v", err)
	}

	sysStrat, err := seal.NewSoftwareStrategy([]byte("reattach-system-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	system, err := seal.NewStorageBarrier(reattachTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "system/root-key"}, sysStrat)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := system.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}

	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	reg, err := seal.NewBarrierRegistry(reattachTestLogger(), system,
		seal.WithVolumePool(mgr, state, "default"),
		seal.WithPerTenantRegistry(perTenantReg),
	)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	svc, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, reattachTestLogger(),
		tenant.WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes:     []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
			AllowVolumeStorage:        true,
			AllowSystemManagedBarrier: true,
		}))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc, reg
}

// TestReattachVolumeTenants_AutoBuildsHost_SelfStaysSealed verifies the boot
// re-attach branch: a system-managed (auto-unseal) volume tenant is unsealed
// credential-free and its Host is built, while a self-managed tenant is left
// sealed and its Host is NOT built (the tenant admin must unseal first).
func TestReattachVolumeTenants_AutoBuildsHost_SelfStaysSealed(t *testing.T) {
	svc, reg := newReattachFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "vauto", "Auto", tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant(auto): %v", err)
	}
	if _, err := svc.CreateTenant(ctx, "vself", "Self", tenant.IsolationVolume); err != nil {
		t.Fatalf("CreateTenant(self): %v", err)
	}

	// Simulate shutdown: seal the auto barrier (the self one was never
	// initialized and is already sealed).
	if err := reg.SealTenant("vauto"); err != nil {
		t.Fatalf("SealTenant(vauto): %v", err)
	}

	hostReg := NewTenantHostRegistryFromLookup(reg, svc, reattachTestLogger())
	t.Cleanup(func() { _ = hostReg.CloseAll() })

	if err := reattachVolumeTenants(ctx, svc, reg, hostReg, reattachTestLogger()); err != nil {
		t.Fatalf("reattachVolumeTenants: %v", err)
	}

	// Auto tenant: barrier unsealed and Host built (resolvable).
	autoTB, err := reg.GetTenant("vauto")
	if err != nil {
		t.Fatalf("GetTenant(vauto): %v", err)
	}
	if autoTB.IsSealed() {
		t.Error("auto tenant barrier should be unsealed after reattach")
	}
	if _, err := hostReg.Services(ctx, "vauto"); err != nil {
		t.Errorf("auto tenant Host should be built after reattach: %v", err)
	}

	// Self-managed tenant: barrier still sealed, Host NOT built.
	selfTB, err := reg.GetTenant("vself")
	if err != nil {
		t.Fatalf("GetTenant(vself): %v", err)
	}
	if !selfTB.IsSealed() {
		t.Error("self-managed tenant barrier must remain sealed after reattach")
	}
	if _, err := hostReg.Services(ctx, "vself"); err == nil {
		t.Error("self-managed tenant Host must NOT be built while sealed")
	}
}

// TestReattachVolumeTenants_AttachError verifies that reattachVolumeTenants
// returns *VolumeReattachError when hostReg.Attach fails because a file at
// {DataRoot}/databases blocks directory creation.
func TestReattachVolumeTenants_AttachError(t *testing.T) {
	svc, reg := newReattachFixture(t)
	ctx := context.Background()

	// Create a system-managed (auto-unseal) volume tenant.
	info, err := svc.CreateTenant(ctx, "vattach-err", "AttachErr",
		tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true))
	if err != nil {
		t.Fatalf("CreateTenant(vattach-err): %v", err)
	}

	// Block newTenantHost from creating {DataRoot}/databases/{wal,nodehost} by
	// pre-creating a plain file at {DataRoot}/databases.
	dbsPath := filepath.Join(info.DataRoot, "databases")
	if err := os.WriteFile(dbsPath, []byte("block"), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", dbsPath, err)
	}

	// Seal the tenant barrier to simulate a daemon restart.
	if err := reg.SealTenant("vattach-err"); err != nil {
		t.Fatalf("SealTenant(vattach-err): %v", err)
	}

	hostReg := NewTenantHostRegistryFromLookup(reg, svc, reattachTestLogger())
	t.Cleanup(func() { _ = hostReg.CloseAll() })

	rerr := reattachVolumeTenants(ctx, svc, reg, hostReg, reattachTestLogger())
	if rerr == nil {
		t.Fatal("reattachVolumeTenants must return an error when Attach fails")
	}
	var vrErr *VolumeReattachError
	if !errors.As(rerr, &vrErr) {
		t.Errorf("expected *VolumeReattachError from Attach failure, got %T: %v", rerr, rerr)
	}
}
