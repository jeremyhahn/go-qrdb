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
	"testing"

	gomysql "github.com/go-sql-driver/mysql"

	qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
)

// ---------------------------------------------------------------------------
// FOREIGN KEY over the node:// DSN, through database/sql.
//
// Everything below is asserted at the level a real client sees it: a
// *mysql.MySQLError carrying a MySQL error number. That number is the contract,
// not a detail -- gorm.io/driver/mysql's error translator maps 1451 and 1452 to
// gorm.ErrForeignKeyViolated, so a violation that surfaced with any other
// number would reach a GORM caller as an unrecognized driver error.
//
// embedded:// deliberately cannot be used here: it has no service.Registry
// and therefore no relational catalog at all (see
// TestEmbeddedScheme_CreateTableStillUnavailable), so CREATE TABLE never gets
// far enough to declare a constraint.
// ---------------------------------------------------------------------------

// mysqlErrNo asserts err reached the caller as a *mysql.MySQLError and returns
// its MySQL error number.
func mysqlErrNo(t *testing.T, err error) uint16 {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	stateErr, ok := err.(*gomysql.MySQLError)
	if !ok {
		t.Fatalf("error = %v (%T), want *mysql.MySQLError", err, err)
	}
	return stateErr.Number
}

// createFKSchema creates a companies/users pair joined by a foreign key with
// the given referential action clause.
func createFKSchema(t *testing.T, db *sql.DB, actions string) {
	t.Helper()
	if _, err := db.Exec("CREATE TABLE companies (id BIGINT PRIMARY KEY, name VARCHAR(64))"); err != nil {
		t.Fatalf("CREATE TABLE companies: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE users (
		id BIGINT PRIMARY KEY,
		name VARCHAR(64),
		company_id BIGINT,
		CONSTRAINT fk_users_company FOREIGN KEY (company_id) REFERENCES companies(id) ` + actions + `
	)`); err != nil {
		t.Fatalf("CREATE TABLE users: %v", err)
	}
}

// TestNode_ForeignKeyChildViolationIsMySQL1452 verifies inserting a child row
// with no matching parent is refused, and that the refusal reaches the caller
// as MySQL 1452 (ER_NO_REFERENCED_ROW_2).
func TestNode_ForeignKeyChildViolationIsMySQL1452(t *testing.T) {
	db := openNode(t, "node://memory?database=fkchild")
	createFKSchema(t, db, "")

	if _, err := db.Exec("INSERT INTO companies VALUES (1, 'acme')"); err != nil {
		t.Fatalf("INSERT parent: %v", err)
	}
	if _, err := db.Exec("INSERT INTO users VALUES (1, 'bob', 1)"); err != nil {
		t.Fatalf("INSERT valid child: %v", err)
	}

	_, err := db.Exec("INSERT INTO users VALUES (2, 'eve', 99)")
	if got := mysqlErrNo(t, err); got != 1452 {
		t.Errorf("error number = %d, want 1452 (ER_NO_REFERENCED_ROW_2); err = %v", got, err)
	}

	// The rejected row must not have been written.
	var count int64
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatalf("SELECT COUNT: %v", err)
	}
	if count != 1 {
		t.Errorf("user count = %d, want 1", count)
	}

	// A NULL key column is exempt from the check (MATCH SIMPLE).
	if _, err := db.Exec("INSERT INTO users VALUES (3, 'orphan', NULL)"); err != nil {
		t.Errorf("INSERT with NULL foreign key column: %v", err)
	}
}

// TestNode_ForeignKeyParentViolationIsMySQL1451 verifies deleting a parent
// that still has children is refused under the default referential action,
// and that the refusal reaches the caller as MySQL 1451
// (ER_ROW_IS_REFERENCED_2).
func TestNode_ForeignKeyParentViolationIsMySQL1451(t *testing.T) {
	db := openNode(t, "node://memory?database=fkparent")
	createFKSchema(t, db, "")

	if _, err := db.Exec("INSERT INTO companies VALUES (1, 'acme')"); err != nil {
		t.Fatalf("INSERT parent: %v", err)
	}
	if _, err := db.Exec("INSERT INTO users VALUES (1, 'bob', 1)"); err != nil {
		t.Fatalf("INSERT child: %v", err)
	}

	_, err := db.Exec("DELETE FROM companies WHERE id = 1")
	if got := mysqlErrNo(t, err); got != 1451 {
		t.Errorf("error number = %d, want 1451 (ER_ROW_IS_REFERENCED_2); err = %v", got, err)
	}

	var count int64
	if err := db.QueryRow("SELECT COUNT(*) FROM companies").Scan(&count); err != nil {
		t.Fatalf("SELECT COUNT: %v", err)
	}
	if count != 1 {
		t.Errorf("company count = %d, want 1 (the restricted parent must survive)", count)
	}

	// Updating the referenced key is restricted for the same reason.
	_, err = db.Exec("UPDATE companies SET id = 2 WHERE id = 1")
	if got := mysqlErrNo(t, err); got != 1451 {
		t.Errorf("UPDATE error number = %d, want 1451; err = %v", got, err)
	}

	// Removing the child releases both.
	if _, err := db.Exec("DELETE FROM users WHERE id = 1"); err != nil {
		t.Fatalf("DELETE child: %v", err)
	}
	if _, err := db.Exec("DELETE FROM companies WHERE id = 1"); err != nil {
		t.Fatalf("DELETE parent after child removed: %v", err)
	}
}

// TestNode_ForeignKeyCascadeDelete verifies ON DELETE CASCADE propagates
// through the driver, which is the strongest end-to-end evidence that the
// stored constraint metadata is actually driving enforcement.
func TestNode_ForeignKeyCascadeDelete(t *testing.T) {
	db := openNode(t, "node://memory?database=fkcascade")
	createFKSchema(t, db, "ON DELETE CASCADE")

	if _, err := db.Exec("INSERT INTO companies VALUES (1, 'acme'), (2, 'globex')"); err != nil {
		t.Fatalf("INSERT parents: %v", err)
	}
	if _, err := db.Exec("INSERT INTO users VALUES (1, 'a', 1), (2, 'b', 2), (3, 'c', 1)"); err != nil {
		t.Fatalf("INSERT children: %v", err)
	}

	if _, err := db.Exec("DELETE FROM companies WHERE id = 1"); err != nil {
		t.Fatalf("cascading DELETE: %v", err)
	}

	rows, err := db.Query("SELECT id FROM users ORDER BY id")
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(ids) != 1 || ids[0] != 2 {
		t.Errorf("surviving user ids = %v, want [2]", ids)
	}
}

// TestNode_ForeignKeySurvivesReconnect verifies the constraint is genuinely
// persisted in the replicated catalog rather than held in the connection that
// declared it: a fresh connection over the same node must enforce it.
func TestNode_ForeignKeySurvivesReconnect(t *testing.T) {
	cfg, err := qrdbdriver.ParseDSN("node://memory?database=fkpersist")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	connector := New(cfg)
	t.Cleanup(func() { _ = connector.Close() })

	first := sql.OpenDB(connector)
	createFKSchema(t, first, "")
	if _, err := first.Exec("INSERT INTO companies VALUES (1, 'acme')"); err != nil {
		t.Fatalf("INSERT parent: %v", err)
	}
	// Retire every pooled connection, so the pool below must dial a fresh
	// session rather than hand back the one that ran the DDL. Closing the
	// *sql.DB itself would close the shared connector and take the node with
	// it.
	first.SetMaxIdleConns(0)

	second := sql.OpenDB(connector)

	_, err = second.Exec("INSERT INTO users VALUES (1, 'eve', 99)")
	if got := mysqlErrNo(t, err); got != 1452 {
		t.Errorf("error number over a fresh connection = %d, want 1452; err = %v", got, err)
	}
	if _, err := second.Exec("INSERT INTO users VALUES (1, 'bob', 1)"); err != nil {
		t.Errorf("valid INSERT over a fresh connection: %v", err)
	}
}

// TestNode_ResetSessionRestoresForeignKeyChecksAfterFailedDropTable pins one
// connection, as the stock GORM migrator's DropTable does, and leaves
// FOREIGN_KEY_CHECKS off on it by failing before the SET that would have
// turned it back on -- a real DROP TABLE failure stops at exactly the same
// place (docs/sql/gorm.md). Returning that connection to the pool and taking
// it back out, which is what calls (*embeddedConn).ResetSession, must not
// hand the next caller a connection that skips foreign key enforcement.
func TestNode_ResetSessionRestoresForeignKeyChecksAfterFailedDropTable(t *testing.T) {
	db := openNode(t, "node://memory?database=fkresetsession")
	db.SetMaxOpenConns(1) // only one physical connection can ever exist

	createFKSchema(t, db, "")
	if _, err := db.Exec("INSERT INTO companies VALUES (1, 'acme')"); err != nil {
		t.Fatalf("INSERT parent: %v", err)
	}

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Fatalf("SET FOREIGN_KEY_CHECKS = 0: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "DROP TABLE does_not_exist_xyz"); err == nil {
		t.Fatal("DROP TABLE does_not_exist_xyz: want an error, got nil")
	}
	// Close returns the pinned connection to the pool without running the
	// restoring SET -- the same thing a failed DropTable leaves behind.
	if err := conn.Close(); err != nil {
		t.Fatalf("conn.Close: %v", err)
	}

	// MaxOpenConns(1) guarantees this checks the connection above back out
	// of the pool, which is what calls ResetSession.
	_, err = db.Exec("INSERT INTO users VALUES (2, 'eve', 99)")
	if got := mysqlErrNo(t, err); got != 1452 {
		t.Errorf("error number on the reused connection = %d, want 1452 (ER_NO_REFERENCED_ROW_2) -- foreign key checks were not restored; err = %v", got, err)
	}

	var count int64
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatalf("SELECT COUNT: %v", err)
	}
	if count != 0 {
		t.Errorf("user count = %d, want 0 (the rejected row must not have been written)", count)
	}
}
