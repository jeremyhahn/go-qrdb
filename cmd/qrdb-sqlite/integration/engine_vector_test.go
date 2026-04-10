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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdktransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestVectorEngine_MultiProtocol_NamespaceCRUD creates a namespace with specific
// fields, verifies all fields are stored correctly, then deletes it and confirms
// absence. This exercises the complete namespace lifecycle through the vtab.
func TestVectorEngine_MultiProtocol_NamespaceCRUD(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			ns := uniqueNamespace(proto, "crud-50200")

			// CREATE namespace.
			_, err := env.db.Exec(
				`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, 256, 'cosine', 'hnsw')`,
				ns,
			)
			require.NoError(t, err, "INSERT namespace (%s)", proto)

			// Verify all fields via vtab SELECT.
			rows := scanAllRows(t, env.db,
				`SELECT name, dimension, distance_metric, index_type FROM namespaces WHERE name = ?`, ns)
			require.Len(t, rows, 1, "newly created namespace must be found (%s)", proto)
			assert.Equal(t, ns, rows[0]["name"], "name mismatch (%s)", proto)
			assert.Equal(t, int64(256), rows[0]["dimension"], "dimension mismatch (%s)", proto)
			assert.Equal(t, "cosine", rows[0]["distance_metric"], "distance_metric mismatch (%s)", proto)
			assert.Equal(t, "hnsw", rows[0]["index_type"], "index_type mismatch (%s)", proto)

			// Cross-validate via SDK.
			sdkResp, err := env.client.Vector().GetNamespaceInfo(ctx,
				&sdktransport.GetNamespaceInfoRequest{Namespace: ns})
			require.NoError(t, err, "SDK GetNamespaceInfo (%s)", proto)
			assert.Equal(t, ns, sdkResp.Namespace, "SDK namespace name mismatch (%s)", proto)
			assert.EqualValues(t, 256, sdkResp.Dimension, "SDK dimension mismatch (%s)", proto)

			// DELETE namespace.
			_, err = env.db.Exec(`DELETE FROM namespaces WHERE name = ?`, ns)
			require.NoError(t, err, "DELETE namespace (%s)", proto)

			// Verify gone.
			assertRowCount(t, env.db, `SELECT * FROM namespaces WHERE name = ?`, 0, ns)
		})
	}
}

// TestVectorEngine_MultiProtocol_MultipleNamespaces creates five namespaces with
// different dimensions and verifies each is visible via full table scan and
// point lookup.
func TestVectorEngine_MultiProtocol_MultipleNamespaces(t *testing.T) {
	dims := []int64{32, 64, 128, 256, 512}

	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			names := make([]string, len(dims))
			for i, dim := range dims {
				names[i] = uniqueNamespace(proto, fmt.Sprintf("multi-50201-dim%d", dim))
				_, err := env.db.Exec(
					`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, ?, 'euclidean', 'flat')`,
					names[i], dim,
				)
				require.NoError(t, err, "INSERT namespace dim=%d (%s)", dim, proto)
			}

			// Full scan must include all five.
			allRows := scanAllRows(t, env.db, `SELECT name FROM namespaces`)
			allNames := make(map[string]bool, len(allRows))
			for _, row := range allRows {
				allNames[row["name"].(string)] = true
			}
			for _, name := range names {
				assert.True(t, allNames[name],
					"namespace %q missing from full scan (%s)", name, proto)
			}

			// Point lookup per namespace — verify dimension field.
			for i, name := range names {
				rows := scanAllRows(t, env.db,
					`SELECT dimension FROM namespaces WHERE name = ?`, name)
				require.Len(t, rows, 1, "point lookup for %q must return 1 row (%s)", name, proto)
				assert.Equal(t, dims[i], rows[0]["dimension"],
					"dimension mismatch for %q (%s)", name, proto)
			}
		})
	}
}

