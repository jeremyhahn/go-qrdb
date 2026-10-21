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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// policyStores resolves each tenant to its own in-memory store; a tenant it
// does not hold reads as sealed.
type policyStores map[string]kvstore.KVStore

func (p policyStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	if s, ok := p[tenantID]; ok {
		return s, nil
	}
	return nil, tenant.ErrTenantSealed
}

func newPolicyTransport(t *testing.T) *Transport {
	t.Helper()
	svc, err := service.NewPolicyService(policyStores{"acme": kvstore.NewMemoryKVStore()})
	if err != nil {
		t.Fatal(err)
	}
	return newExtTransport(t, WithPolicyService(svc))
}

func embeddedTestPolicy(name string, enabled *bool) *abac.Policy {
	return &abac.Policy{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       abac.PolicyKind,
		Metadata:   abac.PolicyMetadata{Name: name},
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

// TestEmbeddedPolicy_LifecycleForTheBoundTenant drives every method for the
// tenant bound to ctx, and holds spec.enabled's presence across them.
func TestEmbeddedPolicy_LifecycleForTheBoundTenant(t *testing.T) {
	tr := newPolicyTransport(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	created, err := tr.CreateABACPolicy(ctx, embeddedTestPolicy("p", nil))
	if err != nil || created.Metadata.Name != "p" || created.Spec.Enabled != nil {
		t.Fatalf("create = %+v, %v", created, err)
	}
	got, err := tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"})
	if err != nil || got.Spec.Enabled != nil {
		t.Fatalf("get = %+v, %v", got, err)
	}
	off := false
	updated, err := tr.UpdateABACPolicy(ctx, embeddedTestPolicy("p", &off))
	if err != nil || updated.Spec.Enabled == nil || *updated.Spec.Enabled {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	list, err := tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{})
	if err != nil || len(list.Policies) != 1 || list.Policies[0].Spec.Enabled == nil {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := tr.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err = tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{})
	if err != nil || list.Policies == nil || len(list.Policies) != 0 {
		t.Fatalf("list after delete = %#v, %v, want an empty array", list, err)
	}
	var nf *service.PolicyNotFoundError
	if _, err := tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"}); !errors.As(err, &nf) {
		t.Fatalf("get after delete = %v, want *PolicyNotFoundError", err)
	}
}

// TestEmbeddedPolicy_RefusesAnotherTenant holds a request naming another
// tenant to the service's cross-tenant refusal.
func TestEmbeddedPolicy_RefusesAnotherTenant(t *testing.T) {
	tr := newPolicyTransport(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	var cross *service.PolicyCrossTenantError
	if _, err := tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{TenantID: "globex"}); !errors.As(err, &cross) {
		t.Fatalf("list globex = %v, want *PolicyCrossTenantError", err)
	}
	if err := tr.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{TenantID: "globex", Name: "p"}); !errors.As(err, &cross) {
		t.Fatalf("delete globex = %v, want *PolicyCrossTenantError", err)
	}
}

func TestEmbeddedPolicy_NilServiceNilRequestAndClosed(t *testing.T) {
	ctx := context.Background()
	unwired := newExtTransport(t)
	var nse *NilServiceError
	if _, err := unwired.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{}); !errors.As(err, &nse) {
		t.Errorf("unwired: %v", err)
	}

	tr := newPolicyTransport(t)
	checks := map[string]error{}
	_, checks["create"] = tr.CreateABACPolicy(ctx, nil)
	_, checks["get"] = tr.GetABACPolicy(ctx, nil)
	_, checks["list"] = tr.ListABACPolicies(ctx, nil)
	_, checks["update"] = tr.UpdateABACPolicy(ctx, nil)
	checks["delete"] = tr.DeleteABACPolicy(ctx, nil)
	for op, err := range checks {
		var nre *transport.NilRequestError
		if !errors.As(err, &nre) {
			t.Errorf("%s(nil): %v", op, err)
		}
	}

	_ = tr.Close()
	var ce *transport.ClosedError
	if err := tr.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"}); !errors.As(err, &ce) {
		t.Errorf("after close: %v", err)
	}
}
