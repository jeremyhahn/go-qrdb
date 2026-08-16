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

package provider

import (
	"context"
	"errors"
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/sqltypes"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/catalog"
)

// ---------------------------------------------------------------------------
// rewriteRowInserter
//
// The sql.RowInserter RelationalTable.RewriteInserter returns. Its Insert
// method is deliberately a no-op: go-mysql-server streams every existing
// row of the table through Insert once (see rewriteTable in
// go-mysql-server's rowexec package) as part of the RewritableTable
// contract, but this table performs the actual migration exactly once, in
// Close, via runRewrite -- the same routine resumeRewrite uses to finish an
// interrupted rewrite with no query engine involved at all. Routing both
// the engine-driven path and the crash-resume path through one function is
// what keeps them from silently drifting apart into two slightly different
// implementations of "how to re-encode a row" -- a correctness risk more
// serious than the redundant table scan this trades away (ALTER TABLE DROP/
// MODIFY COLUMN is not a hot path).
// ---------------------------------------------------------------------------

type rewriteRowInserter struct {
	kvSvc    service.KVServicer
	tableSvc service.TableServicer
	database string
	name     string
	current  *service.TableDef
	journal  *service.TableDDLJournal
}

var _ sqle.RowInserter = (*rewriteRowInserter)(nil)

// StatementBegin marks the start of the rewrite. No-op: progress is tracked
// durably through row content (see catalog.RowSchemaVersionAtLeast), not
// through any in-memory checkpoint.
func (r *rewriteRowInserter) StatementBegin(_ *sqle.Context) {}

// DiscardChanges discards changes made since StatementBegin. No-op: rows
// already migrated stay migrated (that is what makes a resumed rewrite
// cheap), and the catalog is not touched until Close, so there is nothing
// to unwind here specifically.
func (r *rewriteRowInserter) DiscardChanges(_ *sqle.Context, _ error) error { return nil }

// StatementComplete marks the end of the rewrite statement. No-op: Close
// (called immediately after by go-mysql-server) is where the real work
// happens.
func (r *rewriteRowInserter) StatementComplete(_ *sqle.Context) error { return nil }

// Insert is a no-op; see the type doc comment.
func (r *rewriteRowInserter) Insert(_ *sqle.Context, _ sqle.Row) error { return nil }

