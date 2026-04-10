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
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// kvSchema is the DDL that declares the KV virtual table schema to SQLite.
// key is the primary key column; value is an opaque BLOB.
const kvSchema = "CREATE TABLE x(key TEXT PRIMARY KEY, value BLOB)"

// Column index constants keep column references readable.
const (
	colKey   = 0
	colValue = 1
)

// Index strategy numbers communicated through BestIndex → Filter.
//
//	idxFullScan    — no usable constraint: scan("") to fetch all pairs.
//	idxPointLookup — key = ?: call get(key) for a single row.
//	idxPrefixScan  — key LIKE 'prefix%' or key GLOB 'prefix*': scan(prefix).
const (
	idxFullScan    = 0
	idxPointLookup = 1
	idxPrefixScan  = 2
)

// ---------------------------------------------------------------------------
// kvModule — implements sqlite3.Module
// ---------------------------------------------------------------------------

// kvModule is the virtual table module factory for the qrdb_kv table.
// One module instance is shared across all connections that use the same
// registered driver name.
type kvModule struct {
	kv        kvStore
	pragma    *PragmaStore
	chunkSize int
}

// Create is called when SQLite executes CREATE VIRTUAL TABLE … USING qrdb_kv.
// It declares the schema and returns a new kvTable instance.
func (m *kvModule) Create(c *sqlite3.SQLiteConn, _ []string) (sqlite3.VTab, error) {
	if err := c.DeclareVTab(kvSchema); err != nil {
		return nil, &ModuleCreateError{Module: kvModuleName, Err: err}
	}
	return m.newTable(), nil
}

// Connect is called when an existing virtual table is re-attached (e.g. after
// a reconnect). The schema declaration is the same as Create.
func (m *kvModule) Connect(c *sqlite3.SQLiteConn, _ []string) (sqlite3.VTab, error) {
	if err := c.DeclareVTab(kvSchema); err != nil {
		return nil, &ModuleCreateError{Module: kvModuleName, Err: err}
	}
	return m.newTable(), nil
}

// DestroyModule is called when the module is unregistered. Nothing to clean up.
func (m *kvModule) DestroyModule() {}

// newTable creates a kvTable that shares the module's kv store and pragma store.
func (m *kvModule) newTable() *kvTable {
	return &kvTable{
		kv:        m.kv,
		pragma:    m.pragma,
		chunkSize: m.chunkSize,
	}
}

// ---------------------------------------------------------------------------
// kvTable — implements sqlite3.VTab and sqlite3.VTabUpdater
// ---------------------------------------------------------------------------

// kvTable is a single virtual table instance. SQLite may create multiple
// kvTable instances for the same logical table across different connections.
//
// rowidSeq provides globally unique rowids across all cursors opened on this
// table instance. rowidKeys maps those rowids back to key strings so that
// Delete can recover the key from the integer rowid SQLite passes to xUpdate.
type kvTable struct {
	kv        kvStore
	pragma    *PragmaStore
	chunkSize int

	// rowidSeq is a monotonically increasing counter used to assign unique
	// rowids to rows as they are visited by cursors.
	rowidSeq atomic.Int64

	// rowidKeys maps rowid → key for any rows visited by active cursors.
	// Entries accumulate over the lifetime of the table instance, which is
	// acceptable because the table lives only for the duration of one SQLite
	// connection.
	rowidKeys sync.Map
}

// nextRowid returns the next unique rowid and atomically advances the counter.
func (t *kvTable) nextRowid() int64 {
	return t.rowidSeq.Add(1)
}

// registerRowid stores key under rowid so Delete can look it up.
func (t *kvTable) registerRowid(rowid int64, key string) {
	t.rowidKeys.Store(rowid, key)
}

