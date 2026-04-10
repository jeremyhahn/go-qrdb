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

// Package integration_test contains complete GenericDAO feature tests exercised
// through the qrdb-sqlite collection virtual table. Each test mirrors a DAO
// operation pattern (ID generation, pagination, filtering, partial updates,
// upsert, etc.) and validates the full path from SQL through the vtab to qrdbd.
package integration_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDAOComplete_MultiProtocol_IDGenerationStrategy inserts documents without
// an explicit _id and verifies that the server auto-assigns a non-zero,
// globally unique ID for each document. This mirrors the DAO's auto-ID
// generation strategy.
func TestDAOComplete_MultiProtocol_IDGenerationStrategy(t *testing.T) {
	const n = 5
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			// Insert without explicit _id (NULL → server generates).
			insertedIDs := make(map[int64]bool, n)
			for i := 0; i < n; i++ {
				result, err := env.db.Exec(
					`INSERT INTO docs (title, score) VALUES (?, ?)`,
					fmt.Sprintf("autoid-doc-%d", i), float64(i+1),
				)
				require.NoError(t, err, "INSERT auto-id doc %d (%s)", i, proto)

				lastID, err := result.LastInsertId()
				require.NoError(t, err, "LastInsertId doc %d (%s)", i, proto)
				assert.NotZero(t, lastID,
					"auto-generated _id must be non-zero for doc %d (%s)", i, proto)
				assert.False(t, insertedIDs[lastID],
					"auto-generated _id %d must be unique (collision at doc %d, %s)", lastID, i, proto)
				insertedIDs[lastID] = true
			}

			// All n auto-IDs must be distinct.
			assert.Len(t, insertedIDs, n,
				"must have %d distinct auto-generated IDs (%s)", n, proto)
		})
	}
}

