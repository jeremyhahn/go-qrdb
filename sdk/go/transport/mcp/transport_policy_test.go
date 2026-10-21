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

package mcp

import (
	"context"
	"errors"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// connectPolicyInProcess serves the qrdb_abac_policy_* tools of a real
// PolicyService in process and returns a connected client.
func connectPolicyInProcess(t *testing.T) *Transport {
	t.Helper()
	svc, err := service.NewPolicyService(dekStores{})
	if err != nil {
		t.Fatal(err)
	}
	srv := mcpapi.NewServer(newMockKVService(), newMockAdminService(), ":0", mcpapi.WithPolicyService(svc))
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func mcpTestPolicy(name string, enabled *bool) *abac.Policy {
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

func TestPolicy_RoundTripThroughTheServerTools(t *testing.T) {
	tr := connectPolicyInProcess(t)
	ctx := context.Background()

	if p, err := tr.CreateABACPolicy(ctx, mcpTestPolicy("p", nil)); err != nil || p.Metadata.Name != "p" || p.Metadata.Created.IsZero() {
		t.Fatalf("create = %+v, %v", p, err)
	}
	if p, err := tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"}); err != nil || p.Spec.Enabled != nil {
		t.Fatalf("get = %+v, %v", p, err)
	}
	off := false
	if p, err := tr.UpdateABACPolicy(ctx, mcpTestPolicy("p", &off)); err != nil || p.Spec.Enabled == nil || *p.Spec.Enabled {
		t.Fatalf("update = %+v, %v", p, err)
	}
	if list, err := tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{}); err != nil || len(list.Policies) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := tr.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	list, err := tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{})
	if err != nil || list.Policies == nil || len(list.Policies) != 0 {
		t.Fatalf("list after delete = %#v, %v, want an empty array", list, err)
	}
}

func TestPolicy_RefusalsReachTheClient(t *testing.T) {
	tr := connectPolicyInProcess(t)
	ctx := context.Background()
	if _, err := tr.CreateABACPolicy(ctx, mcpTestPolicy("p", nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.CreateABACPolicy(ctx, mcpTestPolicy("p", nil)); err == nil {
		t.Error("a duplicate name must fail")
	}
	if _, err := tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "absent"}); err == nil {
		t.Error("a missing policy must fail")
	}
	if err := tr.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{TenantID: "globex", Name: "p"}); err == nil {
		t.Error("a cross-tenant request must fail")
	}
}

func TestPolicy_NilRequestsAndClosed(t *testing.T) {
	tr := connectPolicyInProcess(t)
	ctx := context.Background()
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

	unencodable := mcpTestPolicy("p", nil)
	unencodable.Spec.Subjects.Match = map[string]interface{}{"f": func() {}}
	var je *jsonEncodeError
	if _, err := tr.CreateABACPolicy(ctx, unencodable); !errors.As(err, &je) {
		t.Errorf("create of an unencodable document: %v", err)
	}

	_ = tr.Close()
	closed := map[string]error{}
	_, closed["create"] = tr.CreateABACPolicy(ctx, mcpTestPolicy("p", nil))
	_, closed["get"] = tr.GetABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"})
	_, closed["list"] = tr.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{})
	_, closed["update"] = tr.UpdateABACPolicy(ctx, mcpTestPolicy("p", nil))
	closed["delete"] = tr.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "p"})
	for op, err := range closed {
		var ce *transport.ClosedError
		if !errors.As(err, &ce) {
			t.Errorf("%s after close: %v", op, err)
		}
	}
}
