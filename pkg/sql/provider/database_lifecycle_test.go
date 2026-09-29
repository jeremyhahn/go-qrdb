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

package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// tenantSession returns a SQL context whose session is authenticated as
// subject in tenantID.
func tenantSession(subject, tenantID string) *sqle.Context {
	ctx := sqle.NewContext(context.Background())
	ctx.Session = NewSession(sqle.NewBaseSession(), &auth.Identity{Subject: subject, TenantID: tenantID})
	return ctx
}

// recordingAuthorizer records every lifecycle authorization and answers with
// deny when set.
type recordingAuthorizer struct {
	deny     error
	tenants  []string
	perms    []permission.Permission
	subjects []string
}

func (a *recordingAuthorizer) AuthorizeTenant(ctx context.Context, target string, perm permission.Permission) error {
	a.tenants = append(a.tenants, target)
	a.perms = append(a.perms, perm)
	if id := auth.IdentityFromContext(ctx); id != nil {
		a.subjects = append(a.subjects, id.Subject)
	}
	return a.deny
}

// Without a tenant-scoped database service a tenant session's CREATE and DROP
// could be neither metered nor attributed, so both are refused -- the system
// tenant, which owns every connection on an unauthenticated listener, keeps
// the manager path.
func TestProvider_Lifecycle_TenantWithoutServiceRefused(t *testing.T) {
	mgr := newTestManager(t)
	p := NewManagerProvider(mgr, &mockKVService{}, WithLifecycleAuthorizer(&recordingAuthorizer{}))
	ctx := tenantSession("alice", "acme")

	var unavailable *DatabaseLifecycleUnavailableError
	require.ErrorAs(t, p.CreateDatabase(ctx, "orphan"), &unavailable)
	require.Equal(t, "CREATE DATABASE", unavailable.Operation)
	require.Equal(t, "acme", unavailable.TenantID)
	_, err := mgr.Get("orphan")
	require.Error(t, err)

	require.NoError(t, NewManagerProvider(mgr, &mockKVService{}).CreateDatabase(newTestContext(), "sysdb"))
	require.ErrorAs(t, p.DropDatabase(ctx, "sysdb"), &unavailable)
	require.Equal(t, "DROP DATABASE", unavailable.Operation)
	_, err = mgr.Get("sysdb")
	require.NoError(t, err, "a tenant session dropped a database with no tenant-scoped lifecycle")
}

// Without an authorizer a tenant session is refused even when the service is
// present: RBAC cannot be skipped by omitting it.
func TestProvider_Lifecycle_TenantWithoutAuthorizerRefused(t *testing.T) {
	mgr := newTestManager(t)
	p := NewManagerProvider(mgr, &mockKVService{}, WithDatabaseService(service.NewDatabaseService(mgr)))

	var unavailable *DatabaseLifecycleUnavailableError
	require.ErrorAs(t, p.CreateDatabase(tenantSession("alice", "acme"), "unchecked"), &unavailable)
	_, err := mgr.Get("unchecked")
	require.Error(t, err)
}

// The authorizer is asked for databases:create and databases:delete against
// the session's own tenant, with the session's identity in the context, and
// a denial stops the statement before the catalog is touched.
func TestProvider_Lifecycle_AuthorizesAgainstSessionTenant(t *testing.T) {
	mgr := newTestManager(t)
	authz := &recordingAuthorizer{}
	p := NewManagerProvider(mgr, &mockKVService{},
		WithDatabaseService(service.NewDatabaseService(mgr)),
		WithLifecycleAuthorizer(authz))
	ctx := tenantSession("alice", "acme")

	require.NoError(t, p.CreateDatabase(ctx, "ledger"))
	db, err := mgr.Get("ledger")
	require.NoError(t, err)
	require.Equal(t, "acme", db.TenantID())
	require.NoError(t, p.DropDatabase(ctx, "ledger"))

	require.Equal(t, []string{"acme", "acme"}, authz.tenants)
	require.Equal(t, []permission.Permission{permission.DatabasesCreate, permission.DatabasesDelete}, authz.perms)
	require.Equal(t, []string{"alice", "alice"}, authz.subjects)

	authz.deny = auth.ErrUnauthorized
	err = p.CreateDatabase(ctx, "denied")
	require.True(t, errors.Is(err, auth.ErrUnauthorized), "err = %v, want auth.ErrUnauthorized", err)
	_, getErr := mgr.Get("denied")
	require.Error(t, getErr)
}

// A system-tenant session keeps working without a service or authorizer: it
// is the unauthenticated listener's only session.
func TestProvider_Lifecycle_SystemSessionUsesManager(t *testing.T) {
	mgr := newTestManager(t)
	p := NewManagerProvider(mgr, &mockKVService{})
	ctx := tenantSession("root", tenantid.System)

	require.NoError(t, p.CreateDatabase(ctx, "opsdb"))
	require.NoError(t, p.DropDatabase(ctx, "opsdb"))
	_, err := mgr.Get("opsdb")
	require.Error(t, err)
}

func TestDatabaseLifecycleUnavailableError_Error(t *testing.T) {
	msg := (&DatabaseLifecycleUnavailableError{Operation: "DROP DATABASE", TenantID: "acme"}).Error()
	for _, want := range []string{"DROP DATABASE", "acme", "tenant-scoped"} {
		require.True(t, strings.Contains(msg, want), "Error() = %q, want %q", msg, want)
	}
}
