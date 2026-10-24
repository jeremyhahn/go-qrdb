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

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// policyStores resolves each tenant to its own in-memory store, the
// confinement a real resolver gives.
type policyStores map[string]kvstore.KVStore

func (p policyStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	if s, ok := p[tenantID]; ok {
		return s, nil
	}
	return nil, tenant.ErrTenantSealed
}

// plainKVStore hides a store's guarded-write methods, so it is a
// kvstore.KVStore and not a kvstore.AtomicKVStore.
type plainKVStore struct{ kvstore.KVStore }

// racingStore loses every guarded write, as a store whose record changed
// between the service's read and its write does.
type racingStore struct{ *kvstore.MemoryKVStore }

func (racingStore) CompareAndSwap(context.Context, string, []byte, []byte) (bool, error) {
	return false, nil
}

func (racingStore) CompareAndDelete(context.Context, string, []byte) (bool, error) {
	return false, nil
}

// failingStore fails every store call with err.
type failingStore struct {
	*kvstore.MemoryKVStore
	err error
}

func (f failingStore) Get(context.Context, string) ([]byte, error) { return nil, f.err }

func (f failingStore) Scan(context.Context, string, func(string, []byte) error) error {
	return f.err
}

func (f failingStore) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return false, f.err
}

const policyTestTenant = "acme"

func policyCtx(tenantID string) context.Context {
	return tenant.WithTenantID(context.Background(), tenantID)
}

func boolPtr(b bool) *bool { return &b }

func testPolicy(name string, enabled *bool) *abac.Policy {
	return &abac.Policy{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       abac.PolicyKind,
		Metadata:   abac.PolicyMetadata{Name: name, Description: "test policy"},
		Spec: abac.PolicySpec{
			Enabled:  enabled,
			Subjects: abac.SubjectSelector{Roles: []string{"auditor"}},
			Rules: []abac.Rule{{
				Name:      "deny-secrets",
				Effect:    abac.EffectDeny,
				Actions:   []string{"read"},
				Resources: []string{"/secrets/**"},
			}},
		},
	}
}

func newTestPolicyService(t *testing.T) (*PolicyService, *kvstore.MemoryKVStore) {
	t.Helper()
	store := kvstore.NewMemoryKVStore()
	svc, err := NewPolicyService(policyStores{policyTestTenant: store})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	clock := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	return svc, store
}

func TestNewPolicyService_NilResolver(t *testing.T) {
	_, err := NewPolicyService(nil)
	var nc *SubsystemNotConfiguredError
	if !errors.As(err, &nc) {
		t.Fatalf("NewPolicyService(nil) = %v, want *SubsystemNotConfiguredError", err)
	}
}

func TestNewPolicyService_Resolver(t *testing.T) {
	svc, err := NewPolicyService(policyStores{})
	if err != nil || svc == nil {
		t.Fatalf("NewPolicyService = %v, %v", svc, err)
	}
}

