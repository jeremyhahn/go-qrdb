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

// sql_query_tenant_scope_test.go exercises the ad-hoc SQL query surface
// (POST /sql/query — the same execution core shared by the gRPC/QUIC/Unix/
// MCP SQLQuery RPCs, pkg/sql/query) end-to-end against a real, freshly
// spawned qrdbd node, proving the fix for the SYSTEM-tenant escalation and
// read-permission/write-capability mismatch documented in
// pkg/sql/query/service.go:
//
//  1. A non-system tenant's ad-hoc SQL query is scoped to that tenant's own
//     namespace — the same composite "<tenant>/<db>/" namespace the MySQL
//     wire protocol server uses (pkg/sql/mysql) — never the shared system
//     keyspace, and never another tenant's data.
//  2. The ad-hoc SQL query surface is read-only: INSERT/UPDATE/DELETE are
//     rejected regardless of the caller's identity, because the served
//     engine has no write-capable tables (see provider.WithReadOnly),
//     independent of whatever permission the caller's role happens to grant.
package integration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
)

// sqlQueryResponse mirrors service.SQLQueryResult for decoding POST
// /sql/query responses in tests.
type sqlQueryResponse struct {
	Columns  []string         `json:"columns"`
	Rows     []map[string]any `json:"rows"`
	RowCount int              `json:"rowCount"`
	Duration string           `json:"duration"`
}

// sqlQueryHTTP posts {database, query} to POST /sql/query using client and
// returns the HTTP status code and raw response body.
func sqlQueryHTTP(t *testing.T, client *http.Client, port int, database, query string) (int, []byte) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/sql/query", port)
	payload, err := json.Marshal(map[string]string{"database": database, "query": query})
	if err != nil {
		t.Fatalf("sqlQueryHTTP: marshal: %v", err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("sqlQueryHTTP: post: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("sqlQueryHTTP: read body: %v", err)
	}
	return resp.StatusCode, body
}

// sqlQueryHTTPOK posts {database, query} and requires a 200 response,
// returning the decoded result.
func sqlQueryHTTPOK(t *testing.T, client *http.Client, port int, database, query string) sqlQueryResponse {
	t.Helper()
	status, body := sqlQueryHTTP(t, client, port, database, query)
	if status != http.StatusOK {
		t.Fatalf("sql query %q: status %d, body: %s", query, status, body)
	}
	var result sqlQueryResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("sql query %q: decode: %v (body: %s)", query, err, body)
	}
	return result
}

// adminPostJSON posts payload (marshaled to JSON) to path using client and
// requires the given expected status, returning the raw response body.
func adminPostJSON(t *testing.T, client *http.Client, port int, path string, payload any, want int) []byte {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d%s", port, path)
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("adminPostJSON %s: marshal: %v", path, err)
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("adminPostJSON %s: post: %v", path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("adminPostJSON %s: read body: %v", path, err)
	}
	if resp.StatusCode != want {
		t.Fatalf("adminPostJSON %s: status %d, want %d, body: %s", path, resp.StatusCode, want, respBody)
	}
	return respBody
}

// provisionTenantWithSQLReader creates a prefix-isolated tenant, a role
// scoped to that tenant granting only permission.KVRange ("kv:range" — the
// same read permission every transport maps the ad-hoc SQL query surface
// to, see pkg/auth/middleware/mapping.go), and a user bound to that role
// whose username matches the CN of the mTLS client certificate the caller
// will authenticate with. All calls are made as the SYSTEM tenant admin
// identity, whose cross-tenant power (the system-tenant rule) is required
// to provision another tenant's IAM records.
func provisionTenantWithSQLReader(t *testing.T, adminClient *http.Client, adminPort int, tenantID, username string) {
	t.Helper()

	adminPostJSON(t, adminClient, adminPort, "/v1/tenants", map[string]string{
		"id":   tenantID,
		"name": tenantID,
	}, http.StatusCreated)

	roleBody := adminPostJSON(t, adminClient, adminPort, "/roles", map[string]any{
		"name":        "sql-reader",
		"tenant_id":   tenantID,
		"permissions": []string{"kv:range"},
	}, http.StatusCreated)
	var role struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(roleBody, &role); err != nil {
		t.Fatalf("provisionTenantWithSQLReader: decode role: %v (body: %s)", err, roleBody)
	}
	if role.ID == "" {
		t.Fatalf("provisionTenantWithSQLReader: role create returned empty id (body: %s)", roleBody)
	}

	adminPostJSON(t, adminClient, adminPort, "/users", map[string]any{
		"username":  username,
		"tenant_id": tenantID,
		"role_ids":  []string{role.ID},
	}, http.StatusCreated)
}

