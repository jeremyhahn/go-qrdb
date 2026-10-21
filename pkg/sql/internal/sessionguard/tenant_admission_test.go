//go:build qrdb_sql

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

package sessionguard

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// allowAll authorizes every request; TenantAdmission asks only the chain's
// tenant status gate.
type allowAll struct{}

func (allowAll) Authorize(context.Context, *auth.Identity, string, string) error { return nil }

// newTenantAdmission returns the admission of an auth chain reading tenant
// status from a tenant service holding the active tenant acme.
func newTenantAdmission(t *testing.T) (Admission, tenant.TenantServicer) {
	t.Helper()
	tenants, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.Default())
	require.NoError(t, err)
	_, err = tenants.CreateTenant(context.Background(), "acme", "acme", tenant.IsolationPrefix)
	require.NoError(t, err)
	return TenantAdmission(authmw.New(allowAll{}, authmw.WithTenantStatusReader(tenants))), tenants
}

func setStatus(t *testing.T, tenants tenant.TenantServicer, s tenant.Status) {
	t.Helper()
	_, err := tenants.UpdateTenant(context.Background(), "acme", tenant.TenantPatch{Status: &s})
	require.NoError(t, err)
}

// Only a definitive not-active answer revokes a tenant; an active tenant, the
// system tenant and a tenant the reader cannot find keep their connections.
func TestTenantAdmission_Revoked(t *testing.T) {
	adm, tenants := newTenantAdmission(t)

	require.False(t, adm.Revoked(context.Background(), "acme"), "an active tenant")
	require.False(t, adm.Revoked(context.Background(), ""), "the system tenant")
	require.False(t, adm.Revoked(context.Background(), "nonexistent"), "a tenant with no record")

	setStatus(t, tenants, tenant.StatusSuspended)
	require.True(t, adm.Revoked(context.Background(), "acme"), "a suspended tenant")
}

// A disabled tenant is revoked, and a reactivated one is admitted again.
func TestTenantAdmission_RevokedFollowsStatus(t *testing.T) {
	adm, tenants := newTenantAdmission(t)

	setStatus(t, tenants, tenant.StatusDisabled)
	require.True(t, adm.Revoked(context.Background(), "acme"), "a disabled tenant")

	setStatus(t, tenants, tenant.StatusActive)
	require.False(t, adm.Revoked(context.Background(), "acme"), "a reactivated tenant")
}
