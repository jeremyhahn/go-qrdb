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

// Package privilege_test holds the tests that must drive a REAL engine
// through the privilege plane.
//
// It is an external test package because pkg/sql/engine imports
// pkg/sql/privilege (that is where the post-analyze rewrite rule is wired
// in), so a test inside package privilege could not build an engine at all.
// Tests of this package's internals live alongside it in package privilege.
package privilege_test

import (
	"context"
	"sync/atomic"
	"testing"

	gms "github.com/dolthub/go-mysql-server"
	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const (
	// benchDatabase is the database every in-process test and benchmark in
	// this package runs against.
	benchDatabase = "privdb"

	// benchQuery is the statement the SELECT baseline was measured with.
	benchQuery = "SELECT id, name FROM users WHERE id = 1"
)

// benchConnID hands each session a distinct connection id, which
// go-mysql-server keys its prepared-statement cache by.
var benchConnID atomic.Uint32

// benchSystemIdentity is a system-tenant identity: exempt from privilege
// checks permanently, which is the posture of every deployment that has not
// armed a privilege catalog.
var benchSystemIdentity = &auth.Identity{Subject: "operator", TenantID: tenant.SystemTenantID}

// benchTenantIdentity is an ordinary tenant identity, subject to whatever the
// database's privilege catalog says.
var benchTenantIdentity = &auth.Identity{Subject: "alice", TenantID: "acme"}

// benchHarness is one in-process engine over one in-memory relational
// catalog, plus the session every statement runs on.
type benchHarness struct {
	harness  *sqltest.Harness
	engine   *gms.Engine
	session  *provider.QRDBSession
	identity *auth.Identity
}

// fatalf is the subset of *testing.T and *testing.B this file needs, so one
// harness constructor serves both tests and benchmarks.
type fatalf interface {
	Fatalf(format string, args ...any)
	Helper()
}

// newBenchHarness builds an engine over a fresh in-memory catalog containing
// a single `users` table with one row, running as the given identity.
func newBenchHarness(t fatalf, identity *auth.Identity) *benchHarness {
	t.Helper()

	h := sqltest.NewHarness()
	prov := provider.NewProviderWithEngine(
		benchDatabase, h.KVSvc, database.EngineKV, provider.WithRegistry(h.Registry))

	base := sqle.NewBaseSessionWithClientServer("bench",
		sqle.Client{User: identity.Subject, Address: "bench"}, benchConnID.Add(1))
	base.SetCurrentDatabase(benchDatabase)

	bh := &benchHarness{
		harness:  h,
		engine:   engine.New(engine.Config{Provider: prov}),
		session:  provider.NewSession(base, identity),
		identity: identity,
	}

	// The table is created as the system tenant so that the setup itself is
	// never the thing under test, and so a latched-database benchmark starts
	// from a table that already exists.
	setupSession := provider.NewSession(
		sqle.NewBaseSessionWithClientServer("bench-setup",
			sqle.Client{User: "operator", Address: "bench"}, benchConnID.Add(1)), benchSystemIdentity)
	setupSession.SetCurrentDatabase(benchDatabase)
	setupCtx := sqle.NewContext(context.Background(), sqle.WithSession(setupSession))
	setupCtx.SetCurrentDatabase(benchDatabase)

	for _, stmt := range []string{
		"CREATE TABLE users (id BIGINT PRIMARY KEY, name VARCHAR(64))",
		"INSERT INTO users VALUES (1, 'alice')",
	} {
		if _, iter, _, err := bh.engine.Query(setupCtx, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		} else if _, err := sqle.RowIterToRows(setupCtx, iter); err != nil {
			t.Fatalf("setup drain %q: %v", stmt, err)
		}
	}

	return bh
}

// as returns a view of the same catalog and the same engine -- and therefore
// the same authorization handler and its snapshot cache -- driven by a second
// session running as a different identity. It is how a test observes one
// identity's grants from another identity's session, which is the only way to
// tell a per-session decision from a per-catalog one.
func (b *benchHarness) as(identity *auth.Identity) *benchHarness {
	base := sqle.NewBaseSessionWithClientServer("bench",
		sqle.Client{User: identity.Subject, Address: "bench"}, benchConnID.Add(1))
	base.SetCurrentDatabase(benchDatabase)
	return &benchHarness{
		harness:  b.harness,
		engine:   b.engine,
		session:  provider.NewSession(base, identity),
		identity: identity,
	}
}

// context builds a fresh per-statement context on the harness's session.
func (b *benchHarness) context() *sqle.Context {
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(b.session))
	ctx.SetCurrentDatabase(benchDatabase)
	return ctx
}

// grantSelect arms the database's privilege catalog and gives the harness's
// identity SELECT on the users table, through the service layer directly so
// that the grant is setup rather than part of what is measured.
func (b *benchHarness) grantSelect(t fatalf) {
	t.Helper()
	svc, err := b.harness.Registry.PrivilegeService(b.identity.TenantID, benchDatabase)
	if err != nil {
		t.Fatalf("privilege service: %v", err)
	}
	if err := svc.Grant(context.Background(), &service.GrantPrivilegesRequest{
		Database: benchDatabase,
		Grantee:  b.identity.Subject + "@%",
		Grants:   []service.PrivilegeGrant{{Table: "users", Privileges: service.PrivilegeSelect}},
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
}

// query runs one statement on the harness's session and returns its rows.
func (b *benchHarness) query(t *testing.T, statement string) ([]sqle.Row, error) {
	t.Helper()
	ctx := b.context()
	_, iter, _, err := b.engine.Query(ctx, statement)
	if err != nil {
		return nil, err
	}
	return sqle.RowIterToRows(ctx, iter)
}