// TestDAOComplete_MultiProtocol_ExplicitVsAutoID inserts one document with an
// explicit _id and one without, then verifies both coexist with distinct IDs.
// This mirrors dao.Save() accepting either a pre-assigned or zero ID.
func TestDAOComplete_MultiProtocol_ExplicitVsAutoID(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			explicitID := protoDocID(proto, 50100)

			// Insert with explicit ID.
			_, err := env.db.Exec(
				`INSERT INTO docs (_id, title, score) VALUES (?, 'explicit', 1.0)`, explicitID,
			)
			require.NoError(t, err, "INSERT with explicit _id (%s)", proto)

			// Insert without ID (NULL → server generates).
			result, err := env.db.Exec(
				`INSERT INTO docs (title, score) VALUES ('auto', 2.0)`,
			)
			require.NoError(t, err, "INSERT with auto _id (%s)", proto)
			autoID, err := result.LastInsertId()
			require.NoError(t, err, "LastInsertId (%s)", proto)

			assert.NotZero(t, autoID, "auto _id must be non-zero (%s)", proto)
			assert.NotEqual(t, int64(explicitID), autoID,
				"explicit and auto IDs must not collide (%s)", proto)

			// Both documents must be retrievable.
			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT _id, title FROM docs WHERE _id = %d`, explicitID))
			require.Len(t, rows, 1, "explicit-ID doc must be found (%s)", proto)
			assert.Equal(t, "explicit", rows[0]["title"], "explicit doc title (%s)", proto)
		})
	}
}

// TestDAOComplete_MultiProtocol_PageIteration inserts 50 documents then iterates
// through 5 pages of 10 each using LIMIT/OFFSET. Each page must have exactly 10
// rows, no row may appear on two pages, and the union of all pages must equal
// all 50 inserted documents. This mirrors dao.ForEachPage().
func TestDAOComplete_MultiProtocol_PageIteration(t *testing.T) {
	const total = 50
	const pageSize = 10
	const pages = total / pageSize

	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 50110)
			lastID := protoDocID(proto, int64(50110+total-1))

			tx, err := env.db.Begin()
			require.NoError(t, err, "BEGIN (%s)", proto)
			stmt, err := tx.Prepare(`INSERT INTO docs (_id, title, score) VALUES (?, ?, ?)`)
			require.NoError(t, err)
			defer stmt.Close()
			for i := 0; i < total; i++ {
				docID := protoDocID(proto, int64(50110+i))
				_, err := stmt.Exec(docID, fmt.Sprintf("page-iter-%03d", i), float64(i))
				require.NoError(t, err, "INSERT row %d (%s)", i, proto)
			}
			require.NoError(t, tx.Commit(), "COMMIT (%s)", proto)

			seen := make(map[int64]int) // id → page index where it was seen

			for page := 0; page < pages; page++ {
				offset := page * pageSize
				query := fmt.Sprintf(
					`SELECT _id FROM docs WHERE _id >= %d AND _id <= %d ORDER BY _id ASC LIMIT %d OFFSET %d`,
					baseID, lastID, pageSize, offset,
				)
				rows := scanAllRows(t, env.db, query)
				require.Len(t, rows, pageSize,
					"page %d must return exactly %d rows (%s)", page, pageSize, proto)

				for _, row := range rows {
					id := row["_id"].(int64)
					if prevPage, exists := seen[id]; exists {
						t.Errorf("_id %d appeared on both page %d and page %d (%s)",
							id, prevPage, page, proto)
					}
					seen[id] = page
				}
			}

			// Union of all pages must equal total.
			assert.Len(t, seen, total,
				"union of all pages must equal %d unique documents (%s)", total, proto)
		})
	}
}

// TestDAOComplete_MultiProtocol_SortByMultipleFields inserts documents with the
// same title but different scores, then verifies a combined multi-column ORDER
// BY (title ASC, score DESC) returns the expected row sequence.
func TestDAOComplete_MultiProtocol_SortByMultipleFields(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 50120)
			lastID := protoDocID(proto, 50124)

			// Two groups: "alpha" and "beta", each with three different scores.
			docs := []struct {
				id    int64
				title string
				score float64
			}{
				{protoDocID(proto, 50120), "alpha", 3.0},
				{protoDocID(proto, 50121), "alpha", 1.0},
				{protoDocID(proto, 50122), "alpha", 2.0},
				{protoDocID(proto, 50123), "beta", 9.0},
				{protoDocID(proto, 50124), "beta", 5.0},
			}

			for _, d := range docs {
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, '%s', %g)`,
						d.id, d.title, d.score),
				)
				require.NoError(t, err, "INSERT doc id=%d (%s)", d.id, proto)
			}

			// ORDER BY title ASC, score DESC.
			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT title, score FROM docs WHERE _id >= %d AND _id <= %d ORDER BY title ASC, score DESC`,
					baseID, lastID))
			require.Len(t, rows, 5, "must return 5 rows (%s)", proto)

			// Expected order: alpha/3, alpha/2, alpha/1, beta/9, beta/5.
			expected := []struct {
				title string
				score float64
			}{
				{"alpha", 3.0},
				{"alpha", 2.0},
				{"alpha", 1.0},
				{"beta", 9.0},
				{"beta", 5.0},
			}
			for i, e := range expected {
				assert.Equal(t, e.title, rows[i]["title"],
					"row %d title mismatch (%s)", i, proto)
				assert.InDelta(t, e.score, rows[i]["score"], 0.001,
					"row %d score mismatch (%s)", i, proto)
			}
		})
	}
}

// TestDAOComplete_MultiProtocol_FilterOperators inserts 10 documents with scores
// 1.0..10.0 and verifies all six comparison operators plus BETWEEN. This mirrors
// every filter operator available in the DAO query builder.
func TestDAOComplete_MultiProtocol_FilterOperators(t *testing.T) {
	const n = 10
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 50130)
			lastID := protoDocID(proto, int64(50130+n-1))

			for i := 1; i <= n; i++ {
				docID := protoDocID(proto, int64(50130+i-1))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'filter-%d', %g)`,
						docID, i, float64(i)),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i, proto)
			}

			filter := func(op, extra string, want int) {
				t.Helper()
				q := fmt.Sprintf(
					`SELECT COUNT(*) AS c FROM docs WHERE score %s AND _id >= %d AND _id <= %d %s`,
					op, baseID, lastID, extra,
				)
				rows := scanAllRows(t, env.db, q)
				require.Len(t, rows, 1, "COUNT query must return 1 row: %s (%s)", q, proto)
				assert.Equal(t, int64(want), rows[0]["c"],
					"filter %q expected %d rows (%s)", op, want, proto)
			}

			filter("= 5.0", "", 1)
			filter("> 5.0", "", 5)
			filter(">= 5.0", "", 6)
			filter("< 5.0", "", 4)
			filter("<= 5.0", "", 5)
			filter("!= 5.0", "", 9)
			filter("BETWEEN 3.0 AND 7.0", "", 5)
		})
	}
}

