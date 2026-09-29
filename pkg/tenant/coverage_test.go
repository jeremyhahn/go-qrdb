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
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// database context scope
// ---------------------------------------------------------------------------

// TestWithDatabase_RoundTrip verifies a bound database scope is read back, while
// an unbound or empty scope falls back to DefaultDatabase.
func TestWithDatabase_RoundTrip(t *testing.T) {
	base := context.Background()
	if got := DatabaseFromContext(base); got != DefaultDatabase {
		t.Fatalf("unbound context database = %q, want %q", got, DefaultDatabase)
	}

	bound := WithDatabase(base, "orders")
	if got := DatabaseFromContext(bound); got != "orders" {
		t.Fatalf("bound database = %q, want orders", got)
	}

	// An explicit empty string maps back to the bootstrap default on read.
	empty := WithDatabase(base, "")
	if got := DatabaseFromContext(empty); got != DefaultDatabase {
		t.Fatalf("empty-bound database = %q, want %q", got, DefaultDatabase)
	}
}

// ---------------------------------------------------------------------------
// kvTenantService entitlement surface (the no-seal embedded service)
// ---------------------------------------------------------------------------

// newKVChecker builds a kvTenantService and returns it as both a TenantServicer
// and an EntitlementChecker so the entitlement methods can be exercised.
func newKVChecker(t *testing.T) (TenantServicer, quota.EntitlementChecker) {
	t.Helper()
	svc, err := NewKVTenantService(newMemKVStore(), slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	ec, ok := svc.(quota.EntitlementChecker)
	if !ok {
		t.Fatalf("kvTenantService does not implement quota.EntitlementChecker")
	}
	return svc, ec
}

// TestKVUpdateEntitlements covers validation, not-found, and the persisting
// round-trip for the embedded KV service.
func TestKVUpdateEntitlements(t *testing.T) {
	ctx := context.Background()
	svc, _ := newKVChecker(t)

	if _, err := svc.UpdateEntitlements(ctx, "", &quota.Entitlements{}); !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("empty id: got %v, want ErrEmptyTenantID", err)
	}
	if _, err := svc.UpdateEntitlements(ctx, "acme", nil); !errors.Is(err, ErrNilEntitlements) {
		t.Errorf("nil entitlements: got %v, want ErrNilEntitlements", err)
	}
	if _, err := svc.UpdateEntitlements(ctx, "ghost", &quota.Entitlements{}); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("missing tenant: got %v, want ErrTenantNotFound", err)
	}

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	ents := &quota.Entitlements{
		AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
		AllowVolumeStorage:    true,
	}
	updated, err := svc.UpdateEntitlements(ctx, "acme", ents)
	if err != nil {
		t.Fatalf("UpdateEntitlements: %v", err)
	}
	if updated.Entitlements == nil || !updated.Entitlements.AllowVolumeStorage {
		t.Fatalf("entitlements not applied: %+v", updated.Entitlements)
	}
	got, err := svc.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Entitlements == nil || !got.Entitlements.AllowsIsolation(quota.IsolationModeVolume) {
		t.Fatalf("entitlements not persisted: %+v", got.Entitlements)
	}
}

// TestKVCheckIsolation covers the system implicit grant, default-deny, explicit
// grant, and the not-found error path.
func TestKVCheckIsolation(t *testing.T) {
	ctx := context.Background()
	svc, ec := newKVChecker(t)

	// System tenant is implicitly entitled to every mode.
	if err := ec.CheckIsolation(ctx, tenantid.System, quota.IsolationModeRaft); err != nil {
		t.Errorf("system CheckIsolation: %v", err)
	}

	// A missing tenant surfaces the lookup error (no default short-circuit here).
	if err := ec.CheckIsolation(ctx, "ghost", quota.IsolationModePrefix); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("missing tenant CheckIsolation: got %v, want ErrTenantNotFound", err)
	}

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Default entitlements allow prefix only.
	if err := ec.CheckIsolation(ctx, "acme", quota.IsolationModePrefix); err != nil {
		t.Errorf("prefix should be allowed by default: %v", err)
	}
	var entErr *quota.EntitlementError
	if err := ec.CheckIsolation(ctx, "acme", quota.IsolationModeRaft); !errors.As(err, &entErr) {
		t.Errorf("raft should be denied: got %v", err)
	} else if entErr.Mode != quota.IsolationModeRaft {
		t.Errorf("EntitlementError.Mode = %q", entErr.Mode)
	}

	// After granting volume, it is allowed (exercises effectiveEntitlements
	// returning the tenant's own entitlements).
	if _, err := svc.UpdateEntitlements(ctx, "acme", &quota.Entitlements{
		AllowedIsolationModes: []string{quota.IsolationModePrefix, quota.IsolationModeVolume},
	}); err != nil {
		t.Fatalf("UpdateEntitlements: %v", err)
	}
	if err := ec.CheckIsolation(ctx, "acme", quota.IsolationModeVolume); err != nil {
		t.Errorf("volume should be allowed after grant: %v", err)
	}
}