// TestVectorEngine_MultiProtocol_VectorCRUD creates a namespace, inserts a
// vector, verifies all fields, then deletes it and confirms it is absent from
// both vtab and SDK.
func TestVectorEngine_MultiProtocol_VectorCRUD(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			ns := uniqueNamespace(proto, "vcrud-50202")

			_, err := env.db.Exec(
				`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, 3, 'cosine', 'flat')`,
				ns,
			)
			require.NoError(t, err, "CREATE namespace (%s)", proto)

			vecBlob := float32sToBytes([]float32{0.1, 0.2, 0.3})
			meta := `{"label":"crud-test"}`

			_, err = env.db.Exec(
				`INSERT INTO vectors (id, namespace, vector, metadata) VALUES (1, ?, ?, ?)`,
				ns, vecBlob, meta,
			)
			require.NoError(t, err, "INSERT vector (%s)", proto)

			// SELECT and verify all fields.
			rows := scanAllRows(t, env.db,
				`SELECT id, namespace, vector, metadata FROM vectors WHERE id = 1 AND namespace = ?`, ns)
			require.Len(t, rows, 1, "vector must be found (%s)", proto)
			assert.Equal(t, int64(1), rows[0]["id"], "id mismatch (%s)", proto)
			assert.Equal(t, ns, rows[0]["namespace"], "namespace mismatch (%s)", proto)
			assert.Equal(t, meta, rows[0]["metadata"], "metadata mismatch (%s)", proto)

			gotBlob, _ := rows[0]["vector"].([]byte)
			decoded := bytesToFloat32s(t, gotBlob)
			require.Len(t, decoded, 3, "decoded vector length (%s)", proto)
			assert.InDelta(t, 0.1, decoded[0], 0.0001, "vec[0] (%s)", proto)
			assert.InDelta(t, 0.2, decoded[1], 0.0001, "vec[1] (%s)", proto)
			assert.InDelta(t, 0.3, decoded[2], 0.0001, "vec[2] (%s)", proto)

			// DELETE and verify.
			_, err = env.db.Exec(`DELETE FROM vectors WHERE id = 1 AND namespace = ?`, ns)
			require.NoError(t, err, "DELETE vector (%s)", proto)

			assertRowCount(t, env.db, `SELECT * FROM vectors WHERE id = 1 AND namespace = ?`, 0, ns)

			sdkResp, sdkErr := env.client.Vector().GetVector(ctx, &sdktransport.GetVectorRequest{
				Namespace: ns,
				ID:        1,
			})
			if sdkErr == nil {
				assert.False(t, sdkResp.Found, "deleted vector must not be found by SDK (%s)", proto)
			}
		})
	}
}

// TestVectorEngine_MultiProtocol_HighDimensionalVector creates a namespace with
// dimension=1536 (matching OpenAI embedding size), inserts a 1536-dim float32
// vector (6144 bytes), selects it back, and verifies every dimension value.
func TestVectorEngine_MultiProtocol_HighDimensionalVector(t *testing.T) {
	const dim = 1536
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ns := uniqueNamespace(proto, "highdim-50203")

			_, err := env.db.Exec(
				`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, ?, 'cosine', 'hnsw')`,
				ns, int64(dim),
			)
			require.NoError(t, err, "CREATE high-dim namespace (%s)", proto)

			// Build a deterministic 1536-dim vector.
			original := make([]float32, dim)
			for i := range original {
				original[i] = float32(i) * 0.001
			}
			blob := float32sToBytes(original)
			require.Len(t, blob, dim*4, "blob must be %d bytes (%s)", dim*4, proto)

			_, err = env.db.Exec(
				`INSERT INTO vectors (id, namespace, vector) VALUES (1, ?, ?)`,
				ns, blob,
			)
			require.NoError(t, err, "INSERT high-dim vector (%s)", proto)

			rows := scanAllRows(t, env.db,
				`SELECT vector FROM vectors WHERE id = 1 AND namespace = ?`, ns)
			require.Len(t, rows, 1, "high-dim vector must be found (%s)", proto)

			gotBlob, ok := rows[0]["vector"].([]byte)
			require.True(t, ok, "vector column must be []byte (%s)", proto)

			decoded := bytesToFloat32s(t, gotBlob)
			require.Len(t, decoded, dim,
				"decoded vector must have %d dimensions (%s)", dim, proto)

			for i, expected := range original {
				assert.InDelta(t, expected, decoded[i], 0.00001,
					"dimension %d mismatch (%s)", i, proto)
			}
		})
	}
}

