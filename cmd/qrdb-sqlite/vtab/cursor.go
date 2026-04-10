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

// Package vtab provides the core infrastructure for QRDB SQLite virtual
// tables, including the prefetching cursor and transaction write buffer.
package vtab

// ---------------------------------------------------------------------------
// Row
// ---------------------------------------------------------------------------

// row represents a single result row with arbitrary column values.
type row struct {
	values []any
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// CursorEOFError is returned when an operation is attempted on a cursor
// that has reached the end of its result set.
type CursorEOFError struct{}

// Error implements the error interface.
func (e *CursorEOFError) Error() string {
	return "vtab: cursor is at EOF"
}

// CursorColumnOutOfBoundsError is returned when the requested column index
// exceeds the number of columns in the current row.
type CursorColumnOutOfBoundsError struct {
	Index int
	Width int
}

// Error implements the error interface.
func (e *CursorColumnOutOfBoundsError) Error() string {
	return "vtab: column index out of bounds"
}

// CursorFetchError is returned when the chunked fetcher function fails to
// retrieve the next batch of rows.
type CursorFetchError struct {
	Err error
}

// Error implements the error interface.
func (e *CursorFetchError) Error() string {
	return "vtab: cursor fetch failed: " + e.Err.Error()
}

// Unwrap returns the underlying fetch error.
func (e *CursorFetchError) Unwrap() error {
	return e.Err
}

// ---------------------------------------------------------------------------
// prefetchCursor
// ---------------------------------------------------------------------------

// defaultChunkSize is the number of rows fetched per chunk when using
// chunked iteration. This value balances memory pressure against round-trip
// overhead for large result sets.
const defaultChunkSize = 1000

// prefetchCursor buffers rows fetched in bulk from qrdb.
//
// SQLite calls xFilter once (populating the buffer) and then calls xNext one
// row at a time. For small result sets, callers use load to populate all rows
// immediately. For large result sets, callers use loadChunked to enable lazy
// chunk-by-chunk fetching so that memory use stays bounded.
type prefetchCursor struct {
	rows    []row
	pos     int
	eof     bool
	fetcher func() ([]row, bool, error) // returns rows, hasMore, error
	hasMore bool                        // true when more chunks remain
	chunk   int                         // chunk size
}

// newPrefetchCursor creates a prefetchCursor with the given chunk size.
// If chunkSize is zero or negative, defaultChunkSize is used.
func newPrefetchCursor(chunkSize int) *prefetchCursor {
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	return &prefetchCursor{
		chunk: chunkSize,
		eof:   true, // empty until loaded
	}
}

// load populates the cursor with all rows at once. This is appropriate for
// small result sets where fetching everything upfront is acceptable.
// Calling load replaces any previously loaded data and resets the position.
func (c *prefetchCursor) load(rows []row) {
	c.rows = rows
	c.pos = 0
	c.fetcher = nil
	c.hasMore = false
	c.eof = len(rows) == 0
}

// loadChunked configures the cursor for lazy chunk-by-chunk loading.
// The provided fetcher is called each time a new chunk is needed. It returns
// the next batch of rows, a hasMore flag (true when additional chunks follow),
// and any error. The first chunk is fetched immediately so that isEOF reflects
// the correct initial state.
func (c *prefetchCursor) loadChunked(fetcher func() ([]row, bool, error)) {
	c.fetcher = fetcher
	c.rows = nil
	c.pos = 0
	c.eof = false
	c.hasMore = false

	// Fetch the first chunk immediately so callers can determine whether
	// there are any results without calling next first.
	rows, more, err := fetcher()
	if err != nil {
		c.eof = true
		return
	}
	c.rows = rows
	c.hasMore = more
	c.eof = len(rows) == 0 && !more
}

// next advances the cursor to the next row. If the current chunk is exhausted
// and more chunks exist, the next chunk is fetched via the fetcher function.
// Returns CursorEOFError when no more rows are available.
func (c *prefetchCursor) next() error {
	if c.eof {
		return &CursorEOFError{}
	}

	c.pos++

	// Current chunk still has rows — nothing more to do.
	if c.pos < len(c.rows) {
		return nil
	}

	// Current chunk exhausted. Fetch next chunk if one exists.
	if c.fetcher != nil && c.hasMore {
		rows, more, err := c.fetcher()
		if err != nil {
			c.eof = true
			return &CursorFetchError{Err: err}
		}
		c.rows = rows
		c.pos = 0
		c.hasMore = more
		if len(rows) == 0 {
			c.eof = true
		}
		return nil
	}

	// No more data.
	c.eof = true
	return nil
}

// isEOF returns true when the cursor has no more rows to return.
func (c *prefetchCursor) isEOF() bool {
	return c.eof
}

// column returns the value at the given column index for the current row.
// Returns CursorEOFError if the cursor is at EOF, or
// CursorColumnOutOfBoundsError if idx is out of range.
func (c *prefetchCursor) column(idx int) (any, error) {
	r, err := c.currentRow()
	if err != nil {
		return nil, err
	}
	if idx < 0 || idx >= len(r.values) {
		return nil, &CursorColumnOutOfBoundsError{Index: idx, Width: len(r.values)}
	}
	return r.values[idx], nil
}

// reset clears all buffered data and returns the cursor to a reusable empty
// state. After reset, isEOF returns true until load or loadChunked is called.
func (c *prefetchCursor) reset() {
	c.rows = nil
	c.pos = 0
	c.eof = true
	c.fetcher = nil
	c.hasMore = false
}

// currentRow returns the full row at the current cursor position.
// Returns CursorEOFError if the cursor is at EOF.
func (c *prefetchCursor) currentRow() (row, error) {
	if c.eof || c.pos >= len(c.rows) {
		return row{}, &CursorEOFError{}
	}
	return c.rows[c.pos], nil
}
