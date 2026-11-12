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

//go:build !integration

package node

import (
	"context"
	"crypto/fips140"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// asIdentity binds id as the authenticated caller, in its own tenant, the
// way the network transports' auth middleware does once it has finalized an
// identity. Mirrors pkg/auth/middleware's own `as` test helper, so a call
// through Node.AuthMiddleware() sees the identical context shape a real
// request would.
func asIdentity(id *auth.Identity) context.Context {
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), id.TenantID)
}

// TestNode_AuthMiddlewareAndAuthorizer_ModuleTargetAuth is the go-qrdb#587
// regression: an embedding host -- or, through pkg/apimodule, a registered
// module -- reaches the node's live auth middleware through
// Node.AuthMiddleware and Node.Authorizer instead of capturing it
// indirectly through WithAuthorizerWrapper, and can authorize its OWN
// operation against a caller-supplied target tenant via
// AuthorizeTenantTarget, under the identical RBAC ScopeCross/ScopeSelf
// rules go-qrdb's own tenant routes enforce.
func TestNode_AuthMiddlewareAndAuthorizer_ModuleTargetAuth(t *testing.T) {
	n := newMemoryNodeWithUser(t)

	mw := n.AuthMiddleware()
	require.NotNil(t, mw, "AuthMiddleware() must be non-nil on a node with auth enabled")
	authz := n.Authorizer()
	require.NotNil(t, authz, "Authorizer() must be non-nil on a node with auth enabled")
	require.Same(t, authz, mw.Authorizer(),
		"Authorizer() must be the SAME instance AuthMiddleware() authorizes every transport against")

	svcNode := n.Services()
	ctx := context.Background()
	_, err := svcNode.Tenant.CreateTenant(ctx, "acme", "Acme", tenant.IsolationPrefix)
	require.NoError(t, err)

	// CreateTenant already seeds acme's canonical built-in Admin role under
	// its deterministic id; seedAdminRole would try to create it a second
	// time and fail with "already exists". The system tenant has no such
	// auto-seed on this helper's node, so its Admin role is created here.
	acmeAdminRoleID := role.SystemRoleID("acme", role.RoleNameAdmin)
	systemAdminRoleID := seedAdminRole(t, svcNode.Role, tenantid.System)

	acmeAdmin := asIdentity(&auth.Identity{Subject: "acme-admin", TenantID: "acme", Roles: []string{acmeAdminRoleID}})
	systemAdmin := asIdentity(&auth.Identity{Subject: "root", TenantID: tenantid.System, Roles: []string{systemAdminRoleID}})

	// ScopeSelf: the acme admin is admitted targeting its OWN tenant...
	require.NoError(t, mw.AuthorizeTenantTarget(acmeAdmin, "acme", permission.TenantsRead))
	// ...and refused targeting any other tenant, cross-tenant.
	require.Error(t, mw.AuthorizeTenantTarget(acmeAdmin, tenantid.System, permission.TenantsRead))

	// ScopeCross: TenantsSetIsolation is a platform-operator-only
	// permission -- the target tenant does not decide the outcome, only
	// whether the caller IS the platform operator.
	require.NoError(t, mw.AuthorizeTenantTarget(systemAdmin, "acme", permission.TenantsSetIsolation))
	require.Error(t, mw.AuthorizeTenantTarget(acmeAdmin, "acme", permission.TenantsSetIsolation),
		"a tenant admin, even on its own tenant, must not hold the platform-operator-only permission")
}

// TestNode_AuthMiddlewareAndAuthorizer_NilWhenAuthDisabled verifies both
// accessors answer nil -- not a wrapped zero value -- when the node's auth
// system is disabled, matching every other data-plane accessor's documented
// "nil in that window" contract.
func TestNode_AuthMiddlewareAndAuthorizer_NilWhenAuthDisabled(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	disabled := false
	cfg.Auth.Enabled = &disabled

	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	require.Nil(t, n.AuthMiddleware(), "AuthMiddleware() must be nil while auth is disabled")
	require.Nil(t, n.Authorizer(), "Authorizer() must be nil while auth is disabled")
}

// TestNode_StrictSeparation_ReportsResolvedPosture proves StrictSeparation
// returns the posture the node resolved at start: the setting on gives true,
// and the setting off gives what FIPS 140-3 mode forces (false outside it).
// wiring's TestResolveStrictSeparation proves FIPS forces true through the
// resolver's explicit FIPS seam.
func TestNode_StrictSeparation_ReportsResolvedPosture(t *testing.T) {
	for name, tc := range map[string]struct {
		configured bool
		want       bool
	}{
		"setting on":  {configured: true, want: true},
		"setting off": {configured: false, want: fips140.Enabled()},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := qrdbconfig.DefaultConfig()
			cfg.Raft.DataDir = t.TempDir()
			cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
			cfg.Protocols.REST = false
			cfg.Protocols.GRPC = false
			cfg.Protocols.QUIC = false
			cfg.Protocols.Unix = false
			cfg.Protocols.MCP = false
			disabled := false
			cfg.Auth.Enabled = &disabled
			cfg.Auth.SeparationOfDuties = tc.configured

			n, err := NewStandaloneNode(context.Background(), cfg, nil)
			require.NoError(t, err)
			t.Cleanup(func() { _ = n.Close() })

			require.Equal(t, tc.want, n.StrictSeparation())
			require.Equal(t, sdk.ResolveStrictSeparation(tc.configured), n.StrictSeparation(),
				"the node must report the posture the sdk resolver gives for its setting")
		})
	}
}
