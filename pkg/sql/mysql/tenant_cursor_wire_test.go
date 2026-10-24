//go:build qrdb_mysql

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

package mysql

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	vtmysql "github.com/dolthub/vitess/go/mysql"
	"github.com/dolthub/vitess/go/vt/vttls"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// sniTenantAuthenticator authenticates every TLS connection as an admin of
// the tenant named by the server name the client asked for, so one listener
// serves several tenants' connections side by side.
type sniTenantAuthenticator struct{}

func (sniTenantAuthenticator) Authenticate(ctx context.Context) (*auth.Identity, error) {
	state := auth.TLSStateFromContext(ctx)
	if state == nil || state.ServerName == "" {
		return nil, auth.ErrNoPeerCertificate
	}
	return &auth.Identity{Subject: "admin@" + state.ServerName, TenantID: state.ServerName, Roles: []string{"admin"}}, nil
}

func (sniTenantAuthenticator) Name() string { return "test-sni-tenant" }

// tenantCursorServer starts one TLS MySQL listener whose auth chain reads
// tenant status from tenants, over a locking harness.
func tenantCursorServer(t *testing.T, tenants tenant.TenantServicer) *Server {
	t.Helper()
	h, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	serverTLS, _ := wireTLSPair(t)
	mw := authmw.New(&mockAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithAuthenticator(sniTenantAuthenticator{}), authmw.WithTenantStatusReader(tenants))
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithServiceRegistry(h.Registry),
		WithTLS(serverTLS),
		WithAuthMiddleware(mw),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Stop)
	return srv
}

// openTenantCursor connects to srv as tenantID, fills a table of cursorRows
// rows over that connection and opens a server-side cursor on it.
func openTenantCursor(t *testing.T, srv *Server, tenantID string) (*cursorClient, uint32) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(srv.Addr())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	vc, err := vtmysql.Connect(ctx, &vtmysql.ConnParams{
		Host: host, Port: port, Uname: "admin", DbName: "qrdb",
		SslMode: vttls.Required, ServerName: tenantID,
	})
	require.NoError(t, err)
	t.Cleanup(vc.Close)

	table := "rows_" + tenantID
	_, err = vc.ExecuteFetch("CREATE TABLE "+table+" (id BIGINT PRIMARY KEY)", 0, false)
	require.NoError(t, err)
	values := make([]string, cursorRows)
	for i := range values {
		values[i] = "(" + strconv.Itoa(i+1) + ")"
	}
	_, err = vc.ExecuteFetch("INSERT INTO "+table+" VALUES "+strings.Join(values, ","), 0, false)
	require.NoError(t, err)

	c := &cursorClient{conn: vc.Conn}
	return c, c.openCursor(t, "SELECT id FROM "+table+" ORDER BY id", vc.Capabilities&vtmysql.CapabilityClientDeprecateEOF != 0)
}

// TestOpenCursor_TenantSuspensionClosesOnlyItsConnection: a server-side cursor
// opened before its tenant is suspended does not keep serving rows after it.
// COM_STMT_FETCH is answered inside vitess and never reaches the statement
// gate, so the session guard re-admits each open connection's tenant and
// closes a suspended tenant's connections: the next fetch fails. Another
// tenant's cursor on the same listener keeps fetching.
func TestOpenCursor_TenantSuspensionClosesOnlyItsConnection(t *testing.T) {
	tenants, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.Default())
	require.NoError(t, err)
	for _, id := range []string{"acme", "globex"} {
		_, err = tenants.CreateTenant(context.Background(), id, id, tenant.IsolationPrefix)
		require.NoError(t, err)
	}
	srv := tenantCursorServer(t, tenants)
	acme, acmeStmt := openTenantCursor(t, srv, "acme")
	globex, globexStmt := openTenantCursor(t, srv, "globex")
	for _, c := range []struct {
		client *cursorClient
		stmt   uint32
	}{{acme, acmeStmt}, {globex, globexStmt}} {
		if rows, err := c.client.fetch(c.stmt); err != nil || rows != 1 {
			t.Fatalf("fetch before the suspension = %d rows, %v; want 1 row", rows, err)
		}
	}

	setTenantStatus(t, tenants, tenant.StatusSuspended)

	// The guard closes acme's connection once the status cache expires and
	// its next tick re-admits the tenant; every fetch until then is served.
	var fetchErr error
	require.Eventually(t, func() bool {
		_, fetchErr = acme.fetch(acmeStmt)
		return fetchErr != nil
	}, 10*time.Second, 20*time.Millisecond, "the suspended tenant's cursor kept serving")
	requireSealedEnd(t, fetchErr)

	for range 3 {
		if rows, err := globex.fetch(globexStmt); err != nil || rows != 1 {
			t.Fatalf("another tenant's fetch after the suspension = %d rows, %v; want 1 row", rows, err)
		}
	}
	// The guard ends acme's session by closing its connection; the listener
	// releases the session when it sees the close, which can follow the
	// client's failed fetch.
	require.Eventually(t, func() bool {
		return srv.sessions.guard.Len() == 1
	}, 10*time.Second, 20*time.Millisecond, "only the suspended tenant's connection was closed")
}

// TestNewServer_AuthMiddlewareAloneStartsTheGuard: a listener with an auth
// chain and no seal probe still guards its connections, since the tenant
// re-admission runs in the guard.
func TestNewServer_AuthMiddlewareAloneStartsTheGuard(t *testing.T) {
	tenants, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.Default())
	require.NoError(t, err)
	srv := tenantCursorServer(t, tenants)
	require.NotNil(t, srv.sessions.guard)

	bare := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithDatabaseName("qrdb"),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	require.NoError(t, bare.Start())
	t.Cleanup(bare.Stop)
	require.True(t, bare.sessions == nil || bare.sessions.guard == nil, "a listener with nothing to guard runs no guard")
}
