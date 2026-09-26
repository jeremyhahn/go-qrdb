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
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s: %v", path, err)
	}
	return false
}

// TestBuildTenantInfra_BootReclaimsATenantDeletedWhileTheNodeWasDown is the
// verification #469 names: a replica that was offline while a tenant was
// deleted reclaims that tenant's directories when it comes back, and a live
// tenant on the same node is untouched.
//
// The deletion is applied the way another node's DeleteTenant reaches this
// one -- through the replicated metadata store only: the tombstone, then the
// metadata delete. Nothing of this node's own teardown runs, which is the
// state a node that was down during the delete boots into.
func TestBuildTenantInfra_BootReclaimsATenantDeletedWhileTheNodeWasDown(t *testing.T) {
	ctx := context.Background()
	fx, reg, perTenantReg := newFreshRegistryFixture(t, &quota.Entitlements{
		AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
		AllowVolumeStorage:    true,
	})
	meta := seal.NewMemoryBackend() // the replicated tenant metadata store
	ents := tenant.WithDefaultEntitlements(&quota.Entitlements{
		AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
		AllowVolumeStorage:    true,
	})
	svc, err := tenant.NewTenantService(meta, reg, reattachTestLogger(), ents)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := svc.CreateTenant(ctx, "vgone", "Gone", tenant.IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	live, err := svc.CreateTenant(ctx, "vlive", "Live", tenant.IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}

	// The node goes down.
	if err := perTenantReg.CloseAll(ctx); err != nil {
		t.Fatal(err)
	}

	// Another node deletes vgone; only the replicated records reach here.
	ts, _ := json.Marshal(&tenant.Tombstone{ID: "vgone", IsolationType: tenant.IsolationVolume, DeletedAt: time.Now().UTC()})
	if err := meta.Put(ctx, "__sys:tenants-deleted/vgone", ts); err != nil {
		t.Fatal(err)
	}
	if err := meta.Delete(ctx, "__sys:tenants/vgone"); err != nil {
		t.Fatal(err)
	}

	// The node comes back.
	reg2 := fx.rebuildRegistry(t)
	svc2, err := tenant.NewTenantService(meta, reg2, reattachTestLogger(), ents)
	if err != nil {
		t.Fatal(err)
	}
	ti, err := BuildTenantInfra(TenantInfraConfig{
		Ctx:          ctx,
		BarrierReg:   reg2,
		TenantSvc:    svc2,
		TenantLookup: svc2,
		VolumePool:   &qrdbpool.VolumePoolSubsystem{},
		Logger:       reattachTestLogger(),
	})
	if err != nil {
		t.Fatalf("BuildTenantInfra: %v", err)
	}
	t.Cleanup(func() { _ = ti.Hosts.CloseAll() })

	if exists(t, gone.DataRoot) {
		t.Fatalf("the deleted tenant's DataRoot %s survived the boot", gone.DataRoot)
	}
	if _, err := reg2.GetTenant("vgone"); !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("the boot restored the deleted tenant's barrier: %v", err)
	}
	if !exists(t, live.DataRoot) {
		t.Fatal("the boot removed a LIVE tenant's DataRoot")
	}
	if _, err := reg2.GetTenant("vlive"); err != nil {
		t.Fatalf("the live tenant's barrier was not restored: %v", err)
	}
}

// failingReconcileSvc is a tenant service whose reconcile fails; everything
// else is the real service.
type failingReconcileSvc struct {
	tenant.TenantServicer
	calls  int
	report *tenant.ReconcileReport
	err    error
}

func (f *failingReconcileSvc) ReconcileDeletedTenants(context.Context) (*tenant.ReconcileReport, error) {
	f.calls++
	return f.report, f.err
}

// TestReconcileDeletedTenantsAtBoot_PartialFailureStillLogsWhatWasDone: a pass
// that failed for one id and reclaimed another returns both its report and
// its error; the boot logs the failure AND what was reclaimed, rather than
// dropping the report because an error came with it.
func TestReconcileDeletedTenantsAtBoot_PartialFailureStillLogsWhatWasDone(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	svc := &failingReconcileSvc{
		report: &tenant.ReconcileReport{Inspected: 2, Reclaimed: []tenant.ReclaimedPath{{TenantID: "vb", Path: "/srv/vb"}}},
		err:    &tenant.ResidueReconcileErrors{Errors: []*tenant.ResidueReconcileError{{TenantID: "va", Op: "remove", Err: errors.New("boom")}}},
	}
	reconcileDeletedTenantsAtBoot(context.Background(), svc, logger)
	out := buf.String()
	if !strings.Contains(out, "deleted-tenant reconcile failed") || !strings.Contains(out, "reclaimed=1") {
		t.Fatalf("log = %q; want both the failure and the reclaimed count", out)
	}
}

