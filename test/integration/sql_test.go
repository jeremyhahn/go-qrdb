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
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"testing"
	"time"

	"net/http"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// startSingleNodeWithSQL starts a single-node QRDB instance with MySQL and
// PostgreSQL wire protocol servers enabled. It returns the admin, MySQL, and
// PostgreSQL ports along with the test CA used for mTLS.
//
// The state machine is "mvcc", not "memory": qrdbd's boot-time role/user
// stores now require a kvstore.GuardedBatchKVStore-capable state machine
// (go-qrdb#555), which "memory" does not implement, so a node started with it
// refuses at first boot and never reaches healthy — before the relational
// stack's own "memory" incompatibility (see startSingleNodeWithSQLMVCC) would
// even become visible.
func startSingleNodeWithSQL(t *testing.T) (adminPort, mysqlPort, pgsqlPort int, ca *testCA) {
	t.Helper()
	return startSingleNodeSQL(t, "mvcc")
}

// startSingleNodeSQL boots the single-node SQL fixture over the named state
// machine. See startSingleNodeWithSQL and startSingleNodeWithSQLMVCC.
func startSingleNodeSQL(t *testing.T, stateMachine string) (adminPort, mysqlPort, pgsqlPort int, ca *testCA) {
	t.Helper()

	bin := buildBinary(t)
	ca = generateTestCA(t)
	raftPorts := allocatePorts(t, 1)
	adminPorts := allocateTCPPorts(t, 1)
	grpcPorts := allocateTCPPorts(t, 1)
	mysqlPorts := allocateTCPPorts(t, 1)
	pgsqlPorts := allocateTCPPorts(t, 1)
	dataDir := t.TempDir()

	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[0]),
		"--data-dir", dataDir,
		"--sm", stateMachine,
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPorts[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPorts[0]),
		"--grpc-port", fmt.Sprintf("%d", grpcPorts[0]),
		"--enable-mysql",
		"--mysql-port", fmt.Sprintf("%d", mysqlPorts[0]),
		"--enable-pgsql",
		"--pgsql-port", fmt.Sprintf("%d", pgsqlPorts[0]),
		"--sql-database", "qrdb",
	}
	args = append(args, ca.tlsFlags(t)...)

	startNode(t, bin, args, "[sql-node] ")
	bootstrapNodes(t, ca, adminPorts[0])
	waitForHealthy(t, dataDir, 30*time.Second)
	waitForAdmin(t, ca.Pool, adminPorts[0], 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{adminPorts[0]}, 60*time.Second)
	warmupWrite(t, ca, adminPorts[0], 15*time.Second)

	return adminPorts[0], mysqlPorts[0], pgsqlPorts[0], ca
}

// startSingleNodeWithSQLMVCC is now identical to startSingleNodeWithSQL: both
// boot over "mvcc", since go-qrdb#555's boot-time capability probe made
// "memory" unable to complete first boot at all, not merely unable to run
// CREATE TABLE. The name is kept as a distinct, explicit call for tests that
// specifically depend on the MVCC state machine's relational guarantees —
// the table catalog compare-and-swap, interactive-transaction commit, and
// Raft-committed AUTO_INCREMENT sequence described below — so that intent
// survives even though the underlying call is now the same.
func startSingleNodeWithSQLMVCC(t *testing.T) (adminPort, mysqlPort, pgsqlPort int, ca *testCA) {
	t.Helper()
	return startSingleNodeSQL(t, "mvcc")
}

// ---------------------------------------------------------------------------
// Multi-node SQL cluster harness
// ---------------------------------------------------------------------------
//
// startSingleNodeWithSQL above proves the SQL surface works; it cannot prove
// anything about the distributed tier, because there is only one replica and
// one state machine. sqlCluster below is its 3-node counterpart, and it is the
// single harness every sql_*_test.go file that needs more than one replica
// uses. It deliberately reuses the existing primitives rather than
// reimplementing them: qrdbdharness's reserved-window port allocator
// (allocatePorts/allocateTCPPorts), the shared CA (generateTestCA), node
// spawning (startNode), readiness (waitForHealthy/waitForAdmin/
// waitForLeaderViaAdmin/warmupWrite) and MySQL dialling (waitForMySQL).

