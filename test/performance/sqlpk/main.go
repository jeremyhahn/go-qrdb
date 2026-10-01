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

//go:build benchmark

// Command sqlpk measures how the latency of a primary-key point statement
// over the MySQL wire depends on the size of the table it addresses.
//
// It starts one single-node qrdbd, provisions a tenant, a SQL user and one
// database per concurrency mode exactly as an operator would through the
// qrdb CLI, grows a table through a series of sizes and, at every size,
// times sequential single-row INSERT, SELECT, UPDATE and DELETE statements
// by primary key. A point statement touches one row, so its latency should
// not depend on how many other rows the table holds; this benchmark is the
// regression guard for that property. Results are printed as a Markdown
// table and written as JSON.
//
//	make bench-sqlpk
//	go run -tags benchmark ./test/performance/sqlpk -qrdbd bin/qrdbd -qrdb bin/qrdb
package main

import (
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// fillBatch is how many rows one multi-row INSERT adds while a table grows.
const fillBatch = 500

// tenantID is the tenant every database of the run belongs to.
const tenantID = "bench"

var (
	qrdbdBin = flag.String("qrdbd", "bin/qrdbd", "qrdbd binary, built with the qrdb_sql and qrdb_mysql tags")
	qrdbBin  = flag.String("qrdb", "bin/qrdb", "qrdb CLI binary")
	workDir  = flag.String("work", "", "directory for certificates and data (default: a fresh temporary directory)")
	modes    = flag.String("modes", "optimistic,pessimistic", "comma-separated SQL concurrency modes")
	sizes    = flag.String("sizes", "0,1000,16000,64000", "comma-separated table sizes, ascending")
	ops      = flag.Int("ops", 200, "statements timed per workload per size")
	label    = flag.String("label", "run", "label recorded on every result")
	outFile  = flag.String("out", "", "JSON results file (default: <label>-sqlpk.json in the work directory)")
)

// result is one workload's latency distribution at one table size.
type result struct {
	Label    string  `json:"label"`
	Mode     string  `json:"mode"`
	Rows     int     `json:"rows"`
	Workload string  `json:"workload"`
	N        int     `json:"n"`
	P50ms    float64 `json:"p50_ms"`
	P95ms    float64 `json:"p95_ms"`
	P99ms    float64 `json:"p99_ms"`
	MaxMs    float64 `json:"max_ms"`
}

// benchError is a step of the run that failed.
type benchError struct {
	Step   string
	Output string
	Err    error
}

func (e *benchError) Error() string {
	if e.Output != "" {
		return "sqlpk: " + e.Step + ": " + e.Err.Error() + ": " + strings.TrimSpace(e.Output)
	}
	return "sqlpk: " + e.Step + ": " + e.Err.Error()
}

func (e *benchError) Unwrap() error { return e.Err }

// node is the running daemon and the credentials the run talks to it with.
type node struct {
	daemon    *qrdbdharness.Daemon
	ca        *qrdbdharness.CA
	admin     *qrdbdharness.KeyPair
	client    *qrdbdharness.KeyPair
	adminPort int
	mysqlPort int
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	dir := *workDir
	if dir == "" {
		d, err := os.MkdirTemp("", "qrdb-sqlpk-*")
		if err != nil {
			return &benchError{Step: "work dir", Err: err}
		}
		dir = d
	}
	targets, err := parseSizes(*sizes)
	if err != nil {
		return err
	}

	n, err := start(dir)
	if err != nil {
		return err
	}
	defer func() { _ = n.daemon.Stop() }()

	var results []result
	for _, mode := range strings.Split(*modes, ",") {
		got, err := n.measureMode(strings.TrimSpace(mode), targets)
		if err != nil {
			return err
		}
		results = append(results, got...)
	}

	out := *outFile
	if out == "" {
		out = filepath.Join(dir, *label+"-sqlpk.json")
	}
	b, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return &benchError{Step: "encode results", Err: err}
	}
	if err := os.WriteFile(out, b, 0o600); err != nil {
		return &benchError{Step: "write results", Err: err}
	}
	printTable(results)
	fmt.Println("results:", out)
	return nil
}

// parseSizes parses the ascending table sizes the run grows through.
func parseSizes(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		v, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || v < 0 || (len(out) > 0 && v < out[len(out)-1]) {
			return nil, &benchError{Step: "parse -sizes", Err: errors.New("sizes must be non-negative and ascending: " + s)}
		}
		out = append(out, v)
	}
	return out, nil
}

