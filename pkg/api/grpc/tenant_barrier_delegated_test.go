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

package grpc

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

const barrierDelegatedPrincipal = "grpc-barrier-deleg-test"

func init() {
	permission.RegisterRoleGrant(barrierDelegatedPrincipal, permission.TenantBarrierSeal, permission.TenantBarrierOfficerList)
}

// newDelegatedBarrierServer returns a tenant barrier server whose gate
// verifies d's delegations and audits every delegated decision, and the
// context carrying d's delegated identity.
func newDelegatedBarrierServer(t *testing.T) (*tenantBarrierServer, *authtest.Delegation) {
	t.Helper()
	d := authtest.NewDelegation(t, delegation.Grant{Tenant: "acme", Principal: barrierDelegatedPrincipal,
		Scope: []string{string(permission.TenantBarrierSeal), string(permission.TenantBarrierOfficerList)}})
	roleSvc := &barrierGateRoleSvc{perms: []permission.Permission{permission.TenantBarrierSeal, permission.TenantBarrierOfficerList}}
	mw := authmw.New(rbac.New(roleSvc),
		authmw.WithRoleResolver(authtest.BuiltInRoles(t, "acme")),
		authmw.WithDelegationVerifier(d.Authenticator),
		authmw.WithAuditLogger(audit.NewDecisionLogger(audit.NewRingBuffer(8))))
	return &tenantBarrierServer{svc: newMockTenantService(), authMW: mw}, d
}

// TestTenantBarrierGate_Delegated_SealAndOfficerList proves a #873
// delegation for acme passes the seal and officer_list gates in acme.
func TestTenantBarrierGate_Delegated_SealAndOfficerList(t *testing.T) {
	srv, d := newDelegatedBarrierServer(t)
	ctx := auth.WithIdentity(t.Context(), d.Identity(t))

	if _, err := srv.Seal(ctx, &pb.TenantBarrierSealRequest{TenantId: "acme"}); err != nil {
		t.Fatalf("delegated Seal in acme: %v; want the gate to pass", err)
	}
	if _, err := srv.OfficerList(ctx, &pb.TenantBarrierOfficerListRequest{TenantId: "acme"}); err != nil {
		t.Fatalf("delegated OfficerList in acme: %v; want the gate to pass", err)
	}
}

// TestTenantBarrierGate_Delegated_CrossTenantDenied proves the same
// delegation is PermissionDenied on another tenant's barrier.
func TestTenantBarrierGate_Delegated_CrossTenantDenied(t *testing.T) {
	srv, d := newDelegatedBarrierServer(t)
	ctx := auth.WithIdentity(t.Context(), d.Identity(t))

	if _, err := srv.Seal(ctx, &pb.TenantBarrierSealRequest{TenantId: "bob-corp"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("delegated Seal in bob-corp: %v; want PermissionDenied", err)
	}
	if _, err := srv.OfficerList(ctx, &pb.TenantBarrierOfficerListRequest{TenantId: "bob-corp"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("delegated OfficerList in bob-corp: %v; want PermissionDenied", err)
	}
}