// sqlClusterNode is one replica of a sqlCluster.
type sqlClusterNode struct {
	replicaID int
	adminPort int
	mysqlPort int
	dataDir   string
	cmd       *exec.Cmd

	// dead is set once the test has deliberately killed this replica, so the
	// cluster helpers stop polling it and stop offering it to callers.
	dead bool
}

// sqlCluster is a 3-node qrdbd cluster with the MySQL wire protocol server
// enabled on every replica. Every node serves the same Raft shard, so a write
// accepted anywhere is committed through the same log: quicraft's Host.Propose
// forwards a proposal to the shard leader whenever a leader is known, which is
// what makes "write to any node" meaningful here rather than a NotLeader
// error.
type sqlCluster struct {
	ca         *testCA
	nodes      []*sqlClusterNode
	clientCert string
	clientKey  string
}

// startThreeNodeClusterWithSQL boots a 3-node cluster whose replicas each
// serve the MySQL wire protocol, and blocks until every node is healthy, a
// leader has been elected, and the ReadIndex pipeline is warm.
//
// The state machine is "mvcc", and that is load-bearing rather than
// incidental: the whole relational stack is built on MVCC revisions -- the
// table catalog is a compare-and-swap keyed off a revision, an interactive
// transaction commits its buffered write-set as one revision-guarded
// compare-and-swap, and AUTO_INCREMENT allocates from a Raft-committed
// sequence the same way. Booted with the default "pebble" SM, the very first
// CREATE TABLE fails with `statemachine: mvcc not configured`. Booted with
// "memory" or "concurrent", the node never gets that far: go-qrdb#555's
// boot-time capability probe refuses first boot outright, since neither
// implements the transaction plane atomic role/user creation requires —
// which is why startSingleNodeWithSQL no longer uses "memory" either.
func startThreeNodeClusterWithSQL(t *testing.T) *sqlCluster {
	t.Helper()

	bin := buildBinary(t)
	ca := generateTestCA(t)
	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	mysqlPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(raftPorts)
	nodeTLS, memberIDs := ca.clusterNodeTLS(t, len(raftPorts))

	c := &sqlCluster{ca: ca}
	for i := range 3 {
		dataDir := t.TempDir()
		args := []string{
			"start",
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			"--data-dir", dataDir,
			"--sm", "mvcc",
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIDs,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--enable-mysql",
			"--mysql-port", fmt.Sprintf("%d", mysqlPorts[i]),
			"--sql-database", sqlClusterDatabase,
		}
		args = append(args, nodeTLS[i]...)

		cmd := startNode(t, bin, args, fmt.Sprintf("[sqlcluster-node%d] ", i+1))
		c.nodes = append(c.nodes, &sqlClusterNode{
			replicaID: i + 1,
			adminPort: adminPorts[i],
			mysqlPort: mysqlPorts[i],
			dataDir:   dataDir,
			cmd:       cmd,
		})
	}

	bootstrapNodes(t, ca, adminPorts...)
	for _, n := range c.nodes {
		waitForHealthy(t, n.dataDir, 60*time.Second)
		waitForAdmin(t, ca.Pool, n.adminPort, 90*time.Second)
	}
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	warmupWrite(t, ca, leaderPort, 15*time.Second)

	c.clientCert, c.clientKey = ca.boundAdminCert(t, t.TempDir())
	return c
}

// sqlClusterDatabase is the --sql-database every sqlCluster node serves. It
// matches the DSN waitForMySQL builds ("/qrdb").
const sqlClusterDatabase = "qrdb"

// liveAdminPorts returns the admin ports of every replica the test has not
// killed.
func (c *sqlCluster) liveAdminPorts() []int {
	var ports []int
	for _, n := range c.nodes {
		if !n.dead {
			ports = append(ports, n.adminPort)
		}
	}
	return ports
}

// leaderIndex returns the index of the replica currently leading shard 1,
// polling until one is found. Killed replicas are never polled and never
// returned.
func (c *sqlCluster) leaderIndex(t *testing.T, timeout time.Duration) int {
	t.Helper()
	leaderPort := waitForLeaderViaAdmin(t, c.ca, c.liveAdminPorts(), timeout)
	for i, n := range c.nodes {
		if n.adminPort == leaderPort {
			return i
		}
	}
	t.Fatalf("leaderIndex: leader admin port %d is not a cluster member", leaderPort)
	return -1
}

