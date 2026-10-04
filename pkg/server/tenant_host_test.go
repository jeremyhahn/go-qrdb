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
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func volTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newVolumePooledRegistry builds a BarrierRegistry backed by a single-path
// local volume pool and a per-tenant Pebble registry, suitable for registering
// IsolationVolume tenants in package-server tests. The system barrier uses the
// software (passphrase) strategy, so tenants inherit an encrypted posture.
func newVolumePooledRegistry(t *testing.T) *seal.BarrierRegistry {
	t.Helper()
	sysStrat, err := seal.NewSoftwareStrategy(context.Background(), []byte("vol-host-test"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	return newVolumePooledRegistryWith(t, sysStrat)
}

// newVolumePooledRegistryWith builds the same volume-pooled registry as
// newVolumePooledRegistry but seals the system barrier with the supplied
// strategy. Pass seal.NewNoneStrategy() to register UNENCRYPTED volume tenants
// (the source state for an enable-encryption migration test).
func newVolumePooledRegistryWith(t *testing.T, sysStrat seal.SealingStrategy) *seal.BarrierRegistry {
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

	sysStore := seal.NewMemoryBackend()
	system, err := seal.NewStorageBarrier(volTestLogger(), sysStore,
		seal.BarrierConfig{RootKeyPath: "system/root-key"}, sysStrat)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	reg, err := seal.NewBarrierRegistry(volTestLogger(), system,
		seal.WithVolumePool(mgr, state, "default"),
		seal.WithPerTenantRegistry(perTenantReg),
	)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	// RegisterVolumeTenant/ReattachVolumeTenant refuse with ErrSealed while
	// the system barrier is sealed; every caller of this helper registers a
	// volume tenant immediately, so the system barrier must be initialized
	// (and so unsealed) up front.
	if err := system.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	return reg
}

// registerVolumeTenant registers and unseals a volume tenant barrier, returning
// the DataRoot and the live barrier.
func registerVolumeTenant(t *testing.T, reg *seal.BarrierRegistry, id string) (string, *seal.TenantBarrier) {
	t.Helper()
	ctx := context.Background()
	dataRoot, tb, err := reg.RegisterVolumeTenant(ctx, id, false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant(%s): %v", id, err)
	}
	if err := reg.InitializeTenant(ctx, id, seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(%s): %v", id, err)
	}
	return dataRoot, tb
}

// TestTenantHost_LifecycleAndDurability builds a per-tenant Host, exercises the
// full engines (KV + MVCC), closes it (releasing the Pebble lock), then re-opens
// at the same DataRoot and verifies the data persisted — proving the Pebble lock
// is released on Close and re-attach recovers durable state.
func TestTenantHost_LifecycleAndDurability(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	dataRoot, tb := registerVolumeTenant(t, reg, "t1")
	ctx := context.Background()

	th, err := newTenantHost(context.Background(), "t1", dataRoot, tb, volTestLogger())
	if err != nil {
		t.Fatalf("newTenantHost: %v", err)
	}

	// KV engine round-trip.
	if err := th.services.KV.Put(ctx, "alpha", []byte("one")); err != nil {
		t.Fatalf("KV.Put: %v", err)
	}
	got, err := th.services.KV.Get(ctx, "alpha")
	if err != nil {
		t.Fatalf("KV.Get: %v", err)
	}
	if string(got) != "one" {
		t.Errorf("KV.Get = %q, want %q", got, "one")
	}

	// MVCC engine round-trip (proves the full state machine, not KV-only).
	if err := th.services.MVCC.Put(ctx, "beta", []byte("two")); err != nil {
		t.Fatalf("MVCC.Put: %v", err)
	}
	mv, err := th.services.MVCC.Get(ctx, "beta")
	if err != nil {
		t.Fatalf("MVCC.Get: %v", err)
	}
	if string(mv) != "two" {
		t.Errorf("MVCC.Get = %q, want %q", mv, "two")
	}

	// Close releases the Pebble lock on {DataRoot}/databases. Then mirror the
	// daemon lifecycle across a restart: seal the tenant barrier on shutdown
	// and re-unseal it on boot (reattachVolumeTenants) before rebuilding the
	// Host, so the re-opened Host can decrypt the existing WAL.
	if err := th.Close(); err != nil {
		t.Fatalf("tenantHost.Close: %v", err)
	}
	if err := reg.SealTenant("t1"); err != nil {
		t.Fatalf("SealTenant (shutdown): %v", err)
	}
	if err := reg.UnsealTenant(ctx, "t1", seal.Credentials{}); err != nil {
		t.Fatalf("UnsealTenant (boot re-attach): %v", err)
	}

	// Re-open at the same DataRoot: must succeed (lock released) and recover data.
	th2, err := newTenantHost(context.Background(), "t1", dataRoot, tb, volTestLogger())
	if err != nil {
		t.Fatalf("newTenantHost (re-open): %v", err)
	}
	t.Cleanup(func() { _ = th2.Close() })

	got, err = getWhenReadable(ctx, t, th2.services.KV.Get, "alpha")
	if err != nil {
		t.Fatalf("KV.Get after re-open: %v", err)
	}
	if string(got) != "one" {
		t.Errorf("KV.Get after re-open = %q, want %q (data did not persist)", got, "one")
	}
}

// TestTenantHost_FullEngineIsolationAndEncryption proves that two volume
// tenants are isolated across all engines, that the time-series engine works
// (full engines, not KV-only), and that tenant data is encrypted at rest under
// the tenant DEK (the plaintext value never appears in the on-disk files).
func TestTenantHost_FullEngineIsolationAndEncryption(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	ctx := context.Background()

	rootA, tbA := registerVolumeTenant(t, reg, "ta")
	rootB, tbB := registerVolumeTenant(t, reg, "tb")

	ha, err := newTenantHost(context.Background(), "ta", rootA, tbA, volTestLogger())
	if err != nil {
		t.Fatalf("newTenantHost(ta): %v", err)
	}
	defer func() { _ = ha.Close() }()
	hb, err := newTenantHost(context.Background(), "tb", rootB, tbB, volTestLogger())
	if err != nil {
		t.Fatalf("newTenantHost(tb): %v", err)
	}
	defer func() { _ = hb.Close() }()

	const secret = "PLAINTEXT-SECRET-VALUE-TA-9173"

	// Same key, different values per tenant — proves keyspace isolation.
	if err := ha.services.KV.Put(ctx, "shared/key", []byte(secret)); err != nil {
		t.Fatalf("ta KV.Put: %v", err)
	}
	if err := hb.services.KV.Put(ctx, "shared/key", []byte("tenant-b-value")); err != nil {
		t.Fatalf("tb KV.Put: %v", err)
	}
	got, err := ha.services.KV.Get(ctx, "shared/key")
	if err != nil || string(got) != secret {
		t.Fatalf("ta KV.Get = %q, %v; want %q", got, err, secret)
	}
	gotB, err := hb.services.KV.Get(ctx, "shared/key")
	if err != nil || string(gotB) != "tenant-b-value" {
		t.Fatalf("tb KV.Get = %q, %v; want tenant-b-value", gotB, err)
	}

	// MVCC isolation: a key written on ta must not be visible on tb.
	if err := ha.services.MVCC.Put(ctx, "mvcc/only-ta", []byte("m")); err != nil {
		t.Fatalf("ta MVCC.Put: %v", err)
	}
	if _, err := hb.services.MVCC.Get(ctx, "mvcc/only-ta"); err == nil {
		t.Error("tb MVCC.Get found ta-only key — isolation breach")
	}

	// Time-series engine reached through the same seam (proves the full state
	// machine, not KV-only).
	assertTimeSeriesLeg(ctx, t, ha.services.TimeSeries, "ta ", "cpu")

	// Encryption at rest: close ta's host so files flush, then scan every file
	// under its DataRoot/databases for the plaintext secret. It must not appear.
	if err := ha.Close(); err != nil {
		t.Fatalf("ta Close: %v", err)
	}
	assertPlaintextAbsent(t, filepath.Join(rootA, "databases"), []byte(secret))
}

// assertPlaintextAbsent walks dir and fails if needle appears in any file,
// proving the on-disk state is encrypted rather than stored in plaintext.
func assertPlaintextAbsent(t *testing.T, dir string, needle []byte) {
	t.Helper()
	scanned := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// #nosec G122 -- the tree being walked is a t.TempDir() this test created and no other process can reach, so there is no window between the walk and the read for a path to be substituted.
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		scanned++
		if bytes.Contains(data, needle) {
			t.Errorf("plaintext secret found in %q — data is NOT encrypted at rest", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %q: %v", dir, err)
	}
	if scanned == 0 {
		t.Fatalf("no files scanned under %q", dir)
	}
}

// fakeVolumeTenantSvc satisfies tenant.TenantServicer by embedding the
// interface; only GetTenant is implemented (the resolver's sole call).
type fakeVolumeTenantSvc struct {
	tenant.TenantServicer
	info *tenant.TenantInfo
}

func (f *fakeVolumeTenantSvc) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	if f.info != nil && f.info.ID == id {
		return f.info, nil
	}
	return nil, tenant.ErrTenantNotFound
}

// TestResolver_RoutesVolumeTenantToTenantHost verifies the transport-neutral
// resolver routes an IsolationVolume tenant to its per-tenant Host rather than
// the shared system service set, and that the composite namespace is applied.
func TestResolver_RoutesVolumeTenantToTenantHost(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	dataRoot, _ := registerVolumeTenant(t, reg, "vt")
	ctx := context.Background()

	source := &lookupDataRootSource{lookup: &fakeVolumeTenantSvc{
		info: &tenant.TenantInfo{ID: "vt", IsolationType: tenant.IsolationVolume, DataRoot: dataRoot},
	}}
	hostReg := NewTenantHostRegistry(reg, source, volTestLogger())
	t.Cleanup(func() { _ = hostReg.CloseAll() })

	dbReg := NewDatabaseServiceRegistry("system")
	rr := &restServiceRegistry{
		dbReg:        dbReg,
		barrierReg:   reg,
		tenantSvc:    &fakeVolumeTenantSvc{info: &tenant.TenantInfo{ID: "vt", IsolationType: tenant.IsolationVolume, DataRoot: dataRoot}},
		tenantHosts:  hostReg,
		systemTenant: "system",
	}

	// Resolve the KV service for the volume tenant's default database and
	// round-trip a value through it.
	kv, err := rr.KVService("vt", "")
	if err != nil {
		t.Fatalf("KVService(volume tenant): %v", err)
	}
	if err := kv.Put(ctx, "routed", []byte("yes")); err != nil {
		t.Fatalf("Put via resolver: %v", err)
	}
	got, err := kv.Get(ctx, "routed")
	if err != nil {
		t.Fatalf("Get via resolver: %v", err)
	}
	if string(got) != "yes" {
		t.Errorf("Get via resolver = %q, want %q", got, "yes")
	}

	// The write must land on the per-tenant Host under the composite namespace,
	// proving it routed to the tenant host (not the shared system services).
	base, err := hostReg.Services(ctx, "vt")
	if err != nil {
		t.Fatalf("hostReg.Services: %v", err)
	}
	prefix := tenant.ComposeNamespace("vt", "")
	raw, err := base.KV.Get(ctx, prefix+"routed")
	if err != nil {
		t.Fatalf("tenant host base KV.Get(%q): %v", prefix+"routed", err)
	}
	if string(raw) != "yes" {
		t.Errorf("tenant host base KV value = %q, want %q", raw, "yes")
	}
}

// TestTenantHostRegistry_LazyBuildAfterUnseal proves the self-managed flow: a
// sealed (uninitialized) volume tenant's Host is NOT built — Services fails
// closed with the sealed error — and the Host is built lazily only once the
// barrier is initialized + unsealed (the tenant-admin step).
func TestTenantHostRegistry_LazyBuildAfterUnseal(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	ctx := context.Background()

	// Self-managed registration: registered but NOT initialized -> sealed.
	dataRoot, tb, err := reg.RegisterVolumeTenant(ctx, "lazy", false)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if !tb.IsSealed() {
		t.Fatal("self-managed volume barrier must start sealed")
	}

	source := &lookupDataRootSource{lookup: &fakeVolumeTenantSvc{
		info: &tenant.TenantInfo{ID: "lazy", IsolationType: tenant.IsolationVolume, DataRoot: dataRoot},
	}}
	hostReg := NewTenantHostRegistry(reg, source, volTestLogger())
	t.Cleanup(func() { _ = hostReg.CloseAll() })

	// While sealed, the Host must not be built.
	if _, err := hostReg.Services(ctx, "lazy"); err == nil {
		t.Fatal("Host must not build while the self-managed barrier is sealed")
	}

	// Tenant-admin step: initialize (and thereby unseal) the barrier.
	if err := reg.InitializeTenant(ctx, "lazy", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}

	// Now the Host builds lazily on first access.
	svcs, err := hostReg.Services(ctx, "lazy")
	if err != nil {
		t.Fatalf("Host should build after unseal: %v", err)
	}
	if svcs == nil || svcs.KV == nil {
		t.Fatal("expected a live data-plane service set after unseal")
	}
}