// TestDAOComplete_MultiProtocol_CountWithFilters inserts 20 documents with
// mixed title groups and varying scores, then verifies total count and two
// conditional counts. This mirrors dao.Count() and
// dao.Find().Where(...).Count().
func TestDAOComplete_MultiProtocol_CountWithFilters(t *testing.T) {
	const n = 20
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 50140)
			lastID := protoDocID(proto, int64(50140+n-1))

			// First 10 in group-a, last 10 in group-b; scores 1..20.
			for i := 0; i < n; i++ {
				group := "group-a"
				if i >= 10 {
					group = "group-b"
				}
				docID := protoDocID(proto, int64(50140+i))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, '%s-doc', %g)`,
						docID, group, float64(i+1)),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i, proto)
			}

			// Total count.
			totalRows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT COUNT(*) AS c FROM docs WHERE _id >= %d AND _id <= %d`,
					baseID, lastID))
			require.Len(t, totalRows, 1)
			assert.Equal(t, int64(n), totalRows[0]["c"],
				"total COUNT must be %d (%s)", n, proto)

			// Count for group-a (title LIKE 'group-a%').
			groupARows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT COUNT(*) AS c FROM docs WHERE title LIKE 'group-a%%' AND _id >= %d AND _id <= %d`,
					baseID, lastID))
			require.Len(t, groupARows, 1)
			assert.Equal(t, int64(10), groupARows[0]["c"],
				"group-a COUNT must be 10 (%s)", proto)

			// Count where score > 10 (docs with scores 11..20 → 10 docs).
			highScoreRows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT COUNT(*) AS c FROM docs WHERE score > 10.0 AND _id >= %d AND _id <= %d`,
					baseID, lastID))
			require.Len(t, highScoreRows, 1)
			assert.Equal(t, int64(10), highScoreRows[0]["c"],
				"score>10 COUNT must be 10 (%s)", proto)
		})
	}
}

