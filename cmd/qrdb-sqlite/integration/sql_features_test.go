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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// JOINs
// ---------------------------------------------------------------------------

// TestSQL_MultiProtocol_JoinKVWithKV inserts complementary keys into the KV
// virtual table and verifies that a self-join on the table using REPLACE to
// derive one key from another returns the expected name/score pair.
func TestSQL_MultiProtocol_JoinKVWithKV(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			userKey := uniqueKey(proto, "join-users/1")
			scoreKey := uniqueKey(proto, "join-scores/1")

			_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", userKey, []byte("alice"))
			require.NoError(t, err, "INSERT user key (%s)", proto)

			_, err = env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", scoreKey, []byte("100"))
			require.NoError(t, err, "INSERT score key (%s)", proto)

			// Self-join: derive the score key from the user key via REPLACE.
			query := fmt.Sprintf(`
				SELECT a.value AS name, b.value AS score
				FROM kv a
				JOIN kv b ON REPLACE(a.key, %q, %q) = b.key
				WHERE a.key = %q`,
				uniqueKey(proto, "join-users/"),
				uniqueKey(proto, "join-scores/"),
				userKey,
			)

			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 1, "join must return exactly one row (%s)", proto)
			assert.Equal(t, []byte("alice"), rows[0]["name"])
			assert.Equal(t, []byte("100"), rows[0]["score"])
		})
	}
}

// TestSQL_MultiProtocol_JoinCollectionSelfJoin inserts two documents sharing
// the same title and verifies a self-join on the docs table returns the pair.
func TestSQL_MultiProtocol_JoinCollectionSelfJoin(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			idA := protoDocID(proto, 10001)
			idB := protoDocID(proto, 10002)
			sharedTitle := fmt.Sprintf("shared-title-%s", proto)

			_, err := env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, '%s', 1.0)`, idA, sharedTitle),
			)
			require.NoError(t, err, "INSERT doc A (%s)", proto)

			_, err = env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, '%s', 2.0)`, idB, sharedTitle),
			)
			require.NoError(t, err, "INSERT doc B (%s)", proto)

			// Self-join: find pairs that share a title where a._id < b._id.
			query := fmt.Sprintf(`
				SELECT a._id AS id_a, b._id AS id_b
				FROM docs a
				JOIN docs b ON a.title = b.title AND a._id < b._id
				WHERE a.title = '%s'`, sharedTitle)

			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 1, "self-join must return exactly one pair (%s)", proto)
			assert.Equal(t, int64(idA), rows[0]["id_a"])
			assert.Equal(t, int64(idB), rows[0]["id_b"])
		})
	}
}

// ---------------------------------------------------------------------------
// GROUP BY
// ---------------------------------------------------------------------------

// TestSQL_MultiProtocol_GroupBy inserts documents with duplicate titles and
// verifies that GROUP BY returns the correct per-title counts.
func TestSQL_MultiProtocol_GroupBy(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 11000)
			lastID := protoDocID(proto, 11005)

			// Insert 3 docs with title "alpha" and 2 with title "beta".
			titles := []string{"alpha", "alpha", "alpha", "beta", "beta", "gamma"}
			for i, title := range titles {
				docID := protoDocID(proto, int64(11000+i))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, '%s', %g)`,
						docID, title, float64(i)),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i, proto)
			}

			query := fmt.Sprintf(
				`SELECT title, COUNT(*) AS cnt FROM docs WHERE _id >= %d AND _id <= %d GROUP BY title ORDER BY title ASC`,
				baseID, lastID,
			)
			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 3, "GROUP BY must produce 3 title groups (%s)", proto)

			want := map[string]int64{"alpha": 3, "beta": 2, "gamma": 1}
			for _, r := range rows {
				title, _ := r["title"].(string)
				cnt, _ := r["cnt"].(int64)
				assert.Equal(t, want[title], cnt, "count for title=%q (%s)", title, proto)
			}
		})
	}
}

// TestSQL_MultiProtocol_GroupByHaving uses HAVING to filter GROUP BY results
// and verifies that only groups with count > 1 are returned.
func TestSQL_MultiProtocol_GroupByHaving(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 12000)
			lastID := protoDocID(proto, 12004)

			// "alpha"×3, "beta"×1, "gamma"×1
			data := []struct {
				offset int64
				title  string
			}{
				{0, "alpha"}, {1, "alpha"}, {2, "alpha"}, {3, "beta"}, {4, "gamma"},
			}
			for _, d := range data {
				docID := protoDocID(proto, 12000+d.offset)
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, '%s', 1.0)`, docID, d.title),
				)
				require.NoError(t, err, "INSERT (%s)", proto)
			}

			query := fmt.Sprintf(
				`SELECT title, COUNT(*) AS cnt FROM docs WHERE _id >= %d AND _id <= %d GROUP BY title HAVING cnt > 1`,
				baseID, lastID,
			)
			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 1, "HAVING cnt > 1 must return only 1 group (%s)", proto)
			assert.Equal(t, "alpha", rows[0]["title"])
			assert.Equal(t, int64(3), rows[0]["cnt"])
		})
	}
}

