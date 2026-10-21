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
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// mcpPolicyStores resolves each tenant to its own in-memory store; a tenant
// it does not hold reads as sealed.
type mcpPolicyStores map[string]kvstore.KVStore

func (p mcpPolicyStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	if s, ok := p[tenantID]; ok {
		return s, nil
	}
	return nil, tenant.ErrTenantSealed
}

func newTestPolicyService(t *testing.T) service.PolicyServicer {
	t.Helper()
	svc, err := service.NewPolicyService(mcpPolicyStores{"acme": kvstore.NewMemoryKVStore()})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func mcpPolicyJSON(name, enabled string) string {
	spec := `"subjects":{"roles":["auditor"]},"rules":[{"name":"deny-secrets","effect":"deny","actions":["read"],"resources":["/secrets/**"]}]`
	if enabled != "" {
		spec = `"enabled":` + enabled + `,` + spec
	}
	return `{"apiVersion":"abac.qrdb.io/v1","kind":"Policy","metadata":{"name":"` + name + `"},"spec":{` + spec + `}}`
}

// TestPolicyToolsLifecycle drives create, get, list, update and delete, and
// holds the presence of spec.enabled across them: an omitted enabled reads
// back absent and an explicit false reads back false.
func TestPolicyToolsLifecycle(t *testing.T) {
	svc := newTestPolicyService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	var created abac.Policy
	if err := dekCall(t, ctx, handlePolicyCreate(svc), map[string]any{"policy": mcpPolicyJSON("p", "")}, &created); err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Metadata.Name != "p" || created.Spec.Enabled != nil {
		t.Fatalf("create = %+v, want name p and enabled absent", created)
	}

	var got abac.Policy
	if err := dekCall(t, ctx, handlePolicyGet(svc), map[string]any{"name": "p"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Metadata.Name != "p" || got.Spec.Enabled != nil {
		t.Fatalf("get = %+v", got)
	}

	var updated abac.Policy
	if err := dekCall(t, ctx, handlePolicyUpdate(svc), map[string]any{"policy": mcpPolicyJSON("p", "false")}, &updated); err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Spec.Enabled == nil || *updated.Spec.Enabled {
		t.Fatalf("update enabled = %v, want explicit false", updated.Spec.Enabled)
	}

	var list policyListResult
	if err := dekCall(t, ctx, handlePolicyList(svc), map[string]any{}, &list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Policies) != 1 || list.Policies[0].Spec.Enabled == nil || *list.Policies[0].Spec.Enabled {
		t.Fatalf("list = %+v", list.Policies)
	}

	var deleted map[string]bool
	if err := dekCall(t, ctx, handlePolicyDelete(svc), map[string]any{"name": "p"}, &deleted); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted["deleted"] {
		t.Fatalf("delete = %v", deleted)
	}
	if err := dekCall(t, ctx, handlePolicyList(svc), map[string]any{}, &list); err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if list.Policies == nil || len(list.Policies) != 0 {
		t.Fatalf("list after delete = %#v, want an empty array", list.Policies)
	}
}

// TestPolicyToolErrorsCarryTheSharedClassification holds each refusal to
// the wire code the other transports give it.
func TestPolicyToolErrorsCarryTheSharedClassification(t *testing.T) {
	svc := newTestPolicyService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	var seed abac.Policy
	if err := dekCall(t, ctx, handlePolicyCreate(svc), map[string]any{"policy": mcpPolicyJSON("p", "")}, &seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cases := []struct {
		name string
		h    func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
		args map[string]any
		want wire.Code
	}{
		{"create exists", handlePolicyCreate(svc), map[string]any{"policy": mcpPolicyJSON("p", "")}, wire.CodeAlreadyExists},
		{"create bad json", handlePolicyCreate(svc), map[string]any{"policy": "{"}, wire.CodeInvalidArgument},
		{"create bad effect", handlePolicyCreate(svc), map[string]any{"policy": strings.Replace(mcpPolicyJSON("q", ""), `"deny"`, `"maybe"`, 1)}, wire.CodeInvalidArgument},
		{"create cross tenant", handlePolicyCreate(svc), map[string]any{"policy": strings.Replace(mcpPolicyJSON("q", ""), `"name":"q"`, `"name":"q","tenant":"globex"`, 1)}, wire.CodePermissionDenied},
		{"get missing", handlePolicyGet(svc), map[string]any{"name": "missing"}, wire.CodeNotFound},
		{"list cross tenant", handlePolicyList(svc), map[string]any{"tenant_id": "globex"}, wire.CodePermissionDenied},
		{"update bad json", handlePolicyUpdate(svc), map[string]any{"policy": "["}, wire.CodeInvalidArgument},
		{"update missing", handlePolicyUpdate(svc), map[string]any{"policy": mcpPolicyJSON("missing", "")}, wire.CodeNotFound},
		{"delete missing", handlePolicyDelete(svc), map[string]any{"name": "missing"}, wire.CodeNotFound},
	}
	for _, tc := range cases {
		var out any
		err := dekCall(t, ctx, tc.h, tc.args, &out)
		if code := ServiceErrorCode(err); code != tc.want {
			t.Errorf("%s: code %v (err %v), want %v", tc.name, code, err, tc.want)
		}
	}
}

// TestPolicyToolsRequireTheirArguments holds a missing required argument to
// a tool-result error, never a service call.
func TestPolicyToolsRequireTheirArguments(t *testing.T) {
	svc := newTestPolicyService(t)
	ctx := tenant.WithTenantID(context.Background(), "acme")
	for name, h := range map[string]func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error){
		"create": handlePolicyCreate(svc),
		"get":    handlePolicyGet(svc),
		"update": handlePolicyUpdate(svc),
		"delete": handlePolicyDelete(svc),
	} {
		res, err := h(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{}}})
		if err != nil {
			t.Fatalf("%s: err %v, want a tool-result error", name, err)
		}
		if !res.IsError {
			t.Fatalf("%s: result %+v, want IsError", name, res)
		}
	}
}

// TestPolicyToolsRegisteredOnlyWithTheService holds WithPolicyService to
// the five tools: absent without it, present with it.
func TestPolicyToolsRegisteredOnlyWithTheService(t *testing.T) {
	names := []string{"qrdb_abac_policy_create", "qrdb_abac_policy_get", "qrdb_abac_policy_list", "qrdb_abac_policy_update", "qrdb_abac_policy_delete"}
	bare := NewServer(newMockKVService(), newMockAdminService(), ":0")
	with := NewServer(newMockKVService(), newMockAdminService(), ":0", WithPolicyService(newTestPolicyService(t)))
	for _, n := range names {
		if bare.mcpSrv.GetTool(n) != nil {
			t.Errorf("%s registered without WithPolicyService", n)
		}
		if with.mcpSrv.GetTool(n) == nil {
			t.Errorf("%s not registered with WithPolicyService", n)
		}
	}
}
