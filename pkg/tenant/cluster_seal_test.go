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

package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal/clustersealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ownerCtx is a request from tenantID's own administrator, as every
// transport builds it: the tenant bound and the identity attached.
func ownerCtx(tenantID string) context.Context {
	ctx := WithTenantID(context.Background(), tenantID)
	return auth.WithIdentity(ctx, &auth.Identity{Subject: "admin", TenantID: tenantID})
}

// newSealedClusterTenant creates a passphrase tenant and wires sealer to the
// service; sealer's Seal enforces by sealing the tenant's barrier on this node,
// as the node enforcer does.
func newClusterTenant(t *testing.T, sealer *clustersealtest.Sealer, tenantID string) (*TenantService, *seal.BarrierRegistry) {
	t.Helper()
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger(), WithClusterSealer(sealer))
	if err != nil {
		t.Fatal(err)
	}
	sealer.OnSeal(func(b clusterseal.Barrier) error { return reg.SealTenant(b.Tenant()) })
	if _, err := svc.CreateTenant(context.Background(), tenantID, "Cluster", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TenantBarrierInit(ownerCtx(tenantID), tenantID, []byte("tenant-passphrase"), 0, 0); err != nil {
		t.Fatal(err)
	}
	return svc, reg
}

// TestTenantBarrierOps_SystemTenantRefused: the platform operator does not
// seal or unseal a tenant's barrier. Before, enforceTenantScope let
// the system tenant act on every tenant.
func TestTenantBarrierOps_SystemTenantRefused(t *testing.T) {
	svc, _ := newClusterTenant(t, clustersealtest.New(false), "acme")
	for _, sys := range []context.Context{context.Background(), WithTenantID(context.Background(), tenantid.System)} {
		ops := map[string]func() error{
			"seal": func() error { return svc.TenantBarrierSeal(sys, "acme") },
			"unseal": func() error {
				return svc.TenantBarrierUnseal(sys, "acme", []byte("tenant-passphrase"))
			},
		}
		for name, op := range ops {
			var scope *TenantScopeError
			if err := op(); !errors.As(err, &scope) {
				t.Errorf("%s by the system tenant: err = %v, want *TenantScopeError", name, err)
			}
		}
	}
	if st, _ := svc.TenantBarrierStatus(ownerCtx("acme"), "acme"); st.Sealed {
		t.Fatal("a refused seal sealed the tenant")
	}
}

func TestTenantBarrierSeal_InactiveSealsThisNodeOnly(t *testing.T) {
	sealer := clustersealtest.New(false)
	svc, reg := newClusterTenant(t, sealer, "acme")
	if err := svc.TenantBarrierSeal(ownerCtx("acme"), "acme"); err != nil {
		t.Fatal(err)
	}
	if st, _ := reg.TenantStatus("acme"); !st.Sealed {
		t.Fatal("not sealed")
	}
	if len(sealer.Seals()) != 0 {
		t.Fatal("recorded a cluster seal while inactive")
	}
}

func TestTenantBarrierSeal_ActiveRecordsTheTenantSeal(t *testing.T) {
	sealer := clustersealtest.New(true)
	svc, reg := newClusterTenant(t, sealer, "acme")
	if err := svc.TenantBarrierSeal(ownerCtx("acme"), "acme"); err != nil {
		t.Fatal(err)
	}
	if got := sealer.Seals(); len(got) != 1 || got[0] != clusterseal.TenantBarrier("acme") {
		t.Fatalf("seals = %v", got)
	}
	if got := sealer.Principals()[0]; got != (clusterseal.Principal{Name: "admin", Tenant: "acme"}) {
		t.Fatalf("principal = %+v", got)
	}
	if st, _ := reg.TenantStatus("acme"); !st.Sealed {
		t.Fatal("the local enforcement did not seal the tenant")
	}
}

