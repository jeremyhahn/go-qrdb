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

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

func TestEffectiveIsolationPolicy(t *testing.T) {
	tests := []struct {
		name   string
		floor  ClusterIsolationPolicy
		tenant TenantIsolationPolicy
		want   ClusterIsolationPolicy
	}{
		{"both permissive", ClusterIsolationPolicyOperatorAccess, TenantIsolationPolicyInherit, ClusterIsolationPolicyOperatorAccess},
		{"floor strict", ClusterIsolationPolicyStrict, TenantIsolationPolicyInherit, ClusterIsolationPolicyStrict},
		{"tenant strict", ClusterIsolationPolicyOperatorAccess, TenantIsolationPolicyStrict, ClusterIsolationPolicyStrict},
		{"both strict", ClusterIsolationPolicyStrict, TenantIsolationPolicyStrict, ClusterIsolationPolicyStrict},
		{"unset floor, no tenant opt-in", ClusterIsolationPolicyUnset, TenantIsolationPolicyInherit, ClusterIsolationPolicyOperatorAccess},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveIsolationPolicy(tc.floor, tc.tenant); got != tc.want {
				t.Fatalf("EffectiveIsolationPolicy(%q, %q) = %q, want %q", tc.floor, tc.tenant, got, tc.want)
			}
		})
	}
}