// followerIndexes returns the indexes of every live replica that is not idx.
func (c *sqlCluster) followerIndexes(idx int) []int {
	var out []int
	for i, n := range c.nodes {
		if i != idx && !n.dead {
			out = append(out, i)
		}
	}
	return out
}

// liveIndexes returns the indexes of every replica the test has not killed.
func (c *sqlCluster) liveIndexes() []int {
	var out []int
	for i, n := range c.nodes {
		if !n.dead {
			out = append(out, i)
		}
	}
	return out
}

// mysql opens a pooled MySQL connection to replica idx as admin@system. The
// pool is closed on test cleanup by waitForMySQL.
func (c *sqlCluster) mysql(t *testing.T, idx int) *sql.DB {
	t.Helper()
	return waitForMySQL(t, c.ca, c.clientCert, c.clientKey, c.nodes[idx].mysqlPort, adminUser)
}

// kill terminates replica idx immediately (SIGKILL, no graceful shutdown) and
// marks it dead so the cluster helpers stop considering it. This is the
// failure the AUTO_INCREMENT suite injects: a leader that vanishes mid-write
// without draining, forcing a real election.
func (c *sqlCluster) kill(t *testing.T, idx int) {
	t.Helper()
	n := c.nodes[idx]
	if n.dead {
		return
	}
	if n.cmd.Process != nil {
		if err := n.cmd.Process.Kill(); err != nil {
			t.Fatalf("kill replica %d: %v", n.replicaID, err)
		}
		n.cmd.Wait()
	}
	n.dead = true
}

// generateClientCert creates a client certificate signed by the test CA with
// the specified CN (username) and OU (tenantID). The cert and key are written
// to temporary files and their paths are returned.
func generateClientCert(t *testing.T, ca *testCA, cn, ou string) (certPath, keyPath string) {
	t.Helper()
	kp := ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(cn, ou))
	return kp.CertPath, kp.KeyPath
}

// loadTLSCert loads a TLS certificate and key from PEM files on disk.
func loadTLSCert(t *testing.T, certPath, keyPath string) tls.Certificate {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("loadTLSCert: %v", err)
	}
	return cert
}

// waitForMySQL opens a MySQL connection with mTLS and retries until the
// server is ready or the timeout expires. The returned *sql.DB is registered
// for cleanup via t.Cleanup.
func waitForMySQL(t *testing.T, ca *testCA, certPath, keyPath string, port int, user string) *sql.DB {
	t.Helper()

	cert := loadTLSCert(t, certPath, keyPath)
	tlsCfg := &tls.Config{
		RootCAs:      ca.Pool,
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	// Register a unique TLS config name per test to avoid collisions.
	tlsName := fmt.Sprintf("sql-test-%d-%d", port, time.Now().UnixNano())
	if err := gomysql.RegisterTLSConfig(tlsName, tlsCfg); err != nil {
		t.Fatalf("waitForMySQL: RegisterTLSConfig: %v", err)
	}
	t.Cleanup(func() {
		gomysql.DeregisterTLSConfig(tlsName)
	})

	dsn := fmt.Sprintf("%s@tcp(127.0.0.1:%d)/qrdb?tls=%s", user, port, tlsName)

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

	t.Fatalf("waitForMySQL: timed out after 30s waiting for MySQL on port %d", port)
	return nil
}

// waitForPgSQL opens a PostgreSQL connection with mTLS and retries until the
// server is ready or the timeout expires. The returned *pgx.Conn is registered
// for cleanup via t.Cleanup.
func waitForPgSQL(t *testing.T, ca *testCA, certPath, keyPath string, port int, user string) *pgx.Conn {
	t.Helper()

	cert := loadTLSCert(t, certPath, keyPath)
	tlsCfg := &tls.Config{
		RootCAs:      ca.Pool,
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		ServerName:   "localhost",
	}

	connStr := fmt.Sprintf("host=127.0.0.1 port=%d dbname=qrdb user=%s sslmode=require", port, user)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		config, err := pgx.ParseConfig(connStr)
		if err != nil {
			t.Fatalf("waitForPgSQL: ParseConfig: %v", err)
		}
		config.TLSConfig = tlsCfg

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		conn, err := pgx.ConnectConfig(ctx, config)
		cancel()
		if err == nil {
			t.Cleanup(func() {
				conn.Close(context.Background())
			})
			return conn
		}
		time.Sleep(200 * time.Millisecond)
	}

	t.Fatalf("waitForPgSQL: timed out after 30s waiting for PgSQL on port %d", port)
	return nil
}