// ---------------------------------------------------------------------------
// Aggregations
// ---------------------------------------------------------------------------

// TestSQL_MultiProtocol_MinMax inserts documents with varying scores and
// verifies that MIN and MAX aggregate functions return the correct extremes.
func TestSQL_MultiProtocol_MinMax(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 13000)
			lastID := protoDocID(proto, 13004)

			scores := []float64{5.0, 1.5, 9.9, 3.3, 7.7}
			for i, score := range scores {
				docID := protoDocID(proto, int64(13000+i))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'minmax-%d', %g)`, docID, i, score),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i, proto)
			}

			query := fmt.Sprintf(
				`SELECT MIN(score) AS min_score, MAX(score) AS max_score FROM docs WHERE _id >= %d AND _id <= %d`,
				baseID, lastID,
			)
			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 1, "MIN/MAX must return one row (%s)", proto)
			assert.InDelta(t, 1.5, rows[0]["min_score"], 0.001, "MIN (%s)", proto)
			assert.InDelta(t, 9.9, rows[0]["max_score"], 0.001, "MAX (%s)", proto)
		})
	}
}

// TestSQL_MultiProtocol_CountDistinct inserts documents with duplicate titles
// and verifies that COUNT(DISTINCT title) returns the number of unique titles.
func TestSQL_MultiProtocol_CountDistinct(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 14000)
			lastID := protoDocID(proto, 14005)

			// 3 unique titles across 6 docs.
			titles := []string{"x", "x", "y", "y", "z", "z"}
			for i, title := range titles {
				docID := protoDocID(proto, int64(14000+i))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, '%s', 1.0)`, docID, title),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i, proto)
			}

			query := fmt.Sprintf(
				`SELECT COUNT(DISTINCT title) AS cnt FROM docs WHERE _id >= %d AND _id <= %d`,
				baseID, lastID,
			)
			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 1)
			assert.Equal(t, int64(3), rows[0]["cnt"], "COUNT(DISTINCT title) (%s)", proto)
		})
	}
}

// ---------------------------------------------------------------------------
// Complex WHERE
// ---------------------------------------------------------------------------

