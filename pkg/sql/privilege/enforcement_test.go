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

package privilege_test

import (
	"context"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	vtmysql "github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/privilege"
)

// requireAccessDenied asserts that err is a MySQL access-denied failure --
// one of the three object-level codes -- rather than any other kind of
// error. A refusal that surfaces as a generic engine failure is a bug: a
// client cannot tell it apart from a broken query.
func requireAccessDenied(t *testing.T, err error) *vtmysql.SQLError {
	t.Helper()
	require.Error(t, err)

	sqlErr := sqle.CastSQLError(err)
	require.NotNilf(t, sqlErr, "refusal did not carry a MySQL error code: %T %v", err, err)
	assert.Containsf(t, []int{
		privilege.ErrCodeDatabaseAccessDenied,
		privilege.ErrCodeTableAccessDenied,
		privilege.ErrCodeColumnAccessDenied,
	}, sqlErr.Num, "refusal used code %d, want a MySQL access-denied code: %v", sqlErr.Num, err)
	assert.Equal(t, "42000", sqlErr.State)
	return sqlErr
}

// grant applies a grant to the harness's identity through the service layer,
// which is also what arms the database's activation latch.
func (b *benchHarness) grant(t *testing.T, grants ...service.PrivilegeGrant) {
	t.Helper()
	svc, err := b.harness.Registry.PrivilegeService(b.identity.TenantID, benchDatabase)
	require.NoError(t, err)
	require.NoError(t, svc.Grant(context.Background(), &service.GrantPrivilegesRequest{
		Database: benchDatabase,
		Grantee:  b.identity.Subject + "@%",
		Grants:   grants,
	}))
}

// ---------------------------------------------------------------------------
// Default posture: a database with no privilege catalog is unrestricted
// ---------------------------------------------------------------------------

func TestUnlatchedDatabaseIsUnrestricted(t *testing.T) {
	h := newBenchHarness(t, benchTenantIdentity)

	// The identity holds no grants at all, and none has ever been made in
	// this database. Every statement must still succeed.
	for _, statement := range []string{
		"SELECT id, name FROM users WHERE id = 1",
		"INSERT INTO users VALUES (2, 'bob')",
		"UPDATE users SET name = 'carol' WHERE id = 2",
		"DELETE FROM users WHERE id = 2",
		"CREATE TABLE scratch (id BIGINT PRIMARY KEY)",
		"CREATE INDEX idx_name ON users (name)",
		"DROP TABLE scratch",
	} {
		_, err := h.query(t, statement)
		assert.NoErrorf(t, err, "unlatched database refused %q", statement)
	}
}

// ---------------------------------------------------------------------------
// Latched posture: default-deny, then per-privilege admission
// ---------------------------------------------------------------------------

// TestLatchedDatabaseDeniesThenAdmitsPerPrivilege drives every core DML and
// DDL statement twice through a REAL engine: once without the privilege it
// needs, once with it.
func TestLatchedDatabaseDeniesThenAdmitsPerPrivilege(t *testing.T) {
	tests := []struct {
		name      string
		statement string
		needs     service.PrivilegeMask
	}{
		{"select", "SELECT id, name FROM users WHERE id = 1", service.PrivilegeSelect},
		{"insert", "INSERT INTO users VALUES (7, 'grace')", service.PrivilegeInsert},
		{"update", "UPDATE users SET name = 'grace' WHERE id = 1", service.PrivilegeUpdate},
		{"delete", "DELETE FROM users WHERE id = 1", service.PrivilegeDelete},
		{"drop", "DROP TABLE users", service.PrivilegeDrop},
		{"truncate", "TRUNCATE TABLE users", service.PrivilegeDrop},
		{"alter", "ALTER TABLE users ADD COLUMN age INT", service.PrivilegeAlter},
		{"create index", "CREATE INDEX idx_name ON users (name)", service.PrivilegeIndex},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("denied without the privilege", func(t *testing.T) {
				h := newBenchHarness(t, benchTenantIdentity)
				// Arm the catalog with an unrelated grant, so the database is
				// latched but this statement's privilege is absent.
				h.grant(t, service.PrivilegeGrant{Table: "unrelated", Privileges: service.PrivilegeSelect})

				_, err := h.query(t, tc.statement)
				requireAccessDenied(t, err)
			})

			t.Run("permitted with the privilege", func(t *testing.T) {
				h := newBenchHarness(t, benchTenantIdentity)
				h.grant(t, service.PrivilegeGrant{Table: "users", Privileges: tc.needs})

				_, err := h.query(t, tc.statement)
				assert.NoError(t, err)
			})
		})
	}
}

