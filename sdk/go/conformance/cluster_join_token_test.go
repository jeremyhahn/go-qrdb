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

package conformance

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// joinTokenTestTrustDomain is the SPIFFE trust domain this suite's minted
// tokens are bound to.
const joinTokenTestTrustDomain = "conformance.cluster"

// bootJoinTokenCell boots a node over tk whose acting identity holds
// roleName in callerTenant, with a replicated join-token store wired into
// the node's AdminService (service.AdminService.SetClusterJoinTokenStore) --
// the same store cmd/qrdbd wires on --cluster-join-enabled -- so
// MintJoinToken is actually configured rather than refusing every caller
// with *service.ClusterJoinTokenNotConfiguredError regardless of
// permission. Works for every transportKind, embedded included; see
// clusterJoinTokenRefusals's doc comment for why only the refusal
// assertion is remote-only.
func bootJoinTokenCell(t *testing.T, tk transportKind, callerTenant, roleName string) *sdk.Client {
	t.Helper()
	return bootCellAs(t, tk, callerTenant, nil, func(n *node.Node) {
		seedJoinTokenFixtures(t, n, callerTenant, roleName)

		adminSvc, ok := n.Services().Admin.(*service.AdminService)
		if !ok {
			t.Fatalf("%s: node's AdminService is not *service.AdminService", tk)
		}
		adminSvc.SetClusterJoinTokenStore(kvstore.NewMemoryKVStore(), joinTokenTestTrustDomain)
	})
}

// seedJoinTokenFixtures creates callerTenant (when not the system tenant,
// which already exists) and reconciles its built-in roles, then creates the
// "matrix" user the remote cells authenticate as (via their mTLS client
// certificate, issued to that username/tenant by bootCellAs), holding
// roleName.
func seedJoinTokenFixtures(t *testing.T, n *node.Node, callerTenant, roleName string) {
	t.Helper()
	svc := n.Services()
	ctx := boundedCtx(t)
	if callerTenant != tenantid.System {
		if _, err := svc.Tenant.CreateTenant(ctx, callerTenant, "Tenant "+callerTenant, tenant.IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", callerTenant, err)
		}
	}
	if _, err := svc.Role.ReconcileBuiltInRoles(ctx, callerTenant); err != nil {
		t.Fatalf("ReconcileBuiltInRoles(%s): %v", callerTenant, err)
	}
	// ReconcileBuiltInRoles deliberately does NOT create Owner when it is
	// absent (pkg/role/reconcile.go's reconcilableRoles: "provisioned only
	// under the standard RBAC posture"), so a node that never ran the
	// first-boot bootstrap-admin path -- this minimal harness boot -- has
	// no Owner role for any tenant yet. Create it explicitly, with its
	// canonical id and permission set (CreateRole's requireCanonicalSystemRole
	// refuses anything else for a reserved name), tolerating
	// *role.RoleNameExistsError for a tenant that already has one.
	if roleName == role.RoleNameOwner {
		_, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
			ID:          role.SystemRoleID(callerTenant, role.RoleNameOwner),
			Name:        role.RoleNameOwner,
			TenantID:    callerTenant,
			Permissions: permission.All(),
		})
		if err != nil {
			var exists *role.RoleNameExistsError
			if !errors.As(err, &exists) {
				t.Fatalf("CreateRole(owner, %s): %v", callerTenant, err)
			}
		}
	}
	roleID := role.SystemRoleID(callerTenant, roleName)
	if _, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "matrix",
		TenantID:             callerTenant,
		RoleIDs:              []string{roleID},
	}); err != nil {
		t.Fatalf("create user in %s with role %s: %v", callerTenant, roleName, err)
	}
}

// requireMinter type-asserts c's Admin client to transport.
// ClusterJoinTokenMinter, failing the test if the transport does not carry
// it -- the capability itself must exist on every remote transport
// regardless of whether this specific call is expected to succeed.
func requireMinter(t *testing.T, tk transportKind, c *sdk.Client) transport.ClusterJoinTokenMinter {
	t.Helper()
	minter, ok := c.Admin().(transport.ClusterJoinTokenMinter)
	if !ok {
		t.Fatalf("%s: AdminClient does not implement transport.ClusterJoinTokenMinter", tk)
	}
	return minter
}

