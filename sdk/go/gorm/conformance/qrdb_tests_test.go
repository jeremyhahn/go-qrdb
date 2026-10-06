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

//go:build gormconformance

// Package tests_test is qrdb's substitute for upstream gorm.io/gorm's
// tests/tests_test.go. It is materialized alongside the unmodified upstream
// *_test.go files by `make gorm-suite-sync` (see sync.go and
// GORM_SUITE.lock) and supplies the four package-level symbols every other
// upstream file in this package references: DB, OpenTestConnection,
// postgresDSN, and gaussdbDSN.
//
// Upstream selects a dialect via a hard-coded `switch os.Getenv(
// "GORM_DIALECT")` with no extension point for a fifth backend, and connects
// in a package init() rather than testing.M's TestMain. This file replaces
// that switch with a target dispatch table over qrdb's three surfaces
// (node, mysql, pgsql) selected by GORM_QRDB_TARGET, and moves the
// connect-and-migrate sequence into TestMain, where a failure can call
// os.Exit with a diagnostic instead of silently leaving DB nil for every
// test to fault on. GORM_DIALECT and GORM_DSN are deliberately never
// consulted: honoring them would let a stray environment variable open a
// real MySQL/Postgres server and have this harness report qrdb green.
package tests_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"log"
	"os"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	// Dot-imported for the shared model types (User, Account, Pet, ...)
	// every upstream test file in this package references, exactly as
	// upstream's tests_test.go does. These live in gorm.io/gorm's main
	// module at utils/tests, not in the tests/ submodule this suite is
	// pulled from, so they are not vendored — see GORM_SUITE.lock.
	. "gorm.io/gorm/utils/tests"

	qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
	qrdbgorm "github.com/jeremyhahn/go-qrdb/sdk/go/gorm"

	// Registers the node:// DSN scheme with sdk/go/driver. It is a separate
	// package, and therefore a separate import, because it pulls in the whole
	// in-process node stack (~175 packages more than sdk/go/driver alone)
	// that embedded:// and rest:// users must not be made to link.
	_ "github.com/jeremyhahn/go-qrdb/sdk/go/driver/node"
)

// targetEnvVar is the environment variable that selects which qrdb surface
// this test binary exercises. See target and openers below.
const targetEnvVar = "GORM_QRDB_TARGET"

// DB is the shared *gorm.DB every upstream GORM conformance test file in
// this package references as a package-level global. TestMain populates it
// before m.Run() and every subsequent OpenTestConnection call opens an
// independent connection against the same target.
var DB *gorm.DB

// postgresDSN and gaussdbDSN are the DSNs upstream's postgres-/gaussdb-
// guarded blocks (table_test.go, migrate_test.go) gorm.Open a throwaway
// connection with. qrdb has no gaussdb target, so gaussdbDSN keeps the dead
// upstream literal for compilation only. postgresDSN starts as the same
// kind of literal but is RE-POINTED at the harness daemon's live pgsql
// listener by TestMain for the pgsql target: qrdb's "pgsql" target's
// Dialector.Name() is genuinely "postgres" (gorm.io/driver/postgres,
// unmodified), so those guarded blocks DO run, and one of them
// (TestInvalidCachedPlanSimpleProtocol) issues real queries over the
// connection it opens.
var (
	postgresDSN = "user=gorm password=gorm dbname=gorm host=localhost port=9920 sslmode=disable TimeZone=Asia/Shanghai"
	gaussdbDSN  = "user=gaussdb password=Gaussdb@123 dbname=gorm host=localhost port=9950 sslmode=disable TimeZone=Asia/Shanghai"
)

// target identifies which of qrdb's three GORM-reachable surfaces
// OpenTestConnection dials into.
type target string

