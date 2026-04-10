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

// Package integration_test contains DAO-like integration tests that exercise
// GenericDAO / entity persistence patterns through the qrdb-sqlite collection
// virtual table. Each test mirrors a DAO operation (Save, Get, Page, Count,
// Find, Delete) and proves the full path:
//
//	DAO SQL → collection vtab → SDK adapter → transport → qrdbd → Raft
package integration_test

import (
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// CRUD lifecycle — mirrors dao.Save / dao.Get / dao.Delete
// ---------------------------------------------------------------------------

// TestDAO_MultiProtocol_CreateReadUpdateDelete exercises a complete entity
// lifecycle through the collection virtual table: INSERT → SELECT by ID →
// UPDATE → verify update → DELETE → verify gone. This mirrors the DAO pattern:
// dao.Save() → dao.Get() → dao.Save() → dao.Delete().
func TestDAO_MultiProtocol_CreateReadUpdateDelete(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			docID := protoDocID(proto, 30000)

			// CREATE — mirrors dao.Save(entity) for a new entity.
			_, err := env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title, body, score) VALUES (%d, 'entity-v1', 'initial body', 1.0)`, docID),
			)
			require.NoError(t, err, "dao CREATE (%s)", proto)

			// READ — mirrors dao.Get(id).
			rows := scanAllRows(t, env.db, fmt.Sprintf(`SELECT _id, title, body, score FROM docs WHERE _id = %d`, docID))
			require.Len(t, rows, 1, "dao GET must return one row (%s)", proto)
			assert.Equal(t, int64(docID), rows[0]["_id"])
			assert.Equal(t, "entity-v1", rows[0]["title"])
			assert.Equal(t, "initial body", rows[0]["body"])
			assert.InDelta(t, 1.0, rows[0]["score"], 0.001)

			// UPDATE — mirrors dao.Save(updatedEntity).
			_, err = env.db.Exec(
				fmt.Sprintf(`UPDATE docs SET title = 'entity-v2', score = 9.5 WHERE _id = %d`, docID),
			)
			require.NoError(t, err, "dao UPDATE (%s)", proto)

			// Verify update persisted.
			rows = scanAllRows(t, env.db, fmt.Sprintf(`SELECT title, score FROM docs WHERE _id = %d`, docID))
			require.Len(t, rows, 1, "updated doc must still exist (%s)", proto)
			assert.Equal(t, "entity-v2", rows[0]["title"])
			assert.InDelta(t, 9.5, rows[0]["score"], 0.001)

			// DELETE — mirrors dao.Delete(id).
			_, err = env.db.Exec(fmt.Sprintf(`DELETE FROM docs WHERE _id = %d`, docID))
			require.NoError(t, err, "dao DELETE (%s)", proto)

			// Verify gone.
			assertRowCount(t, env.db,
				fmt.Sprintf(`SELECT * FROM docs WHERE _id = %d`, docID), 0)
		})
	}
}

// ---------------------------------------------------------------------------
// Pagination — mirrors dao.Page(ctx, PageQuery{Size, Page})
// ---------------------------------------------------------------------------

// TestDAO_MultiProtocol_PageQuery inserts 50 documents and pages through them
// using LIMIT/OFFSET to verify that each page of 10 returns the correct IDs.
// This mirrors dao.Page(ctx, PageQuery{Size: 10, Page: n}).
func TestDAO_MultiProtocol_PageQuery(t *testing.T) {
	const total = 50
	const pageSize = 10
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 31000)

			tx, err := env.db.Begin()
			require.NoError(t, err, "BEGIN (%s)", proto)
			stmt, err := tx.Prepare(`INSERT INTO docs (_id, title, score) VALUES (?, ?, ?)`)
			require.NoError(t, err)
			defer stmt.Close()

			for i := 0; i < total; i++ {
				docID := protoDocID(proto, int64(31000+i))
				_, err := stmt.Exec(docID, fmt.Sprintf("page-doc-%03d", i), float64(i))
				require.NoError(t, err, "INSERT row %d (%s)", i, proto)
			}
			require.NoError(t, tx.Commit(), "COMMIT (%s)", proto)

			lastID := protoDocID(proto, int64(31000+total-1))

			// Page through all records, verifying each page is correct.
			for page := 0; page < total/pageSize; page++ {
				offset := page * pageSize
				query := fmt.Sprintf(
					`SELECT _id FROM docs WHERE _id >= %d AND _id <= %d ORDER BY _id ASC LIMIT %d OFFSET %d`,
					baseID, lastID, pageSize, offset,
				)
				rows := scanAllRows(t, env.db, query)
				require.Len(t, rows, pageSize,
					"page %d must return %d rows (%s)", page, pageSize, proto)

				// Each row's _id must match the expected ID for its position.
				for pos, row := range rows {
					expectedID := protoDocID(proto, int64(31000+offset+pos))
					assert.Equal(t, int64(expectedID), row["_id"],
						"page %d pos %d ID mismatch (%s)", page, pos, proto)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Sort — mirrors dao.Page(ctx, PageQuery{SortBy, SortOrder})
// ---------------------------------------------------------------------------

// TestDAO_MultiProtocol_SortByField inserts documents with scores in random
// order and verifies ascending and descending ORDER BY on the score field.
// This mirrors dao.Page(ctx, PageQuery{SortBy: "score", SortOrder: "asc"}).
func TestDAO_MultiProtocol_SortByField(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 32000)
			lastID := protoDocID(proto, 32004)

			// Insert in deliberately unsorted order.
			unsorted := []float64{5.0, 1.0, 9.0, 3.0, 7.0}
			for i, score := range unsorted {
				docID := protoDocID(proto, int64(32000+i))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'sort-%d', %g)`,
						docID, i, score),
				)
				require.NoError(t, err, "INSERT (%s)", proto)
			}

			wantAsc := append([]float64(nil), unsorted...)
			sort.Float64s(wantAsc)

			wantDesc := make([]float64, len(wantAsc))
			for i, v := range wantAsc {
				wantDesc[len(wantAsc)-1-i] = v
			}

			// Ascending sort.
			ascRows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT score FROM docs WHERE _id >= %d AND _id <= %d ORDER BY score ASC LIMIT 5`, baseID, lastID))
			require.Len(t, ascRows, 5, "ASC sort must return 5 rows (%s)", proto)
			for i, r := range ascRows {
				assert.InDelta(t, wantAsc[i], r["score"], 0.001, "ASC row %d (%s)", i, proto)
			}

			// Descending sort.
			descRows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT score FROM docs WHERE _id >= %d AND _id <= %d ORDER BY score DESC LIMIT 5`, baseID, lastID))
			require.Len(t, descRows, 5, "DESC sort must return 5 rows (%s)", proto)
			for i, r := range descRows {
				assert.InDelta(t, wantDesc[i], r["score"], 0.001, "DESC row %d (%s)", i, proto)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Count — mirrors dao.Count(ctx) and dao.Find(ctx).Where(...).Count()
// ---------------------------------------------------------------------------

// TestDAO_MultiProtocol_CountQuery inserts N documents and verifies total and
// conditional counts. This mirrors dao.Count(ctx) and
// dao.Find(ctx).Where("score > ?", 5.0).Count().
func TestDAO_MultiProtocol_CountQuery(t *testing.T) {
	const n = 10
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 33000)
			lastID := protoDocID(proto, int64(33000+n-1))

			// scores 1..10; scores > 5 are IDs at offsets 5..9 (5 docs).
			for i := 0; i < n; i++ {
				docID := protoDocID(proto, int64(33000+i))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'count-%d', %g)`,
						docID, i, float64(i+1)),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i, proto)
			}

			// Total count.
			assertRowCount(t, env.db,
				fmt.Sprintf(`SELECT * FROM docs WHERE _id >= %d AND _id <= %d`, baseID, lastID), n)

			// Conditional count: score > 5.
			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT COUNT(*) AS cnt FROM docs WHERE score > 5.0 AND _id >= %d AND _id <= %d`,
					baseID, lastID))
			require.Len(t, rows, 1)
			assert.Equal(t, int64(5), rows[0]["cnt"], "count where score > 5 (%s)", proto)
		})
	}
}