// TestSQL_MultiProtocol_WhereAndOr inserts documents with varying scores and
// titles and verifies that compound AND/OR predicates filter correctly.
func TestSQL_MultiProtocol_WhereAndOr(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 15000)
			lastID := protoDocID(proto, 15003)

			data := []struct {
				offset int64
				title  string
				score  float64
			}{
				{0, "regular", 2.0}, // neither condition
				{1, "regular", 8.0}, // score > 5.0
				{2, "special", 1.0}, // title = 'special'
				{3, "special", 9.0}, // both
			}
			for _, d := range data {
				docID := protoDocID(proto, 15000+d.offset)
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, '%s', %g)`,
						docID, d.title, d.score),
				)
				require.NoError(t, err, "INSERT (%s)", proto)
			}

			specialTitle := "special"
			query := fmt.Sprintf(
				`SELECT _id FROM docs WHERE (score > 5.0 OR title = '%s') AND _id >= %d AND _id <= %d ORDER BY _id ASC`,
				specialTitle, baseID, lastID,
			)
			rows := scanAllRows(t, env.db, query)
			// Expecting IDs at offsets 1, 2, 3 (score>5 or title='special').
			require.Len(t, rows, 3, "AND/OR filter must return 3 rows (%s)", proto)
			assert.Equal(t, int64(protoDocID(proto, 15001)), rows[0]["_id"])
			assert.Equal(t, int64(protoDocID(proto, 15002)), rows[1]["_id"])
			assert.Equal(t, int64(protoDocID(proto, 15003)), rows[2]["_id"])
		})
	}
}

// TestSQL_MultiProtocol_WhereIn inserts KV pairs and verifies that an IN
// clause correctly filters to the specified set of keys.
func TestSQL_MultiProtocol_WhereIn(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			k1 := uniqueKey(proto, "in-key-1")
			k2 := uniqueKey(proto, "in-key-2")
			k3 := uniqueKey(proto, "in-key-3")
			kOut := uniqueKey(proto, "in-key-out")

			for _, kv := range []struct{ k, v string }{
				{k1, "v1"}, {k2, "v2"}, {k3, "v3"}, {kOut, "vout"},
			} {
				_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", kv.k, []byte(kv.v))
				require.NoError(t, err, "INSERT %q (%s)", kv.k, proto)
			}

			query := fmt.Sprintf(
				`SELECT key FROM kv WHERE key IN ('%s', '%s', '%s') ORDER BY key ASC`,
				k1, k2, k3,
			)
			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 3, "IN clause must return exactly 3 rows (%s)", proto)

			keys := make([]string, 3)
			for i, r := range rows {
				keys[i], _ = r["key"].(string)
			}
			assert.Contains(t, keys, k1)
			assert.Contains(t, keys, k2)
			assert.Contains(t, keys, k3)
		})
	}
}

// TestSQL_MultiProtocol_WhereBetween inserts documents with varying scores and
// verifies that a BETWEEN predicate on a non-key column filters correctly.
func TestSQL_MultiProtocol_WhereBetween(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 16000)
			lastID := protoDocID(proto, 16009)

			scores := []float64{1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0, 9.0, 10.0}
			for i, score := range scores {
				docID := protoDocID(proto, int64(16000+i))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'between-%d', %g)`,
						docID, i, score),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i, proto)
			}

			// BETWEEN 3.0 AND 7.0 should match scores 3,4,5,6,7 → 5 rows.
			query := fmt.Sprintf(
				`SELECT _id, score FROM docs WHERE score BETWEEN 3.0 AND 7.0 AND _id >= %d AND _id <= %d ORDER BY score ASC`,
				baseID, lastID,
			)
			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 5, "BETWEEN 3.0 AND 7.0 must return 5 rows (%s)", proto)
			assert.InDelta(t, 3.0, rows[0]["score"], 0.001)
			assert.InDelta(t, 7.0, rows[4]["score"], 0.001)
		})
	}
}

// ---------------------------------------------------------------------------
// Subqueries
// ---------------------------------------------------------------------------

