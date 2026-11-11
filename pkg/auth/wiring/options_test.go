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

package wiring

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// recordUserService answers GetUser with one fixed account.
type recordUserService struct {
	fakeUserService
	rec *user.User
}

func (r *recordUserService) GetUser(context.Context, uint64) (*user.User, error) {
	return r.rec, nil
}

// guardAsked records the account the certificate target guard was asked
// about, and refuses.
type guardAsked struct {
	target *user.User
	err    error
}

func (g *guardAsked) AuthorizeCertTarget(_ context.Context, _ string, target *user.User) error {
	g.target = target
	return g.err
}

// TestBuildAuthMiddleware_CertTargetGuardReachesAddCertBinding: the guard the
// composer passes is the one users:add_cert_binding is held to; with none the
// verb fails closed.
func TestBuildAuthMiddleware_CertTargetGuardReachesAddCertBinding(t *testing.T) {
	users := &recordUserService{rec: &user.User{ID: 7, Username: "sam", TenantID: "acme"}}
	refused := errors.New("ceremony only")
	g := &guardAsked{err: refused}
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{Enabled: true, RoleService: adminRoleService(), UserService: users, CertTargetGuard: g})
	require.NoError(t, err)
	require.ErrorIs(t, mw.AuthorizeCertBindingTarget(context.Background(), users, 7), refused)
	require.NotNil(t, g.target)
	require.Equal(t, "sam", g.target.Username)

	none, err := BuildAuthMiddleware(AuthMiddlewareConfig{Enabled: true, RoleService: adminRoleService(), UserService: users})
	require.NoError(t, err)
	var required *middleware.CertTargetGuardRequiredError
	require.ErrorAs(t, none.AuthorizeCertBindingTarget(context.Background(), users, 7), &required)
}

// pendingNone has no pending tenant admins: absent is (nil, nil), as
// tenantadmin.KVStore answers it.
type pendingNone struct{}

func (pendingNone) Get(context.Context, string) (*tenantadmin.Pending, error) {
	return nil, nil
}

// TestBuildAuthMiddleware_OptionalServicesAreAccepted: the fallbacks and the
// pending-admin record are optional inputs; supplying them builds a working
// middleware that still authorizes through the primary services.
func TestBuildAuthMiddleware_OptionalServicesAreAccepted(t *testing.T) {
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:             true,
		RoleService:         adminRoleService(),
		RoleServiceFallback: adminRoleService(),
		UserService:         &fakeUserService{},
		UserServiceFallback: &fakeUserService{},
		PendingTenantAdmins: pendingNone{},
	})
	require.NoError(t, err)
	ctx := auth.WithIdentity(context.Background(), adminIdentity())
	require.NoError(t, mw.AuthorizeTenant(ctx, tenantid.System, permission.AdminStatus))
}

// TestBuildAuthorizer_SuppliedAuthorizerIsUsedAsIs: a composer's own
// authorizer is returned unchanged, with no RBAC stack built around it.
func TestBuildAuthorizer_SuppliedAuthorizerIsUsedAsIs(t *testing.T) {
	own, err := BuildAuthorizer(AuthorizerConfig{RoleService: adminRoleService()})
	require.NoError(t, err)
	got, err := BuildAuthorizer(AuthorizerConfig{Authorizer: own})
	require.NoError(t, err)
	require.Same(t, own, got)
}
