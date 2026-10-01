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

	"github.com/jeremyhahn/go-qrdb/pkg/database"
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
func TestEmbedded_CreateDatabaseChecksProvisioning(t *testing.T) {
	ctx := context.Background()
	n, err := NewStandaloneNode(ctx, newTenantIsolationConfig(t), nil,
		WithTenantIsolation(), WithDefaultTenantEntitlements(volumeEntitlements()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	_, err = n.Services().Tenant.CreateTenant(ctx, "vol", "vol", tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true))
	require.NoError(t, err)
	_, err = n.Services().Tenant.CreateTenant(ctx, "acme", "acme", tenant.IsolationPrefix)
	require.NoError(t, err)

	req := transport.CreateDatabaseRequest{Name: "vault", Engine: string(database.EngineKV), Backend: string(database.BackendPebble)}
	_, err = n.Client().Database().CreateDatabase(tenant.WithTenantID(ctx, "vol"), req)
	var denied *service.ProvisioningDeniedError
	require.Truef(t, errors.As(err, &denied), "create for a volume tenant with no identity = %v, want *service.ProvisioningDeniedError", err)
	require.Equal(t, "databases:create_volume", string(denied.Permission))
	_, getErr := n.dbManager.Get("vault")
	require.Error(t, getErr, "a refused create reached the catalog")

	// An ordinary database of a shared tenant needs no provisioning
	// permission, and is owned by the caller's tenant.
	_, err = n.Client().Database().CreateDatabase(tenant.WithTenantID(ctx, "acme"),
		transport.CreateDatabaseRequest{Name: "ledger", Engine: string(database.EngineKV), Backend: string(database.BackendMemory)})
	require.NoError(t, err)
	db, err := n.dbManager.Get("ledger")
	require.NoError(t, err)
	require.Equal(t, "acme", db.TenantID(), "the database is owned by the caller's tenant")
}
