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

package wiring

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// fakeRoleService is a minimal role.RoleServicer whose ResolvePermissions
// returns the union of a fixed per-role-ID permission set. Every other
// method is unused by the tests in this file.
type fakeRoleService struct {
	perms map[string][]permission.Permission
}

func (f *fakeRoleService) CreateRole(context.Context, *role.CreateRoleRequest) (*role.Role, error) {
	return nil, errors.ErrUnsupported
}
func (f *fakeRoleService) GetRole(context.Context, string) (*role.Role, error) {
	return nil, errors.ErrUnsupported
}

// GetRoleByName answers as the real store does for a name it does not hold:
// the fake's roles are keyed by id only, so it holds no name, and the auth
// chain's tenant-ownership read finds no Admin role.
func (f *fakeRoleService) GetRoleByName(_ context.Context, _, name string) (*role.Role, error) {
	return nil, &role.RoleNotFoundError{ID: name}
}
func (f *fakeRoleService) UpdateRole(context.Context, *role.UpdateRoleRequest) (*role.Role, error) {
	return nil, errors.ErrUnsupported
}
func (f *fakeRoleService) DeleteRole(context.Context, string) error {
	return errors.ErrUnsupported
}
func (f *fakeRoleService) ListRoles(context.Context, *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	return nil, errors.ErrUnsupported
}
func (f *fakeRoleService) ResolvePermissions(_ context.Context, roleIDs []string) ([]permission.Permission, error) {
	seen := make(map[permission.Permission]struct{})
	for _, id := range roleIDs {
		for _, p := range f.perms[id] {
			seen[p] = struct{}{}
		}
	}
	out := make([]permission.Permission, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	return out, nil
}

var _ role.RoleServicer = (*fakeRoleService)(nil)

// fakeUserService is a minimal user.UserServicer that resolves a single
// fixed enabled user for GetUserByUsername. Every other method is unused
// by the tests in this file.
type fakeUserService struct {
	u *user.User
}

func (f *fakeUserService) CreateUser(context.Context, *user.CreateUserRequest) (*user.User, error) {
	return nil, errors.ErrUnsupported
}
func (f *fakeUserService) GetUser(context.Context, uint64) (*user.User, error) {
	return nil, errors.ErrUnsupported
}
func (f *fakeUserService) GetUserByUsername(_ context.Context, tenantID, username string) (*user.User, error) {
	if f.u != nil && f.u.TenantID == tenantID && f.u.Username == username {
		return f.u, nil
	}
	return nil, &user.UserNotFoundError{Lookup: tenantID + "/" + username}
}

// GetUserByEmail mirrors the real store: tenant-scoped, normalized comparison.
// A double that ignored the tenant or the normalization would pass tests the
// store itself would fail.
func (f *fakeUserService) GetUserByEmail(_ context.Context, tenantID, email string) (*user.User, error) {
	normalized := user.NormalizeEmail(email)
	if f.u != nil && f.u.TenantID == tenantID && normalized != "" && f.u.Email == normalized {
		return f.u, nil
	}
	return nil, &user.UserNotFoundError{Lookup: tenantID + "/" + normalized}
}
func (f *fakeUserService) UserByCertThumbprint(context.Context, string) (*user.User, error) {
	return nil, errors.ErrUnsupported
}
func (f *fakeUserService) AddCertBinding(context.Context, uint64, string, int64) (*user.User, error) {
	return nil, errors.ErrUnsupported
}
func (f *fakeUserService) RemoveCertBinding(context.Context, uint64, string) (*user.User, error) {
	return nil, errors.ErrUnsupported
}
func (f *fakeUserService) UpdateUser(context.Context, *user.UpdateUserRequest) (*user.User, error) {
	return nil, errors.ErrUnsupported
}
func (f *fakeUserService) DeleteUser(context.Context, uint64) error {
	return errors.ErrUnsupported
}
func (f *fakeUserService) ListUsers(context.Context, *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	return nil, errors.ErrUnsupported
}
func (f *fakeUserService) EnableUser(context.Context, uint64) error  { return errors.ErrUnsupported }
func (f *fakeUserService) DisableUser(context.Context, uint64) error { return errors.ErrUnsupported }
func (f *fakeUserService) SuspendUser(context.Context, uint64) error { return errors.ErrUnsupported }
func (f *fakeUserService) UnsuspendUser(context.Context, uint64) error {
	return errors.ErrUnsupported
}

var _ user.UserServicer = (*fakeUserService)(nil)

const testAdminRoleID = "role-admin"

func adminRoleService() *fakeRoleService {
	return &fakeRoleService{
		perms: map[string][]permission.Permission{
			testAdminRoleID: {permission.AdminStatus},
		},
	}
}

func adminIdentity() *auth.Identity {
	return &auth.Identity{
		Subject:  "admin",
		TenantID: tenantid.System,
		Roles:    []string{testAdminRoleID},
	}
}

// ---------------------------------------------------------------------------
// BuildAuthorizer
// ---------------------------------------------------------------------------

// TestBuildAuthorizer_MissingRoleService verifies the fail-loud posture: a
// nil RoleService is refused rather than silently admitting every request.
func TestBuildAuthorizer_MissingRoleService(t *testing.T) {
	_, err := BuildAuthorizer(AuthorizerConfig{})
	require.Error(t, err)
	var missing *MissingServiceError
	require.True(t, errors.As(err, &missing))
	assert.Equal(t, "role", missing.Service)
}

// TestBuildAuthorizer_RBACOnly verifies that with no ABAC configuration the
// returned authorizer enforces plain RBAC: a granted permission is admitted
// and an ungranted one is denied.
func TestBuildAuthorizer_RBACOnly(t *testing.T) {
	authorizer, err := BuildAuthorizer(AuthorizerConfig{RoleService: adminRoleService()})
	require.NoError(t, err)
	require.NotNil(t, authorizer)

	ctx := tenant.WithTenantID(context.Background(), tenantid.System)
	id := adminIdentity()

	require.NoError(t, authorizer.Authorize(ctx, id, "admin", "status"))
	require.Error(t, authorizer.Authorize(ctx, id, "seal", "rekey"))
}

// TestBuildAuthorizer_ABACDenyOverridesRBAC verifies that an ABAC deny rule
// composed via ABACPolicyDir overrides an RBAC-granted permission.
func TestBuildAuthorizer_ABACDenyOverridesRBAC(t *testing.T) {
	dir := t.TempDir()
	policy := `apiVersion: abac.qrdb.io/v1
kind: Policy
metadata:
  name: wiring-guardrail
  tenant: system
spec:
  subjects:
    any: true
  rules:
    - name: deny-status
      effect: deny
      actions: ["status"]
      resources: ["admin"]
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "guardrail.yaml"), []byte(policy), 0o600))

	authorizer, err := BuildAuthorizer(AuthorizerConfig{
		RoleService:   adminRoleService(),
		ABACEnabled:   true,
		ABACPolicyDir: dir,
	})
	require.NoError(t, err)

	ctx := tenant.WithTenantID(context.Background(), tenantid.System)
	err = authorizer.Authorize(ctx, adminIdentity(), "admin", "status")
	require.Error(t, err)
	var denied *abac.PolicyDeniedError
	require.True(t, errors.As(err, &denied))
	assert.Equal(t, "wiring-guardrail", denied.Policy)
}

// TestBuildAuthorizer_ABACBadPolicyDirFailsClosed verifies that an
// unreadable ABAC policy directory aborts construction (fail closed)
// rather than falling back to plain RBAC.
func TestBuildAuthorizer_ABACBadPolicyDirFailsClosed(t *testing.T) {
	_, err := BuildAuthorizer(AuthorizerConfig{
		RoleService:   adminRoleService(),
		ABACEnabled:   true,
		ABACPolicyDir: filepath.Join(t.TempDir(), "does-not-exist"),
	})
	require.Error(t, err)
	var dirErr *abac.PolicyDirError
	assert.True(t, errors.As(err, &dirErr))
}

// ---------------------------------------------------------------------------
// DecorateABAC
// ---------------------------------------------------------------------------

// TestDecorateABAC_Unconfigured verifies the pass-through path: neither
// enabled nor programmatic policies returns the inner authorizer unchanged.
func TestDecorateABAC_Unconfigured(t *testing.T) {
	inner := &auth.NoopAuthorizer{}
	got, err := DecorateABAC(inner, false, "", nil)
	require.NoError(t, err)
	assert.Same(t, auth.Authorizer(inner), got)
}

// TestDecorateABAC_ProgrammaticPolicies verifies that policies supplied
// programmatically (without a policy directory) activate the decorator,
// mirroring the SDK node's WithABACPolicies option.
func TestDecorateABAC_ProgrammaticPolicies(t *testing.T) {
	inner := &auth.NoopAuthorizer{}
	policies := []*abac.Policy{{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       abac.PolicyKind,
		Metadata:   abac.PolicyMetadata{Name: "prog", Tenant: tenantid.System},
		Spec: abac.PolicySpec{
			Subjects: abac.SubjectSelector{Any: true},
			Rules: []abac.Rule{{
				Name:      "deny-destroy",
				Effect:    abac.EffectDeny,
				Actions:   []string{"destroy"},
				Resources: []string{"keys"},
			}},
		},
	}}

	authorizer, err := DecorateABAC(inner, false, "", policies)
	require.NoError(t, err)
	require.NotSame(t, auth.Authorizer(inner), authorizer)

	ctx := tenant.WithTenantID(context.Background(), tenantid.System)
	err = authorizer.Authorize(ctx, adminIdentity(), "keys", "destroy")
	require.Error(t, err)
	var denied *abac.PolicyDeniedError
	require.True(t, errors.As(err, &denied))
}

// TestBuildAuthorizer_ABACDisabledPolicySkipped drives the daemon's policy
// input, auth.abac.policy_dir: a guardrail written with enabled: false must
// not deny what RBAC allows.
func TestBuildAuthorizer_ABACDisabledPolicySkipped(t *testing.T) {
	dir := t.TempDir()
	policy := `apiVersion: abac.qrdb.io/v1
kind: Policy
metadata:
  name: wiring-guardrail
  tenant: system
spec:
  enabled: false
  subjects:
    any: true
  rules:
    - name: deny-status
      effect: deny
      actions: ["status"]
      resources: ["admin"]
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "guardrail.yaml"), []byte(policy), 0o600))

	authorizer, err := BuildAuthorizer(AuthorizerConfig{
		RoleService:   adminRoleService(),
		ABACEnabled:   true,
		ABACPolicyDir: dir,
	})
	require.NoError(t, err)

	ctx := tenant.WithTenantID(context.Background(), tenantid.System)
	require.NoError(t, authorizer.Authorize(ctx, adminIdentity(), "admin", "status"))
}