// TestSQL_MultiProtocol_Subquery inserts documents and verifies that a
// correlated scalar subquery (MAX) correctly identifies the highest-scoring doc.
func TestSQL_MultiProtocol_Subquery(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 17000)
			lastID := protoDocID(proto, 17004)

			scores := []float64{3.0, 7.5, 1.0, 9.9, 5.5}
			for i, score := range scores {
				docID := protoDocID(proto, int64(17000+i))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'sub-%d', %g)`,
						docID, i, score),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i, proto)
			}

			// Select the doc whose score equals the max score in the range.
			query := fmt.Sprintf(`
				SELECT _id, score FROM docs
				WHERE score = (SELECT MAX(score) FROM docs WHERE _id >= %d AND _id <= %d)
				  AND _id >= %d AND _id <= %d`,
				baseID, lastID, baseID, lastID,
			)
			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 1, "subquery must return exactly the max-score doc (%s)", proto)
			assert.InDelta(t, 9.9, rows[0]["score"], 0.001, "subquery max score (%s)", proto)
		})
	}
}

// TestSQL_MultiProtocol_SubqueryIn verifies that a subquery used as an IN
// argument filters KV rows correctly.
func TestSQL_MultiProtocol_SubqueryIn(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			prefix := uniqueKey(proto, "subq")
			inKey := prefix + "/in-1"
			outKey := uniqueKey(proto, "subq-out")

			for _, kv := range []struct{ k, v string }{
				{inKey, "inside"},
				{outKey, "outside"},
			} {
				_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", kv.k, []byte(kv.v))
				require.NoError(t, err, "INSERT %q (%s)", kv.k, proto)
			}

			// Subquery selects keys matching the prefix; outer query filters by that set.
			pattern := prefix + "/%"
			query := fmt.Sprintf(
				`SELECT key FROM kv WHERE key IN (SELECT key FROM kv WHERE key LIKE '%s')`,
				pattern,
			)
			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 1, "subquery IN must return 1 row (%s)", proto)
			got, _ := rows[0]["key"].(string)
			assert.Equal(t, inKey, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Ordering
// ---------------------------------------------------------------------------

// TestSQL_MultiProtocol_OrderByMultiColumn inserts documents sharing titles
// but with different scores and verifies multi-column ORDER BY (title ASC,
// score DESC) produces the correct ordering.
func TestSQL_MultiProtocol_OrderByMultiColumn(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 18000)
			lastID := protoDocID(proto, 18003)

			data := []struct {
				offset int64
				title  string
				score  float64
			}{
				{0, "b-title", 1.0},
				{1, "a-title", 5.0},
				{2, "a-title", 9.0},
				{3, "b-title", 3.0},
			}
			for _, d := range data {
				docID := protoDocID(proto, 18000+d.offset)
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, '%s', %g)`,
						docID, d.title, d.score),
				)
				require.NoError(t, err, "INSERT (%s)", proto)
			}

			query := fmt.Sprintf(
				`SELECT title, score FROM docs WHERE _id >= %d AND _id <= %d ORDER BY title ASC, score DESC LIMIT 4`,
				baseID, lastID,
			)
			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 4, "ORDER BY multi-column must return 4 rows (%s)", proto)

			// Expected order: a-title/9.0, a-title/5.0, b-title/3.0, b-title/1.0
			assert.Equal(t, "a-title", rows[0]["title"])
			assert.InDelta(t, 9.0, rows[0]["score"], 0.001)
			assert.Equal(t, "a-title", rows[1]["title"])
			assert.InDelta(t, 5.0, rows[1]["score"], 0.001)
			assert.Equal(t, "b-title", rows[2]["title"])
			assert.InDelta(t, 3.0, rows[2]["score"], 0.001)
			assert.Equal(t, "b-title", rows[3]["title"])
			assert.InDelta(t, 1.0, rows[3]["score"], 0.001)
		})
	}
}

// ---------------------------------------------------------------------------
// DISTINCT
// ---------------------------------------------------------------------------

// TestSQL_MultiProtocol_Distinct inserts documents with duplicate titles and
// verifies that SELECT DISTINCT returns only unique title values.
func TestSQL_MultiProtocol_Distinct(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 19000)
			lastID := protoDocID(proto, 19005)

			// "dup" appears 3 times, "unique" once, "other" twice.
			titles := []string{"dup", "dup", "dup", "unique", "other", "other"}
			for i, title := range titles {
				docID := protoDocID(proto, int64(19000+i))
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, '%s', 1.0)`, docID, title),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i, proto)
			}

			query := fmt.Sprintf(
				`SELECT DISTINCT title FROM docs WHERE _id >= %d AND _id <= %d ORDER BY title ASC`,
				baseID, lastID,
			)
			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 3, "DISTINCT must return 3 unique titles (%s)", proto)
			assert.Equal(t, "dup", rows[0]["title"])
			assert.Equal(t, "other", rows[1]["title"])
			assert.Equal(t, "unique", rows[2]["title"])
		})
	}
}

// ---------------------------------------------------------------------------
// CASE expressions
// ---------------------------------------------------------------------------

// TestSQL_MultiProtocol_CaseExpression inserts documents and verifies that a
// CASE expression correctly categorises scores into "high"/"low" tiers.
func TestSQL_MultiProtocol_CaseExpression(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			baseID := protoDocID(proto, 20000)
			lastID := protoDocID(proto, 20003)

			data := []struct {
				offset int64
				score  float64
			}{
				{0, 2.0}, {1, 6.0}, {2, 5.1}, {3, 4.9},
			}
			for _, d := range data {
				docID := protoDocID(proto, 20000+d.offset)
				_, err := env.db.Exec(
					fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'case-doc', %g)`,
						docID, d.score),
				)
				require.NoError(t, err, "INSERT (%s)", proto)
			}

			query := fmt.Sprintf(`
				SELECT _id, CASE WHEN score > 5 THEN 'high' ELSE 'low' END AS tier
				FROM docs
				WHERE _id >= %d AND _id <= %d
				ORDER BY _id ASC
				LIMIT 4`, baseID, lastID)

			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 4, "CASE expression must return 4 rows (%s)", proto)

			// score 2.0 → low, 6.0 → high, 5.1 → high, 4.9 → low
			assert.Equal(t, "low", rows[0]["tier"], "score 2.0 (%s)", proto)
			assert.Equal(t, "high", rows[1]["tier"], "score 6.0 (%s)", proto)
			assert.Equal(t, "high", rows[2]["tier"], "score 5.1 (%s)", proto)
			assert.Equal(t, "low", rows[3]["tier"], "score 4.9 (%s)", proto)
		})
	}
}