// ---------------------------------------------------------------------------
// MySQL wire protocol tests
// ---------------------------------------------------------------------------

func TestSQL_MySQL_InsertAndSelect(t *testing.T) {
	_, mysqlPort, _, ca := startSingleNodeWithSQL(t)

	certPath, keyPath := ca.boundAdminCert(t, t.TempDir())
	db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")

	// INSERT a key-value pair.
	_, err := db.Exec("INSERT INTO kv (`key`, `value`) VALUES (?, ?)", "greeting", []byte("hello world"))
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// SELECT it back and verify.
	var key string
	var value []byte
	err = db.QueryRow("SELECT `key`, `value` FROM kv WHERE `key` = ?", "greeting").Scan(&key, &value)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if key != "greeting" {
		t.Errorf("key: got %q, want %q", key, "greeting")
	}
	if string(value) != "hello world" {
		t.Errorf("value: got %q, want %q", string(value), "hello world")
	}
}

func TestSQL_MySQL_UpdateAndDelete(t *testing.T) {
	_, mysqlPort, _, ca := startSingleNodeWithSQL(t)

	certPath, keyPath := ca.boundAdminCert(t, t.TempDir())
	db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")

	// INSERT initial value.
	_, err := db.Exec("INSERT INTO kv (`key`, `value`) VALUES (?, ?)", "mutable", []byte("version-1"))
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// UPDATE value.
	result, err := db.Exec("UPDATE kv SET `value` = ? WHERE `key` = ?", []byte("version-2"), "mutable")
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		t.Fatalf("RowsAffected: %v", err)
	}
	if affected != 1 {
		t.Errorf("UPDATE rows affected: got %d, want 1", affected)
	}

	// Verify updated value.
	var value []byte
	err = db.QueryRow("SELECT `value` FROM kv WHERE `key` = ?", "mutable").Scan(&value)
	if err != nil {
		t.Fatalf("SELECT after UPDATE: %v", err)
	}
	if string(value) != "version-2" {
		t.Errorf("value after UPDATE: got %q, want %q", string(value), "version-2")
	}

	// DELETE the key.
	result, err = db.Exec("DELETE FROM kv WHERE `key` = ?", "mutable")
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	affected, err = result.RowsAffected()
	if err != nil {
		t.Fatalf("DELETE RowsAffected: %v", err)
	}
	if affected != 1 {
		t.Errorf("DELETE rows affected: got %d, want 1", affected)
	}

	// Verify key is gone.
	err = db.QueryRow("SELECT `value` FROM kv WHERE `key` = ?", "mutable").Scan(&value)
	if err != sql.ErrNoRows {
		t.Errorf("SELECT after DELETE: got err=%v, want sql.ErrNoRows", err)
	}
}

func TestSQL_MySQL_SelectAll(t *testing.T) {
	_, mysqlPort, _, ca := startSingleNodeWithSQL(t)

	certPath, keyPath := ca.boundAdminCert(t, t.TempDir())
	db := waitForMySQL(t, ca, certPath, keyPath, mysqlPort, "admin")

	// INSERT multiple keys.
	keys := []string{"alpha", "bravo", "charlie"}
	for i, k := range keys {
		_, err := db.Exec("INSERT INTO kv (`key`, `value`) VALUES (?, ?)", k, []byte(fmt.Sprintf("val-%d", i)))
		if err != nil {
			t.Fatalf("INSERT %s: %v", k, err)
		}
	}

	// SELECT * FROM kv and verify count.
	rows, err := db.Query("SELECT `key`, `value` FROM kv")
	if err != nil {
		t.Fatalf("SELECT *: %v", err)
	}
	defer rows.Close()

	var gotKeys []string
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		gotKeys = append(gotKeys, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows iteration: %v", err)
	}

	if len(gotKeys) != len(keys) {
		t.Fatalf("SELECT * count: got %d, want %d (keys: %v)", len(gotKeys), len(keys), gotKeys)
	}

	// Verify all expected keys are present.
	sort.Strings(gotKeys)
	sort.Strings(keys)
	for i, k := range keys {
		if gotKeys[i] != k {
			t.Errorf("key[%d]: got %q, want %q", i, gotKeys[i], k)
		}
	}
}

