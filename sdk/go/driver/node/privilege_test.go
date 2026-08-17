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

package node

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
	qrdbnode "github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

// ---------------------------------------------------------------------------
// SQL table privileges over the full in-process stack.
//
// These run over node:// rather than embedded:// for the reason the package
// doc gives: embedded:// has no service.Registry, so it has neither a
// relational catalog to protect nor a privilege catalog to protect it with.
// Everything here therefore exercises the real service graph -- the Raft state
// machine, the per-(tenant, database) registry, and the privilege catalog's
// own compare-and-swap -- as a client sees it through database/sql.
//
// Both sessions belong to the SAME tenant. That is not a simplification: the
// privilege catalog is scoped to (tenant, database) exactly as the relational
// table catalog is, so a session in another tenant does not see these tables
// and could not administer their privileges if it did. A tenant's catalog is
// bootstrapped from inside that tenant, which the tests below rely on: while a
// database has no catalog it is unrestricted, so the first GRANT can always be
// issued.
// ---------------------------------------------------------------------------

// MySQL's object-level access-denied codes. Asserting the code rather than the
// message is the point: a client classifies a refusal by number, and these are
// the same numbers go-sql-driver/mysql reports over the wire.
const (
	mysqlDatabaseAccessDenied = 1044
	mysqlTableAccessDenied    = 1142
)

const (
	// privilegeTenant is the tenant both sessions below belong to. It is
	// deliberately NOT the system tenant, which is exempt from privilege
	// checks permanently.
	privilegeTenant = "acme"

	// privilegeAdmin bootstraps the catalog and keeps GRANT OPTION.
	privilegeAdmin = "alice"

	// privilegeUser is the governed account every enforcement assertion is
	// made against.
	privilegeUser = "bob"
)

// privilegeFixture is one node serving two connections to the same database
// in the same tenant: the account that bootstraps the privilege catalog, and
// the account governed by it.
type privilegeFixture struct {
	admin *sql.DB
	user  *sql.DB
}