// Close performs the actual rewrite: re-encodes every row against
// r.journal.Target and finishes with TableServicer.CompleteRewrite.
func (r *rewriteRowInserter) Close(ctx *sqle.Context) error {
	if err := runRewrite(ctx, r.kvSvc, r.database, r.current, r.journal.Target, r.journal.Changes); err != nil {
		// A RowRewriteError is the DETERMINISTIC class: the data cannot
		// satisfy the new definition, and every retry fails identically.
		// Abort the journal so the table stays servable under its pre-ALTER
		// definition -- runRewrite converts every row before writing any,
		// so no row was touched -- and PostgreSQL's atomic-ALTER promise
		// holds: the statement failed, the table did not change. Any other
		// error is treated as transient and keeps the journal, which is
		// what makes a crash-interrupted write phase resumable.
		var rowErr *RowRewriteError
		if errors.As(err, &rowErr) {
			if aborter, ok := r.tableSvc.(interface {
				AbortRewrite(ctx context.Context, database, name string) error
			}); ok {
				if abortErr := aborter.AbortRewrite(ctx, r.database, r.name); abortErr != nil {
					var none *service.TableNoRewriteInProgressError
					if !errors.As(abortErr, &none) {
						return errors.Join(err, abortErr)
					}
				}
			}
		}
		return err
	}
	_, err := r.tableSvc.CompleteRewrite(ctx, r.database, r.name)
	if err != nil {
		var none *service.TableNoRewriteInProgressError
		if errors.As(err, &none) {
			// A racing completion (this node's own retry, or another node
			// resuming the same interrupted rewrite) already finished it.
			return nil
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// runRewrite
// ---------------------------------------------------------------------------

// runRewrite re-encodes every row of the table identified by (database,
// current.TableID) from current's schema to target's schema, skipping any
// row already migrated -- recognized via catalog.RowSchemaVersionAtLeast
// without decoding it -- so that calling this twice (a crash-resumed
// rewrite calling it again after a partial pass) is safe and only does the
// remaining work.
//
// changes is the single AlterTableChange (a drop or a modify) that produced
// target from current; it is what lets column remapping be exact even when
// a modify also renames a column, rather than inferred by diffing two
// column lists.
func runRewrite(ctx context.Context, kvSvc service.KVServicer, database string, current, target *service.TableDef, changes []service.AlterTableChange) error {
	computedColumns, remap, err := columnRemap(current, changes)
	if err != nil {
		return err
	}
	if !columnListsPhysicallyEqual(computedColumns, target.Columns) {
		return &RewriteColumnMismatchError{Database: database, Name: target.Name}
	}

	oldCodec, err := catalog.NewRowCodec(current)
	if err != nil {
		return err
	}
	newCodec, err := catalog.NewRowCodec(target)
	if err != nil {
		return err
	}

	newTypes := make([]sqle.Type, len(target.Columns))
	for i, c := range target.Columns {
		t, terr := catalog.ColumnTypeOf(c)
		if terr != nil {
			return terr
		}
		newTypes[i] = t
	}

	prefix := service.TableDataPrefix(database, current.TableID)
	data, err := kvSvc.Scan(ctx, prefix)
	if err != nil {
		return err
	}

	// Convert EVERY row before writing ANY. A conversion failure is
	// deterministic -- 'company-belongs-to' will never become a bigint, on
	// this attempt or any retry -- so discovering it after some rows were
	// already re-encoded would leave the table with mixed encodings that no
	// single codec can read. Converting first makes a data-incompatible
	// ALTER fail with the table's rows untouched, which is what lets the
	// caller abort the journal and restore the pre-ALTER definition exactly
	// as PostgreSQL's atomic ALTER semantics promise. (The write phase can
	// still be interrupted by a crash or storage error; that path keeps the
	// journal and resumes, exactly as before -- already-migrated rows are
	// recognized and skipped.)
	converted := make(map[string][]byte, len(data))
	for key, raw := range data {
		alreadyMigrated, verr := catalog.RowSchemaVersionAtLeast(target.Name, raw, target.SchemaVersion)
		if verr != nil {
			return &RowRewriteError{Key: key, Err: verr}
		}
		if alreadyMigrated {
			continue
		}

		oldRow, derr := oldCodec.Decode(raw)
		if derr != nil {
			return &RowRewriteError{Key: key, Err: derr}
		}

		// Width, not len(remap): a table with no declared primary key
		// carries a trailing hidden row id slot the codec expects but that
		// remap (which covers only declared columns) says nothing about. It
		// stays nil here and is never encoded -- the row id lives in the
		// key, which this rewrite leaves exactly where it found it.
		newRow := make(sqle.Row, newCodec.Width())
		for i, srcIdx := range remap {
			if srcIdx < 0 || srcIdx >= len(oldRow) || oldRow[srcIdx] == nil {
				continue
			}
			converted, _, cerr := newTypes[i].Convert(ctx, oldRow[srcIdx])
			if cerr != nil {
				return &RowRewriteError{Key: key, Err: cerr}
			}
			newRow[i] = converted
		}

		encoded, eerr := newCodec.Encode(newRow)
		if eerr != nil {
			return &RowRewriteError{Key: key, Err: eerr}
		}
		converted[key] = encoded
	}

	for key, encoded := range converted {
		if perr := kvSvc.Put(ctx, key, encoded); perr != nil {
			return perr
		}
	}
	return nil
}

// columnRemapItem pairs a resulting column definition with the ordinal
// position in the pre-rewrite column list it derives its value from, or -1
// when it has none (not produced by DROP/MODIFY COLUMN, which never adds a
// column; kept only so this type stays honest about the general case).
type columnRemapItem struct {
	column service.ColumnDef
	srcIdx int
}

// columnRemap replays changes (the drop or modify recorded on the rewrite
// journal) against current.Columns, returning both the resulting column
// list and, for each resulting column, which ordinal of the *original*
// (current) row it should be read from. This mirrors
// applyAlterChanges/dropColumnReferences in pkg/service/table_service.go
// closely enough to reproduce the same target.Columns exactly -- verified
// by the caller (columnListsPhysicallyEqual) rather than assumed.
func columnRemap(current *service.TableDef, changes []service.AlterTableChange) ([]service.ColumnDef, []int, error) {
	items := make([]columnRemapItem, len(current.Columns))
	for i, c := range current.Columns {
		items[i] = columnRemapItem{column: c, srcIdx: i}
	}

	for _, change := range changes {
		idx := columnRemapIndexOf(items, change.Name)
		switch change.Action {
		case service.AlterDropColumn:
			if idx < 0 {
				return nil, nil, &RewriteColumnNotFoundError{Column: change.Name}
			}
			items = append(items[:idx], items[idx+1:]...)
		case service.AlterModifyColumn:
			if idx < 0 {
				return nil, nil, &RewriteColumnNotFoundError{Column: change.Name}
			}
			newCol := change.Column
			if newCol.Name == "" {
				newCol.Name = change.Name
			}
			items[idx].column = newCol
		default:
			// ADD/RENAME never trigger a rewrite (see
			// RelationalTable.ShouldRewriteTable); a journal produced by
			// this service never carries one of those actions alongside a
			// rewrite-requiring one without also carrying the
			// rewrite-requiring one's effect on target.Columns already
			// reflected -- nothing further to apply here.
		}
	}

	columns := make([]service.ColumnDef, len(items))
	remap := make([]int, len(items))
	for i, it := range items {
		columns[i] = it.column
		remap[i] = it.srcIdx
	}
	return columns, remap, nil
}

// columnRemapIndexOf returns the index of the item whose column name
// matches name case-insensitively, or -1 when none matches.
func columnRemapIndexOf(items []columnRemapItem, name string) int {
	for i, it := range items {
		if strings.EqualFold(it.column.Name, name) {
			return i
		}
	}
	return -1
}

// columnListsPhysicallyEqual reports whether a and b describe the same
// column list for the purposes of row encoding: same length, and at each
// position the same name and type. Comment/Extra/Collation/Default are not
// compared -- they carry no weight in catalog.RowCodec's byte layout.
func columnListsPhysicallyEqual(a, b []service.ColumnDef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i].Name, b[i].Name) || !strings.EqualFold(a[i].Type, b[i].Type) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Primary key compatibility guard
// ---------------------------------------------------------------------------

// validateRewritablePrimaryKey rejects a rewrite that would change the
// table's primary key's physical key encoding: a different set or order of
// primary key columns, a different prefix length, or a column whose type
// moved between the numeric and string key-encoding families (see
// catalog.KeyCodec's doc comment on how each is encoded).
//
// This is a deliberate scope boundary, not an oversight: rows are stored
// under keys computed from their primary key's encoded bytes
// (service.TableDataPrefix + catalog.KeyCodec.Encode), and runRewrite
// overwrites each row's existing key in place rather than re-keying it, on
// the assumption that DROP/MODIFY COLUMN never touches the primary key's
// physical encoding. A change that would violate that assumption (dropping
// a primary key column, or retyping one across the numeric/string
// boundary, or altering a prefix length) is rejected outright with a typed
// error instead of silently corrupting the table by leaving rows
// re-encoded under a key that no longer matches
// catalog.KeyCodec.Encode(target, newRow).
func validateRewritablePrimaryKey(current, target *service.TableDef) error {
	if len(current.PrimaryKey) != len(target.PrimaryKey) {
		return &PrimaryKeyRewriteUnsupportedError{Table: target.Name}
	}
	for i, pk := range current.PrimaryKey {
		tpk := target.PrimaryKey[i]
		if !strings.EqualFold(pk.Name, tpk.Name) || pk.PrefixLength != tpk.PrefixLength {
			return &PrimaryKeyRewriteUnsupportedError{Table: target.Name}
		}

		changedFamily, err := primaryKeyColumnFamilyChanged(current, target, pk.Name)
		if err != nil {
			return err
		}
		if changedFamily {
			return &PrimaryKeyRewriteUnsupportedError{Table: target.Name}
		}
	}
	return nil
}

// primaryKeyColumnFamilyChanged reports whether the named primary key
// column's key-encoding family (numeric vs string; see
// catalog.KeyCodec's doc comment) differs between current and target. A
// column absent from either list (which validateRewritablePrimaryKey's
// caller has already rejected before this is reached in every real case)
// is treated as unchanged rather than erroring here, so this function's
// only job is the family comparison.
func primaryKeyColumnFamilyChanged(current, target *service.TableDef, name string) (bool, error) {
	oldCol, oldOK := findColumnByName(current, name)
	newCol, newOK := findColumnByName(target, name)
	if !oldOK || !newOK {
		return false, nil
	}

	oldType, err := catalog.ColumnTypeOf(oldCol)
	if err != nil {
		return false, err
	}
	newType, err := catalog.ColumnTypeOf(newCol)
	if err != nil {
		return false, err
	}
	return isKeyStringFamily(oldType) != isKeyStringFamily(newType), nil
}

// findColumnByName returns the column named name (matched case
// insensitively) from def.Columns, and whether one was found.
func findColumnByName(def *service.TableDef, name string) (service.ColumnDef, bool) {
	for _, c := range def.Columns {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return service.ColumnDef{}, false
}

// isKeyStringFamily reports whether t belongs to catalog.KeyCodec's
// string/byte key-encoding family (escaped-and-terminated bytes) rather
// than its numeric family (fixed 8-byte big-endian). It mirrors the same
// classification catalog.NewKeyCodec's unexported keyEncodingKind makes,
// scoped down to just the numeric/string distinction this validation needs.
func isKeyStringFamily(t sqle.Type) bool {
	switch t.Type() {
	case sqltypes.Char, sqltypes.VarChar, sqltypes.Text, sqltypes.Binary, sqltypes.VarBinary, sqltypes.Blob:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Resume: an interrupted rewrite, driven from a fresh table resolution
// rather than from go-mysql-server's RewriteInserter callback (see
// Database.relationalTable in relational_database.go).
// ---------------------------------------------------------------------------

// resumeRewrite finishes an in-flight rewrite for (database, name):
// fetches the journal, validates the primary key is still compatible (see
// validateRewritablePrimaryKey), re-runs runRewrite (safe to call again
// against a partially-migrated table -- see its doc comment), and completes
// the rewrite. Returns nil, not an error, when no journal is present:
// either nothing was ever in progress, or a racing resumer already
// finished it, and either way there is nothing left for this caller to do.
func resumeRewrite(ctx context.Context, tableSvc service.TableServicer, kvSvc service.KVServicer, database, name string) error {
	journal, current, err := tableSvc.RewriteJournal(ctx, database, name)
	if err != nil {
		var none *service.TableNoRewriteInProgressError
		if errors.As(err, &none) {
			return nil
		}
		return err
	}
	if journal.Target == nil {
		return &RewriteJournalMissingTargetError{Database: database, Name: name}
	}
	if err := validateRewritablePrimaryKey(current, journal.Target); err != nil {
		return err
	}

	if err := runRewrite(ctx, kvSvc, database, current, journal.Target, journal.Changes); err != nil {
		return err
	}

	_, err = tableSvc.CompleteRewrite(ctx, database, name)
	if err != nil {
		var none *service.TableNoRewriteInProgressError
		if errors.As(err, &none) {
			return nil
		}
		return err
	}
	return nil
}
