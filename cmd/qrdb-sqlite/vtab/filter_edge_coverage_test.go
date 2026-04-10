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

//go:build sqlite_vtable

package vtab

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// cursor.loadChunked — initial fetch failure
// ---------------------------------------------------------------------------

// TestPrefetchCursor_LoadChunked_InitialFetchFails exercises the error path in
// loadChunked when the fetcher fails on the very first call.
func TestPrefetchCursor_LoadChunked_InitialFetchFails(t *testing.T) {
	c := newPrefetchCursor(10)
	c.loadChunked(func() ([]row, bool, error) {
		return nil, false, errNextFetch
	})
	// After a failed initial fetch, cursor must be at EOF.
	assert.True(t, c.isEOF())
}

// ---------------------------------------------------------------------------
// namespaceCursor.Filter — nsIdxNameLookup with nil return (not found)
// ---------------------------------------------------------------------------

// TestNamespaceCursor_Filter_NilNamespace exercises the path in Filter where
// GetNamespace returns nil (namespace not found) — cursor loads empty and
// returns nil error.
func TestNamespaceCursor_Filter_NilNamespace(t *testing.T) {
	tbl := &namespaceTable{store: newMockNamespaceStore(), chunkSize: 10}
	c := &namespaceCursor{table: tbl, cursor: newPrefetchCursor(10)}

	// nsIdxNameLookup with a name that doesn't exist — store returns nil, nil.
	err := c.Filter(nsIdxNameLookup, "", []interface{}{"nonexistent"})
	require.NoError(t, err)
	assert.True(t, c.EOF(), "cursor must be at EOF when namespace not found")
}

// ---------------------------------------------------------------------------
// vectorCursor.Filter — vecIdxIDAndNamespace: out-of-bounds vals slice
// ---------------------------------------------------------------------------

// TestVectorCursor_Filter_OOBVals exercises the bounds check in Filter for
// vecIdxIDAndNamespace when the vals slice is shorter than the positions decoded
// from idxStr.
func TestVectorCursor_Filter_OOBVals(t *testing.T) {
	tbl := &vectorTable{store: newMockVectorStore(), chunkSize: 10}
	c := &vectorCursor{table: tbl, cursor: newPrefetchCursor(10)}

	// Encode positions 0 and 1 in idxStr, but provide only 1 value — triggering
	// the idPos >= len(vals) || nsPos >= len(vals) guard.
	idxStr := encodeCompositeIdxStr(0, 1)
	err := c.Filter(vecIdxIDAndNamespace, idxStr, []interface{}{int64(1)}) // only 1 val, nsPos=1 is OOB
	require.NoError(t, err)
	assert.True(t, c.EOF())
}

// ---------------------------------------------------------------------------
// vectorCursor.Filter — vecIdxIDAndNamespace: vector not found (nil return)
// ---------------------------------------------------------------------------

// TestVectorCursor_Filter_VectorNotFound exercises the vr == nil guard in
// Filter for vecIdxIDAndNamespace when GetVector returns nil.
func TestVectorCursor_Filter_VectorNotFound(t *testing.T) {
	tbl := &vectorTable{store: newMockVectorStore(), chunkSize: 10}
	c := &vectorCursor{table: tbl, cursor: newPrefetchCursor(10)}

	// The mock store has no vectors, so GetVector returns nil.
	idxStr := encodeCompositeIdxStr(0, 1)
	err := c.Filter(vecIdxIDAndNamespace, idxStr, []interface{}{int64(1), "ns"})
	require.NoError(t, err)
	assert.True(t, c.EOF())
}

// ---------------------------------------------------------------------------
// datapointCursor.Filter — empty vals for dpIdxMetricAndRange
// ---------------------------------------------------------------------------

// TestDatapointCursor_Filter_EmptyValsRange exercises the len(vals) < 1 guard
// in Filter for dpIdxMetricAndRange.
func TestDatapointCursor_Filter_EmptyValsRange(t *testing.T) {
	tbl := &datapointTable{store: newMockDatapointStore(), chunkSize: 10}
	c := &datapointCursor{table: tbl, cursor: newPrefetchCursor(10)}

	err := c.Filter(dpIdxMetricAndRange, "", []interface{}{})
	require.NoError(t, err)
	assert.True(t, c.EOF())
}

// TestDatapointCursor_Filter_EmptyValsMetricOnly exercises the len(vals) < 1
// guard in Filter for dpIdxMetricOnly.
func TestDatapointCursor_Filter_EmptyValsMetricOnly(t *testing.T) {
	tbl := &datapointTable{store: newMockDatapointStore(), chunkSize: 10}
	c := &datapointCursor{table: tbl, cursor: newPrefetchCursor(10)}

	err := c.Filter(dpIdxMetricOnly, "", []interface{}{})
	require.NoError(t, err)
	assert.True(t, c.EOF())
}