// startSingleNodeWithSQLDefaultDB is a variant of startSingleNodeWithSQL
// that scopes the MySQL wire protocol server to the well-known default
// database ("system") instead of the ad-hoc "qrdb" name. The default
// database resolves for every tenant without requiring an explicit
// per-tenant database creation call (see restServiceRegistry.databaseExists
// / DatabaseServiceRegistry.DefaultName), so it is the one database name
// the ad-hoc REST SQL query surface and the MySQL wire protocol server can
// both address for an arbitrary, freshly-provisioned tenant.
func startSingleNodeWithSQLDefaultDB(t *testing.T) (adminPort, mysqlPort int, ca *testCA) {
	t.Helper()

	bin := buildBinary(t)
	ca = generateTestCA(t)
	raftPorts := allocatePorts(t, 1)
	adminPorts := allocateTCPPorts(t, 1)
	grpcPorts := allocateTCPPorts(t, 1)
	mysqlPorts := allocateTCPPorts(t, 1)
	dataDir := t.TempDir()

	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[0]),
		"--data-dir", dataDir,
		"--sm", "memory",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPorts[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPorts[0]),
		"--grpc-port", fmt.Sprintf("%d", grpcPorts[0]),
		"--enable-mysql",
		"--mysql-port", fmt.Sprintf("%d", mysqlPorts[0]),
		"--sql-database", "system",
	}
	args = append(args, ca.tlsFlags(t)...)

	startNode(t, bin, args, "[sql-tenant-scope-node] ")
	waitForHealthy(t, dataDir, 30*time.Second)
	waitForAdmin(t, ca.Pool, adminPorts[0], 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{adminPorts[0]}, 60*time.Second)
	warmupWrite(t, ca, adminPorts[0], 15*time.Second)

	return adminPorts[0], mysqlPorts[0], ca
}

// waitForMySQLDefaultDB opens a MySQL connection scoped to the "system"
// database (see startSingleNodeWithSQLDefaultDB) with mTLS and retries
// until the server is ready or the timeout expires.
func waitForMySQLDefaultDB(t *testing.T, ca *testCA, certPath, keyPath string, port int, user string) *sql.DB {
	t.Helper()

	cert := loadTLSCert(t, certPath, keyPath)
	tlsCfg := &tls.Config{
		RootCAs:      ca.Pool,
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	tlsName := fmt.Sprintf("sql-tenant-scope-%d-%d", port, time.Now().UnixNano())
	if err := gomysql.RegisterTLSConfig(tlsName, tlsCfg); err != nil {
		t.Fatalf("waitForMySQLDefaultDB: RegisterTLSConfig: %v", err)
	}
	t.Cleanup(func() {
		gomysql.DeregisterTLSConfig(tlsName)
	})

	dsn := fmt.Sprintf("%s@tcp(127.0.0.1:%d)/system?tls=%s", user, port, tlsName)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		db.SetConnMaxLifetime(30 * time.Second)
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(2)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = db.PingContext(ctx)
		cancel()
		if err == nil {
			t.Cleanup(func() { db.Close() })
			return db
		}
		db.Close()
		time.Sleep(200 * time.Millisecond)
	}

	t.Fatalf("waitForMySQLDefaultDB: timed out after 30s waiting for MySQL on port %d", port)
	return nil
}

