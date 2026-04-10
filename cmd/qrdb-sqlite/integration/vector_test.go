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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdktransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestVector_MultiProtocol_CreateNamespace inserts a namespace row through the
// virtual table and verifies it arrives in the real qrdbd vector store for
// every transport protocol.
func TestVector_MultiProtocol_CreateNamespace(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			ns := uniqueNamespace(proto, "embeddings")

			_, err := env.db.Exec(
				`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, 384, 'cosine', 'hnsw')`,
				ns,
			)
			require.NoError(t, err, "INSERT namespace via vtab (%s)", proto)

			// Cross-validate via SDK.
			resp, err := env.client.Vector().GetNamespaceInfo(ctx,
				&sdktransport.GetNamespaceInfoRequest{Namespace: ns})
			require.NoError(t, err, "SDK GetNamespaceInfo (%s)", proto)
			assert.Equal(t, ns, resp.Namespace)
			assert.EqualValues(t, 384, resp.Dimension)

			// Verify via SQL.
			rows := scanAllRows(t, env.db,
				`SELECT name, dimension, distance_metric, index_type FROM namespaces WHERE name = ?`, ns)
			require.Len(t, rows, 1)
			assert.Equal(t, ns, rows[0]["name"])
			assert.Equal(t, int64(384), rows[0]["dimension"])
			assert.Equal(t, "cosine", rows[0]["distance_metric"])
			assert.Equal(t, "hnsw", rows[0]["index_type"])
		})
	}
}

// TestVector_MultiProtocol_CrossProtocol_WriteSDK_ReadSQL creates a namespace
// via the SDK and verifies it is visible through the namespaces virtual table
// for each protocol.
func TestVector_MultiProtocol_CrossProtocol_WriteSDK_ReadSQL(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			ns := uniqueNamespace(proto, "sdk-ns")

			_, err := env.client.Vector().CreateNamespace(ctx, &sdktransport.CreateNamespaceRequest{
				Namespace:      ns,
				Dimension:      128,
				DistanceMetric: "euclidean",
				IndexType:      "flat",
			})
			require.NoError(t, err, "SDK CreateNamespace (%s)", proto)

			rows := scanAllRows(t, env.db,
				`SELECT name, dimension FROM namespaces WHERE name = ?`, ns)
			require.Len(t, rows, 1, "SDK-created namespace must be visible via vtab (%s)", proto)
			assert.Equal(t, ns, rows[0]["name"])
			assert.Equal(t, int64(128), rows[0]["dimension"])
		})
	}
}

// TestVector_MultiProtocol_ListNamespaces creates three namespaces and
// verifies that a full table scan returns all of them for each protocol.
func TestVector_MultiProtocol_ListNamespaces(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			nsNames := []string{
				uniqueNamespace(proto, "ns-a"),
				uniqueNamespace(proto, "ns-b"),
				uniqueNamespace(proto, "ns-c"),
			}
			for _, name := range nsNames {
				_, err := env.db.Exec(
					`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, 128, 'euclidean', 'flat')`,
					name,
				)
				require.NoError(t, err)
			}

			rows := scanAllRows(t, env.db, `SELECT name FROM namespaces`)
			names := make([]string, 0, len(rows))
			for _, row := range rows {
				names = append(names, row["name"].(string))
			}
			for _, expected := range nsNames {
				assert.Contains(t, names, expected, "namespace %s not found (%s)", expected, proto)
			}
		})
	}
}

// TestVector_MultiProtocol_DeleteNamespace creates a namespace, deletes it via
// the vtab, and confirms it is absent from a subsequent SDK lookup for each
// protocol.
func TestVector_MultiProtocol_DeleteNamespace(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			ns := uniqueNamespace(proto, "to-delete")

			_, err := env.db.Exec(
				`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, 64, 'dot', 'flat')`,
				ns,
			)
			require.NoError(t, err)

			assertRowCount(t, env.db, `SELECT * FROM namespaces WHERE name = ?`, 1, ns)

			_, err = env.db.Exec(`DELETE FROM namespaces WHERE name = ?`, ns)
			require.NoError(t, err)

			assertRowCount(t, env.db, `SELECT * FROM namespaces WHERE name = ?`, 0, ns)

			// Verify deletion propagated to qrdbd via SDK.
			nsResp, nsErr := env.client.Vector().GetNamespaceInfo(ctx,
				&sdktransport.GetNamespaceInfoRequest{Namespace: ns})
			if nsErr == nil {
				assert.Empty(t, nsResp.Namespace, "namespace must be absent after vtab DELETE (%s)", proto)
			}
		})
	}
}