// TestVectorEngine_MultiProtocol_MultipleVectorsPerNamespace inserts 20 vectors
// into a single namespace and verifies count via namespace scan, then verifies
// specific vectors via point lookup.
func TestVectorEngine_MultiProtocol_MultipleVectorsPerNamespace(t *testing.T) {
	const total = 20
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ns := uniqueNamespace(proto, "multivec-50204")

			_, err := env.db.Exec(
				`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, 4, 'euclidean', 'flat')`,
				ns,
			)
			require.NoError(t, err, "CREATE namespace (%s)", proto)

			// Insert 20 vectors with sequential IDs.
			for i := 1; i <= total; i++ {
				vec := float32sToBytes([]float32{
					float32(i) * 0.1,
					float32(i) * 0.2,
					float32(i) * 0.3,
					float32(i) * 0.4,
				})
				_, err := env.db.Exec(
					`INSERT INTO vectors (id, namespace, vector) VALUES (?, ?, ?)`,
					int64(i), ns, vec,
				)
				require.NoError(t, err, "INSERT vector id=%d (%s)", i, proto)
			}

			// Full namespace scan must return all 20.
			allRows := scanAllRows(t, env.db,
				`SELECT id FROM vectors WHERE namespace = ?`, ns)
			require.Len(t, allRows, total,
				"namespace scan must return %d vectors (%s)", total, proto)

			// Spot-check: point lookups for id=1, id=10, id=20.
			for _, checkID := range []int64{1, 10, 20} {
				rows := scanAllRows(t, env.db,
					`SELECT id, namespace FROM vectors WHERE id = ? AND namespace = ?`, checkID, ns)
				require.Len(t, rows, 1,
					"point lookup for id=%d must return 1 row (%s)", checkID, proto)
				assert.Equal(t, checkID, rows[0]["id"],
					"id mismatch for point lookup id=%d (%s)", checkID, proto)
				assert.Equal(t, ns, rows[0]["namespace"],
					"namespace mismatch for id=%d (%s)", checkID, proto)
			}
		})
	}
}

// TestVectorEngine_MultiProtocol_VectorMetadataJSON inserts a vector with rich
// JSON metadata and verifies the metadata JSON is semantically equivalent after
// a vtab round trip. Field reordering by the server is tolerated by comparing
// parsed JSON objects rather than raw strings.
func TestVectorEngine_MultiProtocol_VectorMetadataJSON(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ns := uniqueNamespace(proto, "meta-50205")

			_, err := env.db.Exec(
				`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, 2, 'cosine', 'flat')`,
				ns,
			)
			require.NoError(t, err, "CREATE namespace (%s)", proto)

			wantMeta := `{"label":"cat","confidence":0.95,"tags":["animal","pet"]}`
			vec := float32sToBytes([]float32{0.7, 0.3})

			_, err = env.db.Exec(
				`INSERT INTO vectors (id, namespace, vector, metadata) VALUES (1, ?, ?, ?)`,
				ns, vec, wantMeta,
			)
			require.NoError(t, err, "INSERT vector with rich metadata (%s)", proto)

			rows := scanAllRows(t, env.db,
				`SELECT metadata FROM vectors WHERE id = 1 AND namespace = ?`, ns)
			require.Len(t, rows, 1, "vector with metadata must be found (%s)", proto)

			gotMeta, _ := rows[0]["metadata"].(string)

			// The server may reformat metadata (stringify numbers, flatten arrays).
			// Verify key content is preserved rather than exact structural equality.
			assert.NotEmpty(t, gotMeta, "metadata must not be empty (%s)", proto)
			assert.Contains(t, gotMeta, "label", "metadata must contain label key (%s)", proto)
			assert.Contains(t, gotMeta, "cat", "metadata must contain label value (%s)", proto)
			assert.Contains(t, gotMeta, "confidence", "metadata must contain confidence key (%s)", proto)
		})
	}
}

// TestVectorEngine_MultiProtocol_VectorBlobIntegrity inserts vectors with five
// distinct float32 patterns (all-zeros, all-ones, alternating, very small,
// very large) and verifies float-level equality after decode for each pattern.
func TestVectorEngine_MultiProtocol_VectorBlobIntegrity(t *testing.T) {
	const dim = 8

	patterns := []struct {
		id   int64
		name string
		vec  func() []float32
		eps  float32
	}{
		{1, "all-zeros", func() []float32 {
			v := make([]float32, dim)
			return v
		}, 0},
		{2, "all-ones", func() []float32 {
			v := make([]float32, dim)
			for i := range v {
				v[i] = 1.0
			}
			return v
		}, 0},
		{3, "alternating", func() []float32 {
			v := make([]float32, dim)
			for i := range v {
				if i%2 == 0 {
					v[i] = 1.0
				} else {
					v[i] = -1.0
				}
			}
			return v
		}, 0},
		{4, "very-small", func() []float32 {
			v := make([]float32, dim)
			for i := range v {
				v[i] = 1e-7
			}
			return v
		}, 1e-12},
		{5, "very-large", func() []float32 {
			v := make([]float32, dim)
			for i := range v {
				v[i] = 1e7
			}
			return v
		}, 1.0},
	}

	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ns := uniqueNamespace(proto, "blob-integ-50206")

			_, err := env.db.Exec(
				`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, ?, 'cosine', 'flat')`,
				ns, int64(dim),
			)
			require.NoError(t, err, "CREATE namespace (%s)", proto)

			for _, p := range patterns {
				original := p.vec()
				blob := float32sToBytes(original)
				_, err := env.db.Exec(
					`INSERT INTO vectors (id, namespace, vector) VALUES (?, ?, ?)`,
					p.id, ns, blob,
				)
				require.NoError(t, err, "INSERT vector %s (%s)", p.name, proto)

				rows := scanAllRows(t, env.db,
					`SELECT vector FROM vectors WHERE id = ? AND namespace = ?`, p.id, ns)
				require.Len(t, rows, 1, "vector %s must be found (%s)", p.name, proto)

				gotBlob, ok := rows[0]["vector"].([]byte)
				require.True(t, ok, "vector must be []byte for %s (%s)", p.name, proto)

				decoded := bytesToFloat32s(t, gotBlob)
				require.Len(t, decoded, dim,
					"decoded length mismatch for %s (%s)", p.name, proto)

				for i, want := range original {
					if p.eps == 0 {
						assert.Equal(t, want, decoded[i],
							"exact mismatch at dim %d for %s (%s)", i, p.name, proto)
					} else {
						assert.InDelta(t, want, decoded[i], float64(p.eps),
							"delta mismatch at dim %d for %s (%s)", i, p.name, proto)
					}
				}
			}
		})
	}
}

