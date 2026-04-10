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
	"sync"
	"sync/atomic"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// vectorSchema is the DDL that declares the vector_vectors virtual table
// schema to SQLite. id is the integer primary key; namespace groups vectors;
// vector is a raw float32 BLOB; metadata is a JSON TEXT field.
const vectorSchema = "CREATE TABLE x(id INTEGER PRIMARY KEY, namespace TEXT, vector BLOB, metadata TEXT)"

// vectorModuleName is the SQLite module name used with
// CREATE VIRTUAL TABLE … USING qrdb_vectors.
const vectorModuleName = "qrdb_vectors"

// Vector column indices.
const (
	colVecID        = 0
	colVecNamespace = 1
	colVecVector    = 2
	colVecMetadata  = 3
)

// Vector index strategy numbers communicated through BestIndex → Filter.
//
//	vecIdxFullScan          — no usable constraint: scan all vectors.
//	vecIdxIDAndNamespace    — id = ? AND namespace = ?: composite point lookup.
//	vecIdxNamespaceScan     — namespace = ?: scan all vectors in a namespace.
const (
	vecIdxFullScan       = 0
	vecIdxIDAndNamespace = 1
	vecIdxNamespaceScan  = 2
)

// idxStrSeparator is the delimiter used to encode two constraint argument
// positions in the IdxStr field when both id and namespace are constrained.
const idxStrSeparator = ","

// ---------------------------------------------------------------------------
// VectorRow — domain type
// ---------------------------------------------------------------------------

// VectorRow holds the data for a single vector entry as stored in QRDB.
// Vector holds the raw little-endian float32 bytes; Metadata is a JSON string.
type VectorRow struct {
	ID        uint64
	Namespace string
	Vector    []byte // raw float32 bytes
	Metadata  string // JSON string
}

// ---------------------------------------------------------------------------
// vectorStore interface
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// vectorModule — implements sqlite3.Module
// ---------------------------------------------------------------------------

// vectorModule is the virtual table module factory for the qrdb_vectors table.
// One module instance is shared across all connections that use the same
// registered driver name.
type vectorModule struct {
	store     vectorStore
	chunkSize int
}

// Create is called when SQLite executes
// CREATE VIRTUAL TABLE … USING qrdb_vectors. It declares the schema and
// returns a new vectorTable instance.
func (m *vectorModule) Create(c *sqlite3.SQLiteConn, _ []string) (sqlite3.VTab, error) {
	if err := c.DeclareVTab(vectorSchema); err != nil {
		return nil, &ModuleCreateError{Module: vectorModuleName, Err: err}
	}
	return m.newTable(), nil
}

// Connect is called when an existing virtual table is re-attached.
func (m *vectorModule) Connect(c *sqlite3.SQLiteConn, _ []string) (sqlite3.VTab, error) {
	if err := c.DeclareVTab(vectorSchema); err != nil {
		return nil, &ModuleCreateError{Module: vectorModuleName, Err: err}
	}
	return m.newTable(), nil
}

// DestroyModule is called when the module is unregistered.
func (m *vectorModule) DestroyModule() {}

// newTable creates a vectorTable that shares the module's store.
func (m *vectorModule) newTable() *vectorTable {
	return &vectorTable{
		store:     m.store,
		chunkSize: m.chunkSize,
	}
}

// ---------------------------------------------------------------------------
// vectorTable — implements sqlite3.VTab and sqlite3.VTabUpdater
// ---------------------------------------------------------------------------

// vectorTable is a single virtual table instance for vector storage. SQLite
// may create multiple instances across different connections; each is
// independent.
//
// rowidSeq provides globally unique rowids across all cursors opened on this
// table instance. rowidKeys maps each rowid back to its (namespace, id) pair
// so that Delete can recover the vector identity from the integer rowid SQLite
// passes to xUpdate.
type vectorTable struct {
	store     vectorStore
	chunkSize int

	// rowidSeq is a monotonically increasing counter used to assign unique
	// rowids to rows as they are visited by cursors.
	rowidSeq atomic.Int64

	// rowidKeys maps rowid → vectorKey for rows visited by active cursors.
	// Entries accumulate for the lifetime of the table instance, which is
	// acceptable because the table lives only for one SQLite connection.
	rowidKeys sync.Map
}

// vectorKey stores the namespace and id pair needed to address a single vector.
type vectorKey struct {
	namespace string
	id        uint64
}