// TestPolicyService_EnabledPresenceRoundTrips is the row's contract: an
// omitted enabled reads back nil, false reads back false and true reads
// back true, through create, get, list and update.
func TestPolicyService_EnabledPresenceRoundTrips(t *testing.T) {
	cases := []struct {
		name    string
		enabled *bool
		set     bool
		eff     bool
	}{
		{"absent", nil, false, true},
		{"false", boolPtr(false), true, false},
		{"true", boolPtr(true), true, true},
	}
	svc, store := newTestPolicyService(t)
	ctx := policyCtx(policyTestTenant)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := "p-" + tc.name
			created, err := svc.CreatePolicy(ctx, testPolicy(name, tc.enabled))
			if err != nil {
				t.Fatalf("CreatePolicy: %v", err)
			}
			got, err := svc.GetPolicy(ctx, "", name)
			if err != nil {
				t.Fatalf("GetPolicy: %v", err)
			}
			for label, p := range map[string]*abac.Policy{"create": created, "get": got} {
				if p.Spec.EnabledSet() != tc.set || p.Spec.IsEnabled() != tc.eff {
					t.Fatalf("%s: EnabledSet=%v IsEnabled=%v, want %v %v", label, p.Spec.EnabledSet(), p.Spec.IsEnabled(), tc.set, tc.eff)
				}
			}
			raw, err := store.Get(ctx, policyKey(name))
			if err != nil {
				t.Fatalf("store.Get: %v", err)
			}
			if present := strings.Contains(string(raw), `"enabled"`); present != tc.set {
				t.Fatalf("stored record has enabled=%v, want %v: %s", present, tc.set, raw)
			}
			updated, err := svc.UpdatePolicy(ctx, testPolicy(name, tc.enabled))
			if err != nil {
				t.Fatalf("UpdatePolicy: %v", err)
			}
			if updated.Spec.EnabledSet() != tc.set || updated.Spec.IsEnabled() != tc.eff {
				t.Fatalf("update: EnabledSet=%v IsEnabled=%v", updated.Spec.EnabledSet(), updated.Spec.IsEnabled())
			}
		})
	}
	list, err := svc.ListPolicies(ctx, policyTestTenant)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if len(list) != len(cases) {
		t.Fatalf("ListPolicies returned %d, want %d", len(list), len(cases))
	}
	want := map[string]*bool{"p-absent": nil, "p-false": boolPtr(false), "p-true": boolPtr(true)}
	for _, p := range list {
		w := want[p.Metadata.Name]
		if (w == nil) != (p.Spec.Enabled == nil) || w != nil && *w != *p.Spec.Enabled {
			t.Fatalf("list %s: Enabled=%v, want %v", p.Metadata.Name, p.Spec.Enabled, w)
		}
	}
}

// TestPolicyService_TogglesEnabledPresence pins that an update can move a
// policy between absent, false and true and each reads back as written.
func TestPolicyService_TogglesEnabledPresence(t *testing.T) {
	svc, _ := newTestPolicyService(t)
	ctx := policyCtx(policyTestTenant)
	if _, err := svc.CreatePolicy(ctx, testPolicy("toggle", nil)); err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	for _, enabled := range []*bool{boolPtr(false), nil, boolPtr(true), nil} {
		if _, err := svc.UpdatePolicy(ctx, testPolicy("toggle", enabled)); err != nil {
			t.Fatalf("UpdatePolicy: %v", err)
		}
		got, err := svc.GetPolicy(ctx, "", "toggle")
		if err != nil {
			t.Fatalf("GetPolicy: %v", err)
		}
		if (enabled == nil) != (got.Spec.Enabled == nil) || enabled != nil && *enabled != *got.Spec.Enabled {
			t.Fatalf("Enabled = %v, want %v", got.Spec.Enabled, enabled)
		}
	}
}

func TestPolicyService_CreateStampsTenantAndTimes(t *testing.T) {
	svc, _ := newTestPolicyService(t)
	in := testPolicy("stamp", nil)
	got, err := svc.CreatePolicy(policyCtx(policyTestTenant), in)
	if err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	if got.Metadata.Tenant != policyTestTenant {
		t.Fatalf("Tenant = %q, want %q", got.Metadata.Tenant, policyTestTenant)
	}
	if got.Metadata.Created.IsZero() || !got.Metadata.Created.Equal(got.Metadata.Updated) {
		t.Fatalf("Created=%v Updated=%v", got.Metadata.Created, got.Metadata.Updated)
	}
	if in.Metadata.Tenant != "" || !in.Metadata.Created.IsZero() {
		t.Fatalf("CreatePolicy modified the caller's document: %+v", in.Metadata)
	}
}