func TestValidateIsolationModeAgainstPolicy(t *testing.T) {
	tests := []struct {
		name          string
		tenantID      string
		isolationType IsolationType
		effective     ClusterIsolationPolicy
		wantErr       bool
	}{
		{"prefix under strict refused", "acme", IsolationPrefix, ClusterIsolationPolicyStrict, true},
		{"empty (defaults to prefix) under strict refused", "acme", "", ClusterIsolationPolicyStrict, true},
		{"prefix under operator-access allowed", "acme", IsolationPrefix, ClusterIsolationPolicyOperatorAccess, false},
		{"barrier under strict allowed", "acme", IsolationBarrier, ClusterIsolationPolicyStrict, false},
		{"volume under strict allowed", "acme", IsolationVolume, ClusterIsolationPolicyStrict, false},
		{"raft under strict allowed", "acme", IsolationRaft, ClusterIsolationPolicyStrict, false},
		{"system tenant prefix under strict allowed", tenantid.System, IsolationPrefix, ClusterIsolationPolicyStrict, false},
		{"empty id is the system tenant", "", IsolationPrefix, ClusterIsolationPolicyStrict, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIsolationModeAgainstPolicy(tc.tenantID, tc.isolationType, tc.effective)
			if tc.wantErr {
				var want *StrictRequiresDedicatedStorageError
				if !errors.As(err, &want) {
					t.Fatalf("err = %v, want *StrictRequiresDedicatedStorageError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestClusterIsolationTransitionAllowed(t *testing.T) {
	tests := []struct {
		from, to ClusterIsolationPolicy
		want     bool
	}{
		{ClusterIsolationPolicyUnset, ClusterIsolationPolicyStrict, true},
		{ClusterIsolationPolicyUnset, ClusterIsolationPolicyOperatorAccess, true},
		{ClusterIsolationPolicyOperatorAccess, ClusterIsolationPolicyStrict, true},
		{ClusterIsolationPolicyOperatorAccess, ClusterIsolationPolicyOperatorAccess, false},
		{ClusterIsolationPolicyStrict, ClusterIsolationPolicyOperatorAccess, false},
		{ClusterIsolationPolicyStrict, ClusterIsolationPolicyStrict, false},
	}
	for _, tc := range tests {
		if got := clusterIsolationTransitionAllowed(tc.from, tc.to); got != tc.want {
			t.Errorf("clusterIsolationTransitionAllowed(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TenantService (seal-enabled): the cluster floor
// ---------------------------------------------------------------------------

func TestTenantService_ClusterIsolationPolicy_DefaultsToOperatorAccess(t *testing.T) {
	svc, _ := newTestService(t)
	got, err := svc.ClusterIsolationPolicy(context.Background())
	if err != nil {
		t.Fatalf("ClusterIsolationPolicy: %v", err)
	}
	if got != ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("got %q, want %q (an untouched floor must not change today's behavior)", got, ClusterIsolationPolicyOperatorAccess)
	}
}

func TestTenantService_SetClusterIsolationPolicy_TightenOnly(t *testing.T) {
	ctx := context.Background()

	t.Run("unset to operator-access then to strict", func(t *testing.T) {
		svc, _ := newTestService(t)
		if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyOperatorAccess); err != nil {
			t.Fatalf("Set(operator-access): %v", err)
		}
		if got, err := svc.ClusterIsolationPolicy(ctx); err != nil || got != ClusterIsolationPolicyOperatorAccess {
			t.Fatalf("Get after set = (%q, %v), want operator-access", got, err)
		}
		if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict); err != nil {
			t.Fatalf("Set(strict) after operator-access: %v", err)
		}
		if got, err := svc.ClusterIsolationPolicy(ctx); err != nil || got != ClusterIsolationPolicyStrict {
			t.Fatalf("Get after tightening = (%q, %v), want strict", got, err)
		}
	})

	t.Run("unset directly to strict", func(t *testing.T) {
		svc, _ := newTestService(t)
		if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict); err != nil {
			t.Fatalf("Set(strict): %v", err)
		}
		if got, err := svc.ClusterIsolationPolicy(ctx); err != nil || got != ClusterIsolationPolicyStrict {
			t.Fatalf("Get = (%q, %v), want strict", got, err)
		}
	})

	t.Run("strict never loosens", func(t *testing.T) {
		svc, _ := newTestService(t)
		if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict); err != nil {
			t.Fatalf("Set(strict): %v", err)
		}
		err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyOperatorAccess)
		var changeErr *IsolationPolicyChangeError
		if !errors.As(err, &changeErr) {
			t.Fatalf("Set(operator-access) after strict = %v, want *IsolationPolicyChangeError", err)
		}
		if got, _ := svc.ClusterIsolationPolicy(ctx); got != ClusterIsolationPolicyStrict {
			t.Fatalf("a refused loosen must leave the floor unchanged, got %q", got)
		}
	})

	t.Run("idempotent same-value set is a no-op success", func(t *testing.T) {
		svc, _ := newTestService(t)
		if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyOperatorAccess); err != nil {
			t.Fatalf("Set(operator-access): %v", err)
		}
		if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyOperatorAccess); err != nil {
			t.Fatalf("re-Set(operator-access) = %v, want nil", err)
		}
	})

	t.Run("invalid value refused", func(t *testing.T) {
		svc, _ := newTestService(t)
		err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicy("bogus"))
		var invalid *InvalidIsolationPolicyError
		if !errors.As(err, &invalid) {
			t.Fatalf("Set(bogus) = %v, want *InvalidIsolationPolicyError", err)
		}
	})
}

// TestTenantService_SetClusterIsolationPolicy_PersistsAcrossNewServiceInstance
// proves the floor survives a process restart: a SECOND TenantService built
// over the SAME backing store (standing in for a node coming back up against
// its durable store) reads what the first instance set.
func TestTenantService_SetClusterIsolationPolicy_PersistsAcrossNewServiceInstance(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)

	svc1, err := NewTenantService(store, reg, testLogger(), WithVolumeAllocator(volumeAllocatorFor(t, reg)))
	if err != nil {
		t.Fatalf("NewTenantService (1): %v", err)
	}
	if err := svc1.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict); err != nil {
		t.Fatalf("SetClusterIsolationPolicy: %v", err)
	}

	svc2, err := NewTenantService(store, reg, testLogger(), WithVolumeAllocator(volumeAllocatorFor(t, reg)))
	if err != nil {
		t.Fatalf("NewTenantService (2): %v", err)
	}
	got, err := svc2.ClusterIsolationPolicy(ctx)
	if err != nil {
		t.Fatalf("ClusterIsolationPolicy on the restarted instance: %v", err)
	}
	if got != ClusterIsolationPolicyStrict {
		t.Fatalf("restarted instance reads %q, want strict (the floor did not survive the restart)", got)
	}
}

