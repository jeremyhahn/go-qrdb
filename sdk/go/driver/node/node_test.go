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
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
)

// openNode opens a *sql.DB over an in-memory node:// database, registering
// cleanup. Every test gets its own node, so no test can observe another's
// tables.
func openNode(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("qrdb", dsn)
	if err != nil {
		t.Fatalf("sql.Open(%q): %v", dsn, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ---------------------------------------------------------------------------
// Scheme registration
// ---------------------------------------------------------------------------

// TestScheme_Registered verifies the blank import wired the node scheme into
// the driver's single scheme registry.
func TestScheme_Registered(t *testing.T) {
	if !slices.Contains(qrdbdriver.RegisteredSchemes(), Scheme) {
		t.Fatalf("RegisteredSchemes() = %v, want it to contain %q",
			qrdbdriver.RegisteredSchemes(), Scheme)
	}
}

// TestScheme_ParsesEmbeddedGrammar verifies node:// accepts exactly the
// embedded:// DSN grammar, including its defaults.
func TestScheme_ParsesEmbeddedGrammar(t *testing.T) {
	tests := []struct {
		name        string
		dsn         string
		wantDB      string
		wantBackend string
		wantEngine  string
		wantDataDir string
		wantTenant  string
	}{
		{
			name:        "memory host",
			dsn:         "node://memory?database=mydb",
			wantDB:      "mydb",
			wantBackend: "memory",
			wantEngine:  "kv",
		},
		{
			name:        "path and explicit backend",
			dsn:         "node:///var/lib/qrdb?backend=pebble&database=mydb",
			wantDB:      "mydb",
			wantBackend: "pebble",
			wantEngine:  "kv",
			wantDataDir: "/var/lib/qrdb",
		},
		{
			name:        "tenant parameter",
			dsn:         "node://memory?database=mydb&tenant=acme",
			wantDB:      "mydb",
			wantBackend: "memory",
			wantEngine:  "kv",
			wantTenant:  "acme",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := qrdbdriver.ParseDSN(tt.dsn)
			if err != nil {
				t.Fatalf("ParseDSN(%q): %v", tt.dsn, err)
			}
			if cfg.Scheme != Scheme {
				t.Errorf("Scheme = %q, want %q", cfg.Scheme, Scheme)
			}
			if cfg.Database != tt.wantDB {
				t.Errorf("Database = %q, want %q", cfg.Database, tt.wantDB)
			}
			if cfg.Backend != tt.wantBackend {
				t.Errorf("Backend = %q, want %q", cfg.Backend, tt.wantBackend)
			}
			if cfg.Engine != tt.wantEngine {
				t.Errorf("Engine = %q, want %q", cfg.Engine, tt.wantEngine)
			}
			if cfg.DataDir != tt.wantDataDir {
				t.Errorf("DataDir = %q, want %q", cfg.DataDir, tt.wantDataDir)
			}
			if cfg.Tenant != tt.wantTenant {
				t.Errorf("Tenant = %q, want %q", cfg.Tenant, tt.wantTenant)
			}
		})
	}
}

// TestScheme_MissingDatabase verifies the reused embedded parser still
// rejects a DSN with no database.
func TestScheme_MissingDatabase(t *testing.T) {
	if _, err := qrdbdriver.ParseDSN("node://memory"); err == nil {
		t.Fatal("expected an error for a node DSN with no database")
	} else {
		var pe *qrdbdriver.DSNParseError
		if !errors.As(err, &pe) {
			t.Errorf("error = %v (%T), want *DSNParseError", err, err)
		}
	}
}

// TestOpenConnector_ReturnsNodeConnector verifies the driver builds this
// package's connector for a node DSN.
func TestOpenConnector_ReturnsNodeConnector(t *testing.T) {
	d := &qrdbdriver.Driver{}
	connector, err := d.OpenConnector("node://memory?database=oc")
	if err != nil {
		t.Fatalf("OpenConnector: %v", err)
	}
	c, ok := connector.(*Connector)
	if !ok {
		t.Fatalf("connector = %T, want *node.Connector", connector)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// End-to-end SQL
// ---------------------------------------------------------------------------

// TestNode_RelationalRoundTrip drives the full DML lifecycle over a
// CREATE TABLE-produced relational table and asserts the values come back as
// real Go types rather than rendered strings.
func TestNode_RelationalRoundTrip(t *testing.T) {
	db := openNode(t, "node://memory?database=roundtrip")

	if _, err := db.Exec("CREATE TABLE widgets (id BIGINT PRIMARY KEY, qty INT, name VARCHAR(64))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	if _, err := db.Exec("INSERT INTO widgets (id, qty, name) VALUES (?, ?, ?)", 1, 7, "gizmo"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	var (
		id   int64
		qty  int64
		name string
	)
	if err := db.QueryRow("SELECT id, qty, name FROM widgets WHERE id = ?", 1).Scan(&id, &qty, &name); err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if id != 1 || qty != 7 || name != "gizmo" {
		t.Errorf("row = (%d, %d, %q), want (1, 7, \"gizmo\")", id, qty, name)
	}

	// An INT column must arrive as an integer, not as its decimal rendering:
	// a stringly-typed result would satisfy the Scan above through
	// database/sql's string->int conversion and hide the defect.
	var raw any
	if err := db.QueryRow("SELECT qty FROM widgets WHERE id = 1").Scan(&raw); err != nil {
		t.Fatalf("SELECT into any: %v", err)
	}
	got, ok := raw.(int64)
	if !ok {
		t.Fatalf("qty scanned as %T (%v), want int64", raw, raw)
	}
	if got != 7 {
		t.Errorf("qty = %d, want 7", got)
	}

	res, err := db.Exec("UPDATE widgets SET qty = ? WHERE id = ?", 9, 1)
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	if affected, err := res.RowsAffected(); err != nil {
		t.Fatalf("RowsAffected: %v", err)
	} else if affected != 1 {
		t.Errorf("UPDATE affected %d rows, want 1", affected)
	}
	if err := db.QueryRow("SELECT qty FROM widgets WHERE id = 1").Scan(&qty); err != nil {
		t.Fatalf("SELECT after UPDATE: %v", err)
	}
	if qty != 9 {
		t.Errorf("qty after UPDATE = %d, want 9", qty)
	}

	if _, err := db.Exec("DELETE FROM widgets WHERE id = ?", 1); err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	var count int64
	if err := db.QueryRow("SELECT COUNT(*) FROM widgets").Scan(&count); err != nil {
		t.Fatalf("SELECT COUNT: %v", err)
	}
	if count != 0 {
		t.Errorf("count after DELETE = %d, want 0", count)
	}
}

// TestNode_LastInsertID verifies AUTO_INCREMENT allocates through the
// registry's SequenceServicer and that the allocated value reaches the caller
// as a real LastInsertId.
func TestNode_LastInsertID(t *testing.T) {
	db := openNode(t, "node://memory?database=autoinc")

	if _, err := db.Exec("CREATE TABLE items (id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, label VARCHAR(32))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	first, err := db.Exec("INSERT INTO items (label) VALUES ('one')")
	if err != nil {
		t.Fatalf("first INSERT: %v", err)
	}
	firstID, err := first.LastInsertId()
	if err != nil {
		t.Fatalf("first LastInsertId: %v", err)
	}
	if firstID <= 0 {
		t.Fatalf("first LastInsertId = %d, want a positive auto-increment value", firstID)
	}

	second, err := db.Exec("INSERT INTO items (label) VALUES ('two')")
	if err != nil {
		t.Fatalf("second INSERT: %v", err)
	}
	secondID, err := second.LastInsertId()
	if err != nil {
		t.Fatalf("second LastInsertId: %v", err)
	}
	if secondID != firstID+1 {
		t.Errorf("second LastInsertId = %d, want %d", secondID, firstID+1)
	}

	// The reported id must be the id actually stored, not a counter kept
	// alongside the row.
	var label string
	if err := db.QueryRow("SELECT label FROM items WHERE id = ?", secondID).Scan(&label); err != nil {
		t.Fatalf("SELECT by LastInsertId: %v", err)
	}
	if label != "two" {
		t.Errorf("label = %q, want \"two\"", label)
	}
}

// TestNode_UniqueIndexRejectsDuplicate verifies a secondary unique index is
// enforced and that the violation reaches the caller as MySQL error 1062.
func TestNode_UniqueIndexRejectsDuplicate(t *testing.T) {
	db := openNode(t, "node://memory?database=unique")

	if _, err := db.Exec("CREATE TABLE accounts (id BIGINT PRIMARY KEY, email VARCHAR(64), UNIQUE KEY uk_email (email))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec("INSERT INTO accounts (id, email) VALUES (1, 'a@example.com')"); err != nil {
		t.Fatalf("first INSERT: %v", err)
	}

	_, err := db.Exec("INSERT INTO accounts (id, email) VALUES (2, 'a@example.com')")
	if err == nil {
		t.Fatal("expected the duplicate email to be rejected by the unique index")
	}
	var stateErr *qrdbdriver.SQLStateError
	if !errors.As(err, &stateErr) {
		t.Fatalf("error = %v (%T), want *SQLStateError", err, err)
	}
	if stateErr.Number != 1062 {
		t.Errorf("error number = %d, want 1062 (ER_DUP_ENTRY)", stateErr.Number)
	}

	// The rejected row must not have been written.
	var count int64
	if err := db.QueryRow("SELECT COUNT(*) FROM accounts").Scan(&count); err != nil {
		t.Fatalf("SELECT COUNT: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1", count)
	}
}

// TestNode_TransactionRollbackDiscards verifies an interactive transaction's
// buffered write is visible to itself and gone after ROLLBACK.
func TestNode_TransactionRollbackDiscards(t *testing.T) {
	db := openNode(t, "node://memory?database=txrollback")

	if _, err := db.Exec("CREATE TABLE notes (id BIGINT PRIMARY KEY, body VARCHAR(64))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO notes (id, body) VALUES (1, 'draft')"); err != nil {
		t.Fatalf("INSERT in tx: %v", err)
	}

	var body string
	if err := tx.QueryRow("SELECT body FROM notes WHERE id = 1").Scan(&body); err != nil {
		t.Fatalf("the transaction must see its own uncommitted write: %v", err)
	}
	if body != "draft" {
		t.Errorf("body inside tx = %q, want \"draft\"", body)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	var count int64
	if err := db.QueryRow("SELECT COUNT(*) FROM notes").Scan(&count); err != nil {
		t.Fatalf("SELECT COUNT: %v", err)
	}
	if count != 0 {
		t.Errorf("row count after ROLLBACK = %d, want 0", count)
	}
}

// TestNode_TransactionCommitPersists verifies a committed transaction's write
// survives on a different connection than the one that wrote it.
func TestNode_TransactionCommitPersists(t *testing.T) {
	db := openNode(t, "node://memory?database=txcommit")

	if _, err := db.Exec("CREATE TABLE notes (id BIGINT PRIMARY KEY, body VARCHAR(64))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO notes (id, body) VALUES (1, 'kept')"); err != nil {
		t.Fatalf("INSERT in tx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var body string
	if err := db.QueryRow("SELECT body FROM notes WHERE id = 1").Scan(&body); err != nil {
		t.Fatalf("SELECT after COMMIT: %v", err)
	}
	if body != "kept" {
		t.Errorf("body = %q, want \"kept\"", body)
	}
}

// TestEmbeddedScheme_CreateTableStillUnavailable pins the behaviour node://
// was added ALONGSIDE rather than instead of: embedded:// has no registry and
// therefore no relational catalog, and must keep failing CREATE TABLE with
// the same typed error. If this test ever starts failing, embedded:// grew a
// registry -- which is a deliberate decision to make, not one to discover
// from a conformance run.
func TestEmbeddedScheme_CreateTableStillUnavailable(t *testing.T) {
	db, err := sql.Open("qrdb", "embedded://memory?database=stillkv")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.Exec("CREATE TABLE widgets (id BIGINT PRIMARY KEY)")
	if err == nil {
		t.Fatal("expected CREATE TABLE over embedded:// to fail without a registry")
	}
	var unavailable *provider.RelationalCatalogUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v (%T), want *provider.RelationalCatalogUnavailableError", err, err)
	}
}

// ---------------------------------------------------------------------------
// Tenant / database resolution
// ---------------------------------------------------------------------------

// TestNode_UnknownTenantSurfacesTypedError verifies a DSN naming a tenant the
// node does not know fails with the registry's own typed error, unwrapped, so
// callers can match it with errors.As.
func TestNode_UnknownTenantSurfacesTypedError(t *testing.T) {
	cfg, err := qrdbdriver.ParseDSN("node://memory?database=scoped&tenant=nosuchtenant")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	c := New(cfg)
	defer func() { _ = c.Close() }()

	_, err = c.Connect(context.Background())
	if err == nil {
		t.Fatal("expected Connect to fail for an unknown tenant")
	}
	var notFound *service.TenantNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("error = %v (%T), want *service.TenantNotFoundError", err, err)
	}
	if notFound.TenantID != "nosuchtenant" {
		t.Errorf("TenantID = %q, want %q", notFound.TenantID, "nosuchtenant")
	}
}

// TestNode_DefaultTenantIsSystem verifies a DSN with no tenant resolves its
// services for the same tenant the SQL session will execute as.
func TestNode_DefaultTenantIsSystem(t *testing.T) {
	cfg, err := qrdbdriver.ParseDSN("node://memory?database=deftenant")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if got := qrdbdriver.ConnectionTenant(cfg); got != tenant.SystemTenantID {
		t.Errorf("ConnectionTenant = %q, want %q", got, tenant.SystemTenantID)
	}
}

// ---------------------------------------------------------------------------
// Connector lifecycle
// ---------------------------------------------------------------------------

// TestConnector_LazyStart verifies New starts nothing: the node only exists
// after the first Connect.
func TestConnector_LazyStart(t *testing.T) {
	cfg, err := qrdbdriver.ParseDSN("node://memory?database=lazy")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	c := New(cfg)
	defer func() { _ = c.Close() }()

	if c.node != nil || c.inner != nil {
		t.Fatal("New must not start the node")
	}

	conn, err := c.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if c.node == nil || c.inner == nil {
		t.Fatal("Connect must have started the node")
	}
}

// TestConnector_ConnectSharesOneNode verifies repeated Connects reuse the
// single node the connector started rather than booting one per connection.
func TestConnector_ConnectSharesOneNode(t *testing.T) {
	cfg, err := qrdbdriver.ParseDSN("node://memory?database=shared")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	c := New(cfg)
	defer func() { _ = c.Close() }()

	first, err := c.Connect(context.Background())
	if err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	defer func() { _ = first.Close() }()
	started := c.node

	second, err := c.Connect(context.Background())
	if err != nil {
		t.Fatalf("second Connect: %v", err)
	}
	defer func() { _ = second.Close() }()

	if c.node != started {
		t.Error("the second Connect started a second node")
	}
}

// TestConnector_CloseIsIdempotent verifies a double Close is a no-op rather
// than a double shutdown.
func TestConnector_CloseIsIdempotent(t *testing.T) {
	cfg, err := qrdbdriver.ParseDSN("node://memory?database=idem")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	c := New(cfg)
	if _, err := c.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestConnector_CloseBeforeConnect verifies closing a connector that never
// started anything is safe.
func TestConnector_CloseBeforeConnect(t *testing.T) {
	cfg, err := qrdbdriver.ParseDSN("node://memory?database=neverused")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if err := New(cfg).Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestConnector_ConnectAfterClose verifies a closed connector refuses new
// connections instead of restarting its node.
func TestConnector_ConnectAfterClose(t *testing.T) {
	cfg, err := qrdbdriver.ParseDSN("node://memory?database=afterclose")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	c := New(cfg)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = c.Connect(context.Background())
	if err == nil {
		t.Fatal("expected Connect on a closed connector to fail")
	}
	var closed *qrdbdriver.ConnectorClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("error = %v (%T), want *ConnectorClosedError", err, err)
	}
}

// TestConnector_Driver verifies the connector reports the qrdb driver.
func TestConnector_Driver(t *testing.T) {
	cfg, err := qrdbdriver.ParseDSN("node://memory?database=drv")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	c := New(cfg)
	defer func() { _ = c.Close() }()

	if _, ok := c.Driver().(*qrdbdriver.Driver); !ok {
		t.Errorf("Driver() = %T, want *driver.Driver", c.Driver())
	}
}

// TestConnector_ScratchDirRemovedOnClose verifies the directory an in-memory
// node needs is created for it and removed again, so a memory DSN leaves
// nothing behind.
func TestConnector_ScratchDirRemovedOnClose(t *testing.T) {
	cfg, err := qrdbdriver.ParseDSN("node://memory?database=scratch")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	c := New(cfg)
	if _, err := c.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	dir := c.scratchDir
	if dir == "" {
		t.Fatal("expected a scratch directory for a DSN with no data dir")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("scratch directory %s should exist while the node runs: %v", dir, err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("scratch directory %s survived Close (stat err = %v)", dir, err)
	}
}

// TestConnector_HonorsDSNDataDir verifies a DSN-supplied data directory is
// used as-is and is NOT removed on Close: the caller owns it.
func TestConnector_HonorsDSNDataDir(t *testing.T) {
	dir := t.TempDir()
	db := openNode(t, "node://"+dir+"?backend=memory&database=ondisk")

	if _, err := db.Exec("CREATE TABLE t (id BIGINT PRIMARY KEY)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("caller-supplied data dir must survive Close: %v", err)
	}
	// The node roots its own state inside the supplied directory.
	if _, err := os.Stat(filepath.Join(dir, "vector")); err != nil {
		t.Errorf("expected the node to have used the supplied data dir: %v", err)
	}
}

// TestConnector_PebbleBackendPersists verifies an on-disk node:// database
// survives a connector restart on the same data directory.
func TestConnector_PebbleBackendPersists(t *testing.T) {
	dir := t.TempDir()
	dsn := "node://" + dir + "?backend=pebble&database=durable"

	first := openNodeNoCleanup(t, dsn)
	if _, err := first.Exec("CREATE TABLE kept (id BIGINT PRIMARY KEY, body VARCHAR(32))"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := first.Exec("INSERT INTO kept (id, body) VALUES (1, 'durable')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	second := openNodeNoCleanup(t, dsn)
	defer func() { _ = second.Close() }()

	var body string
	if err := second.QueryRow("SELECT body FROM kept WHERE id = 1").Scan(&body); err != nil {
		t.Fatalf("SELECT after reopen: %v", err)
	}
	if body != "durable" {
		t.Errorf("body = %q, want \"durable\"", body)
	}
}

// openNodeNoCleanup opens a *sql.DB the caller closes itself, for tests that
// need the node down before the test ends.
func openNodeNoCleanup(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("qrdb", dsn)
	if err != nil {
		t.Fatalf("sql.Open(%q): %v", dsn, err)
	}
	return db
}
