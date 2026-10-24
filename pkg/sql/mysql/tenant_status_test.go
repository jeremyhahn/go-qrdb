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

//go:build qrdb_mysql

package mysql

import (
	"context"
	"log/slog"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// A MySQL connection opened while its tenant was active is refused on its
// next statement once the tenant is suspended, with the auth chain's own
// *TenantNotActiveError, and admitted again once it is reactivated: the
// session re-admits its identity before each command.
func TestSessionBuilder_OpenSessionRefusedAfterTenantSuspended(t *testing.T) {
	tenants, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.Default())
	require.NoError(t, err)
	_, err = tenants.CreateTenant(context.Background(), "acme", "acme", tenant.IsolationPrefix)
	require.NoError(t, err)

	authn := &mockAuthenticator{identity: &auth.Identity{Subject: "alice", TenantID: "acme", Roles: []string{"admin"}}}
	mw := authmw.New(&mockAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithAuthenticator(authn), authmw.WithTenantStatusReader(tenants))
	conn := newTLSConn(t)
	defer func() { _ = conn.Conn.Close() }()

	sess, err := newSessionBuilder(mw, nil).Build(context.Background(), conn, "127.0.0.1:3306")
	require.NoError(t, err)
	require.NoError(t, sql.SessionCommandBegin(sess))
	sql.SessionCommandEnd(sess)

	setTenantStatus(t, tenants, tenant.StatusSuspended)
	var notActive *authmw.TenantNotActiveError
	require.ErrorAs(t, sql.SessionCommandBegin(sess), &notActive)
	require.Equal(t, "acme", notActive.TenantID)
	require.Equal(t, string(tenant.StatusSuspended), notActive.Status)

	setTenantStatus(t, tenants, tenant.StatusActive)
	require.NoError(t, sql.SessionCommandBegin(sess))
	sql.SessionCommandEnd(sess)
}

func setTenantStatus(t *testing.T, tenants tenant.TenantServicer, s tenant.Status) {
	t.Helper()
	_, err := tenants.UpdateTenant(context.Background(), "acme", tenant.TenantPatch{Status: &s})
	require.NoError(t, err)
}