// TestKVCheckFeature covers the system implicit grant, explicit grant, denial,
// and the not-found error path.
func TestKVCheckFeature(t *testing.T) {
	ctx := context.Background()
	svc, ec := newKVChecker(t)

	if err := ec.CheckFeature(ctx, tenantid.System, quota.FeatureDedicatedRaft); err != nil {
		t.Errorf("system CheckFeature: %v", err)
	}
	if err := ec.CheckFeature(ctx, "ghost", quota.FeatureDedicatedRaft); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("missing tenant CheckFeature: got %v, want ErrTenantNotFound", err)
	}

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateEntitlements(ctx, "acme", &quota.Entitlements{
		AllowedIsolationModes: []string{quota.IsolationModePrefix},
		AllowDedicatedRaft:    true,
	}); err != nil {
		t.Fatalf("UpdateEntitlements: %v", err)
	}
	if err := ec.CheckFeature(ctx, "acme", quota.FeatureDedicatedRaft); err != nil {
		t.Errorf("dedicated_raft should be allowed: %v", err)
	}
	var entErr *quota.EntitlementError
	if err := ec.CheckFeature(ctx, "acme", quota.FeatureVolumeStorage); !errors.As(err, &entErr) {
		t.Errorf("volume_storage should be denied: got %v", err)
	} else if entErr.Feature != quota.FeatureVolumeStorage {
		t.Errorf("EntitlementError.Feature = %q", entErr.Feature)
	}
}

// ---------------------------------------------------------------------------
// WithDefaultQuotas
// ---------------------------------------------------------------------------

// TestWithDefaultQuotas verifies the operator-configured default quotas are
// returned for a tenant whose own Quotas are nil, and that a tenant's own
// quotas still take precedence.
func TestWithDefaultQuotas(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	defaults := &quota.Quotas{MaxDatabases: 7, MaxStorageBytes: 4096}
	svc, err := NewTenantService(store, reg, testLogger(), WithDefaultQuotas(defaults))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Tenant has no own quotas -> operator default applies.
	q, err := svc.Quotas(ctx, "acme")
	if err != nil {
		t.Fatalf("Quotas: %v", err)
	}
	if q.MaxDatabases != 7 || q.MaxStorageBytes != 4096 {
		t.Fatalf("default quotas not applied: %+v", q)
	}

	// Setting the tenant's own quotas takes precedence over the default.
	if _, err := svc.UpdateQuotas(ctx, "acme", &quota.Quotas{MaxDatabases: 99}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	q2, err := svc.Quotas(ctx, "acme")
	if err != nil {
		t.Fatalf("Quotas: %v", err)
	}
	if q2.MaxDatabases != 99 {
		t.Fatalf("own quotas not honored: %+v", q2)
	}
}

// ---------------------------------------------------------------------------
// Quota-usage invalid-resource and store-fault branches
// ---------------------------------------------------------------------------

// invalidResource is a Resource value that maps to no limit/usage accessor, so
// every dispatch returns *quota.InvalidResourceError.
const invalidResource = quota.Resource(999)

// TestKVQuotaUsage_InvalidResource exercises the unknown-resource error branch
// of each embedded KV quota-usage method.
func TestKVQuotaUsage_InvalidResource(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t).(*kvTenantService)
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Give the tenant a non-nil Quotas so GetQuotaUsage reaches the dispatch.
	if _, err := svc.UpdateQuotas(ctx, "acme", &quota.Quotas{}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}

	assertInvalidResource := func(name string, err error) {
		t.Helper()
		var ire *quota.InvalidResourceError
		if !errors.As(err, &ire) {
			t.Errorf("%s: error = %v, want *quota.InvalidResourceError", name, err)
		}
	}
	_, err := svc.SetQuotaUsage(ctx, "acme", invalidResource, 1)
	assertInvalidResource("SetQuotaUsage", err)
	_, err = svc.GetQuotaUsage(ctx, "acme", invalidResource)
	assertInvalidResource("GetQuotaUsage", err)
	_, err = svc.IncrementQuotaUsage(ctx, "acme", invalidResource, 1)
	assertInvalidResource("IncrementQuotaUsage", err)
	_, err = svc.DecrementQuotaUsage(ctx, "acme", invalidResource, 1)
	assertInvalidResource("DecrementQuotaUsage", err)
}