// TestLatchedDatabaseDeniesCreateTable covers the statement whose
// authorization information names no table at all -- CREATE TABLE arrives as
// AuthType CREATE with an empty database-identifier list -- so it can only be
// governed at the database tier.
func TestLatchedDatabaseDeniesCreateTable(t *testing.T) {
	denied := newBenchHarness(t, benchTenantIdentity)
	denied.grant(t, service.PrivilegeGrant{Table: "users", Privileges: service.PrivilegeSelect})
	_, err := denied.query(t, "CREATE TABLE scratch (id BIGINT PRIMARY KEY)")
	requireAccessDenied(t, err)

	permitted := newBenchHarness(t, benchTenantIdentity)
	permitted.grant(t, service.PrivilegeGrant{Privileges: service.PrivilegeCreate})
	_, err = permitted.query(t, "CREATE TABLE scratch (id BIGINT PRIMARY KEY)")
	assert.NoError(t, err)
}

// TestDatabaseTierCoversEveryTable pins that a database-tier grant admits a
// statement against a table it never names, including one created later.
func TestDatabaseTierCoversEveryTable(t *testing.T) {
	h := newBenchHarness(t, benchTenantIdentity)
	h.grant(t, service.PrivilegeGrant{Privileges: service.PrivilegeSelect | service.PrivilegeCreate})

	_, err := h.query(t, "CREATE TABLE later (id BIGINT PRIMARY KEY)")
	require.NoError(t, err)

	_, err = h.query(t, "SELECT id FROM later")
	assert.NoError(t, err, "the database tier must cover a table created after the grant")
}

// TestTableTierDoesNotLeakAcrossTables pins the isolation between two tables
// in the same latched database.
func TestTableTierDoesNotLeakAcrossTables(t *testing.T) {
	h := newBenchHarness(t, benchSystemIdentity)
	_, err := h.query(t, "CREATE TABLE secrets (id BIGINT PRIMARY KEY, token VARCHAR(64))")
	require.NoError(t, err)

	// Re-open the same catalog as a governed identity.
	governed := h.as(benchTenantIdentity)
	governed.grant(t, service.PrivilegeGrant{Table: "users", Privileges: service.PrivilegeSelect})

	_, err = governed.query(t, "SELECT id FROM users")
	require.NoError(t, err)

	_, err = governed.query(t, "SELECT id FROM secrets")
	requireAccessDenied(t, err)
}

// ---------------------------------------------------------------------------
// Structural coverage
// ---------------------------------------------------------------------------

// TestUnenumeratedStatementIsStillChecked is the structural guarantee this
// design exists for: coverage comes from go-mysql-server ASKING about every
// statement it builds, not from a list of statements maintained in qrdb.
//
// RENAME TABLE is deliberately chosen: it is the one statement whose
// authorization information carries neither a target type nor a table pair in
// the usual shape (its names arrive in groups of four), so nothing about the
// ordinary SELECT/INSERT/UPDATE/DELETE path could have covered it by
// accident. If a future go-mysql-server upgrade routes some new statement
// kind through a shape this package does not model, the closed dispatch
// tables deny it rather than admitting it silently -- which is what the third
// case here pins.
func TestUnenumeratedStatementIsStillChecked(t *testing.T) {
	t.Run("rename is refused without ALTER and DROP on the source", func(t *testing.T) {
		h := newBenchHarness(t, benchTenantIdentity)
		h.grant(t, service.PrivilegeGrant{Table: "users", Privileges: service.PrivilegeSelect})

		_, err := h.query(t, "RENAME TABLE users TO people")
		requireAccessDenied(t, err)
	})

	t.Run("rename is admitted with the full requirement", func(t *testing.T) {
		h := newBenchHarness(t, benchTenantIdentity)
		h.grant(t,
			service.PrivilegeGrant{Table: "users", Privileges: service.PrivilegeAlter | service.PrivilegeDrop},
			service.PrivilegeGrant{Table: "people", Privileges: service.PrivilegeCreate | service.PrivilegeInsert},
		)

		_, err := h.query(t, "RENAME TABLE users TO people")
		assert.NoError(t, err)
	})

	t.Run("a statement kind this plane does not model fails closed", func(t *testing.T) {
		h := newBenchHarness(t, benchTenantIdentity)
		h.grant(t, service.PrivilegeGrant{Privileges: service.PrivilegeAll})

		// CREATE TRIGGER's authorization type (TRIGGER) is not in the
		// dispatch table, and no grant in this plane can name it, so no
		// amount of granting admits it.
		_, err := h.query(t,
			"CREATE TRIGGER t BEFORE INSERT ON users FOR EACH ROW SET NEW.name = 'x'")
		require.Error(t, err)
	})
}