// nextRowid returns the next unique rowid and atomically advances the counter.
func (t *vectorTable) nextRowid() int64 {
	return t.rowidSeq.Add(1)
}

// registerRowid stores the vector key under rowid so Delete can look it up.
func (t *vectorTable) registerRowid(rowid int64, key vectorKey) {
	t.rowidKeys.Store(rowid, key)
}

// lookupRowid returns the vectorKey associated with rowid, or the zero value
// if not found.
func (t *vectorTable) lookupRowid(rowid int64) (vectorKey, bool) {
	v, ok := t.rowidKeys.Load(rowid)
	if !ok {
		return vectorKey{}, false
	}
	return v.(vectorKey), true
}

// BestIndex analyses WHERE-clause constraints and communicates the cheapest
// query plan back to SQLite.
//
// Strategy selection (cheapest first):
//  1. id = ? AND namespace = ?  → vecIdxIDAndNamespace, cost   1
//  2. namespace = ?             → vecIdxNamespaceScan,  cost  50
//  3. full scan                 → vecIdxFullScan,       cost 1000
//
// For the composite key lookup, both constraint argument positions are encoded
// in IdxStr as "<idArgPos>,<nsArgPos>" so that Filter can unpack them.
func (t *vectorTable) BestIndex(csts []sqlite3.InfoConstraint, _ []sqlite3.InfoOrderBy) (*sqlite3.IndexResult, error) {
	used := make([]bool, len(csts))

	// Find equality constraints on id and namespace.
	idIdx := -1
	nsIdx := -1
	for i, c := range csts {
		if !c.Usable || c.Op != sqlite3.OpEQ {
			continue
		}
		switch c.Column {
		case colVecID:
			idIdx = i
		case colVecNamespace:
			nsIdx = i
		}
	}

	// Composite: both id and namespace constrained.
	if idIdx >= 0 && nsIdx >= 0 {
		used[idIdx] = true
		used[nsIdx] = true
		// Encode the two argument positions so Filter can extract them.
		// go-sqlite3 numbers constraint arguments by their position in the
		// used slice (argument 0 = first used constraint, etc.). We therefore
		// record which constraint comes first by index order.
		idxStr := encodeCompositeIdxStr(idIdx, nsIdx)
		return &sqlite3.IndexResult{
			Used:           used,
			IdxNum:         vecIdxIDAndNamespace,
			IdxStr:         idxStr,
			AlreadyOrdered: true,
			EstimatedCost:  1,
			EstimatedRows:  1,
		}, nil
	}

	// Namespace-only scan.
	if nsIdx >= 0 {
		used[nsIdx] = true
		return &sqlite3.IndexResult{
			Used:           used,
			IdxNum:         vecIdxNamespaceScan,
			IdxStr:         "ns",
			AlreadyOrdered: false,
			EstimatedCost:  50,
			EstimatedRows:  100,
		}, nil
	}

	// Full scan fallback.
	return &sqlite3.IndexResult{
		Used:           used,
		IdxNum:         vecIdxFullScan,
		IdxStr:         "scan",
		AlreadyOrdered: false,
		EstimatedCost:  1000,
		EstimatedRows:  10000,
	}, nil
}

// Open creates a new cursor for iterating the vector virtual table.
func (t *vectorTable) Open() (sqlite3.VTabCursor, error) {
	return &vectorCursor{
		table:  t,
		cursor: newPrefetchCursor(t.chunkSize),
	}, nil
}

// Disconnect is called when the connection to this virtual table is closed.
func (t *vectorTable) Disconnect() error { return nil }

// Destroy is called when the virtual table is dropped. Vector data in QRDB
// persists independently of the SQLite virtual table declaration.
func (t *vectorTable) Destroy() error { return nil }

// Insert handles INSERT INTO vectors (id, namespace, vector, metadata) VALUES (…).
//
// The go-sqlite3 VTabUpdater interface passes:
//   - newRowid: the provided or auto-assigned rowid (ignored; id is the PK)
//   - values:   [id, namespace, vector, metadata] as the column values
func (t *vectorTable) Insert(_ interface{}, values []interface{}) (int64, error) {
	vr, err := extractVectorValues(values)
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	if err := t.store.UpsertVector(ctx, vr.Namespace, vr.ID, vr.Vector, vr.Metadata); err != nil {
		return 0, &VectorUpsertError{Namespace: vr.Namespace, ID: vr.ID, Err: err}
	}
	return int64(vr.ID), nil
}