// TestTenantServiceQuotaUsage_InvalidResource exercises the same unknown-resource
// branch on the seal-backed TenantService.
func TestTenantServiceQuotaUsage_InvalidResource(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "acme", &quota.Quotas{}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}

	assertInvalidResource := func(name string, err error) {
		t.Helper()
		var ire *quota.InvalidResourceError
		if !errors.As(err, &ire) {
			t.Errorf("%s: error = %v, want *quota.InvalidResourceError", name, err)
		}
	}
	_, err := svc.SetQuotaUsage(ctx, "acme", invalidResource, 1)
	assertInvalidResource("SetQuotaUsage", err)
	_, err = svc.GetQuotaUsage(ctx, "acme", invalidResource)
	assertInvalidResource("GetQuotaUsage", err)
	_, err = svc.IncrementQuotaUsage(ctx, "acme", invalidResource, 1)
	assertInvalidResource("IncrementQuotaUsage", err)
	_, err = svc.DecrementQuotaUsage(ctx, "acme", invalidResource, 1)
	assertInvalidResource("DecrementQuotaUsage", err)
}

// TestTenantServiceQuotaUsage_StorePutFaults covers the persistence-failure
// branch of SetQuotaUsage and DecrementQuotaUsage.
func TestTenantServiceQuotaUsage_StorePutFaults(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		call func(svc *TenantService) error
	}{
		{"SetQuotaUsage", func(svc *TenantService) error {
			_, err := svc.SetQuotaUsage(ctx, "acme", quota.ResourceStorageBytes, 10)
			return err
		}},
		{"DecrementQuotaUsage", func(svc *TenantService) error {
			_, err := svc.DecrementQuotaUsage(ctx, "acme", quota.ResourceKeys, 1)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := seal.NewMemoryBackend()
			reg := newTestBarrierRegistry(t)
			svc, err := NewTenantService(store, reg, testLogger())
			if err != nil {
				t.Fatalf("NewTenantService: %v", err)
			}
			if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
				t.Fatalf("CreateTenant: %v", err)
			}
			svc.store = &failingBackend{inner: store, failPut: true}
			var se *StoreError
			if err := tc.call(svc); !errors.As(err, &se) {
				t.Errorf("%s: error = %v, want *StoreError", tc.name, err)
			}
		})
	}
}

// TestCreateTenant_AutoUnsealFeatureDenied verifies that requesting a
// system-managed (auto-unseal) barrier without the system_managed_barrier
// entitlement is rejected before any barrier registration occurs.
func TestCreateTenant_AutoUnsealFeatureDenied(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationBarrier, WithBarrierAutoUnseal(true))
	var entErr *quota.EntitlementError
	if !errors.As(err, &entErr) {
		t.Fatalf("error = %v, want *quota.EntitlementError", err)
	}
	if entErr.Feature != quota.FeatureSystemManagedBarrier {
		t.Errorf("EntitlementError.Feature = %q, want %q", entErr.Feature, quota.FeatureSystemManagedBarrier)
	}
}

// TestCreateTenant_StorePutRollback covers the persistence-failure rollback path
// of CreateTenant for a prefix tenant (no barrier registration to undo).
func TestCreateTenant_StorePutRollback(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	svc.store = &failingBackend{inner: store, failPut: true}
	ctx := context.Background()
	_, err = svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v, want *StoreError", err)
	}
	if se.Op != "put" {
		t.Errorf("StoreError.Op = %q, want put", se.Op)
	}
}

// putFaultKV wraps a memKVStore and fails Put when failPut is set, leaving all
// other operations (notably Get) functional so a tenant can be created first.
type putFaultKV struct {
	*memKVStore
	failPut bool
}

func (p *putFaultKV) Put(ctx context.Context, key string, value []byte) error {
	if p.failPut {
		return errKVFault
	}
	return p.memKVStore.Put(ctx, key, value)
}

// TestKVPutFaultBranches covers the persistence-failure branch of the embedded
// KV service's mutating quota/entitlement methods.
func TestKVPutFaultBranches(t *testing.T) {
	ctx := context.Background()
	pf := &putFaultKV{memKVStore: newMemKVStore()}
	base, err := NewKVTenantService(pf, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	svc := base.(*kvTenantService)
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "acme", &quota.Quotas{MaxKeys: 5}); err != nil {
		t.Fatalf("UpdateQuotas (seed): %v", err)
	}

	// All subsequent writes fail at the store layer.
	pf.failPut = true

	assertStoreErr := func(name string, err error) {
		t.Helper()
		var se *StoreError
		if !errors.As(err, &se) {
			t.Errorf("%s: error = %v, want *StoreError", name, err)
		}
	}
	_, err = svc.UpdateQuotas(ctx, "acme", &quota.Quotas{MaxKeys: 9})
	assertStoreErr("UpdateQuotas", err)
	_, err = svc.UpdateEntitlements(ctx, "acme", &quota.Entitlements{})
	assertStoreErr("UpdateEntitlements", err)
	_, err = svc.SetQuotaUsage(ctx, "acme", quota.ResourceKeys, 3)
	assertStoreErr("SetQuotaUsage", err)
	_, err = svc.IncrementQuotaUsage(ctx, "acme", quota.ResourceKeys, 1)
	assertStoreErr("IncrementQuotaUsage", err)
	_, err = svc.DecrementQuotaUsage(ctx, "acme", quota.ResourceKeys, 1)
	assertStoreErr("DecrementQuotaUsage", err)
}
