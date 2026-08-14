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
	"errors"
	"testing"
	"time"

	gmssql "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestResolveIdentity_Precedence verifies the option wins over the DSN, and
// that an absent identity is reported as nil rather than as an empty one.
func TestResolveIdentity_Precedence(t *testing.T) {
	optIdentity := &auth.Identity{Subject: "from-option", TenantID: "opt-tenant"}

	tests := []struct {
		name         string
		opts         *embeddedOptions
		cfg          *DSNConfig
		wantNil      bool
		wantSubject  string
		wantTenantID string
	}{
		{
			name:         "option wins over DSN",
			opts:         &embeddedOptions{identity: optIdentity},
			cfg:          &DSNConfig{Tenant: "dsn-tenant", User: "dsn-user"},
			wantSubject:  "from-option",
			wantTenantID: "opt-tenant",
		},
		{
			name:         "DSN used when no option",
			opts:         &embeddedOptions{},
			cfg:          &DSNConfig{Tenant: "dsn-tenant", User: "dsn-user"},
			wantSubject:  "dsn-user",
			wantTenantID: "dsn-tenant",
		},
		{
			name:         "tenant only",
			opts:         &embeddedOptions{},
			cfg:          &DSNConfig{Tenant: "acme"},
			wantTenantID: "acme",
		},
		{
			name:    "neither yields nil",
			opts:    &embeddedOptions{},
			cfg:     &DSNConfig{},
			wantNil: true,
		},
		{
			name:    "nil config yields nil",
			opts:    &embeddedOptions{},
			cfg:     nil,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveIdentity(tt.opts, tt.cfg)

			if tt.wantNil {
				if got != nil {
					t.Fatalf("identity = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("identity = nil, want non-nil")
			}
			if got.Subject != tt.wantSubject {
				t.Errorf("Subject = %q, want %q", got.Subject, tt.wantSubject)
			}
			if got.TenantID != tt.wantTenantID {
				t.Errorf("TenantID = %q, want %q", got.TenantID, tt.wantTenantID)
			}
		})
	}
}

// TestWithIdentityOption verifies the option records the identity.
func TestWithIdentityOption(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "acme"}

	var opts embeddedOptions
	WithIdentity(id)(&opts)

	if opts.identity != id {
		t.Errorf("identity = %+v, want %+v", opts.identity, id)
	}
}

// TestWithLocationOption verifies the option records the location.
func TestWithLocationOption(t *testing.T) {
	loc := time.FixedZone("test", 7200)

	var opts embeddedOptions
	WithLocation(loc)(&opts)

	if opts.loc != loc {
		t.Errorf("loc = %v, want %v", opts.loc, loc)
	}
}

// TestNewEmbeddedSession_CarriesIdentity verifies the session exposes the
// identity's tenant, which is what scopes table resolution.
func TestNewEmbeddedSession_CarriesIdentity(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "acme"}

	sess := newEmbeddedSession(id, "mydb")

	if sess.Identity() != id {
		t.Errorf("Identity() = %+v, want %+v", sess.Identity(), id)
	}
	if sess.TenantID() != "acme" {
		t.Errorf("TenantID() = %q, want acme", sess.TenantID())
	}
	if sess.GetCurrentDatabase() != "mydb" {
		t.Errorf("GetCurrentDatabase() = %q, want mydb", sess.GetCurrentDatabase())
	}
}

// TestNewEmbeddedSession_NilIdentityIsSystemTenant verifies a session without
// an identity runs as the system tenant, preserving the behaviour of a DSN
// that names no tenant.
func TestNewEmbeddedSession_NilIdentityIsSystemTenant(t *testing.T) {
	sess := newEmbeddedSession(nil, "mydb")

	if sess.Identity() != nil {
		t.Errorf("Identity() = %+v, want nil", sess.Identity())
	}
	if sess.TenantID() != tenant.SystemTenantID {
		t.Errorf("TenantID() = %q, want %q", sess.TenantID(), tenant.SystemTenantID)
	}
}

// TestNewEmbeddedSession_UniqueIDs verifies each session gets a distinct id.
// go-mysql-server keys its prepared-statement cache by session id, so reused
// ids would let one connection observe another's prepared statements.
func TestNewEmbeddedSession_UniqueIDs(t *testing.T) {
	seen := make(map[uint32]bool)
	for i := 0; i < 64; i++ {
		id := newEmbeddedSession(nil, "db").ID()
		if seen[id] {
			t.Fatalf("duplicate session id %d", id)
		}
		seen[id] = true
	}
}

// TestNewSQLContext_ThreadsCallerContext verifies the caller's context reaches
// the engine context, so cancellation and deadlines are honoured.
func TestNewSQLContext_ThreadsCallerContext(t *testing.T) {
	conn := &embeddedConn{
		database: "mydb",
		session:  newEmbeddedSession(nil, "mydb"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	sqlCtx := conn.newSQLContext(ctx, "SELECT 1")

	if sqlCtx.Session != gmssql.Session(conn.session) {
		t.Error("engine context does not carry the connection's session")
	}
	if sqlCtx.GetCurrentDatabase() != "mydb" {
		t.Errorf("current database = %q, want mydb", sqlCtx.GetCurrentDatabase())
	}

	cancel()
	select {
	case <-sqlCtx.Done():
	default:
		t.Error("cancelling the caller context did not cancel the engine context")
	}
	if !errors.Is(sqlCtx.Err(), context.Canceled) {
		t.Errorf("Err() = %v, want context.Canceled", sqlCtx.Err())
	}
}

// TestNewSQLContext_NilContext verifies a nil context is tolerated rather than
// panicking, since driver.Conn implementations may be called with one.
func TestNewSQLContext_NilContext(t *testing.T) {
	conn := &embeddedConn{
		database: "mydb",
		session:  newEmbeddedSession(nil, "mydb"),
	}

	//nolint:staticcheck // deliberately passing a nil context to assert tolerance
	sqlCtx := conn.newSQLContext(nil, "SELECT 1")
	if sqlCtx == nil {
		t.Fatal("newSQLContext returned nil")
	}
	if sqlCtx.GetCurrentDatabase() != "mydb" {
		t.Errorf("current database = %q, want mydb", sqlCtx.GetCurrentDatabase())
	}
}

// testIdentity builds an identity for tests.
func testIdentity(subject, tenantID string) *auth.Identity {
	return &auth.Identity{Subject: subject, TenantID: tenantID}
}
