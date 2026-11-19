//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

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
	"crypto/x509"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

const barrierDelegatedPrincipal = "mcp-barrier-deleg-test"

func init() {
	permission.RegisterRoleGrant(barrierDelegatedPrincipal, permission.TenantBarrierSeal, permission.TenantBarrierOfficerList)
}

// delegatedBarrierResults runs the seal and officer_list tools for tenantID,
// gated by a middleware that verifies a #873 delegation for acme, as that
// delegation, and returns each tool's result by operation name.
func delegatedBarrierResults(t *testing.T, tenantID string) map[string]*mcp.CallToolResult {
	t.Helper()
	d := authtest.NewDelegation(t, delegation.Grant{Tenant: "acme", Principal: barrierDelegatedPrincipal,
		Scope: []string{string(permission.TenantBarrierSeal), string(permission.TenantBarrierOfficerList)}})
	roles := authtest.BuiltInRoles(t, "acme", "bob-corp")
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
		authmw.WithDelegationVerifier(d.Authenticator),
		authmw.WithAuditLogger(audit.NewDecisionLogger(audit.NewRingBuffer(8))))
	ctx, _, err := mw.AuthenticateDelegation(t.Context(), []*x509.Certificate{d.Certificate})
	if err != nil {
		t.Fatalf("AuthenticateDelegation: %v", err)
	}

	svc := newMockTenantService()
	results := map[string]*mcp.CallToolResult{}
	for op, call := range map[string]struct {
		tool    string
		handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	}{
		"seal":         {"qrdb_tenant_barrier_seal", handleTenantBarrierSeal(svc, mw.AuthorizeTenant)},
		"officer_list": {"qrdb_tenant_barrier_officer_list", handleTenantHolderList(svc, mw.AuthorizeTenant)},
	} {
		res, err := call.handler(ctx, buildCallToolRequest(t, call.tool, map[string]any{"tenant_id": tenantID}))
		if err != nil {
			t.Fatalf("%s: transport error %v", op, err)
		}
		results[op] = res
	}
	return results
}

// resultMentions reports whether any text content of res contains want.
func resultMentions(res *mcp.CallToolResult, want string) bool {
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok && strings.Contains(tc.Text, want) {
			return true
		}
	}
	return false
}

// TestTenantBarrierGate_Delegated_SealAndOfficerList proves a #873
// delegation for acme passes the seal and officer_list gates in acme.
func TestTenantBarrierGate_Delegated_SealAndOfficerList(t *testing.T) {
	for op, res := range delegatedBarrierResults(t, "acme") {
		if res.IsError {
			t.Errorf("delegated %s in acme: %v; want the gate to pass", op, res.Content)
		}
	}
}

// TestTenantBarrierGate_Delegated_CrossTenantDenied proves the same
// delegation is refused on another tenant's barrier.
func TestTenantBarrierGate_Delegated_CrossTenantDenied(t *testing.T) {
	for op, res := range delegatedBarrierResults(t, "bob-corp") {
		if !res.IsError || !resultMentions(res, "permission denied") {
			t.Errorf("delegated %s in bob-corp: %v; want a permission denied result", op, res.Content)
		}
	}
}