// ---------------------------------------------------------------------------
// Filter — mirrors dao.Find(ctx).Where(...).Execute()
// ---------------------------------------------------------------------------

// TestDAO_MultiProtocol_FilterByField inserts documents with different titles
// and verifies exact-match and LIKE filters. This mirrors
// dao.Find(ctx).Where("title = ?", "exact").Execute() and
// dao.Find(ctx).Where("title LIKE ?", "%partial%").Execute().
func TestDAO_MultiProtocol_FilterByField(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 34000)
			lastID := protoDocID(proto, 34003)

			titles := []string{
				"exact-match",
				"partial-match-one",
				"partial-match-two",
				"no-match",
			}
			for i, title := range titles {
				docID := protoDocID(proto, int64(34000+i))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, '%s', 1.0)`, docID, title),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i, proto)
			}

			// Exact match filter.
			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT _id, title FROM docs WHERE title = 'exact-match' AND _id >= %d AND _id <= %d`, baseID, lastID))
			require.Len(t, rows, 1, "exact filter must return 1 row (%s)", proto)
			assert.Equal(t, "exact-match", rows[0]["title"])

			// LIKE filter.
			rows = scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT _id, title FROM docs WHERE title LIKE '%%partial%%' AND _id >= %d AND _id <= %d ORDER BY title ASC`, baseID, lastID))
			require.Len(t, rows, 2, "LIKE filter must return 2 rows (%s)", proto)
			assert.Equal(t, "partial-match-one", rows[0]["title"])
			assert.Equal(t, "partial-match-two", rows[1]["title"])
		})
	}
}

// ---------------------------------------------------------------------------
// FindFirst — mirrors dao.Find(ctx).Where(...).First()
// ---------------------------------------------------------------------------

// TestDAO_MultiProtocol_FindFirst inserts multiple documents and verifies that
// ORDER BY _id LIMIT 1 returns only the first document. This mirrors
// dao.Find(ctx).Where(...).First().
func TestDAO_MultiProtocol_FindFirst(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 35000)
			lastID := protoDocID(proto, 35004)

			for i := 0; i < 5; i++ {
				docID := protoDocID(proto, int64(35000+i))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'first-%d', %g)`,
						docID, i, float64(i)),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i, proto)
			}

			query := fmt.Sprintf(
				`SELECT _id, title FROM docs WHERE _id >= %d AND _id <= %d ORDER BY _id ASC LIMIT 1`, baseID, lastID,
			)
			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 1, "FindFirst must return exactly one row (%s)", proto)
			assert.Equal(t, int64(baseID), rows[0]["_id"], "FindFirst must return the earliest doc (%s)", proto)
			assert.Equal(t, "first-0", rows[0]["title"])
		})
	}
}

