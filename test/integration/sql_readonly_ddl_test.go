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

//go:build integration

// sql_readonly_ddl_test.go is the security proof for the ad-hoc SQL query
// surface.
//
// POST /sql/query -- and the SQLQuery RPC every other transport exposes on
// top of the same service -- is authorized as permission.KVRange. That is a
// READ permission (pkg/auth/middleware/mapping.go). A caller holding nothing
// but read permission can therefore reach this endpoint, so the endpoint must
// not be able to write, and must not be able to change the SCHEMA either.
//
// The guard is structural rather than a permission check:
// pkg/sql/provider/readonly_database.go embeds the sql.Database INTERFACE, so
// go-mysql-server's analyzer type-asserts for sql.TableCreator /
// sql.TableDropper / sql.IndexAlterableTable and fails every time, whatever
// the wrapped concrete database implements. pkg/sql/query/service_test.go
// pins that at the unit level against a mock.
//
// This file is the distributed counterpart, and it is not a duplicate of it:
//
//   - It goes over the real network, through mTLS and the real authorizer, at
//     the real route, against a real relational table that really exists and
//     really has rows and a real secondary index -- created out of band over
//     the MySQL wire protocol, which is where the full DDL surface lives.
//     DROP TABLE / ALTER TABLE / CREATE INDEX / TRUNCATE cannot be
//     meaningfully refused against a table that does not exist, and the unit
//     test has no such table.
//   - It asserts on EVERY replica, not one process. A read-only guard that
//     holds on the leader and not on a follower is not a guard.
//   - It asserts the ABSENCE OF EFFECT afterwards -- the table, its columns,
//     its index and its exact rows are all still there -- so a rejection that
//     happened after a partial mutation would still fail the test.
package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// readOnlyStatement is one statement that the ad-hoc SQL surface must refuse.
type readOnlyStatement struct {
	name  string
	query string
	// verb is the SQL verb, used only in failure messages.
	verb string
}

