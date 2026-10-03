//go:build qrdb_sql

// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// End-to-end view tests: real CREATE VIEW / SELECT / DROP VIEW statements
// through go-mysql-server's analyzer against the qrdb provider and its
// Raft-shaped catalog harness. These are the statements GORM's TestMigrateView
// issues verbatim.

package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newViewProvider builds a provider with one seeded relational table.
func newViewProvider(t *testing.T) *Provider {
	t.Helper()
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop",
		"CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, name VARCHAR(64), age BIGINT)")
	mustExecQuery(t, prov, "shop",
		"INSERT INTO users (id, name, age) VALUES (1, 'alice', 30), (2, 'bob', 17), (3, 'carol', 45)")
	return prov
}

// TestCreateView_SelectsThroughLiveData covers the full lifecycle: a created
// view is queryable, reflects data changes made AFTER its creation (a view is
// a live subquery, not a snapshot), and is gone after DROP VIEW.
func TestCreateView_SelectsThroughLiveData(t *testing.T) {
	prov := newViewProvider(t)

	mustExecQuery(t, prov, "shop",
		"CREATE VIEW adults AS SELECT id, name FROM users WHERE age >= 18")

	_, rows := mustExecQuery(t, prov, "shop", "SELECT name FROM adults ORDER BY name")
	require.Len(t, rows, 2)
	assert.Equal(t, "alice", rows[0][0])
	assert.Equal(t, "carol", rows[1][0])

	// Data inserted after the view exists must show through it.
	mustExecQuery(t, prov, "shop", "INSERT INTO users (id, name, age) VALUES (4, 'dave', 52)")
	_, rows = mustExecQuery(t, prov, "shop", "SELECT COUNT(*) FROM adults")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 3, rows[0][0])

	mustExecQuery(t, prov, "shop", "DROP VIEW adults")
	_, _, err := execQuery(prov, "shop", "SELECT * FROM adults")
	require.Error(t, err, "a dropped view must not resolve")
}

// TestCreateView_DuplicateNameRefused pins the ErrExistingView mapping the
// analyzer branches on for CREATE OR REPLACE / IF NOT EXISTS.
func TestCreateView_DuplicateNameRefused(t *testing.T) {
	prov := newViewProvider(t)

	mustExecQuery(t, prov, "shop", "CREATE VIEW v1 AS SELECT id FROM users")
	_, _, err := execQuery(prov, "shop", "CREATE VIEW v1 AS SELECT name FROM users")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")

	// OR REPLACE must succeed over the existing name and swap the definition.
	mustExecQuery(t, prov, "shop", "CREATE OR REPLACE VIEW v1 AS SELECT name FROM users")
	_, rows := mustExecQuery(t, prov, "shop", "SELECT * FROM v1 ORDER BY name LIMIT 1")
	require.Len(t, rows, 1)
	assert.Equal(t, "alice", rows[0][0])
}

// TestDropView_MissingRefusedUnlessIfExists pins the ErrViewDoesNotExist
// mapping DROP VIEW IF EXISTS depends on.
func TestDropView_MissingRefusedUnlessIfExists(t *testing.T) {
	prov := newViewProvider(t)

	_, _, err := execQuery(prov, "shop", "DROP VIEW no_such_view")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")

	// IF EXISTS must swallow exactly that error.
	mustExecQuery(t, prov, "shop", "DROP VIEW IF EXISTS no_such_view")
}

// TestViews_SurviveEngineRestart pins persistence: the definition lives in
// the replicated catalog, not in engine memory, so a fresh engine over the
// same provider still serves it. This is the property that makes views
// durable across daemon restarts and visible on every replica.
func TestViews_SurviveEngineRestart(t *testing.T) {
	prov := newViewProvider(t)

	mustExecQuery(t, prov, "shop", "CREATE VIEW named AS SELECT name FROM users WHERE id = 1")

	// execQuery builds a brand-new engine per call, so this SELECT already
	// crosses an "engine restart"; make the point explicit anyway.
	_, rows := mustExecQuery(t, prov, "shop", "SELECT * FROM named")
	require.Len(t, rows, 1)
	assert.Equal(t, "alice", rows[0][0])
}

// TestAlterColumnType_DataIncompatibleFailsAtomically pins PostgreSQL's
// atomic-ALTER promise for the case that once wedged an entire conformance
// run: ALTER a varchar column to bigint over rows whose values cannot
// convert.
//
// The statement must FAIL — and the table must remain fully servable with
// its original definition and rows. The upstream defect chain this guards
// against: the rewrite converted rows as it wrote them, the conversion
// failure left the DDL journal in flight, and every later access to the
// table (including information_schema listings, which touch every table)
// re-attempted the impossible rewrite forever. One bad ALTER took down 59
// unrelated conformance tests.
func TestAlterColumnType_DataIncompatibleFailsAtomically(t *testing.T) {
	prov := newViewProvider(t)

	// users.name holds 'alice', 'bob', 'carol' — none convertible to BIGINT.
	_, _, err := execQuery(prov, "shop", "ALTER TABLE users MODIFY name BIGINT")
	require.Error(t, err, "an inconvertible ALTER must fail")

	// The table must still serve its ORIGINAL rows under the ORIGINAL
	// definition — not be wedged behind an in-flight rewrite journal.
	_, rows := mustExecQuery(t, prov, "shop", "SELECT name FROM users ORDER BY name")
	require.Len(t, rows, 3)
	assert.Equal(t, "alice", rows[0][0])

	// And further DDL must work: the failed ALTER left no journal behind.
	mustExecQuery(t, prov, "shop", "ALTER TABLE users MODIFY name VARCHAR(128)")
	_, rows = mustExecQuery(t, prov, "shop", "SELECT COUNT(*) FROM users")
	assert.EqualValues(t, 3, rows[0][0])
}
