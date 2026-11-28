// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package grpc

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestGRPC_TenantUpdate_MixedStatusAndIsolationPolicyRefusedWithoutTargetUpdate:
// with strict isolation active, a system principal holding tenants:update
// and tenants:set_status but not tenants:update:cross is refused a TenantUpdate
// on acme carrying status and isolation_policy; an isolation-policy-only
// update by a principal holding tenants:update:cross succeeds (Codex P1, #1423).
func TestGRPC_TenantUpdate_MixedStatusAndIsolationPolicyRefusedWithoutTargetUpdate(t *testing.T) {
	s := strictIsolationActiveGRPCServer(t)
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
	s.authMW = authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles))
	grant := func(name string, perms ...permission.Permission) *auth.Identity {
		t.Helper()
		r, err := roles.CreateRole(t.Context(), &role.CreateRoleRequest{TenantID: tenantid.System, Name: name, Permissions: perms})
		if err != nil {
			t.Fatalf("CreateRole(%s): %v", name, err)
		}
		return &auth.Identity{Subject: name, TenantID: tenantid.System, Roles: []string{r.ID}}
	}

	statusOp := auth.WithIdentity(t.Context(), grant("status-operator", permission.TenantsUpdate, permission.TenantsSetStatus))
	_, err := s.TenantUpdate(statusOp, &pb.TenantUpdateRequest{Id: "acme", Status: proto.String("active"), IsolationPolicy: proto.String("strict")})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("status+isolation_policy on acme: code = %s, want PermissionDenied (%v)", status.Code(err), err)
	}
	got, err := s.tenantSvc.GetTenant(t.Context(), "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.IsolationPolicy == tenant.TenantIsolationPolicyStrict {
		t.Fatalf("a refused update changed acme's isolation policy to %q", got.IsolationPolicy)
	}

	policyOp := auth.WithIdentity(t.Context(), grant("policy-operator", permission.TenantsUpdate, permission.TenantsUpdateCross))
	if _, err := s.TenantUpdate(policyOp, &pb.TenantUpdateRequest{Id: "acme", IsolationPolicy: proto.String("strict")}); err != nil {
		t.Fatalf("isolation_policy on acme with tenants:update:cross: %v", err)
	}
	got, err = s.tenantSvc.GetTenant(t.Context(), "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.IsolationPolicy != tenant.TenantIsolationPolicyStrict {
		t.Fatalf("acme isolation policy = %q, want strict", got.IsolationPolicy)
	}
}
