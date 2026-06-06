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

package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// Test entity helpers
// ---------------------------------------------------------------------------

// bridgeUser mirrors the bridgeTestUser struct registered in cmd/qrdbd/test_entities.go.
// It is used by integration tests to marshal entities and assert field values.
type bridgeUser struct {
	ID    uint64  `json:"id"`
	Name  string  `json:"name"`
	Email string  `json:"email"`
	Age   int64   `json:"age"`
	Score float64 `json:"score"`
}

// putBridgeUser marshals u as JSON and stores it via the REST KV API.
// The key includes the "system/" tenant prefix because the EntityTable
// scans with tenant.PrefixKey(tenantID, entityType + "/"), and the SQL
// session derives its tenant from the authenticated mTLS identity — the
// admin client cert carries OU=system, so the session tenant is "system".
// The DAO write must land under the same namespace the SQL reader scans.
func putBridgeUser(t *testing.T, ca *testCA, adminPort int, u bridgeUser) {
	t.Helper()
	data, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("putBridgeUser: marshal: %v", err)
	}
	httpPut(t, ca, adminPort, fmt.Sprintf("system/bridge_users/%d", u.ID), data)
}

// getBridgeUser retrieves the entity via the REST KV API and unmarshals
// it into a bridgeUser. The key includes the "system/" tenant prefix
// (the admin identity's tenant; see putBridgeUser).
func getBridgeUser(t *testing.T, ca *testCA, adminPort int, id uint64) bridgeUser {
	t.Helper()
	data := httpGet(t, ca, adminPort, fmt.Sprintf("system/bridge_users/%d", id))
	var u bridgeUser
	if err := json.Unmarshal(data, &u); err != nil {
		t.Fatalf("getBridgeUser: unmarshal: %v", err)
	}
	return u
}

// ---------------------------------------------------------------------------
// MySQL wire protocol — entity bridge tests
// ---------------------------------------------------------------------------

// TestEntityBridge_MySQL_DAOWriteSQLRead stores an entity via the REST KV API
// (simulating a DAO write) and reads it back through the MySQL wire protocol,
// verifying the entity-to-SQL bridge surfaces all fields correctly.
func TestEntityBridge_MySQL_DAOWriteSQLRead(t *testing.T) {
	adminPort, mysqlPort, _, ca := startSingleNodeWithSQL(t)

	want := bridgeUser{ID: 100, Name: "Alice", Email: "alice@example.com", Age: 30, Score: 9.5}
	putBridgeUser(t, ca, adminPort, want)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")

	var (
		gotID    int64
		gotName  string
		gotEmail string
		gotAge   int64
		gotScore float64
	)
	err := db.QueryRow(
		"SELECT _id, name, email, age, score FROM bridge_users WHERE _id = ?", 100,
	).Scan(&gotID, &gotName, &gotEmail, &gotAge, &gotScore)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}

	if gotID != int64(want.ID) {
		t.Errorf("id: got %d, want %d", gotID, want.ID)
	}
	if gotName != want.Name {
		t.Errorf("name: got %q, want %q", gotName, want.Name)
	}
	if gotEmail != want.Email {
		t.Errorf("email: got %q, want %q", gotEmail, want.Email)
	}
	if gotAge != want.Age {
		t.Errorf("age: got %d, want %d", gotAge, want.Age)
	}
	if gotScore != want.Score {
		t.Errorf("score: got %f, want %f", gotScore, want.Score)
	}
}