// TestDAOComplete_MultiProtocol_UpdatePartialFields inserts a document with all
// fields set and performs two successive partial UPDATE operations, verifying
// after each that only the targeted field changed and other fields are preserved.
func TestDAOComplete_MultiProtocol_UpdatePartialFields(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			docID := protoDocID(proto, 50150)

			// Full INSERT.
			_, err := env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title, body, score) VALUES (%d, 'original-title', 'original-body', 1.0)`, docID),
			)
			require.NoError(t, err, "INSERT full doc (%s)", proto)

			// UPDATE score only.
			_, err = env.db.Exec(
				fmt.Sprintf(`UPDATE docs SET score = 42.0 WHERE _id = %d`, docID),
			)
			require.NoError(t, err, "UPDATE score (%s)", proto)

			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT title, body, score FROM docs WHERE _id = %d`, docID))
			require.Len(t, rows, 1, "doc must still exist after score UPDATE (%s)", proto)
			assert.Equal(t, "original-title", rows[0]["title"],
				"title must be unchanged after score UPDATE (%s)", proto)
			assert.Equal(t, "original-body", rows[0]["body"],
				"body must be unchanged after score UPDATE (%s)", proto)
			assert.InDelta(t, 42.0, rows[0]["score"], 0.001,
				"score must be updated (%s)", proto)

			// UPDATE title only.
			_, err = env.db.Exec(
				fmt.Sprintf(`UPDATE docs SET title = 'updated-title' WHERE _id = %d`, docID),
			)
			require.NoError(t, err, "UPDATE title (%s)", proto)

			rows = scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT title, body, score FROM docs WHERE _id = %d`, docID))
			require.Len(t, rows, 1, "doc must still exist after title UPDATE (%s)", proto)
			assert.Equal(t, "updated-title", rows[0]["title"],
				"title must be updated (%s)", proto)
			assert.Equal(t, "original-body", rows[0]["body"],
				"body must still be unchanged after title UPDATE (%s)", proto)
			assert.InDelta(t, 42.0, rows[0]["score"], 0.001,
				"score from previous UPDATE must be preserved (%s)", proto)
		})
	}
}

// TestDAOComplete_MultiProtocol_DeleteWithFilter inserts 10 documents and
// deletes those with score > 7.0 using a WHERE filter that also scopes by ID
// range. Only documents with score <= 7.0 must remain.
func TestDAOComplete_MultiProtocol_DeleteWithFilter(t *testing.T) {
	const n = 10
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 50160)
			lastID := protoDocID(proto, int64(50160+n-1))

			for i := 1; i <= n; i++ {
				docID := protoDocID(proto, int64(50160+i-1))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'del-filter-%d', %g)`,
						docID, i, float64(i)),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i, proto)
			}

			// DELETE where score > 7.0 within our ID range.
			_, err := env.db.Exec(
				fmt.Sprintf(`DELETE FROM docs WHERE score > 7.0 AND _id >= %d AND _id <= %d`,
					baseID, lastID),
			)
			require.NoError(t, err, "DELETE with filter (%s)", proto)

			// Only docs with scores 1..7 must remain (7 docs).
			remaining := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT score FROM docs WHERE _id >= %d AND _id <= %d ORDER BY score ASC`,
					baseID, lastID))
			require.Len(t, remaining, 7,
				"must have 7 docs remaining after filtered DELETE (%s)", proto)

			for _, row := range remaining {
				score, _ := row["score"].(float64)
				assert.LessOrEqual(t, score, 7.0,
					"all remaining docs must have score <= 7.0 (%s)", proto)
			}
		})
	}
}

// TestDAOComplete_MultiProtocol_UpsertPattern inserts a document with an
// explicit ID, then attempts to insert the same ID again. If a duplicate-key
// error occurs, an UPDATE is issued instead (standard upsert pattern). The final
// state must reflect the updated fields. This mirrors dao.Save() upsert behavior.
func TestDAOComplete_MultiProtocol_UpsertPattern(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			docID := protoDocID(proto, 50170)

			// Initial INSERT.
			_, err := env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'original', 1.0)`, docID),
			)
			require.NoError(t, err, "INSERT original doc (%s)", proto)

			// Attempt to INSERT again with the same _id.
			_, dupErr := env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'duplicate', 99.0)`, docID),
			)

			if dupErr != nil {
				// Expected: duplicate key → fall through to UPDATE.
				assert.True(t,
					strings.Contains(dupErr.Error(), "UNIQUE") ||
						strings.Contains(dupErr.Error(), "duplicate") ||
						strings.Contains(dupErr.Error(), "exists") ||
						strings.Contains(dupErr.Error(), "constraint"),
					"duplicate INSERT error must be a constraint error (%s): %v", proto, dupErr)

				_, err = env.db.Exec(
					fmt.Sprintf(`UPDATE docs SET title = 'upserted', score = 99.0 WHERE _id = %d`, docID),
				)
				require.NoError(t, err, "UPDATE after duplicate INSERT error (%s)", proto)
			}
			// If no error: the vtab accepted the second INSERT as an upsert. Both are valid.

			// Final state: one row with the latest values.
			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT _id, title, score FROM docs WHERE _id = %d`, docID))
			require.Len(t, rows, 1, "exactly one doc must exist after upsert (%s)", proto)
			assert.InDelta(t, 99.0, rows[0]["score"], 0.001,
				"upserted doc must have score=99.0 (%s)", proto)
		})
	}
}