func TestTenantBarrierSeal_ActiveReportsUnconfirmedRecord(t *testing.T) {
	sealer := clustersealtest.New(true)
	boom := &clusterseal.RecordNotConfirmedError{Barrier: clusterseal.TenantBarrier("acme"), Op: "seal", Err: errors.New("no quorum")}
	sealer.SetSealErr(boom)
	svc, reg := newClusterTenant(t, sealer, "acme")
	if err := svc.TenantBarrierSeal(ownerCtx("acme"), "acme"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if st, _ := reg.TenantStatus("acme"); !st.Sealed {
		t.Fatal("tenant must be sealed on this node regardless")
	}
}

// TestTenantBarrierUnseal_ClearsASealedRecord: unsealing on a node clears the
// cluster seal; a clear record that cannot be written seals the barrier again.
func TestTenantBarrierUnseal_ClearsASealedRecord(t *testing.T) {
	sealer := clustersealtest.New(true)
	svc, reg := newClusterTenant(t, sealer, "acme")
	if err := svc.TenantBarrierSeal(ownerCtx("acme"), "acme"); err != nil {
		t.Fatal(err)
	}
	if err := svc.TenantBarrierUnseal(ownerCtx("acme"), "acme", []byte("tenant-passphrase")); err != nil {
		t.Fatal(err)
	}
	if got := sealer.Clears(); len(got) != 1 || got[0] != clusterseal.TenantBarrier("acme") {
		t.Fatalf("clears = %v", got)
	}
	if st, _ := reg.TenantStatus("acme"); st.Sealed {
		t.Fatal("not unsealed")
	}
	// A record this node sees as clear is still cleared -- this node's view
	// may be stale -- and a clear record is not rewritten.
	if err := svc.TenantBarrierSeal(ownerCtx("acme"), "acme"); err != nil {
		t.Fatal(err)
	}
	sealer.Put(clusterseal.Record{Barrier: clusterseal.TenantBarrier("acme"), Epoch: 9})
	if err := svc.TenantBarrierUnseal(ownerCtx("acme"), "acme", []byte("tenant-passphrase")); err != nil {
		t.Fatal(err)
	}
	if got := sealer.Clears(); len(got) != 2 {
		t.Fatalf("clears = %v, want a clear for a record this node sees as clear", got)
	}
	if rec := sealer.Record(clusterseal.TenantBarrier("acme")); rec.Epoch != 9 || rec.Sealed {
		t.Fatalf("record = %+v, want the clear record left as it was", rec)
	}
}

func TestTenantBarrierUnseal_ClearFailureSealsAgain(t *testing.T) {
	sealer := clustersealtest.New(true)
	svc, reg := newClusterTenant(t, sealer, "acme")
	if err := svc.TenantBarrierSeal(ownerCtx("acme"), "acme"); err != nil {
		t.Fatal(err)
	}
	boom := &clusterseal.RecordNotConfirmedError{Barrier: clusterseal.TenantBarrier("acme"), Op: "clear", Err: errors.New("no quorum")}
	sealer.SetClearErr(boom)
	if err := svc.TenantBarrierUnseal(ownerCtx("acme"), "acme", []byte("tenant-passphrase")); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if st, _ := reg.TenantStatus("acme"); !st.Sealed {
		t.Fatal("left unsealed while the record still says sealed")
	}
}

func TestTenantBarrierStatus_CarriesTheClusterSeal(t *testing.T) {
	sealer := clustersealtest.New(true)
	svc, _ := newClusterTenant(t, sealer, "acme")
	sealer.Put(clusterseal.Record{Barrier: clusterseal.TenantBarrier("acme"), Epoch: 3, Sealed: true, Principal: "admin", PrincipalTenant: "acme"})
	st, err := svc.TenantBarrierStatus(ownerCtx("acme"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if st.ClusterSeal == nil || !st.ClusterSeal.Active || !st.ClusterSeal.Sealed || st.ClusterSeal.Epoch != 3 {
		t.Fatalf("ClusterSeal = %+v", st.ClusterSeal)
	}
	// A tenant with no registered barrier still reports the record.
	svc2, _ := newTestService(t)
	if _, err := svc2.CreateTenant(context.Background(), "bare", "Bare", ""); err != nil {
		t.Fatal(err)
	}
	st, err = svc2.TenantBarrierStatus(ownerCtx("bare"), "bare")
	if err != nil {
		t.Fatal(err)
	}
	if st.ClusterSeal == nil || st.ClusterSeal.Active {
		t.Fatalf("no-sealer ClusterSeal = %+v", st.ClusterSeal)
	}
}

// TestTenantBarrierSeal_NodeLocalEndsTheTenantsStreams: before cluster
// sealing is active a tenant seal is node-local, and it still ends every
// open stream and SQL session of the tenant -- both subscribe to the node's
// stream registry -- and leaves another tenant's.
func TestTenantBarrierSeal_NodeLocalEndsTheTenantsStreams(t *testing.T) {
	deferred := clusterseal.NewDeferred() // inactive: seals are node-local
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(seal.NewMemoryBackend(), reg, testLogger(), WithClusterSealer(deferred))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TenantBarrierInit(ownerCtx("acme"), "acme", []byte("tenant-passphrase"), 0, 0); err != nil {
		t.Fatal(err)
	}
	streams := deferred.Streams()
	watch, err := streams.SubscribeServing("acme", "ledger", nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := streams.SubscribeTenantServing("acme", nil)
	if err != nil {
		t.Fatal(err)
	}
	other, err := streams.SubscribeTenantServing("globex", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.TenantBarrierSeal(ownerCtx("acme"), "acme"); err != nil {
		t.Fatalf("TenantBarrierSeal: %v", err)
	}
	for name, sub := range map[string]*clusterseal.Subscription{"watch": watch, "SQL session": session} {
		select {
		case <-sub.Done():
		default:
			t.Fatalf("the tenant's %s outlived its node-local seal", name)
		}
	}
	select {
	case <-other.Done():
		t.Fatal("another tenant's session was ended")
	default:
	}
}