// ---------------------------------------------------------------------------
// S2: a prefix tenant cannot be strict
// ---------------------------------------------------------------------------

func TestTenantService_CreateTenant_S2_PrefixRefusedUnderStrictFloor(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict); err != nil {
		t.Fatalf("SetClusterIsolationPolicy: %v", err)
	}

	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	var want *StrictRequiresDedicatedStorageError
	if !errors.As(err, &want) {
		t.Fatalf("CreateTenant(prefix) under a strict floor = %v, want *StrictRequiresDedicatedStorageError", err)
	}
	if _, gerr := svc.GetTenant(ctx, "acme"); gerr == nil {
		t.Fatal("a refused create must not leave a record behind")
	}
}

// TestTenantService_CreateTenant_S2_SystemTenantUnderStrictFloor: first boot
// creates the system tenant as a prefix tenant, the only type CreateTenant
// allows it, after a FIPS 140-3 node has set the strict floor. S2 must not
// refuse it, on the first boot or on a restart.
func TestTenantService_CreateTenant_S2_SystemTenantUnderStrictFloor(t *testing.T) {
	ctx := WithInternalCall(context.Background())
	svc, _ := newTestService(t)
	if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict); err != nil {
		t.Fatalf("SetClusterIsolationPolicy: %v", err)
	}

	if _, err := svc.CreateTenant(ctx, tenantid.System, "System", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant(system) under a strict floor = %v, want success", err)
	}
	if _, err := svc.CreateTenant(ctx, tenantid.System, "System", IsolationPrefix); !errors.Is(err, ErrTenantAlreadyExists) {
		t.Fatalf("second CreateTenant(system) = %v, want ErrTenantAlreadyExists", err)
	}
}

func TestTenantService_CreateTenant_S2_PrefixRefusedWithExplicitIsolationPolicy(t *testing.T) {
	ctx := context.Background()
	// The floor is untouched (operator-access), but the caller directly asks
	// for the tenant's own strict opt-in -- S2 must still refuse a prefix
	// tenant.
	svc, _ := newTestService(t)
	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix, WithIsolationPolicy(TenantIsolationPolicyStrict))
	var want *StrictRequiresDedicatedStorageError
	if !errors.As(err, &want) {
		t.Fatalf("CreateTenant(prefix, isolation_policy=strict) = %v, want *StrictRequiresDedicatedStorageError", err)
	}
}

func TestTenantService_CreateTenant_BarrierAllowedUnderStrictFloorWithIsolationPolicy(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict); err != nil {
		t.Fatalf("SetClusterIsolationPolicy: %v", err)
	}
	info, err := svc.CreateTenant(ctx, "secure", "Secure", IsolationBarrier, WithIsolationPolicy(TenantIsolationPolicyStrict))
	if err != nil {
		t.Fatalf("CreateTenant(barrier) under a strict floor: %v", err)
	}
	if info.IsolationPolicy != TenantIsolationPolicyStrict {
		t.Fatalf("IsolationPolicy = %q, want strict", info.IsolationPolicy)
	}
}

