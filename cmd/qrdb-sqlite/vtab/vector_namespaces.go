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

	sqlite3 "github.com/mattn/go-sqlite3"
)

// namespaceSchema is the DDL that declares the vector_namespaces virtual table
// schema to SQLite. name is the primary key; the remaining columns describe the
// namespace configuration and statistics.
const namespaceSchema = "CREATE TABLE x(name TEXT PRIMARY KEY, dimension INTEGER, distance_metric TEXT, index_type TEXT, vector_count INTEGER)"

// namespaceModuleName is the SQLite module name used with
// CREATE VIRTUAL TABLE … USING qrdb_vector_namespaces.
const namespaceModuleName = "qrdb_vector_namespaces"

// Namespace column indices.
const (
	colNsName           = 0
	colNsDimension      = 1
	colNsDistanceMetric = 2
	colNsIndexType      = 3
	colNsVectorCount    = 4
)

// Namespace index strategy numbers communicated through BestIndex → Filter.
//
//	nsIdxFullScan    — no usable constraint: scan all namespaces.
//	nsIdxNameLookup  — name = ?: single point lookup by namespace name.
const (
	nsIdxFullScan   = 0
	nsIdxNameLookup = 1
)

// ---------------------------------------------------------------------------
// namespaceInfo — domain type
// ---------------------------------------------------------------------------

// NamespaceInfo holds the metadata for a single vector namespace as returned
// by the namespaceStore and exposed as a virtual table row.
type NamespaceInfo struct {
	Name           string
	Dimension      uint32
	DistanceMetric string
	IndexType      string
	VectorCount    uint64
}

// ---------------------------------------------------------------------------
// namespaceStore interface
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// namespaceModule — implements sqlite3.Module
// ---------------------------------------------------------------------------

// namespaceModule is the virtual table module factory for the
// qrdb_vector_namespaces table. One module instance is shared across all
// connections that use the same registered driver name.
type namespaceModule struct {
	store     namespaceStore
	chunkSize int
}

// Create is called when SQLite executes
// CREATE VIRTUAL TABLE … USING qrdb_vector_namespaces. It declares the schema
// and returns a new namespaceTable instance.
func (m *namespaceModule) Create(c *sqlite3.SQLiteConn, _ []string) (sqlite3.VTab, error) {
	if err := c.DeclareVTab(namespaceSchema); err != nil {
		return nil, &ModuleCreateError{Module: namespaceModuleName, Err: err}
	}
	return m.newTable(), nil
}

// Connect is called when an existing virtual table is re-attached. The schema
// declaration must match Create exactly.
func (m *namespaceModule) Connect(c *sqlite3.SQLiteConn, _ []string) (sqlite3.VTab, error) {
	if err := c.DeclareVTab(namespaceSchema); err != nil {
		return nil, &ModuleCreateError{Module: namespaceModuleName, Err: err}
	}
	return m.newTable(), nil
}

// DestroyModule is called when the module is unregistered. Nothing to clean up.
func (m *namespaceModule) DestroyModule() {}

// newTable creates a namespaceTable that shares the module's store.
func (m *namespaceModule) newTable() *namespaceTable {
	return &namespaceTable{
		store:     m.store,
		chunkSize: m.chunkSize,
	}
}

// ---------------------------------------------------------------------------
// namespaceTable — implements sqlite3.VTab and sqlite3.VTabUpdater
// ---------------------------------------------------------------------------

// namespaceTable is a single virtual table instance for the vector namespaces
// catalog. SQLite may create multiple instances across different connections;
// each is independent.
type namespaceTable struct {
	store      namespaceStore
	chunkSize  int
	rowidNames sync.Map // int64 rowid → string name
}

// BestIndex analyses WHERE-clause constraints and chooses the optimal query
// strategy.
//
// Strategy selection (cheapest first):
//  1. name = ?    → nsIdxNameLookup, cost   1
//  2. full scan   → nsIdxFullScan,   cost 1000
func (t *namespaceTable) BestIndex(csts []sqlite3.InfoConstraint, _ []sqlite3.InfoOrderBy) (*sqlite3.IndexResult, error) {
	used := make([]bool, len(csts))

	for i, c := range csts {
		if !c.Usable || c.Column != colNsName || c.Op != sqlite3.OpEQ {
			continue
		}
		used[i] = true
		return &sqlite3.IndexResult{
			Used:           used,
			IdxNum:         nsIdxNameLookup,
			IdxStr:         "eq",
			AlreadyOrdered: true,
			EstimatedCost:  1,
			EstimatedRows:  1,
		}, nil
	}

	return &sqlite3.IndexResult{
		Used:           used,
		IdxNum:         nsIdxFullScan,
		IdxStr:         "scan",
		AlreadyOrdered: false,
		EstimatedCost:  1000,
		EstimatedRows:  1000,
	}, nil
}