// TestSQLCluster_ReadOnlySQLEndpoint_RefusesDDLAndDML posts every mutating
// verb to POST /sql/query on every replica of a live cluster and requires all
// of them to be refused, while a plain SELECT continues to work.
func TestSQLCluster_ReadOnlySQLEndpoint_RefusesDDLAndDML(t *testing.T) {
	c := startThreeNodeClusterWithSQL(t)
	leader := c.leaderIndex(t, 60*time.Second)
	t.Logf("shard 1 leader is replica %d", leader+1)

	// --- Build the target out of band, over the MySQL wire protocol -------
	//
	// The wire listeners are the surface that legitimately carries DDL; the
	// ad-hoc query endpoint is the one that must not. Creating the table here
	// is what gives the DROP/ALTER/CREATE INDEX/TRUNCATE attempts below
	// something real to destroy.
	const table = "ro_guard"
	leaderDB := c.mysql(t, leader)
	mustExec(t, leaderDB, fmt.Sprintf(
		"CREATE TABLE %s (id INT PRIMARY KEY, owner VARCHAR(64), balance INT)", table))
	for i := 1; i <= 3; i++ {
		mustExec(t, leaderDB, fmt.Sprintf("INSERT INTO %s (id, owner, balance) VALUES (?, ?, ?)", table),
			i, fmt.Sprintf("owner-%d", i), i*100)
	}
	mustExec(t, leaderDB, fmt.Sprintf("CREATE INDEX idx_%s_owner ON %s (owner)", table, table))

	client := mtlsHTTPClient(t, c.ca, 15*time.Second)

	statements := []readOnlyStatement{
		{"create_table", "CREATE TABLE ro_guard_new (id INT PRIMARY KEY)", "CREATE TABLE"},
		{"drop_table", "DROP TABLE " + table, "DROP TABLE"},
		{"alter_table_add_column", "ALTER TABLE " + table + " ADD COLUMN injected VARCHAR(16)", "ALTER TABLE"},
		{"alter_table_drop_column", "ALTER TABLE " + table + " DROP COLUMN balance", "ALTER TABLE"},
		{"create_index", "CREATE INDEX idx_ro_guard_balance ON " + table + " (balance)", "CREATE INDEX"},
		{"drop_index", "DROP INDEX idx_" + table + "_owner ON " + table, "DROP INDEX"},
		{"truncate", "TRUNCATE TABLE " + table, "TRUNCATE"},
		{"insert", "INSERT INTO " + table + " (id, owner, balance) VALUES (99, 'intruder', 999)", "INSERT"},
		{"update", "UPDATE " + table + " SET balance = 999 WHERE id = 1", "UPDATE"},
		{"delete", "DELETE FROM " + table + " WHERE id = 1", "DELETE"},
	}

	for _, i := range c.liveIndexes() {
		role := "follower"
		if i == leader {
			role = "leader"
		}
		port := c.nodes[i].adminPort
		nodeLabel := fmt.Sprintf("replica%d(%s)", i+1, role)

		t.Run(nodeLabel, func(t *testing.T) {
			// Positive control: the endpoint really is reachable, really is
			// authorized for this caller, and really can read this table.
			// Without this, every rejection below could be an artifact of a
			// broken route or a failed handshake rather than the guard.
			sel := sqlQueryHTTPOK(t, client, port, sqlClusterDatabase,
				"SELECT id, owner, balance FROM "+table+" ORDER BY id ASC")
			if sel.RowCount != 3 {
				t.Fatalf("%s: control SELECT rowCount = %d, want 3", nodeLabel, sel.RowCount)
			}
			if fmt.Sprint(sel.Rows[0]["owner"]) != "owner-1" {
				t.Fatalf("%s: control SELECT first row = %+v, want owner-1", nodeLabel, sel.Rows[0])
			}

			for _, s := range statements {
				t.Run(s.name, func(t *testing.T) {
					status, body := sqlQueryHTTP(t, client, port, sqlClusterDatabase, s.query)
					if status == http.StatusOK {
						t.Fatalf("%s: %s was ACCEPTED by the read-authorized SQL endpoint (status 200); body: %s",
							nodeLabel, s.verb, body)
					}
					// A rejection must be a client error, not a crash: a 5xx
					// would mean the statement got far enough to break
					// something rather than being refused during analysis.
					if status < 400 || status >= 500 {
						t.Fatalf("%s: %s was rejected with status %d, want a 4xx client error; body: %s",
							nodeLabel, s.verb, status, body)
					}
					t.Logf("%s: %s rejected with %d: %s", nodeLabel, s.verb, status, trimBody(body))
				})
			}
		})
	}

	// --- Nothing was mutated ---------------------------------------------
	//
	// Asserted through the MySQL wire protocol, on every replica, because
	// asserting it through the same read-only endpoint that just refused
	// everything would be circular for the schema half.
	for _, i := range c.liveIndexes() {
		db := leaderDB
		if i != leader {
			db = c.mysql(t, i)
		}
		label := fmt.Sprintf("replica%d", i+1)

		tables := sqlShowTables(t, db, label)
		if !contains(tables, table) {
			t.Fatalf("%s: %s is gone after the refused DROP TABLE; SHOW TABLES = %v", label, table, tables)
		}
		if contains(tables, "ro_guard_new") {
			t.Fatalf("%s: the refused CREATE TABLE created ro_guard_new anyway; SHOW TABLES = %v", label, tables)
		}

		cols := sqlColumnNames(t, db, label, table)
		if len(cols) != 3 {
			t.Fatalf("%s: columns after the refused ALTER TABLEs = %v, want the original 3", label, cols)
		}
		for _, c := range cols {
			if strings.EqualFold(c, "injected") {
				t.Fatalf("%s: the refused ALTER TABLE ... ADD COLUMN added %q", label, c)
			}
		}

		indexes := sqlIndexNames(t, db, label, table)
		if !contains(indexes, "idx_"+table+"_owner") {
			t.Fatalf("%s: the refused DROP INDEX removed idx_%s_owner; SHOW INDEX = %v", label, table, indexes)
		}
		if contains(indexes, "idx_ro_guard_balance") {
			t.Fatalf("%s: the refused CREATE INDEX created idx_ro_guard_balance anyway; SHOW INDEX = %v", label, indexes)
		}

		ids := selectIDs(t, db, label, table)
		if len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 3 {
			t.Fatalf("%s: rows after the refused TRUNCATE/INSERT/UPDATE/DELETE = %v, want [1 2 3]", label, ids)
		}
		var balance int
		if err := db.QueryRow(fmt.Sprintf("SELECT balance FROM %s WHERE id = 1", table)).Scan(&balance); err != nil {
			t.Fatalf("%s: SELECT balance: %v", label, err)
		}
		if balance != 100 {
			t.Fatalf("%s: balance of row 1 = %d, want 100 (the refused UPDATE took effect)", label, balance)
		}
	}
}