// TestVector_MultiProtocol_UpsertVector inserts a vector through the vtab and
// verifies it is retrievable via SDK and through SQL for each protocol.
func TestVector_MultiProtocol_UpsertVector(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			ns := uniqueNamespace(proto, "vec-ns")

			// The namespace must exist before vectors can be inserted.
			_, err := env.db.Exec(
				`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, 4, 'cosine', 'flat')`,
				ns,
			)
			require.NoError(t, err)

			vec := float32sToBytes([]float32{0.1, 0.2, 0.3, 0.4})
			meta := `{"label":"test"}`

			_, err = env.db.Exec(
				`INSERT INTO vectors (id, namespace, vector, metadata) VALUES (1, ?, ?, ?)`,
				ns, vec, meta,
			)
			require.NoError(t, err, "INSERT vector via vtab (%s)", proto)

			// Cross-validate via SDK.
			sdkResp, err := env.client.Vector().GetVector(ctx, &sdktransport.GetVectorRequest{
				Namespace: ns,
				ID:        1,
			})
			require.NoError(t, err, "SDK GetVector (%s)", proto)
			assert.EqualValues(t, 1, sdkResp.ID)
			assert.True(t, sdkResp.Found, "vector must be found in qrdbd (%s)", proto)
			if sdkResp.Metadata != nil {
				assert.Equal(t, "test", sdkResp.Metadata["label"])
			}

			// Verify via SQL.
			rows := scanAllRows(t, env.db,
				`SELECT id, namespace, metadata FROM vectors WHERE id = 1 AND namespace = ?`, ns)
			require.Len(t, rows, 1)
			assert.Equal(t, int64(1), rows[0]["id"])
			assert.Equal(t, ns, rows[0]["namespace"])
			assert.Equal(t, meta, rows[0]["metadata"])
		})
	}
}

// TestVector_MultiProtocol_CrossProtocol_UpsertSDK_ReadSQL upserts a vector
// through the SDK and reads it back through the vectors virtual table for each
// protocol.
func TestVector_MultiProtocol_CrossProtocol_UpsertSDK_ReadSQL(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			ns := uniqueNamespace(proto, "sdk-vec-ns")

			_, err := env.client.Vector().CreateNamespace(ctx, &sdktransport.CreateNamespaceRequest{
				Namespace:      ns,
				Dimension:      3,
				DistanceMetric: "cosine",
				IndexType:      "flat",
			})
			require.NoError(t, err)

			_, err = env.client.Vector().UpsertVector(ctx, &sdktransport.UpsertVectorRequest{
				Namespace: ns,
				ID:        99,
				Vector:    []float32{0.5, 0.5, 0.5},
				Metadata:  map[string]interface{}{"source": "sdk"},
			})
			require.NoError(t, err, "SDK UpsertVector (%s)", proto)

			rows := scanAllRows(t, env.db,
				`SELECT id, namespace FROM vectors WHERE id = 99 AND namespace = ?`, ns)
			require.Len(t, rows, 1, "SDK-upserted vector must be visible via vtab (%s)", proto)
			assert.Equal(t, int64(99), rows[0]["id"])
			assert.Equal(t, ns, rows[0]["namespace"])
		})
	}
}