const (
	// targetNode drives the stock gorm.io/driver/mysql dialector over qrdb's
	// in-process database/sql driver, over node://memory — a complete in-process qrdb node
	// (Raft state machine, service registry, relational table catalog,
	// interactive transactions) with every protocol server disabled. No
	// qrdbd subprocess, no network, no TLS.
	//
	// It is deliberately NOT embedded://, which is the KV-only in-process
	// path: that scheme has no service.Registry, so CREATE TABLE fails with
	// *pkg/sql/provider.RelationalCatalogUnavailableError and this whole
	// suite has no tables to run against. See sdk/go/driver/node.
	targetNode target = "node"

	// targetMySQL drives gorm.io/driver/mysql over qrdb's MySQL wire
	// protocol server, mTLS-authenticated. See qrdb_harness_test.go.
	targetMySQL target = "mysql"

	// targetPgSQL drives gorm.io/driver/postgres over qrdb's PostgreSQL
	// wire protocol server, mTLS-authenticated. See qrdb_harness_test.go.
	targetPgSQL target = "pgsql"
)

// currentTarget is set once by TestMain after validating GORM_QRDB_TARGET
// and consulted by every later OpenTestConnection call (upstream test files
// open additional connections beyond DB for PrepareStmt/TranslateError/
// transaction-timeout scenarios).
var currentTarget target

// openers dispatches target to the function that opens a *gorm.DB against
// it. A map, not a switch, per project convention (map-based dispatch over
// switch statements) — and because it doubles as the exhaustive list of
// valid GORM_QRDB_TARGET values TestMain validates against.
var openers = map[target]func(*gorm.Config) (*gorm.DB, error){
	targetNode:  openNode,
	targetMySQL: openMySQL,
	targetPgSQL: openPgSQL,
}

// sqlConcurrency is the SQL concurrency mode every target runs the suite
// under (docs/row-locking). Pessimistic, because the upstream suite includes
// TestConcurrentMany2ManyAssociation: concurrent Appends to one user's
// many2many association all rewrite that user's row, and under the default
// optimistic mode all but one of them can lose their commit with MySQL 1213
// (GORM's Append does not retry), so a language goes missing. Measured on the
// node target with -count=30: optimistic failed 30 of 30, pessimistic passed
// 30 of 30, and the rest of the suite passes unchanged under pessimistic.
// The wire targets create their database with it (--sql-concurrency); the
// node target sets it on every connection through its DSN.
const sqlConcurrency = "pessimistic"

// nodeDSN is the in-process node the node target runs against: a complete
// qrdb node (Raft state machine, service registry, relational table catalog,
// interactive transactions) with every protocol server disabled.
const nodeDSN = "node://memory?database=gorm&concurrency=" + sqlConcurrency

// nodeConnector is the ONE in-process qrdb node the node target runs
// against, opened by TestMain. Every openNode call wraps it in its own
// *sql.DB, so each *gorm.DB gets an independent connection pool over a
// SHARED node.
//
// Both halves of that sentence are load-bearing, and getting either wrong
// breaks the suite in a different way:
//
//   - Shared node. sdk/go/driver/node's contract is that one connector is
//     one node, so two connectors opened on the same node://memory DSN are
//     two INDEPENDENT, separately-bootstrapped databases. openNode used to
//     sql.Open per call, which meant every OpenTestConnection handed back a
//     brand-new EMPTY node while RunMigrations had created the tables on the
//     node behind the package-level DB: those connections reported
//     HasTable(&User{}) == false and failed every statement with 1146. Worse,
//     because gorm's PreparedStmtDB evicts a cache entry whenever Prepare
//     errors, their statement cache could never warm, so every call took the
//     slow Mux.Lock() path and made gorm's own four-party lock inversion
//     deterministic — TestPreparedStmtDeadlock hung the suite forever. This
//     was a harness bug, not a driver one: the driver returns every
//     connection correctly under a pool of one.
//
//   - Independent pools. Sharing one *sql.DB instead is equally wrong:
//     upstream tests call SetMaxOpenConns on the connection OpenTestConnection
//     handed them (TestPreparedStmtDeadlock pins it to 1) and never restore
//     it, so a shared pool starves every later test — measured as a hang in
//     TestOmit, blocked in database/sql.(*DB).conn.
var nodeConnector driver.Connector

// sharedNodeConnector adapts the node connector so database/sql cannot close
// it. sql.DB.Close closes its connector when the connector implements
// io.Closer, so without this wrapper the first upstream test that closes its
// own connection would shut down the node every other test is still using.
// TestMain closes the real connector once, at teardown.
type sharedNodeConnector struct {
	inner driver.Connector
}