func TestTenantService_UpdateTenant_IsolationPolicy_TightenOnly(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	if _, err := svc.CreateTenant(ctx, "secure", "Secure", IsolationBarrier); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	updated, err := svc.UpdateTenant(ctx, "secure", TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict})
	if err != nil {
		t.Fatalf("UpdateTenant(inherit->strict): %v", err)
	}
	if updated.IsolationPolicy != TenantIsolationPolicyStrict {
		t.Fatalf("IsolationPolicy = %q, want strict", updated.IsolationPolicy)
	}

	// A second UpdateTenant that does not mention IsolationPolicy
	// (TenantIsolationPolicyInherit IS the zero value TenantPatch uses for
	// "field not set", exactly as it is for IsolationType) must leave the
	// tenant's strict opt-in untouched rather than reading as a request to
	// revert it -- there is no way to SPELL "revert to inherit" through this
	// patch shape, which is what makes the tighten-only invariant hold for
	// every caller of this API, not just well-behaved ones.
	again, err := svc.UpdateTenant(ctx, "secure", TenantPatch{Name: "Secure Co"})
	if err != nil {
		t.Fatalf("UpdateTenant(unrelated field): %v", err)
	}
	if again.IsolationPolicy != TenantIsolationPolicyStrict {
		t.Fatalf("IsolationPolicy after an unrelated patch = %q, want it to remain strict", again.IsolationPolicy)
	}
}

func TestTenantService_UpdateTenant_IsolationPolicy_S2(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	_, err := svc.UpdateTenant(ctx, "acme", TenantPatch{IsolationPolicy: TenantIsolationPolicyStrict})
	var want *StrictRequiresDedicatedStorageError
	if !errors.As(err, &want) {
		t.Fatalf("UpdateTenant(prefix tenant, isolation_policy=strict) = %v, want *StrictRequiresDedicatedStorageError", err)
	}
}

func TestTenantService_UpdateTenant_IsolationPolicy_NoOpForCurrentValue(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Patching with the tenant's current (inherit) policy must not run the S2
	// check at all, even though the tenant is a prefix tenant.
	if _, err := svc.UpdateTenant(ctx, "acme", TenantPatch{IsolationPolicy: TenantIsolationPolicyInherit}); err != nil {
		t.Fatalf("UpdateTenant(no-op isolation policy) = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// kvTenantService (no-seal): the floor still applies, honoured by the ONE
// isolation type this service ever creates.
// ---------------------------------------------------------------------------

func TestKVTenantService_ClusterIsolationPolicy_DefaultsToOperatorAccess(t *testing.T) {
	svc := &kvTenantService{store: newMemKVStore(), logger: slog.Default()}
	got, err := svc.ClusterIsolationPolicy(context.Background())
	if err != nil {
		t.Fatalf("ClusterIsolationPolicy: %v", err)
	}
	if got != ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("got %q, want operator-access", got)
	}
}

func TestKVTenantService_SetClusterIsolationPolicy_TightenOnly(t *testing.T) {
	ctx := context.Background()
	svc := &kvTenantService{store: newMemKVStore(), logger: slog.Default()}

	if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict); err != nil {
		t.Fatalf("Set(strict): %v", err)
	}
	got, err := svc.ClusterIsolationPolicy(ctx)
	if err != nil || got != ClusterIsolationPolicyStrict {
		t.Fatalf("Get = (%q, %v), want strict", got, err)
	}

	err = svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyOperatorAccess)
	var changeErr *IsolationPolicyChangeError
	if !errors.As(err, &changeErr) {
		t.Fatalf("Set(operator-access) after strict = %v, want *IsolationPolicyChangeError", err)
	}
}

func TestKVCreateTenant_S2_RefusedUnderStrictFloor(t *testing.T) {
	ctx := context.Background()
	svc := &kvTenantService{store: newMemKVStore(), logger: slog.Default()}
	if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict); err != nil {
		t.Fatalf("SetClusterIsolationPolicy: %v", err)
	}
	// This service only ever creates prefix tenants, so a strict floor
	// refuses every create -- matching "without a pool, tenant creation
	// fails cleanly before any write" (there is no pool here either).
	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	var want *StrictRequiresDedicatedStorageError
	if !errors.As(err, &want) {
		t.Fatalf("CreateTenant under a strict floor = %v, want *StrictRequiresDedicatedStorageError", err)
	}
}

