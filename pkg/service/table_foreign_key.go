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

package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Foreign key catalog operations
//
// Foreign keys are stored on the declaring table's own catalog record
// (TableDef.ForeignKeys), so adding, dropping, and updating one is the same
// single-record compare-and-swap every other catalog mutation in this
// package is (see runCatalogCAS). Two things about foreign keys do not fit
// that record-local shape, and both are handled here rather than pushed
// onto callers:
//
//   - A constraint name is unique across the whole DATABASE in MySQL, not
//     merely across one table, so adding or renaming one has to consult
//     every table in the database.
//   - The reverse question -- "which constraints point AT this table?" --
//     is not answerable from the table's own record at all, for the same
//     reason.
//
// Both are answered with a single KVServicer.Scan over the catalog prefix
// (scanTableDefs), not one Get per table, so they cost one round trip
// regardless of how many tables the database holds. That still makes them
// O(catalog size) in bytes transferred and in CPU to decode, which is the
// price of keeping the catalog normalized -- a denormalized parent->child
// index would be O(1) but would add an invariant that every add, drop,
// update, rename, and drop-table would have to maintain in lockstep. If
// this ever shows up in a profile, the fix is a cache keyed off
// tableCatalogEpochKey (which every mutation below already bumps), not a
// second copy of the data.
// ---------------------------------------------------------------------------

// AddForeignKey appends a foreign key constraint to a table's catalog
// entry. See TableServicer.AddForeignKey.
//
// The parent table and its columns are deliberately NOT validated here.
// MySQL permits adding a constraint whose parent does not yet exist while
// foreign_key_checks is off, and the SQL engine -- which is the only layer
// that knows the session's foreign_key_checks setting -- performs that
// validation when it is on. Validating unconditionally here would make the
// off case impossible to express. The declaring table's own columns ARE
// validated, since they are knowable from this record alone and a
// constraint naming a column the table does not have is never valid.
func (s *TableService) AddForeignKey(ctx context.Context, req *CreateTableForeignKeyRequest) error {
	if err := validateTableRef(req.Database, req.Table); err != nil {
		return err
	}

	metaKey := tableMetadataKey(req.Database, req.Table)
	epochKey := tableCatalogEpochKey(req.Database)

	return s.runCatalogCAS(ctx, req.Database, req.Table, func() ([]Compare, []Op, error) {
		noRewrite, err := s.requireNoRewrite(ctx, req.Database, req.Table)
		if err != nil {
			return nil, nil, err
		}
		if err := s.requireForeignKeyNameAvailable(ctx, req.Database, req.ForeignKey.Name); err != nil {
			return nil, nil, err
		}

		rev, err := s.mvcc.GetRevision(ctx, metaKey)
		if err != nil {
			return nil, nil, &TableNotFoundError{Database: req.Database, Name: req.Table}
		}
		var def TableDef
		if err := json.Unmarshal(rev.Value, &def); err != nil {
			return nil, nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}

		if err := validateForeignKey(columnNameSet(&def), req.ForeignKey); err != nil {
			return nil, nil, err
		}

		def.ForeignKeys = append(def.ForeignKeys, req.ForeignKey)
		return foreignKeyCASOps(&def, metaKey, epochKey, rev.ModRevision, noRewrite)
	})
}

// DropForeignKey removes a foreign key constraint from a table's catalog
// entry. See TableServicer.DropForeignKey.
func (s *TableService) DropForeignKey(ctx context.Context, database, table, name string) error {
	if err := validateTableRef(database, table); err != nil {
		return err
	}
	if name == "" {
		return &TableInvalidNameError{Kind: "foreign key", Name: name, Reason: "must not be empty"}
	}

	metaKey := tableMetadataKey(database, table)
	epochKey := tableCatalogEpochKey(database)

	return s.runCatalogCAS(ctx, database, table, func() ([]Compare, []Op, error) {
		noRewrite, err := s.requireNoRewrite(ctx, database, table)
		if err != nil {
			return nil, nil, err
		}
		rev, err := s.mvcc.GetRevision(ctx, metaKey)
		if err != nil {
			return nil, nil, &TableNotFoundError{Database: database, Name: table}
		}
		var def TableDef
		if err := json.Unmarshal(rev.Value, &def); err != nil {
			return nil, nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}

		idx := findForeignKeyIndex(&def, name)
		if idx < 0 {
			return nil, nil, &TableForeignKeyNotFoundError{Database: database, Table: table, Name: name}
		}

		def.ForeignKeys = append(def.ForeignKeys[:idx:idx], def.ForeignKeys[idx+1:]...)
		return foreignKeyCASOps(&def, metaKey, epochKey, rev.ModRevision, noRewrite)
	})
}