// TestDAOComplete_MultiProtocol_EmptyCollection verifies that an empty
// collection returns zero rows, zero COUNT, and NULL aggregates without
// producing any errors. This tests graceful empty-set handling at every layer.
func TestDAOComplete_MultiProtocol_EmptyCollection(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 50180)
			lastID := protoDocID(proto, 50189) // 10-slot window, nothing inserted

			// SELECT must return no rows.
			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT * FROM docs WHERE _id >= %d AND _id <= %d`, baseID, lastID))
			require.Empty(t, rows, "empty window must return 0 rows (%s)", proto)

			// COUNT must be 0.
			countRows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT COUNT(*) AS c FROM docs WHERE _id >= %d AND _id <= %d`, baseID, lastID))
			require.Len(t, countRows, 1)
			assert.Equal(t, int64(0), countRows[0]["c"],
				"COUNT on empty window must be 0 (%s)", proto)

			// SUM and AVG must be NULL.
			aggRows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT SUM(score) AS s, AVG(score) AS a FROM docs WHERE _id >= %d AND _id <= %d`,
					baseID, lastID))
			require.Len(t, aggRows, 1,
				"SUM/AVG on empty set must return 1 aggregate row (%s)", proto)
			assert.Nil(t, aggRows[0]["s"],
				"SUM on empty set must be NULL (%s)", proto)
			assert.Nil(t, aggRows[0]["a"],
				"AVG on empty set must be NULL (%s)", proto)
		})
	}
}

// TestDAOComplete_MultiProtocol_LargeFieldValues inserts a document with a 10 KB
// title and a 100 KB body, then verifies exact round-trip equality and correct
// LENGTH() values. This exercises the vtab's handling of large text field values.
func TestDAOComplete_MultiProtocol_LargeFieldValues(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			docID := protoDocID(proto, 50190)

			// Build large strings.
			largeTitle := strings.Repeat("T", 10*1024) // 10 KB
			largeBody := strings.Repeat("B", 100*1024) // 100 KB

			_, err := env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title, body, score) VALUES (%d, ?, ?, 1.0)`, docID),
				largeTitle, largeBody,
			)
			require.NoError(t, err, "INSERT large field doc (%s)", proto)

			// Exact value round-trip.
			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT title, body FROM docs WHERE _id = %d`, docID))
			require.Len(t, rows, 1, "large-field doc must be found (%s)", proto)
			assert.Equal(t, largeTitle, rows[0]["title"],
				"title must round-trip exactly (%s)", proto)
			assert.Equal(t, largeBody, rows[0]["body"],
				"body must round-trip exactly (%s)", proto)

			// LENGTH() validation.
			lenRows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT LENGTH(title) AS lt, LENGTH(body) AS lb FROM docs WHERE _id = %d`, docID))
			require.Len(t, lenRows, 1)
			assert.Equal(t, int64(len(largeTitle)), lenRows[0]["lt"],
				"LENGTH(title) mismatch (%s)", proto)
			assert.Equal(t, int64(len(largeBody)), lenRows[0]["lb"],
				"LENGTH(body) mismatch (%s)", proto)
		})
	}
}

// TestDAOComplete_MultiProtocol_TypeCoercion inserts documents using integer
// score (not float), a large int64 _id, and verifies that the collection vtab
// returns the correct Go types through database/sql scanning. This confirms that
// the vtab's type affinity rules are compatible with real DAO usage.
func TestDAOComplete_MultiProtocol_TypeCoercion(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			// Integer score (no decimal point).
			intScoreID := protoDocID(proto, 50195)
			_, err := env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'int-score', 5)`, intScoreID),
			)
			require.NoError(t, err, "INSERT integer score (%s)", proto)

			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT score FROM docs WHERE _id = %d`, intScoreID))
			require.Len(t, rows, 1, "int-score doc must be found (%s)", proto)
			// score column has REAL affinity; whether it comes back as int64 or
			// float64 depends on SQLite's affinity rules — either is acceptable.
			switch v := rows[0]["score"].(type) {
			case float64:
				assert.InDelta(t, 5.0, v, 0.001, "score as float64 (%s)", proto)
			case int64:
				assert.Equal(t, int64(5), v, "score as int64 (%s)", proto)
			default:
				t.Errorf("score has unexpected type %T (%s)", rows[0]["score"], proto)
			}

			// Large int64 _id.
			largeID := protoDocID(proto, 50196)
			_, err = env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'large-id', 1.0)`, largeID),
			)
			require.NoError(t, err, "INSERT large-ID doc (%s)", proto)

			idRows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT _id FROM docs WHERE _id = %d`, largeID))
			require.Len(t, idRows, 1, "large-ID doc must be found (%s)", proto)
			gotID, _ := idRows[0]["_id"].(int64)
			assert.Equal(t, int64(largeID), gotID,
				"large int64 _id must round-trip exactly (%s)", proto)
		})
	}
}