// ---------------------------------------------------------------------------
// Bulk CRUD — mirrors batch dao.SaveAll / dao.UpdateAll / dao.DeleteWhere
// ---------------------------------------------------------------------------

// TestDAO_MultiProtocol_BulkCRUD inserts 100 entities in a transaction,
// updates all their scores by +1, deletes a subset, and verifies each step.
// This mirrors dao.SaveAll(entities) → bulk UPDATE → dao.DeleteWhere(...).
func TestDAO_MultiProtocol_BulkCRUD(t *testing.T) {
	const bulkN = 100
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 36000)
			lastID := protoDocID(proto, int64(36000+bulkN-1))

			// Bulk INSERT in one transaction.
			tx, err := env.db.Begin()
			require.NoError(t, err, "BEGIN (%s)", proto)
			stmt, err := tx.Prepare(`INSERT INTO docs (_id, title, score) VALUES (?, ?, ?)`)
			require.NoError(t, err)
			defer stmt.Close()

			for i := 0; i < bulkN; i++ {
				docID := protoDocID(proto, int64(36000+i))
				_, err := stmt.Exec(docID, fmt.Sprintf("bulk-%03d", i), float64(i))
				require.NoError(t, err, "bulk INSERT row %d (%s)", i, proto)
			}
			require.NoError(t, tx.Commit(), "COMMIT bulk INSERT (%s)", proto)

			// Verify all 100 present.
			assertRowCount(t, env.db,
				fmt.Sprintf(`SELECT * FROM docs WHERE _id >= %d AND _id <= %d`, baseID, lastID), bulkN)

			// Bulk UPDATE: increment all scores by 1.
			_, err = env.db.Exec(
				fmt.Sprintf(`UPDATE docs SET score = score + 1 WHERE _id >= %d AND _id <= %d`, baseID, lastID),
			)
			require.NoError(t, err, "bulk UPDATE (%s)", proto)

			// Verify the first doc's score incremented (was 0, now 1).
			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT score FROM docs WHERE _id = %d`, baseID))
			require.Len(t, rows, 1)
			assert.InDelta(t, 1.0, rows[0]["score"], 0.001, "score after bulk UPDATE (%s)", proto)

			// DELETE subset: docs with score > 50 (were 50..99, now 51..100 after +1).
			_, err = env.db.Exec(
				fmt.Sprintf(`DELETE FROM docs WHERE score > 50 AND _id >= %d AND _id <= %d`, baseID, lastID),
			)
			require.NoError(t, err, "partial DELETE (%s)", proto)

			// Verify remaining count: scores 1..50 → 50 docs remain.
			assertRowCount(t, env.db,
				fmt.Sprintf(`SELECT * FROM docs WHERE _id >= %d AND _id <= %d`, baseID, lastID), 50)
		})
	}
}

// ---------------------------------------------------------------------------
// Nullable fields — mirrors DAO nullable field handling
// ---------------------------------------------------------------------------

// TestDAO_MultiProtocol_NullableFields inserts a document with only the
// required title field, verifies optional fields are NULL, then updates one
// optional field and confirms only it changed. This mirrors DAO nullable field
// handling where partial updates preserve unset columns as NULL.
func TestDAO_MultiProtocol_NullableFields(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			docID := protoDocID(proto, 37000)

			// Insert with only title — body and score are intentionally omitted (NULL).
			_, err := env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title) VALUES (%d, 'nullable-title')`, docID),
			)
			require.NoError(t, err, "INSERT nullable doc (%s)", proto)

			// Verify NULLs returned for unset columns.
			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT _id, title, body, score FROM docs WHERE _id = %d`, docID))
			require.Len(t, rows, 1, "nullable doc must be retrievable (%s)", proto)
			assert.Equal(t, "nullable-title", rows[0]["title"])
			assert.Nil(t, rows[0]["body"], "body must be NULL when not inserted (%s)", proto)
			assert.Nil(t, rows[0]["score"], "score must be NULL when not inserted (%s)", proto)

			// Update only body, leave score NULL.
			_, err = env.db.Exec(
				fmt.Sprintf(`UPDATE docs SET body = 'now set' WHERE _id = %d`, docID),
			)
			require.NoError(t, err, "UPDATE body (%s)", proto)

			// Verify body changed, score still NULL.
			rows = scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT body, score FROM docs WHERE _id = %d`, docID))
			require.Len(t, rows, 1)
			assert.Equal(t, "now set", rows[0]["body"], "body after UPDATE (%s)", proto)
			assert.Nil(t, rows[0]["score"], "score must remain NULL after partial UPDATE (%s)", proto)
		})
	}
}