// TestDecorateABAC_ProgrammaticDisabledPolicy drives the SDK's policy input
// (node.WithABACPolicies): a policy whose Spec.Enabled is false is skipped,
// so a set holding only that policy leaves the inner authorizer unwrapped.
func TestDecorateABAC_ProgrammaticDisabledPolicy(t *testing.T) {
	disabled := false
	inner := &auth.NoopAuthorizer{}
	policies := []*abac.Policy{{
		APIVersion: "abac.qrdb.io/v1",
		Kind:       abac.PolicyKind,
		Metadata:   abac.PolicyMetadata{Name: "prog", Tenant: tenantid.System},
		Spec: abac.PolicySpec{
			Enabled:  &disabled,
			Subjects: abac.SubjectSelector{Any: true},
			Rules: []abac.Rule{{
				Name:      "deny-destroy",
				Effect:    abac.EffectDeny,
				Actions:   []string{"destroy"},
				Resources: []string{"keys"},
			}},
		},
	}}

	authorizer, err := DecorateABAC(inner, false, "", policies)
	require.NoError(t, err)
	require.Same(t, auth.Authorizer(inner), authorizer)

	ctx := tenant.WithTenantID(context.Background(), tenantid.System)
	require.NoError(t, authorizer.Authorize(ctx, adminIdentity(), "keys", "destroy"))
}