// TestEveryModelledAuthTypeIsReachable walks the statements whose
// authorization types this plane does model and asserts each one is admitted
// by exactly the privilege it names, through the real engine. It is the
// positive half of the structural guarantee.
func TestEveryModelledAuthTypeIsReachable(t *testing.T) {
	h := newBenchHarness(t, benchTenantIdentity)
	h.grant(t, service.PrivilegeGrant{Privileges: service.PrivilegeAll})

	for _, statement := range []string{
		"SELECT id FROM users",
		"INSERT INTO users VALUES (11, 'ida')",
		"UPDATE users SET name = 'ida2' WHERE id = 11",
		"DELETE FROM users WHERE id = 11",
		"SHOW TABLES",
		"CREATE INDEX idx_name ON users (name)",
		"ALTER TABLE users ADD COLUMN age INT",
		"TRUNCATE TABLE users",

		// Last, because a foreign key referencing users makes users
		// un-truncatable from here on.
		"CREATE TABLE child (id BIGINT PRIMARY KEY, owner BIGINT, FOREIGN KEY (owner) REFERENCES users(id))",
	} {
		_, err := h.query(t, statement)
		assert.NoErrorf(t, err, "ALL PRIVILEGES did not admit %q", statement)
	}
}

// ---------------------------------------------------------------------------
// The system-tenant escape hatch
// ---------------------------------------------------------------------------

func TestSystemTenantIsExemptFromALatchedDatabase(t *testing.T) {
	h := newBenchHarness(t, benchTenantIdentity)
	h.grant(t, service.PrivilegeGrant{Table: "unrelated", Privileges: service.PrivilegeSelect})

	// The governed identity is locked out ...
	_, err := h.query(t, "SELECT id FROM users")
	requireAccessDenied(t, err)

	// ... but the operator never is. This is the only way back from a latch,
	// which is why it can never be revoked.
	operator := h.as(benchSystemIdentity)
	_, err = operator.query(t, "SELECT id FROM users")
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Epoch invalidation across sessions
// ---------------------------------------------------------------------------

// TestRevokeTakesEffectOnTheNextStatement pins that the snapshot cache is
// invalidated by the catalog generation rather than by anything session-local
// -- a revoke issued elsewhere must bind immediately.
func TestRevokeTakesEffectOnTheNextStatement(t *testing.T) {
	h := newBenchHarness(t, benchTenantIdentity)
	h.grant(t, service.PrivilegeGrant{Table: "users", Privileges: service.PrivilegeSelect})

	_, err := h.query(t, "SELECT id FROM users")
	require.NoError(t, err, "the grant must take effect")

	svc, err := h.harness.Registry.PrivilegeService(benchTenantIdentity.TenantID, benchDatabase)
	require.NoError(t, err)
	require.NoError(t, svc.Revoke(context.Background(), &service.RevokePrivilegesRequest{
		Database: benchDatabase,
		Grantee:  benchTenantIdentity.Subject + "@%",
		Revokes:  []service.PrivilegeGrant{{Table: "users", Privileges: service.PrivilegeSelect}},
	}))

	_, err = h.query(t, "SELECT id FROM users")
	requireAccessDenied(t, err)
}

// TestAnotherGranteesMutationDoesNotDisturbThisOne pins the second stage of
// the staged read: a mutation to a different grantee moves the database
// generation, and the session must still resolve its own, unchanged, set.
func TestAnotherGranteesMutationDoesNotDisturbThisOne(t *testing.T) {
	h := newBenchHarness(t, benchTenantIdentity)
	h.grant(t, service.PrivilegeGrant{Table: "users", Privileges: service.PrivilegeSelect})

	_, err := h.query(t, "SELECT id FROM users")
	require.NoError(t, err)

	svc, err := h.harness.Registry.PrivilegeService(benchTenantIdentity.TenantID, benchDatabase)
	require.NoError(t, err)
	require.NoError(t, svc.Grant(context.Background(), &service.GrantPrivilegesRequest{
		Database: benchDatabase,
		Grantee:  "someone-else@%",
		Grants:   []service.PrivilegeGrant{{Table: "users", Privileges: service.PrivilegeDelete}},
	}))

	_, err = h.query(t, "SELECT id FROM users")
	assert.NoError(t, err, "another grantee's mutation must not disturb this session's own privileges")

	_, err = h.query(t, "DELETE FROM users WHERE id = 1")
	requireAccessDenied(t, err)
}