// clusterJoinTokenRefusals reuses crossTenantRefusals' permission-denied
// classification for a DIFFERENT cause (ScopeCross, not tenant
// confinement): both ultimately classify to errclass.PermissionDenied and
// render identically on every wire transport (same HTTP status, gRPC code,
// QUIC wire status and MCP wire code), so the same per-transport checks
// apply unchanged.
//
// embedded has no entry, and this suite skips it (tk.remote()):
// service.AdminService.MintJoinToken, like every other AdminServicer verb
// (AddMember, RemoveMember, Drain, ...), performs no permission check of
// its own -- enforcement for all of them lives in the transport/handler
// layer (REST middleware, the gRPC interceptor reading the proto method's
// permission option, QUIC's handleIf, MCP's Perm field), which the embedded
// transport skips entirely by calling the service directly with no handler
// in between. That is a pre-existing property of every admin verb, not
// something this capability introduces: pkg/api/{rest,grpc,quic,unix,mcp}/
// cluster_admin_scope_test.go already test the identical ScopeCross class
// of verbs (AddMember et al.) over exactly these five transports, with no
// embedded counterpart in any of those packages.
var clusterJoinTokenRefusals = crossTenantRefusals

// TestMintJoinToken_SystemOwnerSucceeds_TenantAdminRefused proves
// cluster:join_token mints identically for the system tenant's Owner (the
// only built-in role that holds a platform-only permission), and is
// refused identically -- the ScopeCross denial, not a missing-capability
// error -- for a tenant's Admin, on every remote transport (REST, gRPC,
// QUIC, Unix, MCP).
func TestMintJoinToken_SystemOwnerSucceeds_TenantAdminRefused(t *testing.T) {
	for _, tk := range transportKinds {
		if !tk.remote() {
			continue
		}

		t.Run(string(tk)+"/system-owner", func(t *testing.T) {
			client := bootJoinTokenCell(t, tk, tenantid.System, role.RoleNameOwner)
			minter := requireMinter(t, tk, client)

			resp, err := minter.MintJoinToken(boundedCtx(t), &transport.MintJoinTokenRequest{NodeID: "node-owner-" + string(tk)})
			if err != nil {
				t.Fatalf("%s: system Owner MintJoinToken: %v", tk, err)
			}
			if resp.Token == "" {
				t.Fatalf("%s: MintJoinToken returned an empty token", tk)
			}
			if !resp.ExpiresAt.After(time.Now()) {
				t.Fatalf("%s: MintJoinToken's ExpiresAt (%v) is not in the future", tk, resp.ExpiresAt)
			}
		})

		t.Run(string(tk)+"/tenant-admin-refused", func(t *testing.T) {
			client := bootJoinTokenCell(t, tk, confineAcme, role.RoleNameAdmin)
			minter := requireMinter(t, tk, client)

			_, err := minter.MintJoinToken(boundedCtx(t), &transport.MintJoinTokenRequest{NodeID: "node-tenant-" + string(tk)})
			if err == nil {
				t.Fatalf("%s: tenant Admin MintJoinToken succeeded, want a ScopeCross refusal", tk)
			}
			refused, ok := clusterJoinTokenRefusals[tk]
			if !ok {
				t.Fatalf("%s declares no permission-denied refusal", tk)
			}
			if !refused(err) {
				t.Fatalf("%s: tenant Admin MintJoinToken error = %T (%v), want the ScopeCross permission-denied refusal", tk, err, err)
			}
		})
	}
}