// ---------------------------------------------------------------------------
// BuildAuthMiddleware
// ---------------------------------------------------------------------------

// TestBuildAuthMiddleware_Disabled verifies that a disabled config returns
// (nil, nil) even when required services are absent — the operator has
// explicitly opted out of enforcement.
func TestBuildAuthMiddleware_Disabled(t *testing.T) {
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{Enabled: false})
	require.NoError(t, err)
	assert.Nil(t, mw)
}

// TestBuildAuthMiddleware_MissingRoleService verifies the fail-loud posture
// for an enabled config missing the role service.
func TestBuildAuthMiddleware_MissingRoleService(t *testing.T) {
	_, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:     true,
		UserService: &fakeUserService{},
	})
	require.Error(t, err)
	var missing *MissingServiceError
	require.True(t, errors.As(err, &missing))
	assert.Equal(t, "role", missing.Service)
}

// TestBuildAuthMiddleware_AuthorizerDoesNotReplaceRoleService verifies a
// caller-supplied Authorizer does not excuse the missing role store: the
// chain decides TenantOwner from it on every request, whoever authorizes.
func TestBuildAuthMiddleware_AuthorizerDoesNotReplaceRoleService(t *testing.T) {
	_, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:     true,
		Authorizer:  &auth.NoopAuthorizer{},
		UserService: &fakeUserService{},
	})
	var missing *MissingServiceError
	require.ErrorAs(t, err, &missing)
	assert.Equal(t, "role", missing.Service)
}