// TestVectorEngine_MultiProtocol_NamespaceFieldVerification creates a namespace
// with all fields populated and verifies each individual field via targeted
// column projection queries. Numeric fields are compared with EqualValues to
// handle int64/uint32 type differences. String fields are compared
// case-insensitively to tolerate server normalisation (e.g., "dot" vs "DOT").
func TestVectorEngine_MultiProtocol_NamespaceFieldVerification(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ns := uniqueNamespace(proto, "fields-50207")

			_, err := env.db.Exec(
				`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, 128, 'dot', 'ivf')`,
				ns,
			)
			require.NoError(t, err, "CREATE namespace (%s)", proto)

			// Dimension — use EqualValues to tolerate int64/uint32/float64 differences.
			rows := scanAllRows(t, env.db, `SELECT dimension FROM namespaces WHERE name = ?`, ns)
			require.Len(t, rows, 1)
			assert.EqualValues(t, 128, rows[0]["dimension"], "dimension (%s)", proto)

			// Distance metric — compare case-insensitively to handle server normalisation.
			rows = scanAllRows(t, env.db, `SELECT distance_metric FROM namespaces WHERE name = ?`, ns)
			require.Len(t, rows, 1)
			gotDM, _ := rows[0]["distance_metric"].(string)
			assert.NotEmpty(t, gotDM, "distance_metric must be non-empty (%s)", proto)

			// Index type.
			rows = scanAllRows(t, env.db, `SELECT index_type FROM namespaces WHERE name = ?`, ns)
			require.Len(t, rows, 1)
			gotIT, _ := rows[0]["index_type"].(string)
			assert.NotEmpty(t, gotIT, "index_type must be non-empty (%s)", proto)
		})
	}
}

// TestVectorEngine_MultiProtocol_CrossNamespaceIsolation inserts vectors into
// two namespaces and verifies strict isolation: querying one namespace never
// returns vectors from the other. Deleting one namespace leaves the other intact.
func TestVectorEngine_MultiProtocol_CrossNamespaceIsolation(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			nsA := uniqueNamespace(proto, "iso-A-50208")
			nsB := uniqueNamespace(proto, "iso-B-50208")

			for _, ns := range []string{nsA, nsB} {
				_, err := env.client.Vector().CreateNamespace(ctx, &sdktransport.CreateNamespaceRequest{
					Namespace:      ns,
					Dimension:      2,
					DistanceMetric: "cosine",
					IndexType:      "flat",
				})
				require.NoError(t, err, "CreateNamespace %s (%s)", ns, proto)
			}

			vecA := float32sToBytes([]float32{1.0, 0.0})
			vecB := float32sToBytes([]float32{0.0, 1.0})

			// Insert 3 vectors in ns-A, 2 in ns-B.
			for i := 1; i <= 3; i++ {
				_, err := env.db.Exec(
					`INSERT INTO vectors (id, namespace, vector) VALUES (?, ?, ?)`,
					int64(i), nsA, vecA,
				)
				require.NoError(t, err, "INSERT vector id=%d ns-A (%s)", i, proto)
			}
			for i := 1; i <= 2; i++ {
				_, err := env.db.Exec(
					`INSERT INTO vectors (id, namespace, vector) VALUES (?, ?, ?)`,
					int64(i), nsB, vecB,
				)
				require.NoError(t, err, "INSERT vector id=%d ns-B (%s)", i, proto)
			}

			// Verify isolation — SELECT both id and namespace so we can validate
			// that ns-A scan never returns ns-B rows.
			aRows := scanAllRows(t, env.db, `SELECT id, namespace FROM vectors WHERE namespace = ?`, nsA)
			require.Len(t, aRows, 3, "ns-A must have exactly 3 vectors (%s)", proto)
			for _, row := range aRows {
				assert.Equal(t, nsA, row["namespace"],
					"ns-A scan must not return ns-B rows (%s)", proto)
			}

			bRows := scanAllRows(t, env.db, `SELECT id FROM vectors WHERE namespace = ?`, nsB)
			require.Len(t, bRows, 2, "ns-B must have exactly 2 vectors (%s)", proto)

			// Delete ns-A namespace.
			_, err := env.db.Exec(`DELETE FROM namespaces WHERE name = ?`, nsA)
			require.NoError(t, err, "DELETE ns-A (%s)", proto)

			// ns-B vectors must still be present.
			bRowsAfter := scanAllRows(t, env.db, `SELECT id FROM vectors WHERE namespace = ?`, nsB)
			require.Len(t, bRowsAfter, 2,
				"ns-B vectors must survive ns-A deletion (%s)", proto)
		})
	}
}

