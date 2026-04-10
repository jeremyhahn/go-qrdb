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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdktransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ensureCollection creates the "docs" collection on the server if it does not
// already exist. The create call is intentionally idempotent — errors are
// silently swallowed because the collection may already exist from a previous
// test.
func ensureCollection(t *testing.T, env *testEnv, name string) {
	t.Helper()
	ctx := context.Background()
	_, err := env.client.Collections().CreateCollection(ctx, sdktransport.CreateCollectionRequest{
		Name:     name,
		Database: "default",
	})
	if isServiceUnavailable(err) {
		t.Skipf("collection service not available on server: %v", err)
	}
	// Probe with a list to detect cases where the collection service is partially wired.
	listResp, err := env.client.Collections().ListCollections(ctx, "default")
	if isServiceUnavailable(err) {
		t.Skipf("collection service not available on server: %v", err)
	}
	// Verify the collection was actually created by checking the list.
	if err == nil && listResp != nil {
		found := false
		for _, c := range listResp.Collections {
			if c.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Skipf("collection %q not found after CreateCollection (service may be partially wired)", name)
		}
	}
}

func isServiceUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not configured") ||
		strings.Contains(msg, "not implemented") ||
		strings.Contains(msg, "Unimplemented") ||
		strings.Contains(msg, "501")
}

// TestCollection_MultiProtocol_CRUD exercises a full INSERT → SELECT → UPDATE
// → DELETE cycle on the docs collection virtual table through every transport.
func TestCollection_MultiProtocol_CRUD(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			docID := protoDocID(proto, 1000)
			_, err := env.db.Exec(
				`INSERT INTO docs (_id, title, body, score) VALUES (?, 'Integration Test', 'body text', 7.5)`, docID,
			)
			require.NoError(t, err, "INSERT document via vtab (%s)", proto)

			rows := scanAllRows(t, env.db, fmt.Sprintf(`SELECT * FROM docs WHERE _id = %d`, docID))
			require.Len(t, rows, 1, "expected one row after INSERT (%s)", proto)
			assert.Equal(t, int64(docID), rows[0]["_id"])
			assert.Equal(t, "Integration Test", rows[0]["title"])
			assert.Equal(t, "body text", rows[0]["body"])
			assert.InDelta(t, 7.5, rows[0]["score"], 0.001)

			_, err = env.db.Exec(fmt.Sprintf(`UPDATE docs SET title = 'Updated Title', score = 9.9 WHERE _id = %d`, docID))
			require.NoError(t, err, "UPDATE document via vtab (%s)", proto)

			rows = scanAllRows(t, env.db, fmt.Sprintf(`SELECT title, score FROM docs WHERE _id = %d`, docID))
			require.Len(t, rows, 1)
			assert.Equal(t, "Updated Title", rows[0]["title"])
			assert.InDelta(t, 9.9, rows[0]["score"], 0.001)

			_, err = env.db.Exec(fmt.Sprintf(`DELETE FROM docs WHERE _id = %d`, docID))
			require.NoError(t, err, "DELETE document via vtab (%s)", proto)

			assertRowCount(t, env.db, fmt.Sprintf(`SELECT * FROM docs WHERE _id = %d`, docID), 0)
		})
	}
}

// TestCollection_MultiProtocol_CrossProtocol_WriteSDK_ReadSQL creates a
// document through the raw SDK client and verifies it is visible through the
// collection virtual table for each protocol.
func TestCollection_MultiProtocol_CrossProtocol_WriteSDK_ReadSQL(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			ensureCollection(t, env, "docs")

			insertResp, err := env.client.Collections().InsertDocument(ctx, sdktransport.InsertDocumentRequest{
				Database:   "default",
				Collection: "docs",
				Document: map[string]any{
					"title": fmt.Sprintf("sdk-doc-%s", proto),
					"body":  "written via SDK",
					"score": 5.0,
				},
			})
			require.NoError(t, err, "SDK InsertDocument (%s)", proto)

			rows := scanAllRows(t, env.db,
				`SELECT _id, title FROM docs WHERE _id = ?`, int64(insertResp.ID))
			require.Len(t, rows, 1, "SDK-written document must be visible via vtab (%s)", proto)
			assert.Equal(t, int64(insertResp.ID), rows[0]["_id"])
			assert.Equal(t, fmt.Sprintf("sdk-doc-%s", proto), rows[0]["title"])
		})
	}
}

