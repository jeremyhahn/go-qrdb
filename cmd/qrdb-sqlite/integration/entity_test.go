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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollection_MultiProtocol_Aggregation inserts multiple documents and
// verifies that SQLite aggregation functions (SUM, AVG) return correct values
// when evaluated over the live collection store for each protocol.
//
// Each protocol inserts 5 documents in a unique _id range (base 100) to avoid
// collisions with parallel protocol subtests sharing the same collection.
func TestCollection_MultiProtocol_Aggregation(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			// Insert 5 docs with IDs in this protocol's range starting at base 100.
			// scores: 2, 4, 6, 8, 10 → SUM=30, AVG=6.
			baseID := protoDocID(proto, 100)
			lastID := protoDocID(proto, 104)
			for i := 0; i < 5; i++ {
				docID := protoDocID(proto, int64(100+i))
				_, err := env.db.Exec(
					`INSERT INTO docs (_id, title, score) VALUES (?, ?, ?)`,
					docID, fmt.Sprintf("agg-doc-%d", i+1), float64(i+1)*2.0,
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i+1, proto)
			}

			assertRowCount(t, env.db,
				fmt.Sprintf(`SELECT * FROM docs WHERE _id >= %d AND _id <= %d`, baseID, lastID), 5)

			// SUM(score) over this protocol's range: 2+4+6+8+10 = 30.
			row := env.db.QueryRow(fmt.Sprintf(
				`SELECT SUM(score) FROM docs WHERE _id >= %d AND _id <= %d`, baseID, lastID))
			var sum float64
			require.NoError(t, row.Scan(&sum))
			assert.InDelta(t, 30.0, sum, 0.001, "SUM(score) mismatch (%s)", proto)

			// AVG(score) over this protocol's range: 30/5 = 6.
			row = env.db.QueryRow(fmt.Sprintf(
				`SELECT AVG(score) FROM docs WHERE _id >= %d AND _id <= %d`, baseID, lastID))
			var avg float64
			require.NoError(t, row.Scan(&avg))
			assert.InDelta(t, 6.0, avg, 0.001, "AVG(score) mismatch (%s)", proto)
		})
	}
}

// TestCollection_MultiProtocol_Pagination inserts 20 documents and verifies
// that LIMIT and OFFSET clauses return the correct slice via the virtual table
// cursor for each protocol.
//
// Each protocol uses a unique _id range (base 200) so that parallel subtests
// sharing the same collection do not interfere with each other's ORDER BY
// results.
func TestCollection_MultiProtocol_Pagination(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			// Insert 20 docs with sequential IDs starting at this protocol's base 200.
			baseID := protoDocID(proto, 200)
			for i := 0; i < 20; i++ {
				docID := protoDocID(proto, int64(200+i))
				_, err := env.db.Exec(
					`INSERT INTO docs (_id, title, score) VALUES (?, ?, ?)`,
					docID, fmt.Sprintf("page-doc-%d", i+1), float64(i+1),
				)
				require.NoError(t, err, "INSERT doc %d (%s)", i+1, proto)
			}

			// LIMIT 5 OFFSET 10 over this protocol's range should return the
			// 11th through 15th inserted IDs, i.e. base+10 .. base+14.
			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT _id FROM docs WHERE _id >= %d ORDER BY _id LIMIT 5 OFFSET 10`, baseID))
			require.Len(t, rows, 5, "pagination result count mismatch (%s)", proto)
			for i, row := range rows {
				wantID := protoDocID(proto, int64(210+i))
				assert.Equal(t, wantID, row["_id"], "row %d: unexpected _id (%s)", i, proto)
			}
		})
	}
}

// TestCollection_MultiProtocol_NullFields inserts a document with only the
// title set and verifies that omitted columns are returned as NULL for each
// protocol.
func TestCollection_MultiProtocol_NullFields(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			docID := protoDocID(proto, 300)
			_, err := env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title) VALUES (%d, 'Null Test')`, docID),
			)
			require.NoError(t, err, "INSERT docs with only title (%s)", proto)

			rows := scanAllRows(t, env.db,
				fmt.Sprintf(`SELECT _id, title, body, score FROM docs WHERE _id = %d`, docID))
			require.Len(t, rows, 1)
			assert.Equal(t, "Null Test", rows[0]["title"])
			assert.Nil(t, rows[0]["body"], "body should be NULL when not inserted (%s)", proto)
			assert.Nil(t, rows[0]["score"], "score should be NULL when not inserted (%s)", proto)
		})
	}
}