func TestPolicyService_CreateRefusesExisting(t *testing.T) {
	svc, _ := newTestPolicyService(t)
	ctx := policyCtx(policyTestTenant)
	if _, err := svc.CreatePolicy(ctx, testPolicy("dup", nil)); err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	_, err := svc.CreatePolicy(ctx, testPolicy("dup", boolPtr(false)))
	var exists *PolicyExistsError
	if !errors.As(err, &exists) || exists.Name != "dup" || exists.Tenant != policyTestTenant {
		t.Fatalf("second CreatePolicy = %v, want *PolicyExistsError", err)
	}
	got, err := svc.GetPolicy(ctx, "", "dup")
	if err != nil || got.Spec.Enabled != nil {
		t.Fatalf("the refused create changed the record: %v, %v", got, err)
	}
}

func TestPolicyService_CreateRefusesInvalid(t *testing.T) {
	svc, _ := newTestPolicyService(t)
	ctx := policyCtx(policyTestTenant)
	noRules := testPolicy("no-rules", nil)
	noRules.Spec.Rules = nil
	badKind := testPolicy("bad-kind", nil)
	badKind.Kind = "Role"
	badCondition := testPolicy("bad-cond", nil)
	badCondition.Spec.Rules[0].When = []abac.Condition{{Expression: "subject.attrs.x == 'single'"}}
	for _, p := range []*abac.Policy{nil, noRules, badKind, badCondition, testPolicy("", nil), testPolicy("a/b", nil)} {
		_, err := svc.CreatePolicy(ctx, p)
		var invalid *PolicyInvalidError
		if !errors.As(err, &invalid) && !errors.Is(err, ErrNilRequest) {
			t.Fatalf("CreatePolicy(%v) = %v, want *PolicyInvalidError", p, err)
		}
	}
}

func TestPolicyService_CrossTenantRefused(t *testing.T) {
	svc, _ := newTestPolicyService(t)
	ctx := policyCtx(policyTestTenant)
	other := testPolicy("x", nil)
	other.Metadata.Tenant = "other"
	_, createErr := svc.CreatePolicy(ctx, other)
	_, updateErr := svc.UpdatePolicy(ctx, other)
	_, getErr := svc.GetPolicy(ctx, "other", "x")
	_, listErr := svc.ListPolicies(ctx, "other")
	deleteErr := svc.DeletePolicy(ctx, "other", "x")
	for op, err := range map[string]error{"create": createErr, "update": updateErr, "get": getErr, "list": listErr, "delete": deleteErr} {
		var cross *PolicyCrossTenantError
		if !errors.As(err, &cross) || cross.CallerTenant != policyTestTenant || cross.RequestedTenant != "other" {
			t.Fatalf("%s = %v, want *PolicyCrossTenantError", op, err)
		}
	}
}

func TestPolicyService_OwnTenantNamedExplicitly(t *testing.T) {
	svc, _ := newTestPolicyService(t)
	ctx := policyCtx(policyTestTenant)
	p := testPolicy("named", nil)
	p.Metadata.Tenant = policyTestTenant
	if _, err := svc.CreatePolicy(ctx, p); err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	if _, err := svc.GetPolicy(ctx, policyTestTenant, "named"); err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
}

// TestPolicyService_TenantsAreConfined pins that one tenant's policies are
// invisible to another: each resolves to its own store.
func TestPolicyService_TenantsAreConfined(t *testing.T) {
	stores := policyStores{"acme": kvstore.NewMemoryKVStore(), "globex": kvstore.NewMemoryKVStore()}
	svc, err := NewPolicyService(stores)
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	if _, err := svc.CreatePolicy(policyCtx("acme"), testPolicy("shared-name", boolPtr(false))); err != nil {
		t.Fatalf("CreatePolicy acme: %v", err)
	}
	_, err = svc.GetPolicy(policyCtx("globex"), "", "shared-name")
	var nf *PolicyNotFoundError
	if !errors.As(err, &nf) || nf.Tenant != "globex" {
		t.Fatalf("globex GetPolicy = %v, want *PolicyNotFoundError", err)
	}
	list, err := svc.ListPolicies(policyCtx("globex"), "")
	if err != nil || len(list) != 0 {
		t.Fatalf("globex ListPolicies = %v, %v; want empty", list, err)
	}
	if _, err := svc.CreatePolicy(policyCtx("globex"), testPolicy("shared-name", nil)); err != nil {
		t.Fatalf("CreatePolicy globex: %v", err)
	}
}