// ---------------------------------------------------------------------------
// PostgreSQL wire protocol tests
// ---------------------------------------------------------------------------

func TestSQL_PgSQL_InsertAndSelect(t *testing.T) {
	_, _, pgsqlPort, ca := startSingleNodeWithSQL(t)

	certPath, keyPath := ca.boundAdminCert(t, t.TempDir())
	conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")
	ctx := context.Background()

	// INSERT a key-value pair.
	_, err := conn.Exec(ctx, "INSERT INTO kv (key, value) VALUES ($1, $2)", "greeting", []byte("hello world"))
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// SELECT it back and verify.
	var key string
	var value []byte
	err = conn.QueryRow(ctx, "SELECT key, value FROM kv WHERE key = $1", "greeting").Scan(&key, &value)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if key != "greeting" {
		t.Errorf("key: got %q, want %q", key, "greeting")
	}
	if string(value) != "hello world" {
		t.Errorf("value: got %q, want %q", string(value), "hello world")
	}
}

func TestSQL_PgSQL_UpdateAndDelete(t *testing.T) {
	_, _, pgsqlPort, ca := startSingleNodeWithSQL(t)

	certPath, keyPath := ca.boundAdminCert(t, t.TempDir())
	conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")
	ctx := context.Background()

	// INSERT initial value.
	_, err := conn.Exec(ctx, "INSERT INTO kv (key, value) VALUES ($1, $2)", "mutable", []byte("version-1"))
	if err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	// UPDATE value.
	cmdTag, err := conn.Exec(ctx, "UPDATE kv SET value = $1 WHERE key = $2", []byte("version-2"), "mutable")
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	if cmdTag.RowsAffected() != 1 {
		t.Errorf("UPDATE rows affected: got %d, want 1", cmdTag.RowsAffected())
	}

	// Verify updated value.
	var value []byte
	err = conn.QueryRow(ctx, "SELECT value FROM kv WHERE key = $1", "mutable").Scan(&value)
	if err != nil {
		t.Fatalf("SELECT after UPDATE: %v", err)
	}
	if string(value) != "version-2" {
		t.Errorf("value after UPDATE: got %q, want %q", string(value), "version-2")
	}

	// DELETE the key.
	cmdTag, err = conn.Exec(ctx, "DELETE FROM kv WHERE key = $1", "mutable")
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if cmdTag.RowsAffected() != 1 {
		t.Errorf("DELETE rows affected: got %d, want 1", cmdTag.RowsAffected())
	}

	// Verify key is gone.
	err = conn.QueryRow(ctx, "SELECT value FROM kv WHERE key = $1", "mutable").Scan(&value)
	if err == nil {
		t.Error("SELECT after DELETE: expected error, got nil")
	}
}

func TestSQL_PgSQL_SelectAll(t *testing.T) {
	_, _, pgsqlPort, ca := startSingleNodeWithSQL(t)

	certPath, keyPath := ca.boundAdminCert(t, t.TempDir())
	conn := waitForPgSQL(t, ca, certPath, keyPath, pgsqlPort, "admin")
	ctx := context.Background()

	// INSERT multiple keys.
	keys := []string{"alpha", "bravo", "charlie"}
	for i, k := range keys {
		_, err := conn.Exec(ctx, "INSERT INTO kv (key, value) VALUES ($1, $2)", k, []byte(fmt.Sprintf("val-%d", i)))
		if err != nil {
			t.Fatalf("INSERT %s: %v", k, err)
		}
	}

	// SELECT * FROM kv and verify count.
	rows, err := conn.Query(ctx, "SELECT key, value FROM kv")
	if err != nil {
		t.Fatalf("SELECT *: %v", err)
	}
	defer rows.Close()

	var gotKeys []string
	for rows.Next() {
		var k string
		var v []byte
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		gotKeys = append(gotKeys, k)
	}
	if rows.Err() != nil {
		t.Fatalf("rows iteration: %v", rows.Err())
	}

	if len(gotKeys) != len(keys) {
		t.Fatalf("SELECT * count: got %d, want %d (keys: %v)", len(gotKeys), len(keys), gotKeys)
	}

	// Verify all expected keys are present.
	sort.Strings(gotKeys)
	sort.Strings(keys)
	for i, k := range keys {
		if gotKeys[i] != k {
			t.Errorf("key[%d]: got %q, want %q", i, gotKeys[i], k)
		}
	}
}