// TestVector_MultiProtocol_DeleteVector inserts a vector, deletes it through
// the vtab, and confirms it is absent from both vtab and SDK for each protocol.
func TestVector_MultiProtocol_DeleteVector(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			ns := uniqueNamespace(proto, "del-vec-ns")

			_, err := env.db.Exec(
				`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, 3, 'cosine', 'flat')`,
				ns,
			)
			require.NoError(t, err)

			vec := float32sToBytes([]float32{1.0, 2.0, 3.0})
			_, err = env.db.Exec(`INSERT INTO vectors (id, namespace, vector) VALUES (10, ?, ?)`, ns, vec)
			require.NoError(t, err)

			assertRowCount(t, env.db, `SELECT * FROM vectors WHERE id = 10 AND namespace = ?`, 1, ns)

			_, err = env.db.Exec(`DELETE FROM vectors WHERE id = 10 AND namespace = ?`, ns)
			require.NoError(t, err)

			assertRowCount(t, env.db, `SELECT * FROM vectors WHERE id = 10 AND namespace = ?`, 0, ns)

			// Confirm deletion via SDK.
			sdkResp, sdkErr := env.client.Vector().GetVector(ctx, &sdktransport.GetVectorRequest{
				Namespace: ns,
				ID:        10,
			})
			if sdkErr == nil {
				assert.False(t, sdkResp.Found, "deleted vector must not be found by SDK (%s)", proto)
			}
		})
	}
}

// TestVector_MultiProtocol_BlobEncoding verifies that a float32 slice survives
// a round trip through the vtab BLOB column without data loss for each
// protocol.
func TestVector_MultiProtocol_BlobEncoding(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ns := uniqueNamespace(proto, "blob-ns")

			_, err := env.db.Exec(
				`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, 5, 'cosine', 'flat')`,
				ns,
			)
			require.NoError(t, err)

			original := []float32{1.5, 2.5, 3.5, 4.5, 5.5}
			blob := float32sToBytes(original)

			_, err = env.db.Exec(`INSERT INTO vectors (id, namespace, vector) VALUES (100, ?, ?)`, ns, blob)
			require.NoError(t, err)

			rows := scanAllRows(t, env.db,
				`SELECT vector FROM vectors WHERE id = 100 AND namespace = ?`, ns)
			require.Len(t, rows, 1)

			gotBlob, ok := rows[0]["vector"].([]byte)
			require.True(t, ok, "vector column should return []byte (%s)", proto)

			decoded := bytesToFloat32s(t, gotBlob)
			require.Len(t, decoded, len(original))
			for i, f := range original {
				assert.InDelta(t, f, decoded[i], 0.0001, "float32 mismatch at index %d (%s)", i, proto)
			}
		})
	}
}

// TestVector_MultiProtocol_QueryByNamespace inserts vectors in two namespaces
// and verifies that WHERE namespace = ? returns only vectors in that namespace
// for each protocol.
func TestVector_MultiProtocol_QueryByNamespace(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			alpha := uniqueNamespace(proto, "alpha")
			beta := uniqueNamespace(proto, "beta")

			for _, ns := range []string{alpha, beta} {
				_, err := env.client.Vector().CreateNamespace(ctx, &sdktransport.CreateNamespaceRequest{
					Namespace:      ns,
					Dimension:      2,
					DistanceMetric: "euclidean",
					IndexType:      "flat",
				})
				require.NoError(t, err)
			}

			vecA := float32sToBytes([]float32{1.0, 0.0})
			vecB := float32sToBytes([]float32{0.0, 1.0})

			_, err := env.db.Exec(`INSERT INTO vectors (id, namespace, vector) VALUES (1, ?, ?)`, alpha, vecA)
			require.NoError(t, err)
			_, err = env.db.Exec(`INSERT INTO vectors (id, namespace, vector) VALUES (2, ?, ?)`, alpha, vecA)
			require.NoError(t, err)
			_, err = env.db.Exec(`INSERT INTO vectors (id, namespace, vector) VALUES (3, ?, ?)`, beta, vecB)
			require.NoError(t, err)

			alphaRows := scanAllRows(t, env.db,
				`SELECT id FROM vectors WHERE namespace = ?`, alpha)
			assert.Len(t, alphaRows, 2, "expected 2 vectors in alpha namespace (%s)", proto)

			betaRows := scanAllRows(t, env.db,
				`SELECT id FROM vectors WHERE namespace = ?`, beta)
			assert.Len(t, betaRows, 1, "expected 1 vector in beta namespace (%s)", proto)
			if len(betaRows) > 0 {
				assert.Equal(t, int64(3), betaRows[0]["id"])
			}
		})
	}
}