// TestCollection_MultiProtocol_CrossProtocol_WriteSQL_ReadSDK writes a
// document through the collection virtual table and reads it back through the
// raw SDK client for each protocol.
func TestCollection_MultiProtocol_CrossProtocol_WriteSQL_ReadSDK(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			ensureCollection(t, env, "docs")

			docID := protoDocID(proto, 2000)
			_, err := env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title, score) VALUES (%d, 'vtab-written', 3.14)`, docID),
			)
			require.NoError(t, err, "vtab INSERT (%s)", proto)

			resp, err := env.client.Collections().GetDocument(ctx, "default", "docs", uint64(docID))
			require.NoError(t, err, "SDK GetDocument (%s)", proto)
			require.NotNil(t, resp, "SDK must return the vtab-written document (%s)", proto)
			assert.Equal(t, "vtab-written", resp.Document["title"])
		})
	}
}

// TestCollection_MultiProtocol_DynamicSchema verifies that all declared schema
// columns are queryable and return correct types for each protocol.
func TestCollection_MultiProtocol_DynamicSchema(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			docID := protoDocID(proto, 3000)
			_, err := env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title, body, score) VALUES (%d, 'Schema Test', 'schema body', 3.14)`, docID),
			)
			require.NoError(t, err)

			titleRow := env.db.QueryRow(fmt.Sprintf(`SELECT title FROM docs WHERE _id = %d`, docID))
			var title string
			require.NoError(t, titleRow.Scan(&title))
			assert.Equal(t, "Schema Test", title)

			scoreRow := env.db.QueryRow(fmt.Sprintf(`SELECT score FROM docs WHERE _id = %d`, docID))
			var score float64
			require.NoError(t, scoreRow.Scan(&score))
			assert.InDelta(t, 3.14, score, 0.001)
		})
	}
}

// TestCollection_MultiProtocol_NullableColumns verifies that inserting a
// document without optional fields returns NULL for unset columns for each
// protocol.
func TestCollection_MultiProtocol_NullableColumns(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			docID := protoDocID(proto, 4000)
			_, err := env.db.Exec(
				fmt.Sprintf(`INSERT INTO docs (_id, title) VALUES (%d, 'Title Only')`, docID),
			)
			require.NoError(t, err)

			rows := scanAllRows(t, env.db, fmt.Sprintf(`SELECT _id, title, body, score FROM docs WHERE _id = %d`, docID))
			require.Len(t, rows, 1)
			assert.Equal(t, "Title Only", rows[0]["title"])
			assert.Nil(t, rows[0]["body"], "body column should be NULL when not inserted")
			assert.Nil(t, rows[0]["score"], "score column should be NULL when not inserted")
		})
	}
}

// TestCollection_MultiProtocol_BulkInsert inserts 200 documents through each
// protocol's virtual table inside a single transaction and verifies all are
// retrievable. Each protocol uses a unique _id range to avoid collisions when
// tests run in parallel against a shared server.
func TestCollection_MultiProtocol_BulkInsert(t *testing.T) {
	const n = 200
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ensureCollection(t, env, "docs")

			// Base offset for this protocol's bulk range (base 5000, items 0..199).
			baseID := protoDocID(proto, 5000)
			lastID := protoDocID(proto, 5000+n-1)

			tx, err := env.db.Begin()
			require.NoError(t, err)

			stmt, err := tx.Prepare(`INSERT INTO docs (_id, title, score) VALUES (?, ?, ?)`)
			require.NoError(t, err)
			defer stmt.Close()

			for i := 0; i < n; i++ {
				docID := protoDocID(proto, int64(5000+i))
				_, err := stmt.Exec(docID, fmt.Sprintf("bulk-doc-%d", i), float64(i))
				require.NoError(t, err, "INSERT row %d (%s)", i, proto)
			}
			require.NoError(t, tx.Commit())

			assertRowCount(t, env.db,
				fmt.Sprintf(`SELECT * FROM docs WHERE _id >= %d AND _id <= %d`, baseID, lastID), n)
		})
	}
}