// Connect opens one connection on the shared node.
func (c sharedNodeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	return c.inner.Connect(ctx)
}

// Driver returns the underlying driver.
func (c sharedNodeConnector) Driver() driver.Driver { return c.inner.Driver() }

// openNode opens a *gorm.DB through the stock gorm.io/driver/mysql dialector,
// configured for qrdb by sdk/go/gorm's NewConfig, against the shared
// in-process node — no qrdbd subprocess, no network, no TLS. The node://
// scheme is registered by the blank import above.
func openNode(cfg *gorm.Config) (*gorm.DB, error) {
	if nodeConnector == nil {
		return nil, &nodeNotOpenedError{}
	}
	dialectorCfg := qrdbgorm.NewConfig()
	dialectorCfg.Conn = sql.OpenDB(sharedNodeConnector{inner: nodeConnector})
	return gorm.Open(mysql.New(dialectorCfg), cfg)
}

// nodeNotOpenedError reports an openNode call made before TestMain opened the
// shared node, which can only happen if the target dispatch table and
// TestMain's setup fall out of sync.
type nodeNotOpenedError struct{}

// Error returns a human-readable description.
func (e *nodeNotOpenedError) Error() string {
	return "gorm-conformance: in-process node not opened for target " + string(targetNode)
}

// openMySQL opens a *gorm.DB through gorm.io/driver/mysql against the
// shared qrdbd subprocess's MySQL wire port, over mTLS. shared is populated
// by TestMain via startWireServer before any opener runs.
func openMySQL(cfg *gorm.Config) (*gorm.DB, error) {
	if shared == nil {
		return nil, &wireServerNotStartedError{Target: targetMySQL}
	}
	// loc=Local is required, not cosmetic. go-sql-driver/mysql defaults loc to
	// UTC, and gorm.io/driver/mysql renders time.Time values in the CONNECTION
	// location when it builds SQL -- so without it a time carrying a local
	// zone comes out converted: TestToSQL builds its fixture with
	// time.ParseInLocation(..., time.Local), expects '2021-10-18 00:00:00',
	// and got '2021-10-18 04:00:00' on a UTC-4 host. That is the upstream
	// suite's own DSN convention (GORM's mysql config uses loc=Local), and
	// leaving it off made a host-timezone-dependent failure look like a qrdb
	// timestamp defect.
	dsn := fmt.Sprintf("%s@tcp(127.0.0.1:%d)/%s?tls=%s&parseTime=true&loc=Local",
		sqlUser, shared.mysqlPort, sqlDatabase, shared.mysqlTLSConfigName)
	return gorm.Open(mysql.Open(dsn), cfg)
}

// pgsqlQueryExecMode is the pgx query execution mode this target runs the
// upstream suite under, set as a connection-string parameter pgx reads in
// ParseConfig.
//
// describe_exec keeps the PostgreSQL EXTENDED query protocol -- Parse,
// Describe, Bind, Execute, with real parameter OIDs -- but re-describes each
// statement instead of reusing a cached RowDescription. pgx's default,
// cache_statement, describes once and then BINDs against that descriptor
// forever, which is unusable for a test suite whose whole shape is
// DropTable/AutoMigrate followed by queries over the recreated table: the
// upstream suite reuses one model name (hn_posts) at two different widths
// across two adjacent tests, so the second one executes against the first
// one's descriptor.
//
// That is not a qrdb limitation. PostgreSQL 16 refuses the identical
// sequence with "cached plan must not change result type (SQLSTATE 0A000)",
// measured directly, and qrdb now returns exactly that (see
// pkg/sql/pgsql/errors.go's CachedPlanResultTypeError). No PostgreSQL of any
// kind can serve those two tests over a cached statement, so running the
// suite that way tests nothing about qrdb -- it only re-measures a
// restriction of the protocol.
//
// Upstream GORM resolves this by setting PreferSimpleProtocol: true for its
// own postgres target, which drops to the SIMPLE query protocol and gives up
// prepared statements, parameter OIDs, and the entire extended path. This
// target deliberately does not go that far: describe_exec is the strictest
// mode that is actually serveable, so qrdb's extended-protocol implementation
// -- by far the larger and subtler half of pkg/sql/pgsql -- stays under test
// rather than being skipped. The cached-statement behaviour itself is covered
// where it belongs, by pkg/sql/pgsql's own tests.
const pgsqlQueryExecMode = "describe_exec"

