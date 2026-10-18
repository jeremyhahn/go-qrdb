// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

//go:build integration

// tenant_owner_test.go asserts transport.Identity.TenantOwner on a real
// node's embedded login ceremony. The embedded transport is the only one
// whose wire returns an identity to the caller: REST, gRPC, QUIC, MCP and
// the unix socket have no identity-returning call, and no qrdb command
// prints the caller's identity. Their per-request ownership decision is
// driven through the real CLI in tenant_owner_cli_test.go.
package integration_test

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTenantOwner_EmbeddedLogin logs in as the system tenant's Admin and
// as an Operator on a real standalone node, then demotes the Admin and logs
// in again: TenantOwner follows the role store at each login, on the
// FinishLogin identity and on both ResolveIdentity paths.
func TestTenantOwner_EmbeddedLogin(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := node.NewStandaloneNode(context.Background(), cfg, nil,
		node.WithSessionIssuer(newMemorySessionIssuer()),
		node.WithLoginFactorVerifier(signedPIVVerifier{}),
	)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })

	ctx := tenant.WithTenantID(context.Background(), tenantid.System)
	svc := n.Services()
	if _, err := svc.Role.ReconcileBuiltInRoles(ctx, tenantid.System); err != nil {
		t.Fatalf("ReconcileBuiltInRoles: %v", err)
	}
	roleID := func(name string) string {
		t.Helper()
		r, err := svc.Role.GetRoleByName(ctx, tenantid.System, name)
		if err != nil {
			t.Fatalf("GetRoleByName(%s): %v", name, err)
		}
		return r.ID
	}
	adminID, operatorID := roleID(role.RoleNameAdmin), roleID(role.RoleNameOperator)

	enroller, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:        "tenant-owner-enroller",
		TenantID:    tenantid.System,
		Permissions: []permission.Permission{permission.UserAuthFactorsAdd},
	})
	if err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	enrollerUser, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		Username: "tenant-owner-enroller",
		Email:    "tenant-owner-enroller@example.com",
		TenantID: tenantid.System,
		RoleIDs:  []string{enroller.ID},
	})
	if err != nil {
		t.Fatalf("CreateUser (enroller): %v", err)
	}
	enrollerIdentity := &auth.Identity{TenantID: tenantid.System, Roles: []string{enroller.ID}}
	enrollerIdentity.BindUser(enrollerUser.ID, enrollerUser.Username)
	enrollCtx := auth.WithIdentity(ctx, enrollerIdentity)
	client := n.Client()

	// account creates a user holding roleIDs with an enrolled PIV credential.
	account := func(name string, roleIDs ...string) (uint64, []byte) {
		t.Helper()
		u, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
			Username: name,
			Email:    name + "@example.com",
			TenantID: tenantid.System,
			RoleIDs:  roleIDs,
		})
		if err != nil {
			t.Fatalf("CreateUser (%s): %v", name, err)
		}
		cred := []byte(name + "-piv")
		if err := client.Factors().Enroll(enrollCtx, u.ID, transport.Factor{
			Type: transport.FactorTypePIV, Label: "piv", Credential: cred,
		}); err != nil {
			t.Fatalf("enrol factor (%s): %v", name, err)
		}
		return u.ID, cred
	}

	// ownerAtLogin logs in with cred and returns TenantOwner, after checking
	// that the login identity and both bearer resolutions agree on it.
	ownerAtLogin := func(step string, cred []byte) bool {
		t.Helper()
		begin, err := client.Login().BeginLogin(ctx, transport.BeginLoginRequest{Type: transport.FactorTypePIV, TenantID: tenantid.System})
		if err != nil {
			t.Fatalf("%s: BeginLogin: %v", step, err)
		}
		fin, err := client.Login().FinishLogin(ctx, transport.FinishLoginRequest{
			SessionID: begin.SessionID,
			Type:      transport.FactorTypePIV,
			LookupKey: cred,
			Signature: []byte("proof"),
		})
		if err != nil {
			t.Fatalf("%s: FinishLogin: %v", step, err)
		}
		if fin.Identity.TenantID != tenantid.System {
			t.Fatalf("%s: TenantID = %q, want %q", step, fin.Identity.TenantID, tenantid.System)
		}
		if _, ok := fin.Identity.Metadata["tenantOwner"]; ok {
			t.Fatalf("%s: Metadata must not carry tenantOwner", step)
		}
		viaLogin, err := client.Login().ResolveIdentity(ctx, fin.Bearer)
		if err != nil {
			t.Fatalf("%s: LoginClient.ResolveIdentity: %v", step, err)
		}
		viaSession, err := client.Sessions().ResolveIdentity(ctx, fin.Bearer)
		if err != nil {
			t.Fatalf("%s: SessionClient.ResolveIdentity: %v", step, err)
		}
		if viaLogin.TenantOwner != fin.Identity.TenantOwner || viaSession.TenantOwner != fin.Identity.TenantOwner {
			t.Fatalf("%s: TenantOwner disagrees: login=%v resolve(login)=%v resolve(session)=%v",
				step, fin.Identity.TenantOwner, viaLogin.TenantOwner, viaSession.TenantOwner)
		}
		return fin.Identity.TenantOwner
	}

	ownerID, ownerCred := account("tenant-owner-admin", adminID)
	_, operatorCred := account("tenant-owner-operator", operatorID)

	if !ownerAtLogin("admin login", ownerCred) {
		t.Fatal("the system tenant's Admin must log in with TenantOwner")
	}
	if ownerAtLogin("operator login", operatorCred) {
		t.Fatal("an Operator must log in without TenantOwner")
	}

	if _, err := svc.User.UpdateUser(ctx, &user.UpdateUserRequest{ID: ownerID, RoleIDs: []string{operatorID}}); err != nil {
		t.Fatalf("UpdateUser (demote): %v", err)
	}
	if ownerAtLogin("login after demotion", ownerCred) {
		t.Fatal("the login after Admin was withdrawn must not carry TenantOwner")
	}
}