// ---------------------------------------------------------------------------
// Unique field query — mirrors dao.QueryOneByIndex
// ---------------------------------------------------------------------------

// TestDAO_MultiProtocol_UniqueFieldQuery inserts documents with unique titles
// and verifies that a WHERE title = '...' predicate returns exactly one result.
// This mirrors dao.QueryOneByIndex(ctx, "title", "unique-title-42").
func TestDAO_MultiProtocol_UniqueFieldQuery(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 38000)
			lastID := protoDocID(proto, 38004)

			// Insert 5 docs with distinct titles.
			for i := 0; i < 5; i++ {
				docID := protoDocID(proto, int64(38000+i))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'unique-title-%d', %g)`,
						docID, i, float64(i)),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i, proto)
			}

			// Query by unique title — must return exactly one result.
			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT _id, title FROM docs WHERE title = 'unique-title-2' AND _id >= %d AND _id <= %d`, baseID, lastID))
			require.Len(t, rows, 1, "unique index query must return exactly 1 row (%s)", proto)
			assert.Equal(t, int64(protoDocID(proto, 38002)), rows[0]["_id"])
			assert.Equal(t, "unique-title-2", rows[0]["title"])

			// Querying a non-existent title must return 0 rows.
			rows = scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT _id FROM docs WHERE title = 'no-such-title' AND _id >= %d AND _id <= %d`, baseID, lastID))
			require.Empty(t, rows, "query for missing title must return 0 rows (%s)", proto)
		})
	}
}