// start launches a single-node qrdbd with the MySQL wire enabled and
// provisions the tenant, role and user the run connects as.
func start(dir string) (*node, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, &benchError{Step: "work dir", Err: err}
	}
	ca, err := qrdbdharness.NewCA(dir)
	if err != nil {
		return nil, &benchError{Step: "CA", Err: err}
	}
	nodeCert, err := ca.Issue(dir, qrdbdharness.WithSubject("qrdb-sqlpk-node", "system"), qrdbdharness.WithServerAuth())
	if err != nil {
		return nil, &benchError{Step: "node certificate", Err: err}
	}
	admin, err := ca.Issue(dir, qrdbdharness.WithSubject("admin", "system"))
	if err != nil {
		return nil, &benchError{Step: "admin certificate", Err: err}
	}
	client, err := ca.Issue(dir, qrdbdharness.WithSubject("sqlpk", tenantID))
	if err != nil {
		return nil, &benchError{Step: "client certificate", Err: err}
	}
	raftPort, err := qrdbdharness.ReserveUDPPort()
	if err != nil {
		return nil, &benchError{Step: "raft port", Err: err}
	}
	ports, err := qrdbdharness.ReserveTCPPorts(4)
	if err != nil {
		return nil, &benchError{Step: "tcp ports", Err: err}
	}
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, &benchError{Step: "data dir", Err: err}
	}
	raftAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(raftPort))
	args := []string{"start",
		"--listen", raftAddr, "--raft-address", raftAddr,
		"--data-dir", dataDir, "--sm", "mvcc", "--discovery", "static",
		"--peers", "1=" + raftAddr, "--shard-id", "1", "--replica-id", "1",
		"--admin-port", strconv.Itoa(ports[0]), "--grpc-port", strconv.Itoa(ports[1]),
		"--enable-mysql", "--mysql-port", strconv.Itoa(ports[2]),
		"--enable-pgsql", "--pgsql-port", strconv.Itoa(ports[3]),
		"--sql-database", "qrdb",
	}
	args = append(args, ca.TLSFlags(nodeCert)...)
	logFile, err := os.Create(filepath.Join(dir, "qrdbd.log"))
	if err != nil {
		return nil, &benchError{Step: "log file", Err: err}
	}
	d, err := qrdbdharness.Start(*qrdbdBin, args, qrdbdharness.WithOutput(logFile))
	if err != nil {
		return nil, &benchError{Step: "start qrdbd", Err: err}
	}
	n := &node{daemon: d, ca: ca, admin: admin, client: client, adminPort: ports[0], mysqlPort: ports[2]}
	if err := n.provision(dataDir); err != nil {
		_ = d.Stop()
		return nil, err
	}
	return n, nil
}

// provision waits for the node to lead and creates the tenant, a role that
// may create and configure databases, and a certificate-only user.
func (n *node) provision(dataDir string) error {
	base := "https://127.0.0.1:" + strconv.Itoa(n.adminPort)
	if err := qrdbdharness.WaitForNodeHost(dataDir, 30*time.Second); err != nil {
		return &benchError{Step: "node host", Err: err}
	}
	if err := qrdbdharness.WaitForHTTPOK(base+"/health", n.ca.Pool, nil, 90*time.Second); err != nil {
		return &benchError{Step: "health", Err: err}
	}
	adminTLS, err := n.admin.TLSCertificate()
	if err != nil {
		return &benchError{Step: "admin TLS", Err: err}
	}
	if err := qrdbdharness.WaitForLeader(base+"/status", n.ca.Pool, &adminTLS, 60*time.Second); err != nil {
		return &benchError{Step: "leader", Err: err}
	}
	if _, err := n.cli(n.admin, "tenant", "create", "--id", tenantID, "--name", tenantID); err != nil {
		return err
	}
	out, err := n.cli(n.admin, "--output", "json", "roles", "create", "--name", "sqlpk-provisioner",
		"--tenant-id", tenantID, "--permissions", "databases:create,databases:update")
	if err != nil {
		return err
	}
	var role struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &role); err != nil {
		return &benchError{Step: "decode role", Output: out, Err: err}
	}
	if _, err := n.cli(n.admin, "users", "create", "--username", "sqlpk", "--tenant-id", tenantID,
		"--roles", role.ID, "--without-email-login-key"); err != nil {
		return err
	}
	clientTLS, err := n.client.TLSCertificate()
	if err != nil {
		return &benchError{Step: "client TLS", Err: err}
	}
	return gomysql.RegisterTLSConfig("sqlpk", &tls.Config{
		RootCAs:      n.ca.Pool,
		Certificates: []tls.Certificate{clientTLS},
		MinVersion:   tls.VersionTLS13,
	})
}

// cli runs one qrdb CLI command as the given identity.
func (n *node) cli(as *qrdbdharness.KeyPair, verb ...string) (string, error) {
	args := []string{"--server", "https://127.0.0.1:" + strconv.Itoa(n.adminPort),
		"--ca-file", n.ca.CertPath, "--cert-file", as.CertPath, "--key-file", as.KeyPath, "--timeout", "30s"}
	out, err := exec.Command(*qrdbBin, append(args, verb...)...).CombinedOutput() // #nosec G204 -- the binary is the operator's own -qrdb flag
	if err != nil {
		return string(out), &benchError{Step: strings.Join(verb, " "), Output: string(out), Err: err}
	}
	return string(out), nil
}

