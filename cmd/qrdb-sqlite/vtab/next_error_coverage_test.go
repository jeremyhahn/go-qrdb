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
// nextFetchError is a typed error used as the fetch failure sentinel.
// ---------------------------------------------------------------------------

type nextFetchError string

func (e nextFetchError) Error() string { return string(e) }

const errNextFetch nextFetchError = "chunked fetch failed"

// failingFetcher returns a fetcher function that returns one initial chunk and
// then fails on the second call — triggering the CursorFetchError path in
// prefetchCursor.next() which propagates as a non-CursorEOFError through the
// outer Next() methods.
func failingFetcher(initialRows []row) func() ([]row, bool, error) {
	called := 0
	return func() ([]row, bool, error) {
		called++
		if called == 1 {
			return initialRows, true, nil // first chunk: has more
		}
		return nil, false, errNextFetch // second chunk: fail
	}
}

// ---------------------------------------------------------------------------
// kvCursor.Next — non-EOF error path
// ---------------------------------------------------------------------------

func TestKVCursor_Next_FetchError(t *testing.T) {
	tbl := &kvTable{
		kv:     newMockKV(),
		pragma: newPragmaStore("t", "", ""),
	}
	c := &kvCursor{table: tbl, cursor: newPrefetchCursor(1)}
	c.cursor.loadChunked(failingFetcher([]row{
		{values: []any{"key1", []byte("val1")}},
	}))

	// First row is available.
	assert.False(t, c.EOF())
	// Advance to the second chunk — the fetcher fails, returning CursorFetchError.
	err := c.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chunked fetch failed")
}

// ---------------------------------------------------------------------------
// collectionCursor.Next — non-EOF error path
// ---------------------------------------------------------------------------

func TestCollectionCursor_Next_FetchError(t *testing.T) {
	tbl := &collectionTable{
		meta:      &collectionMeta{name: "t", database: "db", schema: map[string]string{}},
		store:     newMockCollectionStore(),
		chunkSize: 1,
	}
	c := &collectionCursor{table: tbl, cursor: newPrefetchCursor(1)}
	c.cursor.loadChunked(failingFetcher([]row{
		{values: []any{int64(1)}},
	}))

	assert.False(t, c.EOF())
	err := c.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chunked fetch failed")
}

// ---------------------------------------------------------------------------
// entityCursor.Next — non-EOF error path
// ---------------------------------------------------------------------------

func TestEntityCursor_Next_FetchError(t *testing.T) {
	tbl := &entityTable{
		meta:      buildUserMeta(),
		store:     newMockEntityStore(),
		chunkSize: 1,
	}
	c := &entityCursor{table: tbl, cursor: newPrefetchCursor(1)}
	c.cursor.loadChunked(failingFetcher([]row{
		{values: []any{int64(1), "a@x.com", "Alice", int64(30)}},
	}))

	assert.False(t, c.EOF())
	err := c.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chunked fetch failed")
}

// ---------------------------------------------------------------------------
// datapointCursor.Next — non-EOF error path
// ---------------------------------------------------------------------------

func TestDatapointCursor_Next_FetchError(t *testing.T) {
	tbl := &datapointTable{store: newMockDatapointStore(), chunkSize: 1}
	c := &datapointCursor{table: tbl, cursor: newPrefetchCursor(1)}
	c.cursor.loadChunked(failingFetcher([]row{
		{values: []any{"cpu", int64(1000), float64(0.5), ""}},
	}))

	assert.False(t, c.EOF())
	err := c.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chunked fetch failed")
}

// ---------------------------------------------------------------------------
// metricsCursor.Next — non-EOF error path
// ---------------------------------------------------------------------------

func TestMetricsCursor_Next_FetchError(t *testing.T) {
	tbl := &metricsTable{store: newMockMetricsStore(), chunkSize: 1}
	c := &metricsCursor{table: tbl, cursor: newPrefetchCursor(1)}
	c.cursor.loadChunked(failingFetcher([]row{
		{values: []any{"cpu", "CPU util", "%", "gauge", int64(10)}},
	}))

	assert.False(t, c.EOF())
	err := c.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chunked fetch failed")
}

// ---------------------------------------------------------------------------
// namespaceCursor.Next — non-EOF error path
// ---------------------------------------------------------------------------

func TestNamespaceCursor_Next_FetchError(t *testing.T) {
	tbl := &namespaceTable{store: newMockNamespaceStore(), chunkSize: 1}
	c := &namespaceCursor{table: tbl, cursor: newPrefetchCursor(1)}
	c.cursor.loadChunked(failingFetcher([]row{
		{values: []any{"ns1", uint32(64), "cosine", "flat", int64(0)}},
	}))

	assert.False(t, c.EOF())
	err := c.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chunked fetch failed")
}

// ---------------------------------------------------------------------------
// vectorCursor.Next — non-EOF error path
// ---------------------------------------------------------------------------

func TestVectorCursor_Next_FetchError(t *testing.T) {
	tbl := &vectorTable{store: newMockVectorStore(), chunkSize: 1}
	c := &vectorCursor{table: tbl, cursor: newPrefetchCursor(1)}
	c.cursor.loadChunked(failingFetcher([]row{
		{values: []any{int64(1), "ns1", []byte{0x01}, "meta"}},
	}))

	assert.False(t, c.EOF())
	err := c.Next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chunked fetch failed")
}