// openPgSQL opens a *gorm.DB through gorm.io/driver/postgres against the
// shared qrdbd subprocess's PostgreSQL wire port, over mTLS. shared is
// populated by TestMain via startWireServer before any opener runs.
func openPgSQL(cfg *gorm.Config) (*gorm.DB, error) {
	if shared == nil {
		return nil, &wireServerNotStartedError{Target: targetPgSQL}
	}
	dsn := fmt.Sprintf(
		"host=127.0.0.1 port=%d dbname=%s user=%s sslmode=verify-full sslrootcert=%s sslcert=%s sslkey=%s default_query_exec_mode=%s",
		shared.pgsqlPort, sqlDatabase, sqlUser,
		shared.ca.CertPath, shared.client.CertPath, shared.client.KeyPath,
		pgsqlQueryExecMode,
	)
	return gorm.Open(postgres.Open(dsn), cfg)
}

// OpenTestConnection opens a new *gorm.DB against whatever target TestMain
// selected via GORM_QRDB_TARGET. It is the direct replacement for upstream's
// GORM_DIALECT/GORM_DSN-driven function of the same name and signature;
// every upstream test file that calls it to open an additional connection
// (PrepareStmt, TranslateError, transaction-timeout scenarios) is unaware
// qrdb sits behind it.
func OpenTestConnection(cfg *gorm.Config) (*gorm.DB, error) {
	opener, ok := openers[currentTarget]
	if !ok {
		return nil, fmt.Errorf("OpenTestConnection: no opener registered for target %q", currentTarget)
	}
	// No-op unless GORM_QRDB_SQL_LOG is set; see qrdb_sqllog_test.go.
	installSQLRecorder(cfg)
	return opener(cfg)
}