// TestEntityBridge_MySQL_SQLWriteDAORead inserts an entity via the MySQL wire
// protocol and reads it back through the REST KV API, verifying that SQL
// writes are durably stored and retrievable by the DAO layer.
func TestEntityBridge_MySQL_SQLWriteDAORead(t *testing.T) {
	adminPort, mysqlPort, _, ca := startSingleNodeWithSQL(t)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")

	// Columns are in alphabetical order after _id: _id, age, email, name, score.
	_, err := db.Exec(
		"INSERT INTO bridge_users (_id, age, email, name, score) VALUES (?, ?, ?, ?, ?)",
		200, 25, "bob@example.com", "Bob", 7.2,
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	got := getBridgeUser(t, ca, adminPort, 200)

	if got.ID != 200 {
		t.Errorf("id: got %d, want 200", got.ID)
	}
	if got.Name != "Bob" {
		t.Errorf("name: got %q, want %q", got.Name, "Bob")
	}
	if got.Email != "bob@example.com" {
		t.Errorf("email: got %q, want %q", got.Email, "bob@example.com")
	}
	if got.Age != 25 {
		t.Errorf("age: got %d, want 25", got.Age)
	}
	if got.Score != 7.2 {
		t.Errorf("score: got %f, want %f", got.Score, 7.2)
	}
}

// TestEntityBridge_MySQL_SelectAll stores three entities via the REST KV API
// and verifies that SELECT * FROM bridge_users returns all three rows.
func TestEntityBridge_MySQL_SelectAll(t *testing.T) {
	adminPort, mysqlPort, _, ca := startSingleNodeWithSQL(t)

	users := []bridgeUser{
		{ID: 301, Name: "Charlie", Email: "charlie@example.com", Age: 22, Score: 5.0},
		{ID: 302, Name: "Diana", Email: "diana@example.com", Age: 35, Score: 8.0},
		{ID: 303, Name: "Eve", Email: "eve@example.com", Age: 28, Score: 6.5},
	}
	for _, u := range users {
		putBridgeUser(t, ca, adminPort, u)
	}

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")

	rows, err := db.Query("SELECT _id, name, email FROM bridge_users")
	if err != nil {
		t.Fatalf("SELECT *: %v", err)
	}
	defer rows.Close()

	type row struct {
		id    int64
		name  string
		email string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.name, &r.email); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration: %v", err)
	}

	if len(got) != len(users) {
		t.Fatalf("row count: got %d, want %d", len(got), len(users))
	}

	// Sort both slices by ID for a stable comparison.
	sort.Slice(got, func(i, j int) bool { return got[i].id < got[j].id })
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })

	for i, u := range users {
		if got[i].id != int64(u.ID) {
			t.Errorf("[%d] id: got %d, want %d", i, got[i].id, u.ID)
		}
		if got[i].name != u.Name {
			t.Errorf("[%d] name: got %q, want %q", i, got[i].name, u.Name)
		}
		if got[i].email != u.Email {
			t.Errorf("[%d] email: got %q, want %q", i, got[i].email, u.Email)
		}
	}
}

// TestEntityBridge_MySQL_UpdateAndVerify stores an entity via the REST KV API,
// updates the name field via MySQL, then reads back both via MySQL and the REST
// API to confirm the update propagated through both paths.
func TestEntityBridge_MySQL_UpdateAndVerify(t *testing.T) {
	adminPort, mysqlPort, _, ca := startSingleNodeWithSQL(t)

	original := bridgeUser{ID: 400, Name: "Frank", Email: "frank@example.com", Age: 40, Score: 3.0}
	putBridgeUser(t, ca, adminPort, original)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")

	_, err := db.Exec("UPDATE bridge_users SET name = ? WHERE _id = ?", "Franklin", 400)
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}

	// Verify via MySQL SELECT.
	var gotName string
	err = db.QueryRow("SELECT name FROM bridge_users WHERE _id = ?", 400).Scan(&gotName)
	if err != nil {
		t.Fatalf("SELECT after UPDATE: %v", err)
	}
	if gotName != "Franklin" {
		t.Errorf("MySQL name after UPDATE: got %q, want %q", gotName, "Franklin")
	}

	// Verify via REST GET.
	got := getBridgeUser(t, ca, adminPort, 400)
	if got.Name != "Franklin" {
		t.Errorf("REST name after UPDATE: got %q, want %q", got.Name, "Franklin")
	}
}

// TestEntityBridge_MySQL_DeleteAndVerify stores an entity via the REST KV API,
// deletes it via MySQL DELETE, then verifies it is absent from both the MySQL
// SELECT result and the REST API.
func TestEntityBridge_MySQL_DeleteAndVerify(t *testing.T) {
	adminPort, mysqlPort, _, ca := startSingleNodeWithSQL(t)

	u := bridgeUser{ID: 500, Name: "Grace", Email: "grace@example.com", Age: 27, Score: 4.8}
	putBridgeUser(t, ca, adminPort, u)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")

	result, err := db.Exec("DELETE FROM bridge_users WHERE _id = ?", 500)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("RowsAffected: %v", err)
	}
	if affected != 1 {
		t.Errorf("DELETE rows affected: got %d, want 1", affected)
	}

	// Verify MySQL SELECT returns no rows.
	var gotID int64
	err = db.QueryRow("SELECT _id FROM bridge_users WHERE _id = ?", 500).Scan(&gotID)
	if err != sql.ErrNoRows {
		t.Errorf("SELECT after DELETE: got err=%v, want sql.ErrNoRows", err)
	}

	// Verify REST GET returns 404.
	status := httpGetStatus(t, ca, adminPort, "system/bridge_users/500")
	if status != 404 {
		t.Errorf("REST GET after DELETE: got status %d, want 404", status)
	}
}

