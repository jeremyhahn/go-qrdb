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

package vtab

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// makeRows builds a slice of n rows, each containing a single integer value
// equal to the row's index.
func makeRows(n int) []row {
	rows := make([]row, n)
	for i := range rows {
		rows[i] = row{values: []any{i}}
	}
	return rows
}

// makeChunkedFetcher returns a fetcher that delivers totalRows rows in
// chunks of chunkSize, recording how many times it is called in calls.
func makeChunkedFetcher(totalRows, chunkSize int, calls *int) func() ([]row, bool, error) {
	delivered := 0
	return func() ([]row, bool, error) {
		*calls++
		if delivered >= totalRows {
			return nil, false, nil
		}
		end := delivered + chunkSize
		if end > totalRows {
			end = totalRows
		}
		chunk := makeRows(end - delivered)
		// Shift values so they reflect absolute row indices.
		for i := range chunk {
			chunk[i].values[0] = delivered + i
		}
		delivered = end
		hasMore := delivered < totalRows
		return chunk, hasMore, nil
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestPrefetchCursor_Empty(t *testing.T) {
	c := newPrefetchCursor(0)
	c.load(nil)

	if !c.isEOF() {
		t.Fatal("expected EOF on empty cursor")
	}

	if err := c.next(); !errors.As(err, new(*CursorEOFError)) {
		t.Fatalf("next on empty cursor: want CursorEOFError, got %v", err)
	}
}

func TestPrefetchCursor_SingleRow(t *testing.T) {
	c := newPrefetchCursor(0)
	c.load(makeRows(1))

	if c.isEOF() {
		t.Fatal("cursor should not be EOF after loading one row")
	}

	// Read the single row.
	val, err := c.column(0)
	if err != nil {
		t.Fatalf("column(0): unexpected error: %v", err)
	}
	if val.(int) != 0 {
		t.Fatalf("expected value 0, got %v", val)
	}

	// Advance past the only row.
	if err := c.next(); err != nil {
		t.Fatalf("next: unexpected error: %v", err)
	}
	if !c.isEOF() {
		t.Fatal("cursor should be EOF after advancing past last row")
	}

	// Further calls to next should return CursorEOFError.
	if err := c.next(); !errors.As(err, new(*CursorEOFError)) {
		t.Fatalf("next past EOF: want CursorEOFError, got %v", err)
	}
}

func TestPrefetchCursor_MultipleRows(t *testing.T) {
	const n = 10
	c := newPrefetchCursor(0)
	c.load(makeRows(n))

	for i := 0; i < n; i++ {
		if c.isEOF() {
			t.Fatalf("unexpected EOF at row %d", i)
		}
		val, err := c.column(0)
		if err != nil {
			t.Fatalf("row %d column(0): %v", i, err)
		}
		if val.(int) != i {
			t.Fatalf("row %d: expected %d, got %v", i, i, val)
		}
		if err := c.next(); err != nil && i < n-1 {
			t.Fatalf("row %d next: unexpected error: %v", i, err)
		}
	}

	if !c.isEOF() {
		t.Fatal("cursor should be EOF after iterating all rows")
	}
}

func TestPrefetchCursor_ChunkedIteration(t *testing.T) {
	const totalRows = 5000
	const chunkSize = 1000

	var calls int
	c := newPrefetchCursor(chunkSize)
	c.loadChunked(makeChunkedFetcher(totalRows, chunkSize, &calls))

	// loadChunked fetches the first chunk immediately — that's call #1.
	if calls != 1 {
		t.Fatalf("expected 1 fetch after loadChunked, got %d", calls)
	}

	var count int
	for !c.isEOF() {
		val, err := c.column(0)
		if err != nil {
			t.Fatalf("column(0) at row %d: %v", count, err)
		}
		if val.(int) != count {
			t.Fatalf("row %d: expected value %d, got %v", count, count, val)
		}
		count++
		if err := c.next(); err != nil && !errors.As(err, new(*CursorEOFError)) {
			t.Fatalf("next at row %d: %v", count, err)
		}
	}

	if count != totalRows {
		t.Fatalf("expected %d rows, iterated %d", totalRows, count)
	}

	// 5000 rows / 1000 per chunk = 5 fetcher invocations total.
	if calls != 5 {
		t.Fatalf("expected 5 fetcher calls for %d rows with chunk %d, got %d",
			totalRows, chunkSize, calls)
	}
}

func TestPrefetchCursor_LazyLoad(t *testing.T) {
	// Verify that the second chunk is not fetched until the first is exhausted.
	const chunkSize = 3
	callLog := make([]int, 0, 4)
	chunkNum := 0

	fetcher := func() ([]row, bool, error) {
		chunkNum++
		callLog = append(callLog, chunkNum)
		if chunkNum == 1 {
			return makeRows(chunkSize), true, nil
		}
		return makeRows(chunkSize), false, nil
	}

	c := newPrefetchCursor(chunkSize)
	c.loadChunked(fetcher)

	// After loadChunked: chunk 1 has been fetched.
	if len(callLog) != 1 {
		t.Fatalf("expected 1 fetch after loadChunked, got %d", len(callLog))
	}

	// Exhaust the first chunk (rows 0, 1, 2).
	for i := 0; i < chunkSize-1; i++ {
		if err := c.next(); err != nil {
			t.Fatalf("next at step %d: %v", i, err)
		}
		// Second chunk must not have been fetched yet.
		if len(callLog) != 1 {
			t.Fatalf("second chunk fetched too early at step %d", i)
		}
	}

	// One more next triggers the fetch of chunk 2.
	if err := c.next(); err != nil {
		t.Fatalf("next to trigger chunk 2: %v", err)
	}
	if len(callLog) != 2 {
		t.Fatalf("expected 2 fetches after exhausting chunk 1, got %d", len(callLog))
	}
}

func TestPrefetchCursor_Column(t *testing.T) {
	c := newPrefetchCursor(0)
	c.load([]row{
		{values: []any{"hello", 42, true}},
	})

	tests := []struct {
		idx  int
		want any
	}{
		{0, "hello"},
		{1, 42},
		{2, true},
	}

	for _, tt := range tests {
		got, err := c.column(tt.idx)
		if err != nil {
			t.Errorf("column(%d): unexpected error: %v", tt.idx, err)
			continue
		}
		if got != tt.want {
			t.Errorf("column(%d): want %v, got %v", tt.idx, tt.want, got)
		}
	}
}

func TestPrefetchCursor_Column_OutOfBounds(t *testing.T) {
	c := newPrefetchCursor(0)
	c.load([]row{{values: []any{"only"}}})

	tests := []struct {
		name string
		idx  int
	}{
		{"negative index", -1},
		{"one past end", 1},
		{"far past end", 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := c.column(tt.idx)
			var oobErr *CursorColumnOutOfBoundsError
			if !errors.As(err, &oobErr) {
				t.Fatalf("want CursorColumnOutOfBoundsError, got %v", err)
			}
			if oobErr.Index != tt.idx {
				t.Errorf("error Index: want %d, got %d", tt.idx, oobErr.Index)
			}
			if oobErr.Width != 1 {
				t.Errorf("error Width: want 1, got %d", oobErr.Width)
			}
		})
	}
}

func TestPrefetchCursor_Reset(t *testing.T) {
	c := newPrefetchCursor(0)
	c.load(makeRows(5))

	// Advance a few steps.
	_ = c.next()
	_ = c.next()

	c.reset()

	if !c.isEOF() {
		t.Fatal("cursor should be EOF after reset")
	}
	if _, err := c.currentRow(); !errors.As(err, new(*CursorEOFError)) {
		t.Fatalf("currentRow after reset: want CursorEOFError, got %v", err)
	}

	// Reuse: reload and verify fresh iteration.
	c.load(makeRows(3))
	if c.isEOF() {
		t.Fatal("cursor should not be EOF after reload")
	}
	val, err := c.column(0)
	if err != nil {
		t.Fatalf("column(0) after reload: %v", err)
	}
	if val.(int) != 0 {
		t.Fatalf("expected 0 after reload, got %v", val)
	}
}

func TestPrefetchCursor_CurrentRow(t *testing.T) {
	want := row{values: []any{"a", 1, nil}}
	c := newPrefetchCursor(0)
	c.load([]row{want})

	got, err := c.currentRow()
	if err != nil {
		t.Fatalf("currentRow: unexpected error: %v", err)
	}
	if len(got.values) != len(want.values) {
		t.Fatalf("row length: want %d, got %d", len(want.values), len(got.values))
	}
	for i, v := range want.values {
		if got.values[i] != v {
			t.Errorf("values[%d]: want %v, got %v", i, v, got.values[i])
		}
	}
}

func TestPrefetchCursor_CurrentRow_EOF(t *testing.T) {
	c := newPrefetchCursor(0)

	// Empty cursor — immediately at EOF.
	c.load(nil)
	if _, err := c.currentRow(); !errors.As(err, new(*CursorEOFError)) {
		t.Fatalf("currentRow on empty cursor: want CursorEOFError, got %v", err)
	}

	// Single row cursor advanced past end.
	c.load(makeRows(1))
	_ = c.next() // advances past the only row
	if _, err := c.currentRow(); !errors.As(err, new(*CursorEOFError)) {
		t.Fatalf("currentRow after advancing past last row: want CursorEOFError, got %v", err)
	}
}

func TestCursorErrors_Messages(t *testing.T) {
	t.Run("CursorEOFError", func(t *testing.T) {
		err := &CursorEOFError{}
		if err.Error() == "" {
			t.Fatal("CursorEOFError.Error() must not be empty")
		}
	})

	t.Run("CursorColumnOutOfBoundsError", func(t *testing.T) {
		err := &CursorColumnOutOfBoundsError{Index: 5, Width: 2}
		if err.Error() == "" {
			t.Fatal("CursorColumnOutOfBoundsError.Error() must not be empty")
		}
	})

	t.Run("CursorFetchError", func(t *testing.T) {
		inner := errors.New("io timeout")
		err := &CursorFetchError{Err: inner}
		if err.Error() == "" {
			t.Fatal("CursorFetchError.Error() must not be empty")
		}
		if !errors.Is(err.Unwrap(), inner) {
			t.Fatal("CursorFetchError.Unwrap() must return inner error")
		}
	})
}

func TestPrefetchCursor_ChunkedFetchError(t *testing.T) {
	sentinel := errors.New("network failure")
	calls := 0

	fetcher := func() ([]row, bool, error) {
		calls++
		if calls == 1 {
			return makeRows(2), true, nil
		}
		return nil, false, sentinel
	}

	c := newPrefetchCursor(2)
	c.loadChunked(fetcher)

	if c.isEOF() {
		t.Fatal("cursor should not be EOF after successful first chunk")
	}

	// Exhaust the first chunk.
	_ = c.next()

	// next should trigger the failing fetch.
	err := c.next()
	var fetchErr *CursorFetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("want CursorFetchError, got %v", err)
	}
	if !errors.Is(fetchErr.Unwrap(), sentinel) {
		t.Fatalf("wrapped error: want sentinel, got %v", fetchErr.Unwrap())
	}
	if !c.isEOF() {
		t.Fatal("cursor should be EOF after fetch error")
	}
}