// lookupRowid returns the key associated with rowid, or "" if not found.
func (t *kvTable) lookupRowid(rowid int64) (string, bool) {
	v, ok := t.rowidKeys.Load(rowid)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// BestIndex analyses WHERE-clause constraints and tells SQLite the best
// query strategy. The three strategies, in cost order:
//
//  1. Point lookup (key = ?)       → cost 1,   idxNum = idxPointLookup
//  2. Prefix scan  (key LIKE ?)    → cost 10,  idxNum = idxPrefixScan
//  3. Full scan    (no key cst)    → cost 1e6, idxNum = idxFullScan
func (t *kvTable) BestIndex(csts []sqlite3.InfoConstraint, _ []sqlite3.InfoOrderBy) (*sqlite3.IndexResult, error) {
	used := make([]bool, len(csts))

	for i, c := range csts {
		if !c.Usable || c.Column != colKey {
			continue
		}
		switch c.Op {
		case sqlite3.OpEQ:
			used[i] = true
			return &sqlite3.IndexResult{
				Used:           used,
				IdxNum:         idxPointLookup,
				IdxStr:         "eq",
				AlreadyOrdered: true,
				EstimatedCost:  1,
				EstimatedRows:  1,
			}, nil

		case sqlite3.OpLIKE, sqlite3.OpGLOB:
			used[i] = true
			return &sqlite3.IndexResult{
				Used:           used,
				IdxNum:         idxPrefixScan,
				IdxStr:         "prefix",
				AlreadyOrdered: false,
				EstimatedCost:  10,
				EstimatedRows:  100,
			}, nil
		}
	}

	return &sqlite3.IndexResult{
		Used:           used,
		IdxNum:         idxFullScan,
		IdxStr:         "scan",
		AlreadyOrdered: false,
		EstimatedCost:  1e6,
		EstimatedRows:  1000,
	}, nil
}

// Open creates a new cursor for iterating the virtual table.
func (t *kvTable) Open() (sqlite3.VTabCursor, error) {
	return &kvCursor{
		table:  t,
		cursor: newPrefetchCursor(t.chunkSize),
	}, nil
}

// Disconnect is called when the connection to this virtual table instance is
// closed. No persistent resources to release.
func (t *kvTable) Disconnect() error { return nil }

// Destroy is called when the virtual table is dropped. No server-side schema
// to remove; QRDB data persists independently.
func (t *kvTable) Destroy() error { return nil }

// Insert handles INSERT INTO kv (key, value) VALUES (?, ?).
//
// The go-sqlite3 VTabUpdater interface passes:
//   - newRowid: the provided or auto-assigned rowid (ignored; key is the PK)
//   - values:   [key, value] as the column values
func (t *kvTable) Insert(_ interface{}, values []interface{}) (int64, error) {
	key, val, err := extractKeyValue(values)
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	if err := t.kv.Put(ctx, key, val); err != nil {
		return 0, &KVPutError{Key: key, Err: err}
	}
	return 0, nil
}

// Update handles UPDATE kv SET value = ? WHERE key = ?.
//
// The go-sqlite3 VTabUpdater interface passes:
//   - newRowid: the (possibly changed) rowid — unused
//   - values:   [key, value] as the new column values
func (t *kvTable) Update(_ interface{}, values []interface{}) error {
	key, val, err := extractKeyValue(values)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := t.kv.Put(ctx, key, val); err != nil {
		return &KVPutError{Key: key, Err: err}
	}
	return nil
}

// Delete handles DELETE FROM kv WHERE key = ?.
//
// The go-sqlite3 VTabUpdater interface passes the rowid of the row to delete.
// The rowid was assigned by the cursor's Rowid() call during the preceding
// Filter/Next scan. We recover the key from the rowidKeys registry.
func (t *kvTable) Delete(rowid interface{}) error {
	var key string

	switch v := rowid.(type) {
	case int64:
		// Look up the key that was registered when this row was scanned.
		k, ok := t.lookupRowid(v)
		if !ok {
			// Rowid not found in registry — nothing to delete.
			return nil
		}
		key = k
	case string:
		key = v
	default:
		if rowid == nil {
			return nil
		}
		key = toString(rowid)
	}

	if key == "" {
		return nil
	}

	ctx := context.Background()
	if err := t.kv.Delete(ctx, key); err != nil {
		return &KVDeleteError{Key: key, Err: err}
	}
	return nil
}

// ---------------------------------------------------------------------------
// kvCursor — implements sqlite3.VTabCursor
// ---------------------------------------------------------------------------

// kvCursor iterates rows returned by a KV query. It wraps prefetchCursor for
// row buffering and tracks the current rowid for SQLite.
type kvCursor struct {
	table  *kvTable
	cursor *prefetchCursor
	rowid  int64 // current rowid, assigned from table.nextRowid()
}

// Filter populates the cursor based on the query plan chosen by BestIndex.
//
//	idxFullScan    — scan("") to fetch all key-value pairs.
//	idxPointLookup — get(vals[0]) for a single key.
//	idxPrefixScan  — scan(extractPrefix(vals[0])) for key LIKE 'prefix%'.
func (c *kvCursor) Filter(idxNum int, _ string, vals []interface{}) error {
	c.cursor.reset()
	c.rowid = 0

	ctx := context.Background()

	switch idxNum {
	case idxPointLookup:
		key := toString(vals[0])
		val, err := c.table.kv.Get(ctx, key)
		if err != nil {
			return &KVGetError{Key: key, Err: err}
		}
		if val == nil {
			c.cursor.load(nil)
			return nil
		}
		c.cursor.load([]row{{values: []any{key, val}}})

	case idxPrefixScan:
		pattern := toString(vals[0])
		prefix := extractLikePrefix(pattern)
		rows, err := c.scanToRows(ctx, prefix)
		if err != nil {
			return err
		}
		rows = filterRowsByPattern(rows, pattern)
		c.cursor.load(rows)

	default: // idxFullScan
		rows, err := c.scanToRows(ctx, "")
		if err != nil {
			return err
		}
		c.cursor.load(rows)
	}

	// Assign a rowid to the first row (if any) and register it.
	if !c.cursor.isEOF() {
		c.rowid = c.table.nextRowid()
		c.registerCurrentKey()
	}

	return nil
}

// Next advances the cursor to the next row.
func (c *kvCursor) Next() error {
	if err := c.cursor.next(); err != nil {
		if _, ok := err.(*CursorEOFError); ok {
			return nil
		}
		return err
	}
	if !c.cursor.isEOF() {
		c.rowid = c.table.nextRowid()
		c.registerCurrentKey()
	}
	return nil
}

// EOF returns true when there are no more rows to return.
func (c *kvCursor) EOF() bool {
	return c.cursor.isEOF()
}

// Column writes the value for column col into ctx.
//
//	col 0 (key)   → ResultText
//	col 1 (value) → ResultBlob (or ResultNull for nil values)
func (c *kvCursor) Column(ctx *sqlite3.SQLiteContext, col int) error {
	val, err := c.cursor.column(col)
	if err != nil {
		return err
	}
	switch col {
	case colKey:
		if s, ok := val.(string); ok {
			ctx.ResultText(s)
		} else {
			ctx.ResultNull()
		}
	case colValue:
		if val == nil {
			ctx.ResultNull()
		} else if b, ok := val.([]byte); ok {
			ctx.ResultBlob(b)
		} else {
			ctx.ResultText(toString(val))
		}
	default:
		ctx.ResultNull()
	}
	return nil
}

// Rowid returns the unique identifier for the current row. The rowid is
// assigned during Filter/Next and registered in the table's rowidKeys map
// so that Delete can recover the key from the integer rowid.
func (c *kvCursor) Rowid() (int64, error) {
	return c.rowid, nil
}

// Close releases cursor resources.
func (c *kvCursor) Close() error {
	c.cursor.reset()
	return nil
}

// registerCurrentKey records the mapping from the current rowid to the current
// row's key in the table's registry. It is a no-op when the cursor is at EOF.
func (c *kvCursor) registerCurrentKey() {
	if c.cursor.isEOF() {
		return
	}
	v, err := c.cursor.column(colKey)
	if err != nil {
		return
	}
	if key, ok := v.(string); ok {
		c.table.registerRowid(c.rowid, key)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// scanToRows calls kv.scan and converts the result map into a deterministically
// ordered slice of rows sorted by key.
func (c *kvCursor) scanToRows(ctx context.Context, prefix string) ([]row, error) {
	m, err := c.table.kv.Scan(ctx, prefix)
	if err != nil {
		return nil, &KVScanError{Prefix: prefix, Err: err}
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([]row, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, row{values: []any{k, m[k]}})
	}
	return rows, nil
}

// extractKeyValue unpacks the two column values [key, value] from values.
func extractKeyValue(values []interface{}) (key string, value []byte, err error) {
	if len(values) < 2 {
		return "", nil, &KVPutError{
			Key: "",
			Err: &InvalidPragmaValueError{
				Pragma: "column count",
				Value:  "< 2",
				Valid:  []string{"2"},
			},
		}
	}
	key = toString(values[0])
	if values[1] == nil {
		return key, nil, nil
	}
	switch v := values[1].(type) {
	case []byte:
		value = v
	case string:
		value = []byte(v)
	default:
		value = []byte(toString(v))
	}
	return key, value, nil
}

// extractLikePrefix strips trailing wildcard characters from a LIKE or GLOB
// pattern to obtain the longest constant key prefix. Examples:
//
//	"foo%"    → "foo"
//	"foo*"    → "foo"
//	"foo%bar" → "foo"
//	"foo_bar" → "foo"
func extractLikePrefix(pattern string) string {
	for i, ch := range pattern {
		if ch == '%' || ch == '*' || ch == '_' || ch == '?' {
			return pattern[:i]
		}
	}
	return pattern
}

// filterRowsByPattern retains only rows whose key matches the LIKE/GLOB
// pattern. Post-filter is necessary because the prefix scan may return keys
// that share the prefix but do not satisfy the full pattern (e.g., key LIKE
// 'foo_bar' matches "fooXbar" but not "foo").
func filterRowsByPattern(rows []row, pattern string) []row {
	if pattern == "" {
		return rows
	}
	out := rows[:0:0]
	isGlob := strings.ContainsAny(pattern, "*?")
	for _, r := range rows {
		key, _ := r.values[0].(string)
		if isGlob {
			prefix := extractLikePrefix(pattern)
			if strings.HasPrefix(key, prefix) {
				out = append(out, r)
			}
		} else if likeMatch(pattern, key) {
			out = append(out, r)
		}
	}
	return out
}

// likeMatch performs a case-sensitive LIKE match where '%' matches any
// sequence of characters (including the empty sequence) and '_' matches
// exactly one character.
func likeMatch(pattern, s string) bool {
	return likeMatchAt(pattern, s, 0, 0)
}

// likeMatchAt recursively matches pattern[pi:] against s[si:].
func likeMatchAt(pattern, s string, pi, si int) bool {
	for pi < len(pattern) {
		switch pattern[pi] {
		case '%':
			// Collapse consecutive '%' wildcards.
			for pi < len(pattern) && pattern[pi] == '%' {
				pi++
			}
			if pi == len(pattern) {
				return true
			}
			for ; si <= len(s); si++ {
				if likeMatchAt(pattern, s, pi, si) {
					return true
				}
			}
			return false
		case '_':
			if si >= len(s) {
				return false
			}
			pi++
			si++
		default:
			if si >= len(s) || pattern[pi] != s[si] {
				return false
			}
			pi++
			si++
		}
	}
	return si == len(s)
}

// toString converts an arbitrary value to its string representation. It
// handles the common SQLite value types returned by go-sqlite3 callbacks.
func toString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	case int64:
		return int64ToStr(s)
	case float64:
		return float64ToStr(s)
	}
	return ""
}

// int64ToStr converts n to its decimal string representation without
// importing strconv, keeping allocation overhead minimal.
//
// For negative values the magnitude is computed via uint64 arithmetic to
// correctly handle math.MinInt64 without signed overflow.
func int64ToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	// Compute the magnitude as uint64. For negative n, the two's complement
	// bit pattern of -n is equal to ^uint64(n) + 1, which is the same as
	// uint64(n) negated in unsigned arithmetic. This correctly handles
	// math.MinInt64 where -n would overflow.
	var u uint64
	if neg {
		u = -uint64(n) // unsigned negation of the two's complement value
	} else {
		u = uint64(n)
	}
	buf := [20]byte{}
	pos := len(buf)
	for u > 0 {
		pos--
		buf[pos] = byte('0' + u%10)
		u /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// float64ToStr converts f to a decimal string. Used only for fallback
// rowid conversion; precision beyond integer truncation is not required.
func float64ToStr(f float64) string {
	return int64ToStr(int64(f))
}