// TestEntityBridge_MySQL_EmptyTable verifies that SELECT * FROM bridge_users
// returns zero rows without error when no entities have been written.
func TestEntityBridge_MySQL_EmptyTable(t *testing.T) {
	_, mysqlPort, _, ca := startSingleNodeWithSQL(t)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")

	rows, err := db.Query("SELECT _id, name FROM bridge_users")
	if err != nil {
		t.Fatalf("SELECT *: %v", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration: %v", err)
	}
	if count != 0 {
		t.Errorf("empty table row count: got %d, want 0", count)
	}
}

// TestEntityBridge_MySQL_FilterByAge stores entities with different age values
// and verifies that a WHERE age > N predicate filters rows correctly.
func TestEntityBridge_MySQL_FilterByAge(t *testing.T) {
	adminPort, mysqlPort, _, ca := startSingleNodeWithSQL(t)

	users := []bridgeUser{
		{ID: 601, Name: "Young", Email: "young@example.com", Age: 20, Score: 1.0},
		{ID: 602, Name: "Mid", Email: "mid@example.com", Age: 30, Score: 2.0},
		{ID: 603, Name: "Old", Email: "old@example.com", Age: 40, Score: 3.0},
	}
	for _, u := range users {
		putBridgeUser(t, ca, adminPort, u)
	}

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")

	rows, err := db.Query("SELECT name FROM bridge_users WHERE age > ?", 25)
	if err != nil {
		t.Fatalf("SELECT WHERE age > 25: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration: %v", err)
	}

	if len(names) != 2 {
		t.Fatalf("filtered row count: got %d, want 2 (names: %v)", len(names), names)
	}

	sort.Strings(names)
	if names[0] != "Mid" || names[1] != "Old" {
		t.Errorf("filtered names: got %v, want [Mid Old]", names)
	}
}

// TestEntityBridge_MySQL_ReplaceInto stores an entity via the REST KV API and
// then uses REPLACE INTO to update it via MySQL. The updated fields are read
// back via the REST API to confirm the replacement was applied.
func TestEntityBridge_MySQL_ReplaceInto(t *testing.T) {
	adminPort, mysqlPort, _, ca := startSingleNodeWithSQL(t)

	original := bridgeUser{ID: 700, Name: "Heidi", Email: "heidi@example.com", Age: 33, Score: 6.0}
	putBridgeUser(t, ca, adminPort, original)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")

	// Columns in alphabetical order after _id: _id, age, email, name, score.
	_, err := db.Exec(
		"REPLACE INTO bridge_users (_id, age, email, name, score) VALUES (?, ?, ?, ?, ?)",
		700, 99, "replaced@example.com", "Replaced", 10.0,
	)
	if err != nil {
		t.Fatalf("REPLACE INTO: %v", err)
	}

	got := getBridgeUser(t, ca, adminPort, 700)

	if got.ID != 700 {
		t.Errorf("id: got %d, want 700", got.ID)
	}
	if got.Name != "Replaced" {
		t.Errorf("name: got %q, want %q", got.Name, "Replaced")
	}
	if got.Email != "replaced@example.com" {
		t.Errorf("email: got %q, want %q", got.Email, "replaced@example.com")
	}
	if got.Age != 99 {
		t.Errorf("age: got %d, want 99", got.Age)
	}
	if got.Score != 10.0 {
		t.Errorf("score: got %f, want %f", got.Score, 10.0)
	}
}

// ---------------------------------------------------------------------------
// PostgreSQL wire protocol — entity bridge tests
// ---------------------------------------------------------------------------

// TestEntityBridge_PgSQL_DAOWriteSQLRead stores an entity via the REST KV API
// and reads it back through the PostgreSQL wire protocol, verifying the
// entity-to-SQL bridge surfaces all fields correctly over pgx.
func TestEntityBridge_PgSQL_DAOWriteSQLRead(t *testing.T) {
	adminPort, _, pgsqlPort, ca := startSingleNodeWithSQL(t)

	want := bridgeUser{ID: 800, Name: "Ivan", Email: "ivan@example.com", Age: 45, Score: 8.8}
	putBridgeUser(t, ca, adminPort, want)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")
	ctx := context.Background()

	var (
		gotName  string
		gotEmail string
		gotAge   int64
		gotScore float64
	)
	err := conn.QueryRow(
		ctx,
		"SELECT name, email, age, score FROM bridge_users WHERE _id = $1",
		int64(800),
	).Scan(&gotName, &gotEmail, &gotAge, &gotScore)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}

	// The WHERE _id = 800 clause implicitly verifies the ID matched.
	_ = want.ID
	if gotName != want.Name {
		t.Errorf("name: got %q, want %q", gotName, want.Name)
	}
	if gotEmail != want.Email {
		t.Errorf("email: got %q, want %q", gotEmail, want.Email)
	}
	if gotAge != want.Age {
		t.Errorf("age: got %d, want %d", gotAge, want.Age)
	}
	if gotScore != want.Score {
		t.Errorf("score: got %f, want %f", gotScore, want.Score)
	}
}