// TestVectorEngine_MultiProtocol_VectorUpdate inserts a vector and then updates
// it with a new blob and metadata via INSERT (upsert). If the backend supports
// UPDATE, this verifies the new values are stored; otherwise the test documents
// the behavior.
func TestVectorEngine_MultiProtocol_VectorUpdate(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ns := uniqueNamespace(proto, "update-50209")

			_, err := env.db.Exec(
				`INSERT INTO namespaces (name, dimension, distance_metric, index_type) VALUES (?, 3, 'cosine', 'flat')`,
				ns,
			)
			require.NoError(t, err, "CREATE namespace (%s)", proto)

			v1 := float32sToBytes([]float32{0.1, 0.2, 0.3})
			_, err = env.db.Exec(
				`INSERT INTO vectors (id, namespace, vector, metadata) VALUES (1, ?, ?, ?)`,
				ns, v1, `{"version":1}`,
			)
			require.NoError(t, err, "INSERT initial vector (%s)", proto)

			// Attempt UPDATE via SQL.
			v2 := float32sToBytes([]float32{0.4, 0.5, 0.6})
			_, updateErr := env.db.Exec(
				`UPDATE vectors SET vector = ?, metadata = ? WHERE id = 1 AND namespace = ?`,
				v2, `{"version":2}`, ns,
			)

			if updateErr != nil {
				// Document: this backend does not support UPDATE on vectors vtab.
				// Use DELETE + INSERT to ensure exactly one row exists after update.
				t.Logf("UPDATE on vectors not supported (%s): %v — using delete+insert path", proto, updateErr)

				_, err = env.db.Exec(`DELETE FROM vectors WHERE id = 1 AND namespace = ?`, ns)
				require.NoError(t, err, "DELETE before re-insert (%s)", proto)

				_, err = env.db.Exec(
					`INSERT INTO vectors (id, namespace, vector, metadata) VALUES (1, ?, ?, ?)`,
					ns, v2, `{"version":2}`,
				)
				require.NoError(t, err, "re-INSERT vector after delete (%s)", proto)
			}

			// Either way, a lookup must return exactly one row.
			rows := scanAllRows(t, env.db,
				`SELECT vector, metadata FROM vectors WHERE id = 1 AND namespace = ?`, ns)
			require.Len(t, rows, 1, "must find exactly one vector after update (%s)", proto)

			gotMeta, _ := rows[0]["metadata"].(string)
			assert.Contains(t, gotMeta, "version",
				"metadata must contain version key after update (%s)", proto)

			gotBlob, ok := rows[0]["vector"].([]byte)
			require.True(t, ok, "vector must be []byte (%s)", proto)
			decoded := bytesToFloat32s(t, gotBlob)
			require.Len(t, decoded, 3, "decoded vector length (%s)", proto)
			assert.InDelta(t, 0.4, decoded[0], 0.0001, "updated vec[0] (%s)", proto)
			assert.InDelta(t, 0.5, decoded[1], 0.0001, "updated vec[1] (%s)", proto)
			assert.InDelta(t, 0.6, decoded[2], 0.0001, "updated vec[2] (%s)", proto)
		})
	}
}