// ---------------------------------------------------------------------------
// Multi-tenant isolation
// ---------------------------------------------------------------------------

func TestSQL_MultiTenant_Isolation(t *testing.T) {
	adminPort, mysqlPort, _, ca := startSingleNodeWithSQL(t)

	// Provision both tenants end-to-end first, exactly as production
	// would: the data plane requires the tenant named by the certificate
	// to EXIST (an unknown tenant is tenant-not-found, never an implicit
	// namespace), the SQL default database must be CREATED for a tenant
	// before its wire connections resolve it, and both of those need the
	// tenant's own identity to carry a role granting the database and KV
	// permissions. This mirrors TestIsolationRaft_EndToEnd's provisioning
	// phase for the prefix-isolation tier.
	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)
	// Two client certs with different tenant IDs (OU field), each bound to
	// its user as it is created.
	certA, keyA := generateClientCert(t, ca, "user-a", "tenant-a")
	certB, keyB := generateClientCert(t, ca, "user-b", "tenant-b")
	for _, tc := range []struct{ tenantID, username, certPath string }{
		{"tenant-a", "user-a", certA},
		{"tenant-b", "user-b", certB},
	} {
		body, status := iraftAPIPost(t, sysClient,
			fmt.Sprintf("https://127.0.0.1:%d/v1/tenants", adminPort),
			map[string]interface{}{
				"id":             tc.tenantID,
				"name":           tc.tenantID,
				"isolation_type": "prefix",
			},
		)
		if status != http.StatusCreated && status != http.StatusOK {
			t.Fatalf("create tenant %s: status %d, body: %s", tc.tenantID, status, body)
		}

		roleBody, roleStatus := iraftAPIPost(t, sysClient,
			fmt.Sprintf("https://127.0.0.1:%d/roles", adminPort),
			map[string]interface{}{
				"name":      "sql-user",
				"tenant_id": tc.tenantID,
				"permissions": []string{
					"databases:create", "databases:read", "databases:list",
					"kv:put", "kv:get", "kv:range", "kv:delete",
				},
			},
		)
		if roleStatus != http.StatusCreated && roleStatus != http.StatusOK {
			t.Fatalf("create role for %s: status %d, body: %s", tc.tenantID, roleStatus, roleBody)
		}
		var roleResp struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(roleBody, &roleResp); err != nil || roleResp.ID == "" {
			t.Fatalf("create role for %s: could not decode role ID from: %s", tc.tenantID, roleBody)
		}

		createBoundUser(t, sysClient, adminPort,
			map[string]interface{}{
				"username":  tc.username,
				"email":     tc.username + "@" + tc.tenantID + ".example.com",
				"tenant_id": tc.tenantID,
				"role_ids":  []string{roleResp.ID},
			}, tc.certPath)
	}

	dbA := waitForMySQL(t, ca, certA, keyA, mysqlPort, "user-a")
	dbB := waitForMySQL(t, ca, certB, keyB, mysqlPort, "user-b")

	// Tenant A inserts a key.
	_, err := dbA.Exec("INSERT INTO kv (`key`, `value`) VALUES (?, ?)", "shared-name", []byte("tenant-a-value"))
	if err != nil {
		t.Fatalf("tenant-a INSERT: %v", err)
	}

	// Tenant B inserts a key with the same logical name.
	_, err = dbB.Exec("INSERT INTO kv (`key`, `value`) VALUES (?, ?)", "shared-name", []byte("tenant-b-value"))
	if err != nil {
		t.Fatalf("tenant-b INSERT: %v", err)
	}

	// Tenant A should only see their own value.
	var valueA []byte
	err = dbA.QueryRow("SELECT `value` FROM kv WHERE `key` = ?", "shared-name").Scan(&valueA)
	if err != nil {
		t.Fatalf("tenant-a SELECT: %v", err)
	}
	if string(valueA) != "tenant-a-value" {
		t.Errorf("tenant-a value: got %q, want %q", string(valueA), "tenant-a-value")
	}

	// Tenant B should only see their own value.
	var valueB []byte
	err = dbB.QueryRow("SELECT `value` FROM kv WHERE `key` = ?", "shared-name").Scan(&valueB)
	if err != nil {
		t.Fatalf("tenant-b SELECT: %v", err)
	}
	if string(valueB) != "tenant-b-value" {
		t.Errorf("tenant-b value: got %q, want %q", string(valueB), "tenant-b-value")
	}

	// Tenant A SELECT * should return only tenant A keys.
	rowsA, err := dbA.Query("SELECT `key`, `value` FROM kv")
	if err != nil {
		t.Fatalf("tenant-a SELECT *: %v", err)
	}
	defer rowsA.Close()

	var countA int
	for rowsA.Next() {
		var k string
		var v []byte
		if err := rowsA.Scan(&k, &v); err != nil {
			t.Fatalf("tenant-a Scan: %v", err)
		}
		countA++
		if string(v) != "tenant-a-value" {
			t.Errorf("tenant-a unexpected value: got %q", string(v))
		}
	}
	if err := rowsA.Err(); err != nil {
		t.Fatalf("tenant-a rows iteration: %v", err)
	}
	if countA != 1 {
		t.Errorf("tenant-a row count: got %d, want 1", countA)
	}

	// Tenant B SELECT * should return only tenant B keys.
	rowsB, err := dbB.Query("SELECT `key`, `value` FROM kv")
	if err != nil {
		t.Fatalf("tenant-b SELECT *: %v", err)
	}
	defer rowsB.Close()

	var countB int
	for rowsB.Next() {
		var k string
		var v []byte
		if err := rowsB.Scan(&k, &v); err != nil {
			t.Fatalf("tenant-b Scan: %v", err)
		}
		countB++
		if string(v) != "tenant-b-value" {
			t.Errorf("tenant-b unexpected value: got %q", string(v))
		}
	}
	if err := rowsB.Err(); err != nil {
		t.Fatalf("tenant-b rows iteration: %v", err)
	}
	if countB != 1 {
		t.Errorf("tenant-b row count: got %d, want 1", countB)
	}
}