func TestPolicyService_GetNotFoundAndInvalidName(t *testing.T) {
	svc, _ := newTestPolicyService(t)
	ctx := policyCtx(policyTestTenant)
	_, err := svc.GetPolicy(ctx, "", "missing")
	var nf *PolicyNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("GetPolicy(missing) = %v, want *PolicyNotFoundError", err)
	}
	_, err = svc.GetPolicy(ctx, "", "../escape")
	var invalid *PolicyInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("GetPolicy(../escape) = %v, want *PolicyInvalidError", err)
	}
}

func TestPolicyService_ListSortedAndEmpty(t *testing.T) {
	svc, _ := newTestPolicyService(t)
	ctx := policyCtx(policyTestTenant)
	list, err := svc.ListPolicies(ctx, "")
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty ListPolicies = %v, %v; want empty non-nil", list, err)
	}
	for _, name := range []string{"zeta", "alpha", "mid"} {
		if _, err := svc.CreatePolicy(ctx, testPolicy(name, nil)); err != nil {
			t.Fatalf("CreatePolicy %s: %v", name, err)
		}
	}
	list, err = svc.ListPolicies(ctx, "")
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	var names []string
	for _, p := range list {
		names = append(names, p.Metadata.Name)
	}
	if strings.Join(names, ",") != "alpha,mid,zeta" {
		t.Fatalf("ListPolicies names = %v", names)
	}
}

func TestPolicyService_UpdateKeepsCreated(t *testing.T) {
	svc, _ := newTestPolicyService(t)
	ctx := policyCtx(policyTestTenant)
	created, err := svc.CreatePolicy(ctx, testPolicy("keep", nil))
	if err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	in := testPolicy("keep", boolPtr(false))
	in.Metadata.Created = time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	updated, err := svc.UpdatePolicy(ctx, in)
	if err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	if !updated.Metadata.Created.Equal(created.Metadata.Created) {
		t.Fatalf("Created = %v, want %v", updated.Metadata.Created, created.Metadata.Created)
	}
	if !updated.Metadata.Updated.After(created.Metadata.Updated) {
		t.Fatalf("Updated = %v, not after %v", updated.Metadata.Updated, created.Metadata.Updated)
	}
}

func TestPolicyService_UpdateNotFoundAndInvalid(t *testing.T) {
	svc, _ := newTestPolicyService(t)
	ctx := policyCtx(policyTestTenant)
	_, err := svc.UpdatePolicy(ctx, testPolicy("missing", nil))
	var nf *PolicyNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("UpdatePolicy(missing) = %v, want *PolicyNotFoundError", err)
	}
	if _, err := svc.UpdatePolicy(ctx, nil); !errors.Is(err, ErrNilRequest) {
		t.Fatalf("UpdatePolicy(nil) = %v, want ErrNilRequest", err)
	}
}

func TestPolicyService_DeleteAndNotFound(t *testing.T) {
	svc, _ := newTestPolicyService(t)
	ctx := policyCtx(policyTestTenant)
	if _, err := svc.CreatePolicy(ctx, testPolicy("gone", nil)); err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	if err := svc.DeletePolicy(ctx, "", "gone"); err != nil {
		t.Fatalf("DeletePolicy: %v", err)
	}
	var nf *PolicyNotFoundError
	if _, err := svc.GetPolicy(ctx, "", "gone"); !errors.As(err, &nf) {
		t.Fatalf("GetPolicy after delete = %v, want *PolicyNotFoundError", err)
	}
	if err := svc.DeletePolicy(ctx, "", "gone"); !errors.As(err, &nf) {
		t.Fatalf("second DeletePolicy = %v, want *PolicyNotFoundError", err)
	}
	var invalid *PolicyInvalidError
	if err := svc.DeletePolicy(ctx, "", ""); !errors.As(err, &invalid) {
		t.Fatalf("DeletePolicy(\"\") = %v, want *PolicyInvalidError", err)
	}
}

