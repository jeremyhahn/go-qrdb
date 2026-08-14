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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
)

// ---------------------------------------------------------------------------
// The relational keyspace is not the KV keyspace
//
// A relational row is stored as an ordinary MVCC key under "__tbl:", its
// secondary index entries under "__idx:", and its AUTO_INCREMENT counter
// under "__seq:". Those live in the same keyspace the plain KV API
// enumerates, so unless the internal-key guard covers them a caller holding
// nothing but KV access to the (tenant, database) can enumerate -- and read
// the raw bytes of -- every row of every table in it.
//
// These tests exercise both halves of that guard from the same node:
//
//   - KV enumeration must not disclose the relational keyspace.
//   - The relational layer, which reads its own rows through the very same
//     enumeration calls, must keep working. A guard that hides "__tbl:" from
//     the relational layer as well turns every table scan into a silent
//     zero-row result, which is strictly worse than the disclosure.
//
// Both are asserted here, against one live node, because they are the two
// sides of a single trade-off and testing either alone proves nothing.
// ---------------------------------------------------------------------------

// nodeWithRegistry boots a node:// connector and returns a *sql.DB over it
// together with the KV servicer the node's own service registry resolves for
// (system tenant, database) -- the exact handle any KV-authorized caller
// gets, with no relational surface attached to it.
func nodeWithRegistry(t *testing.T, dsn, database string) (*sql.DB, service.KVServicer) {
	t.Helper()

	cfg, err := qrdbdriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN(%q): %v", dsn, err)
	}
	c := New(cfg)
	t.Cleanup(func() { _ = c.Close() })

	db := sql.OpenDB(c)
	t.Cleanup(func() { _ = db.Close() })

	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	srv := c.node.Server()
	if srv == nil {
		t.Fatal("node has no server")
	}
	reg := srv.ServiceRegistry()
	if reg == nil {
		t.Fatal("node has no service registry")
	}
	kvSvc, err := reg.KVService(tenant.SystemTenantID, database)
	if err != nil {
		t.Fatalf("KVService(%q, %q): %v", tenant.SystemTenantID, database, err)
	}
	return db, kvSvc
}

// relationalPrefixes are the reserved prefixes holding relational rows,
// secondary index entries, and auto-increment counters.
var relationalPrefixes = []string{"__tbl:", "__idx:", "__seq:"}

// relationalPrefixOf returns the reserved relational prefix key begins with,
// or "" when it begins with none of them.
func relationalPrefixOf(key string) string {
	for _, p := range relationalPrefixes {
		if strings.HasPrefix(key, p) {
			return p
		}
	}
	return ""
}

// TestNode_KVScanDoesNotDiscloseRelationalRows is the regression test for the
// disclosure: a KV Scan of the whole keyspace returned the encoded rows of
// every relational table, secret column values and all.
func TestNode_KVScanDoesNotDiscloseRelationalRows(t *testing.T) {
	db, kvSvc := nodeWithRegistry(t, "node://memory?database=reach", "reach")

	if _, err := db.Exec(`CREATE TABLE secrets (
		id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		payload VARCHAR(64) NOT NULL,
		UNIQUE KEY uq_payload (payload)
	)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO secrets (payload) VALUES ('classified')`); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	ctx := context.Background()
	pairs, err := kvSvc.Scan(ctx, "")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for k, v := range pairs {
		if p := relationalPrefixOf(k); p != "" {
			t.Errorf("KV Scan disclosed reserved relational key %q (prefix %q)", k, p)
		}
		if strings.Contains(string(v), "classified") {
			t.Errorf("KV Scan disclosed row content at key %q", k)
		}
	}

	keys, err := kvSvc.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, k := range keys {
		if p := relationalPrefixOf(k); p != "" {
			t.Errorf("KV List disclosed reserved relational key %q (prefix %q)", k, p)
		}
	}
}

// TestNode_RelationalLayerStillReadsItsOwnKeyspace is the counter-test to the
// one above: it fails if the guard was applied so broadly that the relational
// layer can no longer enumerate its own rows.
//
// Every assertion here is a distinct read path over a reserved prefix: a full
// table scan and COUNT(*) over "__tbl:", an indexed lookup and a unique
// violation over "__idx:", and an AUTO_INCREMENT allocation over "__seq:".
// The table holds several rows so that a scan silently returning nothing is
// unmistakable rather than plausibly empty.
func TestNode_RelationalLayerStillReadsItsOwnKeyspace(t *testing.T) {
	db, _ := nodeWithRegistry(t, "node://memory?database=counter", "counter")

	if _, err := db.Exec(`CREATE TABLE t (
		id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
		name VARCHAR(64) NOT NULL,
		grp VARCHAR(16) NOT NULL,
		UNIQUE KEY uq_name (name),
		KEY ix_grp (grp)
	)`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	inserts := []string{
		`INSERT INTO t (name, grp) VALUES ('alpha', 'a')`,
		`INSERT INTO t (name, grp) VALUES ('bravo', 'b')`,
		`INSERT INTO t (name, grp) VALUES ('charlie', 'a')`,
		`INSERT INTO t (name, grp) VALUES ('delta', 'b')`,
	}
	for _, stmt := range inserts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// Full table scan over "__tbl:".
	rows, err := db.Query(`SELECT id, name, grp FROM t ORDER BY id`)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	var names []string
	var ids []uint64
	for rows.Next() {
		var id uint64
		var name, grp string
		if err := rows.Scan(&id, &name, &grp); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close rows: %v", err)
	}
	if len(names) != len(inserts) {
		t.Fatalf("full table scan returned %d rows (%v), want %d -- the internal-key guard is hiding the table's own rows",
			len(names), names, len(inserts))
	}

	// AUTO_INCREMENT over "__seq:": the ids must be the four distinct values
	// the sequence handed out, not four zeroes.
	seen := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			t.Fatalf("AUTO_INCREMENT produced a zero id: %v", ids)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("AUTO_INCREMENT produced a duplicate id: %v", ids)
		}
		seen[id] = struct{}{}
	}

	// COUNT(*) takes the MVCC range path rather than the scan path.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM t`).Scan(&count); err != nil {
		t.Fatalf("COUNT(*): %v", err)
	}
	if count != len(inserts) {
		t.Fatalf("COUNT(*) = %d, want %d", count, len(inserts))
	}

	// Indexed lookup over "__idx:".
	var got string
	if err := db.QueryRow(`SELECT grp FROM t WHERE name = 'charlie'`).Scan(&got); err != nil {
		t.Fatalf("indexed lookup by unique key: %v", err)
	}
	if got != "a" {
		t.Fatalf("indexed lookup returned grp %q, want %q", got, "a")
	}

	var grouped int
	if err := db.QueryRow(`SELECT COUNT(*) FROM t WHERE grp = 'b'`).Scan(&grouped); err != nil {
		t.Fatalf("indexed lookup by secondary key: %v", err)
	}
	if grouped != 2 {
		t.Fatalf("COUNT(*) WHERE grp = 'b' = %d, want 2", grouped)
	}

	// A unique violation is only detectable by reading the index entries, so
	// a silently empty "__idx:" read would let this INSERT succeed.
	if _, err := db.Exec(`INSERT INTO t (name, grp) VALUES ('alpha', 'z')`); err == nil {
		t.Fatal("duplicate INSERT on a UNIQUE column succeeded -- the unique index is not being read")
	}
}
