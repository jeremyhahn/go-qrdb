//go:build qrdb_sql

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

package driver

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// serviceConnectorDSN is the DSN shape a caller-supplied-service connector
// takes: it names the database and engine, but no backend, because the
// connector creates no storage of its own.
func serviceConnectorDSN(database string) *DSNConfig {
	return &DSNConfig{Scheme: SchemeEmbedded, Database: database, Engine: "kv"}
}

// TestNewServiceConnector_RelationalTablesWork verifies a connector built over
// caller-supplied services serves the relational catalog its registry
// provides -- the capability the KV-only embedded path lacks.
func TestNewServiceConnector_RelationalTablesWork(t *testing.T) {
	reg := newRelationalRegistry()

	connector, err := NewServiceConnector(serviceConnectorDSN("svcconn"), reg.kvSvc, reg)
	if err != nil {
		t.Fatalf("NewServiceConnector: %v", err)
	}
	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec("CREATE TABLE gadgets (id BIGINT PRIMARY KEY, name VARCHAR(32))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec("INSERT INTO gadgets (id, name) VALUES (1, 'widget')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	var name string
	if err := db.QueryRow("SELECT name FROM gadgets WHERE id = 1").Scan(&name); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if name != "widget" {
		t.Errorf("name = %q, want %q", name, "widget")
	}
}

// TestNewServiceConnector_CloseLeavesServicesUsable verifies closing the
// connector releases only what the connector created. The caller owns the
// services, and a second connector over the same registry must still see the
// first one's data.
func TestNewServiceConnector_CloseLeavesServicesUsable(t *testing.T) {
	reg := newRelationalRegistry()

	first, err := NewServiceConnector(serviceConnectorDSN("shared"), reg.kvSvc, reg)
	if err != nil {
		t.Fatalf("NewServiceConnector: %v", err)
	}
	firstDB := sql.OpenDB(first)
	if _, err := firstDB.Exec("CREATE TABLE ledger (id BIGINT PRIMARY KEY, amount INT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := firstDB.Exec("INSERT INTO ledger (id, amount) VALUES (1, 100)"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := firstDB.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second, err := NewServiceConnector(serviceConnectorDSN("shared"), reg.kvSvc, reg)
	if err != nil {
		t.Fatalf("second NewServiceConnector: %v", err)
	}
	secondDB := sql.OpenDB(second)
	t.Cleanup(func() { _ = secondDB.Close() })

	var amount int64
	if err := secondDB.QueryRow("SELECT amount FROM ledger WHERE id = 1").Scan(&amount); err != nil {
		t.Fatalf("SELECT through a second connector: %v", err)
	}
	if amount != 100 {
		t.Errorf("amount = %d, want 100", amount)
	}
}

// TestServiceConnector_ConnectAfterClose verifies a closed connector refuses
// new connections rather than rebuilding its engine over services the caller
// may already have torn down.
func TestServiceConnector_ConnectAfterClose(t *testing.T) {
	reg := newRelationalRegistry()
	connector, err := NewServiceConnector(serviceConnectorDSN("closed"), reg.kvSvc, reg)
	if err != nil {
		t.Fatalf("NewServiceConnector: %v", err)
	}
	if err := connector.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = connector.Connect(context.Background())
	var closedErr *ConnectorClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("error = %v (%T), want *ConnectorClosedError", err, err)
	}
}

// TestServiceConnector_InvalidLocation verifies a bad loc parameter is
// reported at Connect rather than silently defaulting to UTC.
func TestServiceConnector_InvalidLocation(t *testing.T) {
	reg := newRelationalRegistry()
	cfg := serviceConnectorDSN("badloc")
	cfg.Location = "Not/AZone"

	connector, err := NewServiceConnector(cfg, reg.kvSvc, reg)
	if err != nil {
		t.Fatalf("NewServiceConnector: %v", err)
	}
	defer func() { _ = connector.Close() }()

	_, err = connector.Connect(context.Background())
	var parseErr *DSNParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("error = %v (%T), want *DSNParseError", err, err)
	}
}

// TestServiceConnector_ExplicitRegistryWinsOverOption verifies the registry
// argument takes precedence over a WithRegistry passed in opts, which is what
// this constructor's contract promises.
func TestServiceConnector_ExplicitRegistryWinsOverOption(t *testing.T) {
	wanted := newRelationalRegistry()
	other := newRelationalRegistry()

	connector, err := NewServiceConnector(serviceConnectorDSN("prec"), wanted.kvSvc, wanted, WithRegistry(other))
	if err != nil {
		t.Fatalf("NewServiceConnector: %v", err)
	}
	defer func() { _ = connector.Close() }()

	sc, ok := connector.(*serviceConnector)
	if !ok {
		t.Fatalf("connector = %T, want *serviceConnector", connector)
	}
	if sc.opts.registry != wanted {
		t.Error("the explicit registry argument must win over a WithRegistry option")
	}
}

// ---------------------------------------------------------------------------
// ConnectionTenant
// ---------------------------------------------------------------------------

// TestConnectionTenant_EdgeCases covers the tenant sources
// TestConnectionTenant (connector_service_api_test.go) does not: a nil
// configuration, an identity carrying no tenant, and an option that has
// nothing to do with identity.
func TestConnectionTenant_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		cfg  *DSNConfig
		opts []EmbeddedOption
		want string
	}{
		{
			name: "nil config defaults to system",
			cfg:  nil,
			want: tenant.SystemTenantID,
		},
		{
			name: "identity without tenant falls back to system",
			cfg:  &DSNConfig{Database: "db"},
			opts: []EmbeddedOption{WithIdentity(&auth.Identity{Subject: "bob"})},
			want: tenant.SystemTenantID,
		},
		{
			name: "unrelated option does not change the tenant",
			cfg:  &DSNConfig{Database: "db", Tenant: "acme"},
			opts: []EmbeddedOption{WithLocation(time.UTC)},
			want: "acme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConnectionTenant(tt.cfg, tt.opts...); got != tt.want {
				t.Errorf("ConnectionTenant = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestConnectionTenant_MatchesSessionTenant verifies the helper agrees with
// the session the same configuration produces -- the invariant that keeps a
// connection from resolving services for one tenant and executing as another.
func TestConnectionTenant_MatchesSessionTenant(t *testing.T) {
	cfg := &DSNConfig{Database: "db", Tenant: "acme", User: "bob"}
	var opts embeddedOptions

	session := newEmbeddedSession(resolveIdentity(&opts, cfg), cfg.Database)
	if got, want := ConnectionTenant(cfg), session.TenantID(); got != want {
		t.Errorf("ConnectionTenant = %q, session.TenantID() = %q; they must agree", got, want)
	}
}