// ---------------------------------------------------------------------------
// Authentication enforcement
// ---------------------------------------------------------------------------

func TestSQL_MySQL_NoClientCert_Rejected(t *testing.T) {
	_, mysqlPort, _, ca := startSingleNodeWithSQL(t)

	// Configure TLS with CA trust but no client certificate.
	tlsCfg := &tls.Config{
		RootCAs:    ca.Pool,
		MinVersion: tls.VersionTLS13,
	}

	tlsName := fmt.Sprintf("no-cert-%d", time.Now().UnixNano())
	if err := gomysql.RegisterTLSConfig(tlsName, tlsCfg); err != nil {
		t.Fatalf("RegisterTLSConfig: %v", err)
	}
	t.Cleanup(func() {
		gomysql.DeregisterTLSConfig(tlsName)
	})

	dsn := fmt.Sprintf("anonymous@tcp(127.0.0.1:%d)/qrdb?tls=%s", mysqlPort, tlsName)

	// Retry to account for server startup delay.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = db.PingContext(ctx)
		cancel()
		db.Close()

		if err != nil {
			// Connection was rejected as expected. The server requires
			// a client certificate for mTLS authentication.
			t.Logf("connection without client cert rejected as expected: %v", err)
			return
		}

		// If Ping succeeded, the server is not enforcing mTLS properly.
		// Try a query to see if it fails at the authorization level.
		db2, _ := sql.Open("mysql", dsn)
		ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
		_, qErr := db2.ExecContext(ctx2, "SELECT 1")
		cancel2()
		db2.Close()

		if qErr != nil {
			t.Logf("query without client cert rejected as expected: %v", qErr)
			return
		}

		t.Fatal("connection without client certificate should have been rejected")
	}

	t.Fatal("timed out waiting for MySQL server to be available for auth test")
}