// TestBuildAuthMiddleware_MissingUserService verifies the fail-loud posture
// for an enabled config missing the user service.
func TestBuildAuthMiddleware_MissingUserService(t *testing.T) {
	_, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:     true,
		RoleService: adminRoleService(),
	})
	require.Error(t, err)
	var missing *MissingServiceError
	require.True(t, errors.As(err, &missing))
	assert.Equal(t, "user", missing.Service)
}

// TestBuildAuthMiddleware_EnabledEnforcesRBAC proves the composed
// AuthMiddleware enforces the RBAC decision built from RoleService: a
// granted permission is admitted through AuthorizeTenant and an ungranted
// one is denied. This is the same enforcement path cmd/qrdbd/start.go and
// sdk/go/node.Node install on every transport via server.WithAuthMiddleware.
func TestBuildAuthMiddleware_EnabledEnforcesRBAC(t *testing.T) {
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:     true,
		RoleService: adminRoleService(),
		UserService: &fakeUserService{},
	})
	require.NoError(t, err)
	require.NotNil(t, mw)

	ctx := auth.WithIdentity(context.Background(), adminIdentity())
	require.NoError(t, mw.AuthorizeTenant(ctx, tenantid.System, permission.AdminStatus))

	ctx2 := auth.WithIdentity(context.Background(), adminIdentity())
	err = mw.AuthorizeTenant(ctx2, tenantid.System, permission.SealRekey)
	require.Error(t, err)
}

// TestBuildAuthMiddleware_UnauthenticatedRejected verifies that a context
// carrying no identity is rejected, matching the security property the SDK
// node wiring restores: a request presenting no credential must never be
// admitted to a permission-gated route.
func TestBuildAuthMiddleware_UnauthenticatedRejected(t *testing.T) {
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:     true,
		RoleService: adminRoleService(),
		UserService: &fakeUserService{},
	})
	require.NoError(t, err)

	err = mw.AuthorizeTenant(context.Background(), tenantid.System, permission.AdminStatus)
	require.Error(t, err)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
}

// TestBuildAuthMiddleware_SPIFFETrustDomain verifies that a non-empty
// SPIFFETrustDomain does not prevent middleware construction (the mTLS
// authenticator is pinned to the trust domain but authorization behaves
// identically since AuthorizeTenant does not re-run authentication).
func TestBuildAuthMiddleware_SPIFFETrustDomain(t *testing.T) {
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:           true,
		RoleService:       adminRoleService(),
		UserService:       &fakeUserService{},
		SPIFFETrustDomain: "spiffe://example.org",
	})
	require.NoError(t, err)
	require.NotNil(t, mw)

	ctx := auth.WithIdentity(context.Background(), adminIdentity())
	require.NoError(t, mw.AuthorizeTenant(ctx, tenantid.System, permission.AdminStatus))
}

// ReconcileBuiltInRoles satisfies role.RoleServicer; this double never drifts.
func (f *fakeRoleService) ReconcileBuiltInRoles(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
	return &role.ReconcileReport{TenantID: tenantID}, nil
}