// TestKVCreateTenant_S2_SystemTenantUnderStrictFloor: the system tenant is
// the one prefix tenant S2 does not refuse, since its prefix is the system
// store it owns; a FIPS 140-3 node sets the strict floor before first boot
// creates it.
func TestKVCreateTenant_S2_SystemTenantUnderStrictFloor(t *testing.T) {
	ctx := context.Background()
	svc := &kvTenantService{store: newMemKVStore(), logger: slog.Default()}
	if err := svc.SetClusterIsolationPolicy(ctx, ClusterIsolationPolicyStrict); err != nil {
		t.Fatalf("SetClusterIsolationPolicy: %v", err)
	}
	if _, err := svc.CreateTenant(WithInternalCall(ctx), tenantid.System, "System", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant(system) under a strict floor = %v, want success", err)
	}
	// Exempting the system tenant did not open S2 for anyone else.
	_, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix)
	var want *StrictRequiresDedicatedStorageError
	if !errors.As(err, &want) {
		t.Fatalf("CreateTenant(acme) under a strict floor = %v, want *StrictRequiresDedicatedStorageError", err)
	}
}

// ---------------------------------------------------------------------------
// A store with no atomic capability: Set refuses, Get reads the permissive
// default (nothing could ever have tightened it on such a store).
// ---------------------------------------------------------------------------

// plainKVStoreView exposes only kvstore.KVStore's methods over a *memKVStore,
// explicitly NOT promoting PutIfAbsent/PutIfAllAbsent/CompareAndDelete/
// CompareAndSwap: embedding would promote everything, which is exactly the
// atomic capability this type exists to withhold.
type plainKVStoreView struct{ inner *memKVStore }

func (v plainKVStoreView) Put(ctx context.Context, key string, value []byte) error {
	return v.inner.Put(ctx, key, value)
}
func (v plainKVStoreView) Get(ctx context.Context, key string) ([]byte, error) {
	return v.inner.Get(ctx, key)
}
func (v plainKVStoreView) Delete(ctx context.Context, key string) error {
	return v.inner.Delete(ctx, key)
}
func (v plainKVStoreView) List(ctx context.Context, prefix string) ([]string, error) {
	return v.inner.List(ctx, prefix)
}
func (v plainKVStoreView) Exists(ctx context.Context, key string) (bool, error) {
	return v.inner.Exists(ctx, key)
}
func (v plainKVStoreView) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	return v.inner.Scan(ctx, prefix, fn)
}
func (v plainKVStoreView) RegisterEntityIndexes(entityType string, indexes []kvstore.EntityIndex) error {
	return v.inner.RegisterEntityIndexes(entityType, indexes)
}
func (v plainKVStoreView) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	return v.inner.QueryIndex(ctx, indexName, value)
}
func (v plainKVStoreView) ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	return v.inner.ScanIndex(ctx, indexName, start, end)
}
func (v plainKVStoreView) QueryCompoundIndex(ctx context.Context, indexName string, values [][]byte) ([][]byte, error) {
	return v.inner.QueryCompoundIndex(ctx, indexName, values)
}
func (v plainKVStoreView) ScanCompoundIndex(ctx context.Context, indexName string, prefixValues [][]byte, rangeStart, rangeEnd []byte) (map[string][][]byte, error) {
	return v.inner.ScanCompoundIndex(ctx, indexName, prefixValues, rangeStart, rangeEnd)
}

func TestKVTenantService_ClusterIsolationPolicy_NoAtomicCapability(t *testing.T) {
	inner := newMemKVStore()
	wrapped := &kvTenantService{store: plainKVStoreView{inner}, logger: slog.Default()}

	got, err := wrapped.ClusterIsolationPolicy(context.Background())
	if err != nil {
		t.Fatalf("ClusterIsolationPolicy: %v", err)
	}
	if got != ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("got %q, want operator-access (no capability, nothing could have tightened it)", got)
	}

	err = wrapped.SetClusterIsolationPolicy(context.Background(), ClusterIsolationPolicyStrict)
	if !errors.Is(err, ErrTenantStoreNotAtomic) {
		t.Fatalf("SetClusterIsolationPolicy on a non-atomic store = %v, want ErrTenantStoreNotAtomic", err)
	}
}