// TestEntityBridge_PgSQL_SQLWriteDAORead inserts an entity via the PostgreSQL
// wire protocol and reads it back through the REST KV API, verifying that pgx
// writes are durably stored and retrievable by the DAO layer.
func TestEntityBridge_PgSQL_SQLWriteDAORead(t *testing.T) {
	adminPort, _, pgsqlPort, ca := startSingleNodeWithSQL(t)

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")
	ctx := context.Background()

	// Columns are in alphabetical order after _id: _id, age, email, name, score.
	_, err := conn.Exec(
		ctx,
		"INSERT INTO bridge_users (_id, age, email, name, score) VALUES ($1, $2, $3, $4, $5)",
		900, 18, "judy@example.com", "Judy", 5.5,
	)
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	got := getBridgeUser(t, ca, adminPort, 900)

	if got.ID != 900 {
		t.Errorf("id: got %d, want 900", got.ID)
	}
	if got.Name != "Judy" {
		t.Errorf("name: got %q, want %q", got.Name, "Judy")
	}
	if got.Email != "judy@example.com" {
		t.Errorf("email: got %q, want %q", got.Email, "judy@example.com")
	}
	if got.Age != 18 {
		t.Errorf("age: got %d, want 18", got.Age)
	}
	if got.Score != 5.5 {
		t.Errorf("score: got %f, want %f", got.Score, 5.5)
	}
}

// TestEntityBridge_PgSQL_SelectAll stores three entities via the REST KV API
// and verifies that SELECT * FROM bridge_users returns all three rows via pgx.
func TestEntityBridge_PgSQL_SelectAll(t *testing.T) {
	adminPort, _, pgsqlPort, ca := startSingleNodeWithSQL(t)

	users := []bridgeUser{
		{ID: 1001, Name: "Kate", Email: "kate@example.com", Age: 24, Score: 7.1},
		{ID: 1002, Name: "Leo", Email: "leo@example.com", Age: 31, Score: 6.3},
		{ID: 1003, Name: "Mia", Email: "mia@example.com", Age: 29, Score: 9.0},
	}
	for _, u := range users {
		putBridgeUser(t, ca, adminPort, u)
	}

	certPath, keyPath := generateClientCert(t, ca, "admin", "system")
	conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")
	ctx := context.Background()

	// Avoid selecting _id directly — BIGINT UNSIGNED uses text OID in the
	// PgSQL wire protocol which pgx cannot decode without a custom type.
	// Instead, select only the string/numeric columns and verify count.
	rows, err := conn.Query(ctx, "SELECT name, email FROM bridge_users")
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	defer rows.Close()

	type row struct {
		name  string
		email string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.name, &r.email); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		got = append(got, r)
	}
	if rows.Err() != nil {
		t.Fatalf("rows iteration: %v", rows.Err())
	}

	if len(got) != len(users) {
		t.Fatalf("row count: got %d, want %d", len(got), len(users))
	}

	sort.Slice(got, func(i, j int) bool { return got[i].name < got[j].name })
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })

	for i, u := range users {
		if got[i].name != u.Name {
			t.Errorf("[%d] name: got %q, want %q", i, got[i].name, u.Name)
		}
		if got[i].email != u.Email {
			t.Errorf("[%d] email: got %q, want %q", i, got[i].email, u.Email)
		}
	}
}

// Compile-time import anchors.
var (
	_ = gomysql.RegisterTLSConfig
	_ = pgx.Connect
	_ = sql.ErrNoRows
)