// newPrivilegeFixture boots a node over node://memory, provisions the tenant,
// and opens both sessions with a `users` table already in place.
//
// The catalog is left UNARMED: each test arms it in the way it needs, which is
// what keeps the default posture (no catalog at all) testable alongside the
// armed one.
func newPrivilegeFixture(t *testing.T, database string) *privilegeFixture {
	t.Helper()

	cfg, err := qrdbdriver.ParseDSN("node://memory?database=" + database)
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}

	connector := New(cfg)
	t.Cleanup(func() { _ = connector.Close() })

	// A system-tenant connection is opened first purely to start the node;
	// no test statement runs on it. Its keyspace, and its privilege catalog,
	// are separate from the tenant's.
	boot := sql.OpenDB(connector)
	t.Cleanup(func() { _ = boot.Close() })
	if err := boot.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	n := connector.Node()
	if n == nil {
		t.Fatal("connector started no node")
	}
	if _, err := n.Services().Tenant.CreateTenant(context.Background(),
		privilegeTenant, "Privilege Test", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	f := &privilegeFixture{
		admin: openTenantDB(t, n, database, privilegeAdmin),
		user:  openTenantDB(t, n, database, privilegeUser),
	}

	if _, err := f.admin.Exec(
		"CREATE TABLE users (id BIGINT PRIMARY KEY, name VARCHAR(64))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := f.admin.Exec("INSERT INTO users VALUES (1, 'alice')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	return f
}

// openTenantDB opens a database/sql handle for one subject in the test
// tenant, over the services of the node the connector already started.
//
// It resolves through the node's own service registry, exactly as the node
// connector resolves its own connection, so both handles share one catalog.
// Two connectors would be two nodes, and therefore two databases.
func openTenantDB(t *testing.T, n *qrdbnode.Node, database, subject string) *sql.DB {
	t.Helper()

	cfg, err := qrdbdriver.ParseDSN(
		"node://memory?database=" + database + "&tenant=" + privilegeTenant + "&user=" + subject)
	if err != nil {
		t.Fatalf("ParseDSN(%s): %v", subject, err)
	}

	registry := n.Server().ServiceRegistry()
	kv, err := registry.KVService(privilegeTenant, database)
	if err != nil {
		t.Fatalf("KVService(%s, %s): %v", privilegeTenant, database, err)
	}

	connector, err := qrdbdriver.NewServiceConnector(cfg, kv, registry)
	if err != nil {
		t.Fatalf("NewServiceConnector(%s): %v", subject, err)
	}

	db := sql.OpenDB(connector)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// arm gives the admin account everything at the DATABASE tier, including
// GRANT OPTION. That first mutation is what arms the database's activation
// latch, and the database-tier GRANT OPTION is what leaves the admin able to
// administer it afterwards -- administering privileges is checked at the
// database tier, because the authorization information GRANT carries names no
// target for a finer check to use.
func (f *privilegeFixture) arm(t *testing.T, database string) {
	t.Helper()
	if _, err := f.admin.Exec(
		"GRANT ALL PRIVILEGES ON " + database + ".* TO '" + privilegeAdmin + "'@'%' WITH GRANT OPTION"); err != nil {
		t.Fatalf("GRANT (arm): %v", err)
	}
}

// assertAccessDenied requires that err carries one of MySQL's access-denied
// codes rather than any other failure.
func assertAccessDenied(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("statement succeeded, want an access-denied refusal")
	}
	var stateErr *qrdbdriver.SQLStateError
	if !errors.As(err, &stateErr) {
		t.Fatalf("refusal carried no MySQL error code: %T %v", err, err)
	}
	switch stateErr.Number {
	case mysqlTableAccessDenied, mysqlDatabaseAccessDenied:
	default:
		t.Fatalf("refusal used code %d, want %d or %d: %v",
			stateErr.Number, mysqlTableAccessDenied, mysqlDatabaseAccessDenied, err)
	}
}

// TestNode_UnlatchedDatabaseIsUnrestricted pins the default posture over the
// real service graph: a database that has never been granted in imposes
// nothing on anyone. Every existing deployment is in this state.
func TestNode_UnlatchedDatabaseIsUnrestricted(t *testing.T) {
	f := newPrivilegeFixture(t, "privnone")

	for _, statement := range []string{
		"SELECT id, name FROM users WHERE id = 1",
		"INSERT INTO users VALUES (2, 'bob')",
		"UPDATE users SET name = 'carol' WHERE id = 2",
		"DELETE FROM users WHERE id = 2",
		"CREATE TABLE scratch (id BIGINT PRIMARY KEY)",
		"DROP TABLE scratch",
	} {
		if _, err := f.user.Exec(statement); err != nil {
			t.Errorf("unlatched database refused %q: %v", statement, err)
		}
	}
}

// TestNode_LatchedDatabaseEnforcesPerPrivilege drives each core statement
// through database/sql twice: refused without its privilege, admitted with it.
func TestNode_LatchedDatabaseEnforcesPerPrivilege(t *testing.T) {
	tests := []struct {
		name      string
		statement string
		privilege string
	}{
		{"select", "SELECT id, name FROM users WHERE id = 1", "SELECT"},
		{"insert", "INSERT INTO users VALUES (5, 'eve')", "INSERT"},
		{"update", "UPDATE users SET name = 'eve' WHERE id = 1", "UPDATE"},
		{"delete", "DELETE FROM users WHERE id = 1", "DELETE"},
		{"drop", "DROP TABLE users", "DROP"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			database := "priv" + tc.name
			f := newPrivilegeFixture(t, database)
			f.arm(t, database)

			_, err := f.user.Exec(tc.statement)
			assertAccessDenied(t, err)

			if _, err := f.admin.Exec(
				"GRANT " + tc.privilege + " ON users TO '" + privilegeUser + "'@'%'"); err != nil {
				t.Fatalf("GRANT %s: %v", tc.privilege, err)
			}

			if _, err := f.user.Exec(tc.statement); err != nil {
				t.Fatalf("GRANT %s did not admit %q: %v", tc.privilege, tc.statement, err)
			}
		})
	}
}