// Update handles UPDATE vectors SET … WHERE id = ?.
//
// Vector data is immutable at the key level; we forward updates as upserts so
// the underlying store can decide the semantics.
func (t *vectorTable) Update(_ interface{}, values []interface{}) error {
	vr, err := extractVectorValues(values)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := t.store.UpsertVector(ctx, vr.Namespace, vr.ID, vr.Vector, vr.Metadata); err != nil {
		return &VectorUpsertError{Namespace: vr.Namespace, ID: vr.ID, Err: err}
	}
	return nil
}

// Delete handles DELETE FROM vectors WHERE id = ?.
//
// The go-sqlite3 VTabUpdater interface passes the rowid of the row to delete.
// The rowid was assigned by the cursor's Rowid() call during the preceding
// Filter/Next scan. We recover the (namespace, id) pair from the rowidKeys
// registry.
func (t *vectorTable) Delete(rowid interface{}) error {
	switch v := rowid.(type) {
	case int64:
		key, ok := t.lookupRowid(v)
		if !ok {
			// Rowid not found — nothing to delete.
			return nil
		}
		ctx := context.Background()
		if err := t.store.DeleteVector(ctx, key.namespace, key.id); err != nil {
			return &VectorDeleteError{Namespace: key.namespace, ID: key.id, Err: err}
		}
	default:
		if rowid == nil {
			return nil
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// vectorCursor — implements sqlite3.VTabCursor
// ---------------------------------------------------------------------------

// vectorCursor iterates rows returned by a vector query. It wraps
// prefetchCursor for row buffering and tracks the current rowid for SQLite.
type vectorCursor struct {
	table  *vectorTable
	cursor *prefetchCursor
	rowid  int64 // current rowid, assigned from table.nextRowid()
}

// Filter populates the cursor based on the query plan chosen by BestIndex.
//
//	vecIdxIDAndNamespace — getVector(namespace=vals[nsPos], id=vals[idPos]).
//	vecIdxNamespaceScan  — listVectors(namespace=vals[0]).
//	vecIdxFullScan       — full scan (requires namespace; falls back to empty).
func (c *vectorCursor) Filter(idxNum int, idxStr string, vals []interface{}) error {
	c.cursor.reset()
	c.rowid = 0

	ctx := context.Background()

	switch idxNum {
	case vecIdxIDAndNamespace:
		idPos, nsPos := decodeCompositeIdxStr(idxStr)
		if idPos >= len(vals) || nsPos >= len(vals) {
			c.cursor.load(nil)
			return nil
		}
		id := toUint64(vals[idPos])
		namespace := toString(vals[nsPos])
		vr, err := c.table.store.GetVector(ctx, namespace, id)
		if err != nil {
			return &VectorGetError{Namespace: namespace, ID: id, Err: err}
		}
		if vr == nil {
			c.cursor.load(nil)
			return nil
		}
		c.cursor.load([]row{vectorRowToRow(vr)})

	case vecIdxNamespaceScan:
		namespace := toString(vals[0])
		rows, err := c.loadNamespaceRows(ctx, namespace)
		if err != nil {
			return err
		}
		c.cursor.load(rows)

	default: // vecIdxFullScan — no namespace constraint; returns empty result
		// A full scan without a namespace filter is unsupported because
		// the underlying store is namespace-scoped. Return no rows rather
		// than scanning all namespaces, which would require an expensive
		// multi-namespace list followed by a merge.
		c.cursor.load(nil)
	}

	if !c.cursor.isEOF() {
		c.rowid = c.table.nextRowid()
		c.registerCurrentKey()
	}
	return nil
}

// Next advances the cursor to the next row.
func (c *vectorCursor) Next() error {
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
func (c *vectorCursor) EOF() bool {
	return c.cursor.isEOF()
}

// Column writes the value for column col into ctx.
//
//	col 0 (id)        → ResultInt64
//	col 1 (namespace) → ResultText
//	col 2 (vector)    → ResultBlob
//	col 3 (metadata)  → ResultText
func (c *vectorCursor) Column(ctx *sqlite3.SQLiteContext, col int) error {
	val, err := c.cursor.column(col)
	if err != nil {
		return err
	}
	switch col {
	case colVecID:
		switch v := val.(type) {
		case int64:
			ctx.ResultInt64(v)
		case uint64:
			ctx.ResultInt64(int64(v))
		default:
			ctx.ResultNull()
		}
	case colVecNamespace, colVecMetadata:
		if s, ok := val.(string); ok {
			ctx.ResultText(s)
		} else {
			ctx.ResultNull()
		}
	case colVecVector:
		if b, ok := val.([]byte); ok {
			ctx.ResultBlob(b)
		} else {
			ctx.ResultNull()
		}
	default:
		ctx.ResultNull()
	}
	return nil
}

// Rowid returns the unique identifier for the current row. The rowid is
// assigned during Filter/Next and registered in the table's rowidKeys map so
// that Delete can recover the (namespace, id) pair.
func (c *vectorCursor) Rowid() (int64, error) {
	return c.rowid, nil
}

// Close releases cursor resources.
func (c *vectorCursor) Close() error {
	c.cursor.reset()
	return nil
}

// registerCurrentKey records the mapping from the current rowid to the current
// row's (namespace, id) pair in the table's registry.
func (c *vectorCursor) registerCurrentKey() {
	if c.cursor.isEOF() {
		return
	}
	idVal, err := c.cursor.column(colVecID)
	if err != nil {
		return
	}
	nsVal, err := c.cursor.column(colVecNamespace)
	if err != nil {
		return
	}
	id := toUint64(idVal)
	namespace, _ := nsVal.(string)
	if id > 0 && namespace != "" {
		c.table.registerRowid(c.rowid, vectorKey{namespace: namespace, id: id})
	}
}

// loadNamespaceRows fetches all vectors for the given namespace and converts
// them into cursor rows.
func (c *vectorCursor) loadNamespaceRows(ctx context.Context, namespace string) ([]row, error) {
	vecs, err := c.table.store.ListVectors(ctx, namespace)
	if err != nil {
		return nil, &VectorListError{Namespace: namespace, Err: err}
	}
	rows := make([]row, len(vecs))
	for i := range vecs {
		rows[i] = vectorRowToRow(&vecs[i])
	}
	return rows, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// vectorRowToRow converts a *VectorRow into a prefetchCursor row whose values
// align with the virtual table schema column order:
// [id, namespace, vector, metadata].
func vectorRowToRow(vr *VectorRow) row {
	return row{values: []any{
		int64(vr.ID),
		vr.Namespace,
		vr.Vector,
		vr.Metadata,
	}}
}

// extractVectorValues unpacks the four column values from the values slice
// provided by go-sqlite3 for INSERT/UPDATE operations.
//
//	values[0] = id        (INTEGER)
//	values[1] = namespace (TEXT)
//	values[2] = vector    (BLOB)
//	values[3] = metadata  (TEXT)
func extractVectorValues(values []interface{}) (VectorRow, error) {
	if len(values) < 4 {
		return VectorRow{}, &VectorUpsertError{
			Namespace: "",
			ID:        0,
			Err: &InvalidPragmaValueError{
				Pragma: "column count",
				Value:  "< 4",
				Valid:  []string{"4"},
			},
		}
	}
	vr := VectorRow{
		ID:        toUint64(values[0]),
		Namespace: toString(values[1]),
	}
	switch v := values[2].(type) {
	case []byte:
		vr.Vector = v
	case string:
		vr.Vector = []byte(v)
	}
	vr.Metadata = toString(values[3])
	return vr, nil
}

// encodeCompositeIdxStr encodes two constraint indices into the IdxStr that
// BestIndex communicates to Filter. The format is "<idIdx>,<nsIdx>" where
// both values are the positions within the vals slice that Filter receives
// (i.e. the number of used constraints before this one in scan order).
//
// go-sqlite3 numbers the vals arguments by the order that used constraints
// appear in the constraints slice, so we count how many earlier used
// constraints precede each of the two.
func encodeCompositeIdxStr(idIdx, nsIdx int) string {
	// Argument position 0 = first used constraint in constraint array order.
	var idArg, nsArg int
	if idIdx < nsIdx {
		idArg = 0
		nsArg = 1
	} else {
		nsArg = 0
		idArg = 1
	}
	return int64ToStr(int64(idArg)) + idxStrSeparator + int64ToStr(int64(nsArg))
}

// decodeCompositeIdxStr decodes the IdxStr encoded by encodeCompositeIdxStr.
// Returns the argument positions for the id and namespace values within the
// vals slice passed to Filter. Returns (0, 1) as a safe default on parse error.
func decodeCompositeIdxStr(s string) (idPos, nsPos int) {
	sep := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			sep = i
			break
		}
	}
	if sep < 0 {
		return 0, 1
	}
	idPos = parseColIdx(s[:sep])
	nsPos = parseColIdx(s[sep+1:])
	return idPos, nsPos
}