// measureMode creates a database in mode and times every workload at every
// size in targets.
func (n *node) measureMode(mode string, targets []int) ([]result, error) {
	name := "sqlpk_" + mode
	if _, err := n.cli(n.client, "database", "create", name, "--engine", "kv", "--backend", "pebble", "--sql-concurrency", mode); err != nil {
		return nil, err
	}
	if _, err := n.cli(n.client, "database", "update-protocol", name, "--mysql"); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("sqlpk@tcp(127.0.0.1:%d)/%s?tls=sqlpk&parseTime=true", n.mysqlPort, name)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, &benchError{Step: "open " + name, Err: err}
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("CREATE TABLE bench (id BIGINT NOT NULL PRIMARY KEY, v BIGINT, s VARCHAR(64))"); err != nil {
		return nil, &benchError{Step: "create table", Err: err}
	}

	var results []result
	rows := 0
	for i, target := range targets {
		if err := fill(db, rows, target); err != nil {
			return nil, err
		}
		rows = target
		// Keys of the timed statements sit above every fill key, and each
		// size uses its own range, so the table holds exactly `rows` rows
		// besides the ones a workload adds and then deletes again.
		base := 1_000_000_000 + i*1_000_000
		for _, w := range workloads(base) {
			lat, err := timeWorkload(db, *ops, w.run)
			if err != nil {
				return nil, &benchError{Step: fmt.Sprintf("%s %s at %d rows", mode, w.name, rows), Err: err}
			}
			r := summarize(mode, rows, w.name, lat)
			fmt.Printf("%-11s rows=%-6d %-9s p50=%7.2fms p95=%7.2fms p99=%7.2fms\n", mode, rows, w.name, r.P50ms, r.P95ms, r.P99ms)
			results = append(results, r)
		}
	}
	return results, nil
}

// workload is one timed statement shape.
type workload struct {
	name string
	run  func(db *sql.DB, i int) error
}

// workloads returns the four point statements, keyed from base. INSERT adds
// the rows SELECT and UPDATE address and DELETE removes, so every statement
// finds its row.
func workloads(base int) []workload {
	return []workload{
		{"insert", func(db *sql.DB, i int) error {
			_, err := db.Exec("INSERT INTO bench (id, v, s) VALUES (?, ?, ?)", base+i, i, "x")
			return err
		}},
		{"select", func(db *sql.DB, i int) error {
			var v int64
			return db.QueryRow("SELECT v FROM bench WHERE id = ?", base+i).Scan(&v)
		}},
		{"update", func(db *sql.DB, i int) error {
			_, err := db.Exec("UPDATE bench SET v = ? WHERE id = ?", i+1, base+i)
			return err
		}},
		{"delete", func(db *sql.DB, i int) error {
			_, err := db.Exec("DELETE FROM bench WHERE id = ?", base+i)
			return err
		}},
	}
}

// fill grows the table from rows to target rows in multi-row INSERTs.
func fill(db *sql.DB, rows, target int) error {
	for rows < target {
		batch := min(target-rows, fillBatch)
		var sb strings.Builder
		sb.WriteString("INSERT INTO bench (id, v, s) VALUES ")
		for i := 0; i < batch; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			id := rows + i + 1
			sb.WriteString("(" + strconv.Itoa(id) + "," + strconv.Itoa(id) + ",'fill')")
		}
		if _, err := db.Exec(sb.String()); err != nil {
			return &benchError{Step: "fill to " + strconv.Itoa(target), Err: err}
		}
		rows += batch
	}
	return nil
}

// timeWorkload runs fn n times sequentially and returns each latency.
func timeWorkload(db *sql.DB, n int, fn func(db *sql.DB, i int) error) ([]time.Duration, error) {
	lat := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		started := time.Now()
		if err := fn(db, i); err != nil {
			return nil, err
		}
		lat = append(lat, time.Since(started))
	}
	return lat, nil
}

// summarize reduces one workload's latencies to its percentiles.
func summarize(mode string, rows int, name string, lat []time.Duration) result {
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) float64 {
		if len(lat) == 0 {
			return 0
		}
		return float64(lat[int(float64(len(lat)-1)*p)].Microseconds()) / 1000
	}
	return result{Label: *label, Mode: mode, Rows: rows, Workload: name, N: len(lat),
		P50ms: pct(0.50), P95ms: pct(0.95), P99ms: pct(0.99), MaxMs: pct(1)}
}

// printTable prints the results as one Markdown table, p50 / p95 per cell.
func printTable(results []result) {
	fmt.Println()
	fmt.Println("| mode | rows | insert p50 / p95 | select p50 / p95 | update p50 / p95 | delete p50 / p95 |")
	fmt.Println("|---|---:|---:|---:|---:|---:|")
	type key struct {
		mode string
		rows int
	}
	cells := map[key]map[string]result{}
	var order []key
	for _, r := range results {
		k := key{r.Mode, r.Rows}
		if cells[k] == nil {
			cells[k] = map[string]result{}
			order = append(order, k)
		}
		cells[k][r.Workload] = r
	}
	for _, k := range order {
		row := "| " + k.mode + " | " + strconv.Itoa(k.rows)
		for _, w := range []string{"insert", "select", "update", "delete"} {
			r := cells[k][w]
			row += fmt.Sprintf(" | %.2f / %.2f ms", r.P50ms, r.P95ms)
		}
		fmt.Println(row + " |")
	}
}