// TestNode_GrantShowRevokeRoundTrip drives the whole administration surface as
// real SQL over the real catalog.
func TestNode_GrantShowRevokeRoundTrip(t *testing.T) {
	f := newPrivilegeFixture(t, "privround")
	f.arm(t, "privround")

	if _, err := f.admin.Exec("GRANT SELECT, INSERT ON users TO 'bob'@'%'"); err != nil {
		t.Fatalf("GRANT: %v", err)
	}

	want := []string{
		"GRANT USAGE ON *.* TO `bob`@`%`",
		"GRANT SELECT, INSERT ON `privround`.`users` TO `bob`@`%`",
	}
	if got := showGrants(t, f.admin, "SHOW GRANTS FOR 'bob'@'%'"); !equalStrings(got, want) {
		t.Fatalf("SHOW GRANTS = %q, want %q", got, want)
	}

	if _, err := f.admin.Exec("REVOKE INSERT ON users FROM 'bob'@'%'"); err != nil {
		t.Fatalf("REVOKE: %v", err)
	}

	want = []string{
		"GRANT USAGE ON *.* TO `bob`@`%`",
		"GRANT SELECT ON `privround`.`users` TO `bob`@`%`",
	}
	if got := showGrants(t, f.admin, "SHOW GRANTS FOR 'bob'@'%'"); !equalStrings(got, want) {
		t.Fatalf("SHOW GRANTS after REVOKE = %q, want %q", got, want)
	}

	// SHOW GRANTS with no FOR clause reports the session's own identity.
	if got := showGrants(t, f.user, "SHOW GRANTS"); !equalStrings(got, want) {
		t.Fatalf("SHOW GRANTS (current user) = %q, want %q", got, want)
	}
}

// TestNode_RevokeDoesNotDisarmTheLatch pins the monotonicity of the posture
// over the real catalog: once armed, always armed.
func TestNode_RevokeDoesNotDisarmTheLatch(t *testing.T) {
	f := newPrivilegeFixture(t, "privlatch")
	f.arm(t, "privlatch")

	if _, err := f.admin.Exec("GRANT SELECT ON users TO 'bob'@'%'"); err != nil {
		t.Fatalf("GRANT: %v", err)
	}
	if _, err := f.admin.Exec("REVOKE SELECT ON users FROM 'bob'@'%'"); err != nil {
		t.Fatalf("REVOKE: %v", err)
	}

	rows, err := f.user.Query("SELECT id FROM users")
	if err == nil {
		_ = rows.Close()
	}
	assertAccessDenied(t, err)
}

// TestNode_UnsupportedGrantIsRefusedAndChangesNothing pins that a form this
// plane cannot enforce is rejected outright, and in particular does not arm
// the database on its way out.
func TestNode_UnsupportedGrantIsRefusedAndChangesNothing(t *testing.T) {
	f := newPrivilegeFixture(t, "privrefuse")

	if _, err := f.admin.Exec("GRANT SUPER ON *.* TO 'bob'@'%'"); err == nil {
		t.Fatal("GRANT SUPER ON *.* succeeded, want a refusal")
	}

	if _, err := f.user.Exec("SELECT id FROM users"); err != nil {
		t.Fatalf("a refused GRANT must not arm the database: %v", err)
	}
}

// showGrants runs a SHOW GRANTS statement and collects its lines.
func showGrants(t *testing.T, db *sql.DB, statement string) []string {
	t.Helper()
	rows, err := db.Query(statement)
	if err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
	defer func() { _ = rows.Close() }()

	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan: %v", err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return lines
}

// equalStrings compares two string slices element-wise.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
