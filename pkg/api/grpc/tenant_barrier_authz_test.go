// Copyright 2026 Jeremy Hahn
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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// barrierGateRoleSvc is a minimal role.RoleServicer that resolves a fixed
// permission set for any role id. Only ResolvePermissions is exercised by the
// RBAC authorizer; the remaining methods panic if reached.
type barrierGateRoleSvc struct {
	perms []permission.Permission
}

func (s *barrierGateRoleSvc) ResolvePermissions(_ context.Context, _ []string) ([]permission.Permission, error) {
	return s.perms, nil
}
func (s *barrierGateRoleSvc) CreateRole(context.Context, *role.CreateRoleRequest) (*role.Role, error) {
	panic("unused")
}
func (s *barrierGateRoleSvc) GetRole(context.Context, string) (*role.Role, error) { panic("unused") }
func (s *barrierGateRoleSvc) GetRoleByName(context.Context, string, string) (*role.Role, error) {
	panic("unused")
}
func (s *barrierGateRoleSvc) UpdateRole(context.Context, *role.UpdateRoleRequest) (*role.Role, error) {
	panic("unused")
}
func (s *barrierGateRoleSvc) DeleteRole(context.Context, string) error { panic("unused") }
func (s *barrierGateRoleSvc) ListRoles(context.Context, *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	panic("unused")
}

// newBarrierGateServer wires a tenantBarrierServer with a real RBAC-backed
// auth middleware whose admin role carries the three tenant_barrier:*
// permissions. The barrier servicer itself is a no-op mock so the test
// isolates the authorization gate.
func newBarrierGateServer() *tenantBarrierServer {
	roleSvc := &barrierGateRoleSvc{perms: []permission.Permission{
		permission.TenantBarrierInit,
		permission.TenantBarrierUnseal,
		permission.TenantBarrierSeal,
	}}
	authz := rbac.New(roleSvc)
	mw := authmw.New(authz)
	return &tenantBarrierServer{svc: newMockTenantService(), authMW: mw}
}

// ctxWithIdentity binds an authenticated identity to the context the way the
// auth middleware would after authentication.
func ctxWithIdentity(tenantID string) context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  tenantID + "-admin",
		TenantID: tenantID,
		Roles:    []string{"admin"},
	})
}

// TestTenantBarrierGate_TenantAdmin_OwnTenant_Allowed proves a tenant admin
// may init their OWN tenant's barrier through the handler gate.
func TestTenantBarrierGate_TenantAdmin_OwnTenant_Allowed(t *testing.T) {
	srv := newBarrierGateServer()
	ctx := ctxWithIdentity("acme")
	req := &pb.TenantBarrierInitRequest{TenantId: "acme", Passphrase: []byte("pw")}
	if _, err := srv.Init(ctx, req); err != nil {
		t.Fatalf("tenant admin init own barrier: %v", err)
	}
}

// TestTenantBarrierGate_TenantAdmin_CrossTenant_Denied proves the gate denies
// a tenant admin of acme initializing bob-corp's barrier.
func TestTenantBarrierGate_TenantAdmin_CrossTenant_Denied(t *testing.T) {
	srv := newBarrierGateServer()
	ctx := ctxWithIdentity("acme")
	req := &pb.TenantBarrierInitRequest{TenantId: "bob-corp", Passphrase: []byte("pw")}
	_, err := srv.Init(ctx, req)
	if err == nil {
		t.Fatal("cross-tenant init must be denied")
	}
	if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %s", st.Code())
	}
}

// TestTenantBarrierGate_SystemAdmin_Denied proves the gate denies a
// system-tenant admin on any tenant barrier, even though the role grants the
// permission.
func TestTenantBarrierGate_SystemAdmin_Denied(t *testing.T) {
	srv := newBarrierGateServer()
	ctx := ctxWithIdentity(tenantpkg.SystemTenantID)
	for _, target := range []string{"acme", tenantpkg.SystemTenantID} {
		req := &pb.TenantBarrierInitRequest{TenantId: target, Passphrase: []byte("pw")}
		_, err := srv.Init(ctx, req)
		if err == nil {
			t.Fatalf("system admin init on %q must be denied", target)
		}
		if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied {
			t.Fatalf("target %q: expected PermissionDenied, got %s", target, st.Code())
		}
	}
}

// ReconcileBuiltInRoles satisfies role.RoleServicer; this double never drifts.
func (s *barrierGateRoleSvc) ReconcileBuiltInRoles(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
	return &role.ReconcileReport{TenantID: tenantID}, nil
}