// TestReconcileDeletedTenantsAtBoot_QuietWhenThereIsNothingToDo: a pass with
// nothing reclaimed or skipped logs nothing.
func TestReconcileDeletedTenantsAtBoot_QuietWhenThereIsNothingToDo(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	reconcileDeletedTenantsAtBoot(context.Background(), &failingReconcileSvc{report: &tenant.ReconcileReport{Inspected: 3}}, logger)
	if buf.Len() != 0 {
		t.Fatalf("log = %q; want nothing", buf.String())
	}
}

// TestReconcileDeletedTenantsAtBoot_FailureDoesNotFailTheBoot pins the policy:
// the boot pass reclaims disk, never correctness, so its failure is logged and
// the boot proceeds.
func TestReconcileDeletedTenantsAtBoot_FailureDoesNotFailTheBoot(t *testing.T) {
	svc, reg := newReattachFixture(t)
	failing := &failingReconcileSvc{TenantServicer: svc, err: errors.New("store unavailable")}
	if _, err := BuildTenantInfra(TenantInfraConfig{
		Ctx:        context.Background(),
		BarrierReg: reg,
		TenantSvc:  failing,
		Logger:     reattachTestLogger(),
	}); err != nil {
		t.Fatalf("BuildTenantInfra failed because the reconcile did: %v", err)
	}
	if failing.calls != 1 {
		t.Fatalf("reconcile ran %d times at boot; want 1", failing.calls)
	}
}

// TestWhenReadIndexReady_RetriesOnlyTheWarmUpTransient: the not-ready
// transient is retried, any other error is returned at once.
func TestWhenReadIndexReady_RetriesOnlyTheWarmUpTransient(t *testing.T) {
	calls := 0
	got, err := whenReadIndexReady(context.Background(), func() (int, error) {
		calls++
		if calls < 3 {
			return 0, quicraft.ErrReadIndexNotReady
		}
		return 42, nil
	})
	if err != nil || got != 42 || calls != 3 {
		t.Fatalf("whenReadIndexReady = %d, %v after %d calls; want 42 after 3", got, err, calls)
	}

	boom := errors.New("boom")
	calls = 0
	if _, err := whenReadIndexReady(context.Background(), func() (int, error) {
		calls++
		return 0, boom
	}); !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("a non-transient error was retried (%d calls) or lost: %v", calls, err)
	}
}

// recordingResidueWiring records the runtime closer it is given.
type recordingResidueWiring struct {
	tenant.TenantServicer
	closer tenant.TenantRuntimeCloser
}

func (r *recordingResidueWiring) SetTenantRuntimeCloser(c tenant.TenantRuntimeCloser) { r.closer = c }

// TestWireTenantRuntimeCloser_WiresOnlyAServiceThatAcceptsIt covers both
// halves of the wiring seam.
func TestWireTenantRuntimeCloser_WiresOnlyAServiceThatAcceptsIt(t *testing.T) {
	hosts := NewTenantHostRegistry(nil, nil, reattachTestLogger())
	w := &recordingResidueWiring{}
	WireTenantRuntimeCloser(w, hosts, nil)
	if w.closer == nil {
		t.Fatal("a service implementing tenant.ResidueWiring was not wired")
	}
	if err := w.closer.CloseTenantRuntime(context.Background(), "nobody"); err != nil {
		t.Fatalf("closing a tenant with nothing running = %v; want nil", err)
	}
	// A service without the surface is left alone rather than failing.
	WireTenantRuntimeCloser(&failingReconcileSvc{}, hosts, nil)
}

// TestTenantRuntimeCloser_ClosesTheTenantsHost: a running volume host for the
// tenant is stopped and evicted, and another tenant's host is untouched.
func TestTenantRuntimeCloser_ClosesTheTenantsHost(t *testing.T) {
	svc, reg := newReattachFixture(t)
	ctx := context.Background()
	for _, id := range []string{"vclose", "vkeep"} {
		if _, err := svc.CreateTenant(ctx, id, id, tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true)); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}
	hosts := NewTenantHostRegistryFromLookup(reg, svc, reattachTestLogger())
	t.Cleanup(func() { _ = hosts.CloseAll() })
	for _, id := range []string{"vclose", "vkeep"} {
		if err := hosts.Attach(ctx, id); err != nil {
			t.Fatalf("Attach(%s): %v", id, err)
		}
	}

	if err := NewTenantRuntimeCloser(hosts, nil).CloseTenantRuntime(ctx, "vclose"); err != nil {
		t.Fatalf("CloseTenantRuntime: %v", err)
	}
	hosts.mu.Lock()
	_, closedStill := hosts.hosts["vclose"]
	_, keptStill := hosts.hosts["vkeep"]
	hosts.mu.Unlock()
	if closedStill {
		t.Fatal("the tenant's host is still running after CloseTenantRuntime")
	}
	if !keptStill {
		t.Fatal("CloseTenantRuntime stopped another tenant's host")
	}
}