// UpdateForeignKey replaces an existing foreign key constraint in place.
// See TableServicer.UpdateForeignKey.
//
// Replacing in place rather than as a drop-then-append keeps the table's
// constraint order stable, which matters because the SQL engine builds its
// per-statement enforcement plan by iterating that order.
func (s *TableService) UpdateForeignKey(ctx context.Context, req *UpdateTableForeignKeyRequest) error {
	if err := validateTableRef(req.Database, req.Table); err != nil {
		return err
	}
	if req.Name == "" {
		return &TableInvalidNameError{Kind: "foreign key", Name: req.Name, Reason: "must not be empty"}
	}

	metaKey := tableMetadataKey(req.Database, req.Table)
	epochKey := tableCatalogEpochKey(req.Database)
	renaming := !strings.EqualFold(req.Name, req.ForeignKey.Name)

	return s.runCatalogCAS(ctx, req.Database, req.Table, func() ([]Compare, []Op, error) {
		noRewrite, err := s.requireNoRewrite(ctx, req.Database, req.Table)
		if err != nil {
			return nil, nil, err
		}
		rev, err := s.mvcc.GetRevision(ctx, metaKey)
		if err != nil {
			return nil, nil, &TableNotFoundError{Database: req.Database, Name: req.Table}
		}
		var def TableDef
		if err := json.Unmarshal(rev.Value, &def); err != nil {
			return nil, nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}

		// The constraint being replaced is located before the new name is
		// checked, so that updating one that does not exist reports exactly
		// that rather than a name collision against some unrelated table.
		idx := findForeignKeyIndex(&def, req.Name)
		if idx < 0 {
			return nil, nil, &TableForeignKeyNotFoundError{Database: req.Database, Table: req.Table, Name: req.Name}
		}
		if err := validateForeignKey(columnNameSet(&def), req.ForeignKey); err != nil {
			return nil, nil, err
		}

		// Only a rename can collide: keeping the same name is by definition
		// still owned by the constraint being replaced.
		if renaming {
			if err := s.requireForeignKeyNameAvailable(ctx, req.Database, req.ForeignKey.Name); err != nil {
				return nil, nil, err
			}
		}

		def.ForeignKeys[idx] = req.ForeignKey
		return foreignKeyCASOps(&def, metaKey, epochKey, rev.ModRevision, noRewrite)
	})
}