// TestMain builds DB against the target named by GORM_QRDB_TARGET, spawns
// (and later tears down) the qrdbd subprocess the mysql/pgsql targets need,
// runs RunMigrations, then hands off to m.Run.
//
// GORM_QRDB_TARGET has deliberately no default: an unset value is a fatal
// error, not a fallback to "node" or any other target, so that running
// this suite without explicitly choosing a target can never be mistaken for
// having exercised one.
func TestMain(m *testing.M) {
	if dumpAllowlistAndExit() {
		return
	}

	tgt := target(os.Getenv(targetEnvVar))
	if tgt == "" {
		fmt.Fprintf(os.Stderr,
			"FATAL: %s is not set. qrdb's GORM conformance harness has no default\n"+
				"target — run `make conformance-test-gorm-node`, `-mysql`, or\n"+
				"`-pgsql` (or set %s=node|mysql|pgsql yourself) rather than a bare\n"+
				"`go test`, so a run can never be silently mistaken for a different\n"+
				"target than the one its results get reported against.\n",
			targetEnvVar, targetEnvVar)
		os.Exit(1)
	}
	if _, ok := openers[tgt]; !ok {
		fmt.Fprintf(os.Stderr, "FATAL: unknown %s %q; valid targets: node, mysql, pgsql\n", targetEnvVar, tgt)
		os.Exit(1)
	}
	currentTarget = tgt

	// Statement recording is opt-in via GORM_QRDB_SQL_LOG and off in every
	// normal run; see qrdb_sqllog_test.go.
	if path := os.Getenv(sqlLogEnvVar); path != "" {
		rec, err := newSQLRecorder(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: %s=%q: %v\n", sqlLogEnvVar, path, err)
			os.Exit(1)
		}
		recorder = rec
	}

	var teardown func()
	switch tgt {
	case targetMySQL, targetPgSQL:
		srv, err := startWireServer(tgt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: startWireServer(%s): %v\n", tgt, err)
			os.Exit(1)
		}
		shared = srv
		teardown = srv.stop
		if tgt == targetPgSQL {
			// Re-point the upstream literal at the live harness daemon.
			// TestInvalidCachedPlanSimpleProtocol opens its own connection
			// with postgresDSN and ISSUES queries over it (unlike
			// table_test.go's parse-only uses), so against the upstream
			// default of localhost:9920 it dials a server that does not
			// exist and fails before reaching what it tests.
			postgresDSN = fmt.Sprintf(
				"host=127.0.0.1 port=%d dbname=%s user=%s sslmode=verify-full sslrootcert=%s sslcert=%s sslkey=%s default_query_exec_mode=%s",
				srv.pgsqlPort, sqlDatabase, sqlUser,
				srv.ca.CertPath, srv.client.CertPath, srv.client.KeyPath,
				pgsqlQueryExecMode,
			)
		}
	case targetNode:
		// One node for the whole run, one pool per connection: see
		// nodeConnector's doc comment for what each of the two alternatives
		// breaks.
		connector, err := (&qrdbdriver.Driver{}).OpenConnector(nodeDSN)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FATAL: OpenConnector(%s): %v\n", nodeDSN, err)
			os.Exit(1)
		}
		nodeConnector = connector
		teardown = func() {
			if closer, ok := connector.(io.Closer); ok {
				_ = closer.Close()
			}
		}
	}

	var err error
	if DB, err = OpenTestConnection(&gorm.Config{}); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: OpenTestConnection(%s): %v\n", tgt, err)
		if teardown != nil {
			teardown()
		}
		os.Exit(1)
	}

	sqlDB, err := DB.DB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: DB.DB(): %v\n", err)
		if teardown != nil {
			teardown()
		}
		os.Exit(1)
	}
	if err := sqlDB.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: ping %s: %v\n", tgt, err)
		if teardown != nil {
			teardown()
		}
		os.Exit(1)
	}

	RunMigrations()

	code := m.Run()
	if teardown != nil {
		teardown()
	}
	if recorder != nil {
		// Explicit rather than deferred: TestMain ends in os.Exit.
		_ = recorder.Close()
	}
	os.Exit(code)
}

// RunMigrations drops and recreates every model table the upstream suite
// depends on. It deviates from upstream's tests_test.go in two ways:
//
//  1. Deterministic model order. Upstream shuffles allModels with
//     rand.Shuffle before migrating. qrdb's DDL coverage is still uneven
//     across targets (see pkg/sql/provider), so shuffling only makes each
//     run's failure set non-reproducible for no benefit; this harness
//     migrates in a fixed, declared order instead.
//
//  2. Non-fatal on failure. Upstream calls os.Exit(1) the instant
//     AutoMigrate returns a non-nil error, which would abort the entire
//     suite before a single Test* function ran. That is exactly what the
//     pgsql target does today — gorm.io/driver/postgres emits `bigserial`
//     for an auto-increment key and qrdb's PostgreSQL surface cannot parse
//     it, so not one model table is created — and exiting fatally there
//     would produce zero per-test signal, the opposite of what this harness
//     exists to surface (see qrdb_allowlist_test.go). RunMigrations logs the
//     failure and lets m.Run proceed, so every upstream Test* function
//     reports its own pass or fail and runner.go's ratchet has real per-test
//     data to reconcile against the allowlist.
func RunMigrations() {
	allModels := []interface{}{&User{}, &Account{}, &Pet{}, &Company{}, &Toy{}, &Language{}, &Coupon{}, &CouponProduct{}, &Order{}, &Parent{}, &Child{}, &Tools{}}

	DB.Migrator().DropTable("user_friends", "user_speaks")

	if err := DB.Migrator().DropTable(allModels...); err != nil {
		log.Printf("RunMigrations: DropTable: %v (continuing — DDL support is incomplete)", err)
	}

	if err := DB.AutoMigrate(allModels...); err != nil {
		log.Printf("RunMigrations: AutoMigrate: %v (continuing — DDL support is incomplete)", err)
	}

	for _, m := range allModels {
		if !DB.Migrator().HasTable(m) {
			log.Printf("RunMigrations: no table for %#v (DDL support is incomplete)", m)
		}
	}
}