func TestPolicyService_LostGuardedWriteIsConflict(t *testing.T) {
	inner := kvstore.NewMemoryKVStore()
	svc, err := NewPolicyService(policyStores{policyTestTenant: racingStore{inner}})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	ctx := policyCtx(policyTestTenant)
	if _, err := svc.CreatePolicy(ctx, testPolicy("race", nil)); err != nil {
		t.Fatalf("CreatePolicy: %v", err)
	}
	var conflict *PolicyConflictError
	if _, err := svc.UpdatePolicy(ctx, testPolicy("race", boolPtr(false))); !errors.As(err, &conflict) {
		t.Fatalf("UpdatePolicy = %v, want *PolicyConflictError", err)
	}
	if err := svc.DeletePolicy(ctx, "", "race"); !errors.As(err, &conflict) {
		t.Fatalf("DeletePolicy = %v, want *PolicyConflictError", err)
	}
}

func TestPolicyService_NonAtomicStoreRefused(t *testing.T) {
	inner := kvstore.NewMemoryKVStore()
	svc, err := NewPolicyService(policyStores{policyTestTenant: plainKVStore{inner}})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	ctx := policyCtx(policyTestTenant)
	var unsupported *PolicyStoreUnsupportedError
	if _, err := svc.CreatePolicy(ctx, testPolicy("p", nil)); !errors.As(err, &unsupported) {
		t.Fatalf("CreatePolicy = %v, want *PolicyStoreUnsupportedError", err)
	}
	if _, err := svc.UpdatePolicy(ctx, testPolicy("p", nil)); !errors.As(err, &unsupported) {
		t.Fatalf("UpdatePolicy = %v, want *PolicyStoreUnsupportedError", err)
	}
	if err := svc.DeletePolicy(ctx, "", "p"); !errors.As(err, &unsupported) {
		t.Fatalf("DeletePolicy = %v, want *PolicyStoreUnsupportedError", err)
	}
}

func TestPolicyService_ResolverErrorPropagates(t *testing.T) {
	svc, _ := newTestPolicyService(t)
	ctx := policyCtx("sealed")
	if _, err := svc.CreatePolicy(ctx, testPolicy("p", nil)); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("CreatePolicy = %v, want ErrTenantSealed", err)
	}
	if _, err := svc.GetPolicy(ctx, "", "p"); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("GetPolicy = %v, want ErrTenantSealed", err)
	}
	if _, err := svc.ListPolicies(ctx, ""); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("ListPolicies = %v, want ErrTenantSealed", err)
	}
	if err := svc.DeletePolicy(ctx, "", "p"); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("DeletePolicy = %v, want ErrTenantSealed", err)
	}
}

func TestPolicyService_StoreErrorPropagates(t *testing.T) {
	storeErr := errors.New("store down")
	svc, err := NewPolicyService(policyStores{policyTestTenant: failingStore{kvstore.NewMemoryKVStore(), storeErr}})
	if err != nil {
		t.Fatalf("NewPolicyService: %v", err)
	}
	ctx := policyCtx(policyTestTenant)
	if _, err := svc.CreatePolicy(ctx, testPolicy("p", nil)); !errors.Is(err, storeErr) {
		t.Fatalf("CreatePolicy = %v", err)
	}
	if _, err := svc.GetPolicy(ctx, "", "p"); !errors.Is(err, storeErr) {
		t.Fatalf("GetPolicy = %v", err)
	}
	if _, err := svc.ListPolicies(ctx, ""); !errors.Is(err, storeErr) {
		t.Fatalf("ListPolicies = %v", err)
	}
	if _, err := svc.UpdatePolicy(ctx, testPolicy("p", nil)); !errors.Is(err, storeErr) {
		t.Fatalf("UpdatePolicy = %v", err)
	}
	if err := svc.DeletePolicy(ctx, "", "p"); !errors.Is(err, storeErr) {
		t.Fatalf("DeletePolicy = %v", err)
	}
}