// ReferencingForeignKeys returns every foreign key in database that names
// parent as its parent table. See TableServicer.ReferencingForeignKeys.
//
// Results are ordered by declaring table name, then by the declaring
// table's own constraint order, so repeated calls return the same list in
// the same order -- the SQL engine builds referential-action plans from
// this order, and a plan that reshuffled between statements would be
// needlessly hard to reason about.
func (s *TableService) ReferencingForeignKeys(ctx context.Context, database, parent string) ([]TableForeignKey, error) {
	if err := validateTableRef(database, parent); err != nil {
		return nil, err
	}

	defs, err := s.scanTableDefs(ctx, database)
	if err != nil {
		return nil, err
	}

	var refs []TableForeignKey
	for i := range defs {
		for _, fk := range defs[i].ForeignKeys {
			if strings.EqualFold(fk.ParentTable, parent) {
				refs = append(refs, TableForeignKey{Table: defs[i].Name, ForeignKey: fk})
			}
		}
	}
	return refs, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// foreignKeyCASOps renders the compare-and-swap that commits a mutated
// definition: the definition itself, guarded on the ModRevision it was read
// at, plus the database's catalog epoch bump every other mutation performs.
func foreignKeyCASOps(def *TableDef, metaKey, epochKey string, modRevision int64, noRewrite Compare) ([]Compare, []Op, error) {
	def.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(def)
	if err != nil {
		return nil, nil, &TableMetadataError{Op: "marshal", Err: err}
	}
	compares := []Compare{
		{Key: []byte(metaKey), Target: CompareModRevision, Result: CompareEqual, ModRevision: modRevision},
		noRewrite,
	}
	ops := []Op{
		{Type: OpPut, Key: []byte(metaKey), Value: data},
		{Type: OpPut, Key: []byte(epochKey), Value: epochValue()},
	}
	return compares, ops, nil
}

// findForeignKeyIndex returns the position of the constraint named name
// (matched case-insensitively, as MySQL matches constraint names) in
// def.ForeignKeys, or -1 when none matches.
func findForeignKeyIndex(def *TableDef, name string) int {
	for i := range def.ForeignKeys {
		if strings.EqualFold(def.ForeignKeys[i].Name, name) {
			return i
		}
	}
	return -1
}

// requireForeignKeyNameAvailable reports TableForeignKeyAlreadyExistsError
// when name is already used by a constraint on ANY table in database.
//
// The check is a linearizable read taken inside the compare-and-swap's
// build function, so it is re-evaluated on every attempt, but it is not
// itself part of the transaction's compare set: making it atomic would
// require one compare per table in the database on every foreign key
// mutation. Two concurrent AddForeignKey calls against DIFFERENT tables
// using the SAME new name can therefore both succeed. Adding to the same
// table is fully serialized, as is every other mutation of one table's
// record.
func (s *TableService) requireForeignKeyNameAvailable(ctx context.Context, database, name string) error {
	if name == "" {
		return &TableInvalidNameError{Kind: "foreign key", Name: name, Reason: "must not be empty"}
	}

	defs, err := s.scanTableDefs(ctx, database)
	if err != nil {
		return err
	}
	for i := range defs {
		for _, fk := range defs[i].ForeignKeys {
			if strings.EqualFold(fk.Name, name) {
				return &TableForeignKeyAlreadyExistsError{Database: database, Table: defs[i].Name, Name: name}
			}
		}
	}
	return nil
}

// scanTableDefs reads every table definition in database with a single
// catalog scan, sorted by metadata key (and therefore by table name). The
// catalog prefix also holds each database's epoch key and each table's DDL
// journal key, which are filtered out the same way ListTables filters them.
//
// A table with a rewrite in flight is included: the record under its
// metadata key is still its live definition (the rewrite's target lives in
// the journal, see AlterTable), so its declared constraints are current
// even though the table itself must not be served until the rewrite
// finishes.
func (s *TableService) scanTableDefs(ctx context.Context, database string) ([]TableDef, error) {
	if database == "" {
		return nil, &TableEmptyDatabaseError{}
	}

	prefix := tableCatalogScanPrefix(database)
	entries, err := s.kv.Scan(ctx, prefix)
	if err != nil {
		return nil, &TableMetadataError{Op: "scan", Err: err}
	}

	epochKey := tableCatalogEpochKey(database)
	keys := make([]string, 0, len(entries))
	for key := range entries {
		if key == epochKey || strings.HasSuffix(key, tableDDLJournalSuffix) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	defs := make([]TableDef, 0, len(keys))
	for _, key := range keys {
		var def TableDef
		if err := json.Unmarshal(entries[key], &def); err != nil {
			return nil, &TableMetadataError{Op: "unmarshal", Err: err}
		}
		defs = append(defs, def)
	}
	return defs, nil
}

// validateForeignKey validates a foreign key definition against the
// declaring table: a well-formed constraint name, a well-formed parent
// table name, at least one column, an equal number of local and parent
// columns, and every local column present in colSet (a case-insensitively
// lower-cased set of the declaring table's column names).
func validateForeignKey(colSet map[string]struct{}, fk ForeignKeyDef) error {
	if err := validateIdentifier("foreign key", fk.Name); err != nil {
		return err
	}
	if err := validateIdentifier("table", fk.ParentTable); err != nil {
		return err
	}
	if len(fk.Columns) == 0 || len(fk.Columns) != len(fk.ParentColumns) {
		return &TableForeignKeyNoColumnsError{Name: fk.Name}
	}
	for _, c := range fk.Columns {
		if _, ok := colSet[strings.ToLower(c)]; !ok {
			return &TableForeignKeyColumnNotFoundError{Name: fk.Name, Column: c}
		}
	}
	return nil
}
