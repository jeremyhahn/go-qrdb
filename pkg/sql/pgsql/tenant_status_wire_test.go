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

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// A PostgreSQL connection opened while its tenant was active is refused on
// its next statement once the tenant is suspended -- the server's sessions
// re-admit their identity before each command, and the seal guard closes a
// suspended tenant's connections -- and the tenant is served again once
// reactivated.
func TestPostgresWire_OpenSessionRefusedAfterTenantSuspended(t *testing.T) {
	tenants, connect := pgTenantServer(t)
	conn, ctx := connect()

	if _, err := conn.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatalf("SELECT 1 while active: %v", err)
	}

	setPgTenantStatus(t, tenants, tenant.StatusSuspended)
	_, err := conn.Exec(ctx, "SELECT 1")
	requirePgRefusedOrClosed(t, conn, err, "SELECT 1 while suspended")

	setPgTenantStatus(t, tenants, tenant.StatusActive)
	conn, ctx = connect()
	if _, err := conn.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatalf("SELECT 1 after reactivation: %v", err)
	}
}

// A statement prepared while its tenant was active read its rows at Parse;
// executing it once the tenant is suspended is refused instead of serving
// those rows, and served again once the tenant is reactivated.
func TestPostgresWire_PreparedStatementRefusedAfterTenantSuspended(t *testing.T) {
	tenants, connect := pgTenantServer(t)
	conn, ctx := connect()
	const name = "prepared_while_active"
	if _, err := conn.Prepare(ctx, name, "SELECT 1"); err != nil {
		t.Fatalf("Prepare while active: %v", err)
	}

	setPgTenantStatus(t, tenants, tenant.StatusSuspended)
	_, err := conn.Exec(ctx, name)
	requirePgRefusedOrClosed(t, conn, err, "executing "+name+" while suspended")

	setPgTenantStatus(t, tenants, tenant.StatusActive)
	conn, ctx = connect()
	if _, err := conn.Prepare(ctx, name, "SELECT 1"); err != nil {
		t.Fatalf("Prepare after reactivation: %v", err)
	}
	if _, err := conn.Exec(ctx, name); err != nil {
		t.Fatalf("executing %s after reactivation: %v", name, err)
	}
}

// requirePgRefusedOrClosed fails unless err is the tenant-not-active refusal
// or conn was closed: the seal guard closes a suspended tenant's connections
// at its next admission tick, which may come before the statement does.
func requirePgRefusedOrClosed(t *testing.T, conn *pgx.Conn, err error, what string) {
	t.Helper()
	if err != nil && (strings.Contains(err.Error(), "tenant is not active: acme (status suspended)") || conn.IsClosed()) {
		return
	}
	t.Fatalf("%s = %v, want the tenant-not-active refusal or the connection closed", what, err)
}

// pgTenantConn starts a server whose auth chain admits alice of tenant acme
// against a tenant service, and returns the service and a connection opened
// while acme is active.
func pgTenantConn(t *testing.T) (tenant.TenantServicer, *pgx.Conn, context.Context) {
	t.Helper()
	tenants, connect := pgTenantServer(t)
	conn, ctx := connect()
	return tenants, conn, ctx
}

// pgTenantServer starts a server whose auth chain admits alice of tenant acme
// against a tenant service, and returns the service and a function that opens
// a connection to the server as alice.
func pgTenantServer(t *testing.T) (tenant.TenantServicer, func() (*pgx.Conn, context.Context)) {
	t.Helper()
	tenants, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	if _, err := tenants.CreateTenant(context.Background(), "acme", "acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	serverTLS, clientTLS := pgMTLS(t)
	mw := authmw.New(pgAllowAll{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)),
		authmw.WithAuthenticator(&pgFixedIdentity{identity: &auth.Identity{Subject: "alice", TenantID: "acme"}}),
		authmw.WithTenantStatusReader(tenants))
	srv := NewServer(&mockKVService{}, nil, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithTLS(serverTLS),
		WithAuthMiddleware(mw),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	cfg, err := pgx.ParseConfig("postgres://alice@" + srv.Addr() + "/qrdb?sslmode=require")
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.TLSConfig = clientTLS
	return tenants, func() (*pgx.Conn, context.Context) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		t.Cleanup(cancel)
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatalf("ConnectConfig: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn, ctx
	}
}

func setPgTenantStatus(t *testing.T, tenants tenant.TenantServicer, s tenant.Status) {
	t.Helper()
	if _, err := tenants.UpdateTenant(context.Background(), "acme", tenant.TenantPatch{Status: &s}); err != nil {
		t.Fatalf("UpdateTenant(%s): %v", s, err)
	}
}