func TestPolicyService_CorruptRecordsRefused(t *testing.T) {
	svc, store := newTestPolicyService(t)
	ctx := policyCtx(policyTestTenant)
	if err := store.Put(ctx, policyKey("garbage"), []byte("{not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	misnamed, err := json.Marshal(testPolicy("other-name", nil))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := store.Put(ctx, policyKey("misnamed"), misnamed); err != nil {
		t.Fatalf("Put: %v", err)
	}
	var corrupt *PolicyCorruptError
	for _, name := range []string{"garbage", "misnamed"} {
		if _, err := svc.GetPolicy(ctx, "", name); !errors.As(err, &corrupt) {
			t.Fatalf("GetPolicy(%s) = %v, want *PolicyCorruptError", name, err)
		}
	}
	if _, err := svc.ListPolicies(ctx, ""); !errors.As(err, &corrupt) {
		t.Fatalf("ListPolicies = %v, want *PolicyCorruptError", err)
	}
	if _, err := svc.UpdatePolicy(ctx, testPolicy("garbage", nil)); !errors.As(err, &corrupt) {
		t.Fatalf("UpdatePolicy = %v, want *PolicyCorruptError", err)
	}
}

func TestValidatePolicyName(t *testing.T) {
	valid := []string{"a", "A1", "eng.guardrail_v2-final", "9lives", strings.Repeat("x", maxPolicyNameLen)}
	for _, name := range valid {
		if err := validatePolicyName(name); err != nil {
			t.Errorf("validatePolicyName(%q) = %v, want nil", name, err)
		}
	}
	invalid := []string{"", "-lead", ".hidden", "_x", "a/b", "a b", "ü", strings.Repeat("x", maxPolicyNameLen+1)}
	for _, name := range invalid {
		var e *PolicyInvalidError
		if err := validatePolicyName(name); !errors.As(err, &e) {
			t.Errorf("validatePolicyName(%q) = %v, want *PolicyInvalidError", name, err)
		}
	}
}

func TestPolicyErrors_Messages(t *testing.T) {
	cause := errors.New("cause")
	cases := []struct {
		err  error
		want string
	}{
		{&PolicyInvalidError{Name: "p", Reason: "compile", Err: cause}, `service: invalid abac policy "p": compile: cause`},
		{&PolicyInvalidError{Name: "p", Reason: "bad name"}, `service: invalid abac policy "p": bad name`},
		{&PolicyNotFoundError{Tenant: "t", Name: "p"}, `service: abac policy "p" not found in tenant "t"`},
		{&PolicyExistsError{Tenant: "t", Name: "p"}, `service: abac policy "p" already exists in tenant "t"`},
		{&PolicyConflictError{Tenant: "t", Name: "p"}, `service: abac policy "p" in tenant "t" changed concurrently`},
		{&PolicyCrossTenantError{CallerTenant: "a", RequestedTenant: "b"}, `service: abac policies of tenant "b" are not reachable from tenant "a"`},
		{&PolicyCorruptError{Tenant: "t", Key: "k", Err: cause}, `service: corrupt abac policy record "k" in tenant "t": cause`},
		{&PolicyStoreUnsupportedError{Tenant: "t"}, `service: the store of tenant "t" does not support guarded writes for abac policies`},
	}
	for _, tc := range cases {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("%T.Error() = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestPolicyErrors_Unwrap(t *testing.T) {
	cause := errors.New("cause")
	if !errors.Is(&PolicyInvalidError{Err: cause}, cause) {
		t.Fatal("PolicyInvalidError does not unwrap to its cause")
	}
	if !errors.Is(&PolicyCorruptError{Err: cause}, cause) {
		t.Fatal("PolicyCorruptError does not unwrap to its cause")
	}
	if (&PolicyInvalidError{}).Unwrap() != nil || (&PolicyCorruptError{}).Unwrap() != nil {
		t.Fatal("Unwrap of an error without a cause is not nil")
	}
}
