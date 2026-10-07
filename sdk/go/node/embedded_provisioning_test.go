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

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The in-process create is held to the same provisioning check as every
// network transport: a database of a volume-isolated tenant needs
// databases:create_volume, decided for the identity the call carries. The
// embedded transport used to call the system-scoped Create, which neither
// scoped the database to the caller's tenant nor checked either
// provisioning permission.
//
// This test is specifically about the PROVISIONING layer
// (service.NewProvisioningAuthorizedDatabaseService), which only wraps the
// database service when auth is enabled -- so, unlike most of this
// package's fixtures (see testhelpers_test.go's sealedStandaloneConfig),
// it keeps auth ON and grants the caller the GENERIC databases:create (the
// embedded transport's own, newer entry check) without the
// provisioning-specific databases:create_volume, so the provisioning
// wrapper's denial is reached and observed on its own, rather than being
// pre-empted by the entry check denying everyone with no identity at all.
func TestEmbedded_CreateDatabaseChecksProvisioning(t *testing.T) {
	ctx := context.Background()
	cfg := newTenantIsolationConfig(t)
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(true)
	n, err := NewStandaloneNode(ctx, cfg,
		nil, WithTenantIsolation(), WithDefaultTenantEntitlements(volumeEntitlements()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	createDefaultPool(t, n)
	_, err = n.Services().Tenant.CreateTenant(ctx, "vol", "vol", tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true))
	require.NoError(t, err)
	_, err = n.Services().Tenant.CreateTenant(ctx, "acme", "acme", tenant.IsolationPrefix)
	require.NoError(t, err)

	// An identity in each target tenant holding ONLY the generic
	// databases:create grant: enough to pass the embedded transport's own entry
	// check for its own tenant, but not the provisioning-specific
	// databases:create_volume the wrapper below additionally requires for a
	// volume-isolated tenant.
	callerFor := func(tenantID string) context.Context {
		tctx := tenant.WithTenantID(ctx, tenantID)
		createRole, err := n.Services().Role.CreateRole(tctx, &role.CreateRoleRequest{
			Name:        "db-create-only",
			TenantID:    tenantID,
			Permissions: []permission.Permission{permission.DatabasesCreate},
		})
		require.NoError(t, err)
		return auth.WithIdentity(tctx, &auth.Identity{
			Subject:  "db-creator",
			TenantID: tenantID,
			Roles:    []string{createRole.ID},
		})
	}

	req := transport.CreateDatabaseRequest{Name: "vault", Backend: string(database.BackendPebble)}
	_, err = n.Client().Database().CreateDatabase(callerFor("vol"), req)
	var denied *service.ProvisioningDeniedError
	require.Truef(t, errors.As(err, &denied), "create for a volume tenant holding only databases:create = %v, want *service.ProvisioningDeniedError", err)
	require.Equal(t, "databases:create_volume", string(denied.Permission))
	_, getErr := n.dbManager.Get("vault")
	require.Error(t, getErr, "a refused create reached the catalog")

	// An ordinary database of a shared tenant needs no provisioning
	// permission, and is owned by the caller's tenant.
	_, err = n.Client().Database().CreateDatabase(callerFor("acme"),
		transport.CreateDatabaseRequest{Name: "ledger", Backend: string(database.BackendMemory)})
	require.NoError(t, err)
	db, err := n.dbManager.Get("ledger")
	require.NoError(t, err)
	require.Equal(t, "acme", db.TenantID(), "the database is owned by the caller's tenant")
}