// bootJoinTokenCellWithExplicitGrant is bootJoinTokenCell's sibling for the
// explicit-grant proof: callerTenant's caller holds a CUSTOM role --
// "explicit-grant", not a built-in -- whose permission set is exactly
// {cluster:join_token}, created directly rather than resolved by name. The
// RBAC authorizer's ScopeCross check runs independently of what permissions
// a role carries: it also requires the CALLER's tenant to be the system
// tenant, so this role existing and holding the permission must still not
// be enough for a tenant caller.
func bootJoinTokenCellWithExplicitGrant(t *testing.T, tk transportKind, callerTenant string) *sdk.Client {
	t.Helper()
	return bootCellAs(t, tk, callerTenant, nil, func(n *node.Node) {
		svc := n.Services()
		ctx := boundedCtx(t)
		if _, err := svc.Tenant.CreateTenant(ctx, callerTenant, "Tenant "+callerTenant, tenant.IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", callerTenant, err)
		}
		if _, err := svc.Role.ReconcileBuiltInRoles(ctx, callerTenant); err != nil {
			t.Fatalf("ReconcileBuiltInRoles(%s): %v", callerTenant, err)
		}

		created, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
			Name:        "explicit-grant",
			TenantID:    callerTenant,
			Permissions: []permission.Permission{permission.ClusterJoinToken},
		})
		if err != nil {
			t.Fatalf("CreateRole(explicit-grant, %s): %v", callerTenant, err)
		}
		// The whole point of this role is the grant it carries: confirm
		// CreateRole actually stored cluster:join_token rather than
		// silently dropping or substituting it, which would make the
		// refusal below prove nothing -- a role that never held the
		// permission is refused for a reason that has nothing to do with
		// ScopeCross.
		if !slices.Contains(created.Permissions, permission.ClusterJoinToken) {
			t.Fatalf("CreateRole(explicit-grant, %s).Permissions = %v, want it to contain %s",
				callerTenant, created.Permissions, permission.ClusterJoinToken)
		}

		if _, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
			WithoutEmailLoginKey: true,
			Username:             "matrix",
			TenantID:             callerTenant,
			RoleIDs:              []string{created.ID},
		}); err != nil {
			t.Fatalf("create user in %s with explicit-grant role: %v", callerTenant, err)
		}

		adminSvc, ok := n.Services().Admin.(*service.AdminService)
		if !ok {
			t.Fatalf("%s: node's AdminService is not *service.AdminService", tk)
		}
		adminSvc.SetClusterJoinTokenStore(kvstore.NewMemoryKVStore(), joinTokenTestTrustDomain)
	})
}

// TestMintJoinToken_ExplicitTenantGrantStillRefused is the strengthened
// scope proof: a tenant role holding cluster:join_token not by omission
// but by EXPLICIT grant is still refused, end to end, on every remote
// transport. This is what distinguishes a genuine ScopeCross confinement
// from a merely-absent-by-default permission -- the built-in-role test
// above (tenant Admin, which simply never carries the permission) could
// not tell the two apart on its own.
func TestMintJoinToken_ExplicitTenantGrantStillRefused(t *testing.T) {
	for _, tk := range transportKinds {
		if !tk.remote() {
			continue
		}
		t.Run(string(tk), func(t *testing.T) {
			client := bootJoinTokenCellWithExplicitGrant(t, tk, confineAcme)
			minter := requireMinter(t, tk, client)

			_, err := minter.MintJoinToken(boundedCtx(t), &transport.MintJoinTokenRequest{NodeID: "node-explicit-grant-" + string(tk)})
			if err == nil {
				t.Fatalf("%s: MintJoinToken succeeded for a tenant role explicitly granted cluster:join_token, want a ScopeCross refusal", tk)
			}
			refused, ok := clusterJoinTokenRefusals[tk]
			if !ok {
				t.Fatalf("%s declares no permission-denied refusal", tk)
			}
			if !refused(err) {
				t.Fatalf("%s: explicit-grant MintJoinToken error = %T (%v), want the ScopeCross permission-denied refusal", tk, err, err)
			}
		})
	}
}

// TestMintJoinToken_EmbeddedCapabilityExists proves the capability itself --
// the verb is callable and configured -- on the embedded transport too, the
// sixth of "every capability on all six transports". It does NOT assert a
// ScopeCross refusal for a tenant caller, because none of the other
// AdminServicer verbs enforce one on this transport either (see
// clusterJoinTokenRefusals's doc comment): asserting a refusal here would
// pass only by `service.AdminService.MintJoinToken` gaining a permission
// check no sibling verb has, which is a broader change than this test's
// job to make. What this DOES prove: embedded is not silently missing the
// verb, and a caller presumed privileged (an in-process embedder is its own
// admission control, same as for AddMember) can use it.
func TestMintJoinToken_EmbeddedCapabilityExists(t *testing.T) {
	client := bootJoinTokenCell(t, embeddedTransport, tenantid.System, role.RoleNameOwner)
	minter := requireMinter(t, embeddedTransport, client)

	resp, err := minter.MintJoinToken(boundedCtx(t), &transport.MintJoinTokenRequest{NodeID: "node-embedded"})
	if err != nil {
		t.Fatalf("embedded: MintJoinToken: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("embedded: MintJoinToken returned an empty token")
	}
}