// Open creates a new cursor for iterating the namespace virtual table.
func (t *namespaceTable) Open() (sqlite3.VTabCursor, error) {
	return &namespaceCursor{
		table:  t,
		cursor: newPrefetchCursor(t.chunkSize),
	}, nil
}

// Disconnect is called when the connection to this virtual table is closed.
func (t *namespaceTable) Disconnect() error { return nil }

// Destroy is called when the virtual table is dropped. Namespace data in QRDB
// persists independently of the SQLite virtual table declaration.
func (t *namespaceTable) Destroy() error { return nil }

// Insert handles INSERT INTO vector_namespaces (name, dimension, …) VALUES (…).
//
// The go-sqlite3 VTabUpdater interface passes:
//   - newRowid: ignored; name is the primary key
//   - values:   [name, dimension, distance_metric, index_type, vector_count]
func (t *namespaceTable) Insert(_ interface{}, values []interface{}) (int64, error) {
	name, dimension, distanceMetric, indexType, err := extractNamespaceValues(values)
	if err != nil {
		return 0, err
	}
	ctx := context.Background()
	if err := t.store.CreateNamespace(ctx, name, dimension, distanceMetric, indexType); err != nil {
		return 0, &NamespaceCreateError{Name: name, Err: err}
	}
	return 0, nil
}

// Update handles UPDATE vector_namespaces SET … WHERE name = ?.
//
// Namespace configuration is immutable after creation in the underlying store;
// the update is forwarded as a re-create (delete then insert) at the store level.
// For simplicity this implementation delegates to createNamespace which the
// store treats as an upsert where supported.
func (t *namespaceTable) Update(_ interface{}, values []interface{}) error {
	name, dimension, distanceMetric, indexType, err := extractNamespaceValues(values)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := t.store.CreateNamespace(ctx, name, dimension, distanceMetric, indexType); err != nil {
		return &NamespaceCreateError{Name: name, Err: err}
	}
	return nil
}

// Delete handles DELETE FROM vector_namespaces WHERE name = ?.
//
// The go-sqlite3 VTabUpdater interface passes the rowid of the row to delete.
// Because name is the PRIMARY KEY, SQLite passes the name value directly as the
// rowid when it is a TEXT primary key.
func (t *namespaceTable) Delete(rowid interface{}) error {
	rid := toInt64(rowid)
	val, ok := t.rowidNames.Load(rid)
	if !ok {
		return nil
	}
	name, _ := val.(string)
	if name == "" {
		return nil
	}
	t.rowidNames.Delete(rid)
	ctx := context.Background()
	if err := t.store.DeleteNamespace(ctx, name); err != nil {
		return &NamespaceDeleteError{Name: name, Err: err}
	}
	return nil
}

// ---------------------------------------------------------------------------
// namespaceCursor — implements sqlite3.VTabCursor
// ---------------------------------------------------------------------------

// namespaceCursor iterates rows returned by a namespace query.
type namespaceCursor struct {
	table  *namespaceTable
	cursor *prefetchCursor
}

// Filter populates the cursor based on the query plan chosen by BestIndex.
//
//	nsIdxNameLookup — getNamespace(vals[0]) for a single namespace.
//	nsIdxFullScan   — listNamespaces() to enumerate all namespaces.
func (c *namespaceCursor) Filter(idxNum int, _ string, vals []interface{}) error {
	c.cursor.reset()

	ctx := context.Background()

	switch idxNum {
	case nsIdxNameLookup:
		name := toString(vals[0])
		ns, err := c.table.store.GetNamespace(ctx, name)
		if err != nil {
			return &NamespaceGetError{Name: name, Err: err}
		}
		if ns == nil {
			c.cursor.load(nil)
			return nil
		}
		c.cursor.load([]row{namespaceInfoToRow(ns)})

	default: // nsIdxFullScan
		nsList, err := c.table.store.ListNamespaces(ctx)
		if err != nil {
			return &NamespaceListError{Err: err}
		}
		rows := make([]row, len(nsList))
		for i := range nsList {
			rows[i] = namespaceInfoToRow(&nsList[i])
		}
		c.cursor.load(rows)
	}

	return nil
}