// TestSQL_REST_TenantScoping_MatchesWireProtocolNamespace proves property
// (a): a non-system tenant's ad-hoc SQL query (POST /sql/query) is scoped
// to that tenant's own composite namespace, never the shared system
// keyspace and never another tenant's data. Rows are seeded through the
// MySQL wire protocol server (already namespace-correct — see
// TestSQL_MultiTenant_Isolation) under each tenant's own mTLS identity, and
// read back through the REST ad-hoc SQL surface using the SAME identity, so
// a passing test proves the two surfaces agree on where a given tenant's
// data lives.
func TestSQL_REST_TenantScoping_MatchesWireProtocolNamespace(t *testing.T) {
	adminPort, mysqlPort, ca := startSingleNodeWithSQLDefaultDB(t)

	adminClient := mtlsHTTPClient(t, ca, 10*time.Second)

	certA, keyA := generateClientCert(t, ca, "user-a", "tenant-a")
	certB, keyB := generateClientCert(t, ca, "user-b", "tenant-b")

	// The REST ad-hoc SQL query surface enforces RBAC (permission.KVRange)
	// before the handler runs, unlike the MySQL wire protocol server, which
	// only authenticates the connection (see pkg/sql/mysql/auth.go — it has
	// no statement-level authorization gate). Provision each tenant with a
	// role granting exactly that read permission so the REST calls below
	// are authorized.
	provisionTenantWithSQLReader(t, adminClient, adminPort, "tenant-a", "user-a")
	provisionTenantWithSQLReader(t, adminClient, adminPort, "tenant-b", "user-b")

	// Seed each tenant's row through the MySQL wire protocol server, which
	// scopes writes via the composite (tenant, database) namespace using
	// the client cert's OU as the tenant.
	dbA := waitForMySQLDefaultDB(t, ca, certA, keyA, mysqlPort, "user-a")
	if _, err := dbA.Exec("INSERT INTO kv (`key`, `value`) VALUES (?, ?)", "shared-name", []byte("tenant-a-value")); err != nil {
		t.Fatalf("tenant-a wire INSERT: %v", err)
	}

	dbB := waitForMySQLDefaultDB(t, ca, certB, keyB, mysqlPort, "user-b")
	if _, err := dbB.Exec("INSERT INTO kv (`key`, `value`) VALUES (?, ?)", "shared-name", []byte("tenant-b-value")); err != nil {
		t.Fatalf("tenant-b wire INSERT: %v", err)
	}

	restA := mtlsHTTPClientFromFiles(t, ca, certA, keyA, 10*time.Second)
	restB := mtlsHTTPClientFromFiles(t, ca, certB, keyB, 10*time.Second)

	// Each tenant's ad-hoc SELECT over REST must see exactly its own row —
	// written under the same identity via the wire protocol — and no other
	// tenant's row.
	resultA := sqlQueryHTTPOK(t, restA, adminPort, "system", "SELECT `value` FROM kv WHERE `key` = 'shared-name'")
	if resultA.RowCount != 1 || fmt.Sprint(resultA.Rows[0]["value"]) != "tenant-a-value" {
		t.Fatalf("tenant-a REST SELECT = %+v, want the tenant-a-owned row", resultA)
	}

	resultB := sqlQueryHTTPOK(t, restB, adminPort, "system", "SELECT `value` FROM kv WHERE `key` = 'shared-name'")
	if resultB.RowCount != 1 || fmt.Sprint(resultB.Rows[0]["value"]) != "tenant-b-value" {
		t.Fatalf("tenant-b REST SELECT = %+v, want the tenant-b-owned row", resultB)
	}

	// A whole-table scan for each tenant must return exactly their own row
	// — proves the REST ad-hoc query does not leak into or merge with
	// another tenant's namespace.
	scanA := sqlQueryHTTPOK(t, restA, adminPort, "system", "SELECT * FROM kv")
	if scanA.RowCount != 1 {
		t.Fatalf("tenant-a REST SELECT * rowCount = %d, want 1 (no cross-tenant leak)", scanA.RowCount)
	}

	scanB := sqlQueryHTTPOK(t, restB, adminPort, "system", "SELECT * FROM kv")
	if scanB.RowCount != 1 {
		t.Fatalf("tenant-b REST SELECT * rowCount = %d, want 1 (no cross-tenant leak)", scanB.RowCount)
	}

	// The SYSTEM tenant (the admin cert, OU=system) never wrote to "qrdb"
	// in this test, so its own ad-hoc SELECT must see zero rows — proving
	// tenant-a's and tenant-b's writes did not land in (or leak from) the
	// shared system keyspace, which is exactly the SYSTEM-tenant
	// escalation this fix closes.
	scanSystem := sqlQueryHTTPOK(t, adminClient, adminPort, "system", "SELECT * FROM kv")
	if scanSystem.RowCount != 0 {
		t.Fatalf("system-tenant REST SELECT * rowCount = %d, want 0 (SYSTEM-tenant escalation regression)", scanSystem.RowCount)
	}
}

// TestSQL_REST_ReadOnly_RejectsWriteRegardlessOfCaller proves property (b):
// every transport maps the ad-hoc SQL query surface (SQLQuery / POST
// /sql/query) to permission.KVRange, a read permission (see
// pkg/auth/middleware/mapping.go), so a write statement must be rejected
// even for the admin identity — the engine has no write-capable tables at
// all, independent of the caller's granted permission.
func TestSQL_REST_ReadOnly_RejectsWriteRegardlessOfCaller(t *testing.T) {
	adminPort, _, _, ca := startSingleNodeWithSQL(t)

	adminClient := mtlsHTTPClient(t, ca, 10*time.Second)

	// A positive control: SELECT must still work.
	sel := sqlQueryHTTPOK(t, adminClient, adminPort, "qrdb", "SELECT 1 AS ok")
	if sel.RowCount != 1 {
		t.Fatalf("control SELECT rowCount = %d, want 1", sel.RowCount)
	}

	writes := []struct {
		name  string
		query string
	}{
		{"insert", "INSERT INTO kv (`key`, `value`) VALUES ('should-fail', 'x')"},
		{"update", "UPDATE kv SET `value` = 'x' WHERE `key` = 'should-fail'"},
		{"delete", "DELETE FROM kv WHERE `key` = 'should-fail'"},
	}
	for _, w := range writes {
		t.Run(w.name, func(t *testing.T) {
			status, body := sqlQueryHTTP(t, adminClient, adminPort, "qrdb", w.query)
			if status != http.StatusBadRequest {
				t.Fatalf("%s: status = %d, want %d (read-only engine must reject writes); body: %s",
					w.name, status, http.StatusBadRequest, body)
			}
		})
	}

	// No row was ever created despite the rejected INSERT.
	after := sqlQueryHTTPOK(t, adminClient, adminPort, "qrdb", "SELECT * FROM kv WHERE `key` = 'should-fail'")
	if after.RowCount != 0 {
		t.Fatalf("rowCount after rejected writes = %d, want 0 (write escalated past the read-only engine)", after.RowCount)
	}
}