// ---------------------------------------------------------------------------
// String functions
// ---------------------------------------------------------------------------

// TestSQL_MultiProtocol_StringFunctions inserts a document and verifies that
// SQLite string functions (UPPER, LENGTH, REPLACE) operate correctly on
// virtual table column data.
func TestSQL_MultiProtocol_StringFunctions(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			docID := protoDocID(proto, 21000)
			title := "hello world"
			_, err := env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, '%s', 1.0)`, docID, title),
			)
			require.NoError(t, err, "INSERT (%s)", proto)

			query := fmt.Sprintf(`
				SELECT
					UPPER(title)             AS up,
					LENGTH(title)            AS len,
					REPLACE(title, 'world', 'qrdb') AS rep
				FROM docs WHERE _id = %d`, docID)

			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 1, "string functions must return one row (%s)", proto)
			assert.Equal(t, "HELLO WORLD", rows[0]["up"], "UPPER (%s)", proto)
			assert.Equal(t, int64(len(title)), rows[0]["len"], "LENGTH (%s)", proto)
			assert.Equal(t, "hello qrdb", rows[0]["rep"], "REPLACE (%s)", proto)
		})
	}
}

// ---------------------------------------------------------------------------
// UNION
// ---------------------------------------------------------------------------

// TestSQL_MultiProtocol_Union combines a KV key and a collection title with
// UNION and verifies that both rows appear in the result set.
func TestSQL_MultiProtocol_Union(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			kvKey := uniqueKey(proto, "union-key")
			_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", kvKey, []byte("kv-union-val"))
			require.NoError(t, err, "INSERT kv (%s)", proto)

			docID := protoDocID(proto, 22000)
			_, err = env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'union-doc-title', 1.0)`, docID),
			)
			require.NoError(t, err, "INSERT doc (%s)", proto)

			query := fmt.Sprintf(`
				SELECT key AS name FROM kv WHERE key = '%s'
				UNION
				SELECT title AS name FROM docs WHERE _id = %d
				ORDER BY name ASC`, kvKey, docID)

			rows := scanAllRows(t, env.db, query)
			require.Len(t, rows, 2, "UNION must return 2 rows (%s)", proto)

			names := make([]string, 2)
			for i, r := range rows {
				names[i], _ = r["name"].(string)
			}
			assert.Contains(t, names, kvKey, "UNION must include kv key (%s)", proto)
			assert.Contains(t, names, "union-doc-title", "UNION must include doc title (%s)", proto)
		})
	}
}

// ---------------------------------------------------------------------------
// INSERT ... SELECT
// ---------------------------------------------------------------------------

// TestSQL_MultiProtocol_InsertSelect uses INSERT INTO ... SELECT to copy KV
// rows with a modified key and verifies the copies are present.
func TestSQL_MultiProtocol_InsertSelect(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			prefix := uniqueKey(proto, "ins-sel")

			// Insert two source rows.
			for i := 0; i < 2; i++ {
				key := fmt.Sprintf("%s/%d", prefix, i)
				_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, []byte(fmt.Sprintf("v%d", i)))
				require.NoError(t, err, "INSERT source key %d (%s)", i, proto)
			}

			// INSERT ... SELECT with a key suffix to create copies.
			_, err := env.db.Exec(fmt.Sprintf(
				`INSERT INTO kv (key, value) SELECT key || '_copy', value FROM kv WHERE key LIKE '%s/%%'`,
				prefix,
			))
			require.NoError(t, err, "INSERT ... SELECT (%s)", proto)

			// Both originals and copies must be present.
			copyPattern := prefix + "/%_copy"
			rows := scanAllRows(t, env.db,
				fmt.Sprintf("SELECT key FROM kv WHERE key LIKE '%s'", copyPattern))
			require.Len(t, rows, 2, "INSERT ... SELECT must create 2 copies (%s)", proto)

			for _, r := range rows {
				k, _ := r["key"].(string)
				assert.True(t, strings.HasSuffix(k, "_copy"), "copied key must end with _copy: %q (%s)", k, proto)
			}
		})
	}
}