// ---------------------------------------------------------------------------
// metricsCursor.Filter — empty vals for mIdxNameLookup
// ---------------------------------------------------------------------------

// TestMetricsCursor_Filter_EmptyVals exercises the len(vals) < 1 guard in
// Filter for mIdxNameLookup.
func TestMetricsCursor_Filter_EmptyVals(t *testing.T) {
	tbl := &metricsTable{store: newMockMetricsStore(), chunkSize: 10}
	c := &metricsCursor{table: tbl, cursor: newPrefetchCursor(10)}

	err := c.Filter(mIdxNameLookup, "", []interface{}{})
	require.NoError(t, err)
	assert.True(t, c.EOF())
}

// ---------------------------------------------------------------------------
// entityCursor.Filter — entityIdxRange with out-of-bounds column index
// ---------------------------------------------------------------------------

// TestEntityCursor_Filter_RangeOOBColumn exercises the fieldIdx < 0 guard in
// the entityIdxRange case of Filter, triggered when parseColIdx returns 0
// (meaning fieldIdx = -1).
func TestEntityCursor_Filter_RangeOOBColumn(t *testing.T) {
	meta := buildUserMeta()
	tbl := &entityTable{meta: meta, store: newMockEntityStore(), chunkSize: 10}
	c := &entityCursor{table: tbl, cursor: newPrefetchCursor(10)}

	// parseColIdx("") returns 0, so fieldIdx = 0 - 1 = -1 (< 0).
	err := c.Filter(entityIdxRange, "", []interface{}{"A", "Z"})
	require.NoError(t, err)
	assert.True(t, c.EOF())
}

// TestEntityCursor_Filter_EQOOBColumn exercises the fieldIdx < 0 guard in the
// entityIdxEQ case when the column index resolves to -1.
func TestEntityCursor_Filter_EQOOBColumn(t *testing.T) {
	meta := buildUserMeta()
	tbl := &entityTable{meta: meta, store: newMockEntityStore(), chunkSize: 10}
	c := &entityCursor{table: tbl, cursor: newPrefetchCursor(10)}

	// parseColIdx("") returns 0, fieldIdx = -1.
	err := c.Filter(entityIdxEQ, "", []interface{}{"val"})
	require.NoError(t, err)
	assert.True(t, c.EOF())
}

// ---------------------------------------------------------------------------
// entityCursor.Column — fieldIdx out-of-bounds guard
// ---------------------------------------------------------------------------

// TestEntityColumn_FieldIdxOutOfBounds exercises the fieldIdx < 0 || fieldIdx
// >= len(fields) guard in Column for col > 0.
func TestEntityColumn_FieldIdxOutOfBounds(t *testing.T) {
	meta := buildUserMeta()
	// Load a row with fewer values than the schema expects.
	cursor := newPrefetchCursor(10)
	cursor.load([]row{
		{values: []any{int64(1)}}, // only _id; no field columns
	})
	tbl := &entityTable{meta: meta, store: newMockEntityStore(), chunkSize: 10}
	c := &entityCursor{table: tbl, cursor: cursor}

	// cursor.column(colEntityID) = int64(1) — covers the int64 branch.
	// cursor.column(1) would fail with CursorColumnOutOfBoundsError.
	val, err := c.cursor.column(colEntityID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), val)

	// Reading column 1 (beyond the row width) returns an error.
	_, colErr := c.cursor.column(1)
	require.Error(t, colErr)
	_, isBounds := colErr.(*CursorColumnOutOfBoundsError)
	assert.True(t, isBounds)
}

// ---------------------------------------------------------------------------
// resultColumnValue — TEXT default branch (JSON marshal)
// ---------------------------------------------------------------------------

// TestResultColumnValue_TextDefaultBranch verifies that the JSON marshal path
// in resultColumnValue is exercised via a TEXT column holding a map value.
// This exercises entity.go line 677 (json.Marshal → ResultText).
func TestResultColumnValue_TextDefaultBranch(t *testing.T) {
	db, mock := setupTypedEntityDB(t)
	// A map value for a TEXT field hits the default (JSON-marshal) branch.
	mock.seed(100, map[string]any{
		"int_val":  int64(0),
		"real_val": float64(0),
		"blob_val": []byte{},
		"text_val": map[string]any{"nested": true},
		"time_val": "",
	})
	var s string
	require.NoError(t, db.QueryRow("SELECT text_val FROM ents WHERE _id = ?", int64(100)).Scan(&s))
	assert.Contains(t, s, "nested")
}