// Next advances the cursor to the next namespace row.
func (c *namespaceCursor) Next() error {
	if err := c.cursor.next(); err != nil {
		if _, ok := err.(*CursorEOFError); ok {
			return nil
		}
		return err
	}
	return nil
}

// EOF returns true when there are no more rows to return.
func (c *namespaceCursor) EOF() bool {
	return c.cursor.isEOF()
}

// Column writes the value for column col into ctx.
//
//	col 0 (name)            → ResultText
//	col 1 (dimension)       → ResultInt64
//	col 2 (distance_metric) → ResultText
//	col 3 (index_type)      → ResultText
//	col 4 (vector_count)    → ResultInt64
func (c *namespaceCursor) Column(ctx *sqlite3.SQLiteContext, col int) error {
	val, err := c.cursor.column(col)
	if err != nil {
		return err
	}
	switch col {
	case colNsName, colNsDistanceMetric, colNsIndexType:
		if s, ok := val.(string); ok {
			ctx.ResultText(s)
		} else {
			ctx.ResultNull()
		}
	case colNsDimension, colNsVectorCount:
		switch v := val.(type) {
		case int64:
			ctx.ResultInt64(v)
		case uint64:
			ctx.ResultInt64(int64(v))
		case uint32:
			ctx.ResultInt64(int64(v))
		default:
			ctx.ResultNull()
		}
	default:
		ctx.ResultNull()
	}
	return nil
}

// Rowid returns the current row's unique identifier. For the namespace table
// we use a synthetic sequential counter; the name primary key is what SQLite
// actually passes back to Delete.
func (c *namespaceCursor) Rowid() (int64, error) {
	// Return the name as a hash-free stable identity: encode the name from
	// column 0. If the cursor is at EOF Rowid is never called.
	val, err := c.cursor.column(colNsName)
	if err != nil || val == nil {
		return 0, err
	}
	// SQLite accepts any int64 rowid; we use a simple FNV-like fold of the
	// name bytes so that each name maps to a unique positive rowid.
	name, _ := val.(string)
	rid := namespaceNameToRowid(name)
	c.table.rowidNames.Store(rid, name)
	return rid, nil
}

// Close releases cursor resources.
func (c *namespaceCursor) Close() error {
	c.cursor.reset()
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// namespaceInfoToRow converts a *NamespaceInfo into a prefetchCursor row whose
// values align with the virtual table schema column order:
// [name, dimension, distance_metric, index_type, vector_count].
func namespaceInfoToRow(ns *NamespaceInfo) row {
	return row{values: []any{
		ns.Name,
		uint32(ns.Dimension),
		ns.DistanceMetric,
		ns.IndexType,
		ns.VectorCount,
	}}
}

// extractNamespaceValues unpacks the five column values from the values slice
// provided by go-sqlite3 for INSERT/UPDATE operations.
//
//	values[0] = name            (TEXT)
//	values[1] = dimension       (INTEGER)
//	values[2] = distance_metric (TEXT)
//	values[3] = index_type      (TEXT)
//	values[4] = vector_count    (INTEGER, ignored on write)
func extractNamespaceValues(values []interface{}) (name string, dimension uint32, distanceMetric, indexType string, err error) {
	if len(values) < 4 {
		err = &NamespaceCreateError{
			Name: "",
			Err:  &InvalidPragmaValueError{Pragma: "column count", Value: "< 4", Valid: []string{"5"}},
		}
		return
	}
	name = toString(values[0])
	dimension = uint32(toUint64(values[1]))
	distanceMetric = toString(values[2])
	indexType = toString(values[3])
	return
}

// namespaceNameToRowid folds a namespace name string into a stable int64 rowid
// using a simple FNV-1a–inspired accumulation. Collisions are negligible for
// the small namespace counts typical in real deployments.
func namespaceNameToRowid(name string) int64 {
	const prime = int64(16777619)
	h := int64(2166136261)
	for i := 0; i < len(name); i++ {
		h ^= int64(name[i])
		h *= prime
	}
	if h <= 0 {
		h = -h + 1
	}
	return h
}