// trimBody renders a response body for a log line, collapsing it to its
// message field when it is the standard JSON error envelope.
func trimBody(body []byte) string {
	var envelope struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		if envelope.Message != "" {
			return envelope.Message
		}
		if envelope.Error != "" {
			return envelope.Error
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// TestSQLCluster_ReadOnlySQLEndpoint_RefusesDDLOnEveryDatabase checks the
// guard is a property of the ENDPOINT, not of one database. The ad-hoc query
// service builds a provider per (tenant, database) scope, so a database
// created after the server started -- one whose provider was constructed by a
// different code path than the default database's -- must be wrapped exactly
// the same way.
func TestSQLCluster_ReadOnlySQLEndpoint_RefusesDDLOnEveryDatabase(t *testing.T) {
	c := startThreeNodeClusterWithSQL(t)
	leader := c.leaderIndex(t, 60*time.Second)
	client := mtlsHTTPClient(t, c.ca, 15*time.Second)
	port := c.nodes[leader].adminPort

	// The reserved system database, which every node serves from boot.
	for _, q := range []string{
		"CREATE TABLE sys_intruder (id INT PRIMARY KEY)",
		"INSERT INTO kv (`key`, `value`) VALUES ('intruder', 'x')",
		"TRUNCATE TABLE kv",
	} {
		status, body := sqlQueryHTTP(t, client, port, "system", q)
		if status == http.StatusOK {
			t.Fatalf("%q was ACCEPTED against the system database; body: %s", q, body)
		}
		if status < 400 || status >= 500 {
			t.Fatalf("%q against the system database: status %d, want a 4xx; body: %s", q, status, body)
		}
	}

	// A SELECT against the same database must still succeed, so the
	// rejections above are attributable to the statement and not to the
	// database being unreachable.
	if _, err := sqlQueryHTTPStatusOK(t, client, port, "system", "SELECT * FROM kv"); err != nil {
		t.Fatalf("control SELECT against the system database: %v", err)
	}
}

// sqlQueryHTTPStatusOK posts query and returns an error rather than failing
// the test, for callers that want to attribute the failure themselves.
func sqlQueryHTTPStatusOK(t *testing.T, client *http.Client, port int, database, query string) (sqlQueryResponse, error) {
	t.Helper()
	status, body := sqlQueryHTTP(t, client, port, database, query)
	if status != http.StatusOK {
		return sqlQueryResponse{}, &sqlQueryStatusError{Status: status, Body: string(body)}
	}
	var result sqlQueryResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return sqlQueryResponse{}, err
	}
	return result, nil
}

// sqlQueryStatusError reports an unexpected HTTP status from POST /sql/query.
type sqlQueryStatusError struct {
	Status int
	Body   string
}

func (e *sqlQueryStatusError) Error() string {
	return fmt.Sprintf("sql query: status %d, body: %s", e.Status, e.Body)
}
