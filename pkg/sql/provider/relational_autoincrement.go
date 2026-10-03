//go:build qrdb_sql

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
	"math"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// AUTO_INCREMENT
//
// Values are drawn from a sequenceBlock (relational_sequence.go), whose doc
// comment describes the Raft-committed, block-allocated,
// gaps-acceptable-duplicates-never contract every generated value in this
// package shares. That contract matches MySQL's own
// innodb_autoinc_lock_mode=2 (interleaved) behavior exactly.
//
// When an explicit value is supplied for the AUTO_INCREMENT column
// (INSERT ... VALUES (42, ...)) that is at or above the sequence's current
// value, GetNextAutoIncrementValue advances the sequence past it
// (SequenceServicer.Advance), so a subsequently auto-generated value never
// collides with one a caller supplied explicitly -- again matching MySQL.
//
// Generating a value is also what publishes the statement's LAST_INSERT_ID
// (publishLastInsertID). MySQL defines LAST_INSERT_ID as the value the
// statement generated for the FIRST row that needed one -- explicitly
// supplied values never set it -- and the generator is the only layer that
// knows which rows those were, exactly as it is in MySQL itself
// (handler::update_auto_increment writing THD::first_successful_insert_id_
// in_cur_stmt). See publishLastInsertID for why go-mysql-server cannot be
// left to derive it on its own.
// ---------------------------------------------------------------------------

// Compile-time interface assertions.
var (
	_ sqle.AutoIncrementTable  = (*RelationalTable)(nil)
	_ sqle.AutoIncrementGetter = (*RelationalTable)(nil)
	_ sqle.AutoIncrementSetter = (*relationalAutoIncSetter)(nil)
)

// autoIncSequenceName returns the SequenceServicer name backing t's
// AUTO_INCREMENT column (service.TableAutoIncrementSequenceName, keyed by
// TableID so a RENAME TABLE never disturbs it).
func (t *RelationalTable) autoIncSequenceName() string {
	return service.TableAutoIncrementSequenceName(t.def.TableID)
}

// requireAutoIncrement reports AutoIncrementUnavailableError when the table
// has no AUTO_INCREMENT column and SequenceServiceUnavailableError when it
// does but no SequenceServicer was supplied (see WithSequenceService).
func (t *RelationalTable) requireAutoIncrement() error {
	if t.def.AutoIncColumn == "" {
		return &AutoIncrementUnavailableError{Table: t.def.Name}
	}
	if t.seqSvc == nil {
		return &SequenceServiceUnavailableError{Table: t.def.Name}
	}
	return nil
}

// PeekNextAutoIncrementValue returns the next AUTO_INCREMENT value without
// allocating it -- one past the sequence's current committed value. This
// deliberately does not consult this table's own in-flight autoInc block:
// SequenceServicer.Allocate commits its block boundary the moment it is
// called, before any row is actually written under it, so the sequence's
// persisted value already reflects every value reserved so far, from any
// table instance or node.
func (t *RelationalTable) PeekNextAutoIncrementValue(ctx *sqle.Context) (uint64, error) {
	if err := t.requireAutoIncrement(); err != nil {
		return 0, err
	}
	cur, err := t.seqSvc.Peek(ctx, t.def.Database, t.autoIncSequenceName())
	if err != nil {
		return 0, err
	}
	return cur + 1, nil
}

// GetNextAutoIncrementValue implements the go-mysql-server AUTO_INCREMENT
// contract (see expression.AutoIncrement.Eval, which calls this once per
// inserted row): insertVal is nil when the row supplied no explicit value
// (or supplied 0, MySQL's "generate one for me" convention), in which case
// the next value is allocated from this table's sequenceBlock. When
// insertVal is non-nil, the row supplied its own value; the sequence is
// advanced past it (never below) so no future generated value can collide
// with it, and insertVal itself is returned -- though go-mysql-server
// discards this particular return value in that case, using the row's own
// supplied value instead.
//
// A generated value -- and only a generated value -- also becomes the
// statement's LAST_INSERT_ID; see publishLastInsertID.
func (t *RelationalTable) GetNextAutoIncrementValue(ctx *sqle.Context, insertVal any) (uint64, error) {
	if err := t.requireAutoIncrement(); err != nil {
		return 0, err
	}
	if insertVal != nil {
		given, ok := autoIncrementUint64(insertVal)
		if ok {
			if err := t.seqSvc.Advance(ctx, t.def.Database, t.autoIncSequenceName(), given); err != nil {
				return 0, err
			}
			return given, nil
		}
		return 0, nil
	}
	next, _, err := t.autoInc.next(ctx, t.seqSvc, t.def.Database, t.autoIncSequenceName())
	if err != nil {
		return 0, err
	}
	t.publishLastInsertID(ctx, next)
	return next, nil
}

// publishLastInsertID records value as the current statement's
// LAST_INSERT_ID, which is what the client reads back as the OK packet's
// insert id (database/sql's Result.LastInsertId) and what the SQL function
// LAST_INSERT_ID() returns.
//
// Only the FIRST value the statement generates is published, matching
// MySQL: a multi-row INSERT reports the id of the first row that needed a
// generated value, and the client derives the rest by adding
// auto_increment_increment. Later calls are dropped by the
// compare-and-swap, which is also what makes this safe without a lock.
// firstAutoInc's zero value is what makes "no value generated yet" and
// "generated the value 0" indistinguishable, which is harmless: an
// AUTO_INCREMENT sequence never generates 0 (see sequenceBlock).
//
// This is required because go-mysql-server cannot derive it reliably on its
// own. Its insertIter counts down to the row index its analyzer predicted
// would generate the first value, but only for rows that take the plain
// insert path: a row diverted into ON DUPLICATE KEY UPDATE skips the
// countdown entirely (sql/rowexec/insert.go, updateLastInsertId is reached
// only after a successful Insert). For
//
//	INSERT INTO t (id, v) VALUES (<existing>, ...), (DEFAULT, ...)
//	  ON DUPLICATE KEY UPDATE ...
//
// -- the shape every ORM emits when it saves an association whose members
// are a mix of existing and new rows -- the countdown therefore never
// reaches zero, nothing is stored, and the accumulator reports the
// PREVIOUS statement's insert id. The client then treats an already-used id
// as the new row's, silently collapsing two rows into one.
func (t *RelationalTable) publishLastInsertID(ctx *sqle.Context, value uint64) {
	if ctx == nil || ctx.Session == nil || value == 0 || value > math.MaxInt64 {
		return
	}
	if !t.firstAutoInc.CompareAndSwap(0, value) {
		return
	}
	ctx.GetLastQueryInfo().LastInsertId.Store(int64(value))
}

// AutoIncrementSetter returns the sql.AutoIncrementSetter backing
// `ALTER TABLE ... AUTO_INCREMENT = N`.
func (t *RelationalTable) AutoIncrementSetter(_ *sqle.Context) sqle.AutoIncrementSetter {
	return &relationalAutoIncSetter{table: t}
}

// autoIncrementUint64 converts insertVal -- whatever concrete Go type the
// AUTO_INCREMENT column's own INSERT expression evaluated to -- to a
// uint64. ok is false for a type this table's AUTO_INCREMENT columns never
// produce (every supported AUTO_INCREMENT column type is one of these
// numeric kinds; see catalog.ColumnTypeOf), in which case the caller
// treats insertVal as if it had not been supplied.
func autoIncrementUint64(v any) (uint64, bool) {
	switch x := v.(type) {
	case uint64:
		return x, true
	case uint32:
		return uint64(x), true
	case uint16:
		return uint64(x), true
	case uint8:
		return uint64(x), true
	case uint:
		return uint64(x), true
	case int64:
		if x < 0 {
			return 0, false
		}
		return uint64(x), true
	case int32:
		if x < 0 {
			return 0, false
		}
		return uint64(x), true
	case int16:
		if x < 0 {
			return 0, false
		}
		return uint64(x), true
	case int8:
		if x < 0 {
			return 0, false
		}
		return uint64(x), true
	case int:
		if x < 0 {
			return 0, false
		}
		return uint64(x), true
	case float64:
		if x < 0 {
			return 0, false
		}
		return uint64(x), true
	case float32:
		if x < 0 {
			return 0, false
		}
		return uint64(x), true
	default:
		return 0, false
	}
}

// ---------------------------------------------------------------------------
// relationalAutoIncSetter
// ---------------------------------------------------------------------------

// relationalAutoIncSetter is the sql.AutoIncrementSetter
// RelationalTable.AutoIncrementSetter returns.
type relationalAutoIncSetter struct {
	table *RelationalTable
}

// SetAutoIncrementValue implements `ALTER TABLE ... AUTO_INCREMENT = N`.
// value == 0 resets the sequence to its zero state (the next allocated
// value starts back at 1); otherwise the sequence is advanced so the next
// allocated value is exactly value.
//
// This can only ever raise the sequence, never lower it below a value
// already committed -- SequenceServicer.Advance is defined that way (see
// its doc comment) -- so, unlike real MySQL/InnoDB, an attempt to set
// AUTO_INCREMENT below the table's current watermark is silently a no-op
// rather than being validated against existing row contents. Any block
// this table's own autoInc had already reserved locally is discarded
// (sequenceBlock.discard) so the next generated value is read fresh from
// the just-advanced sequence rather than from stale local state.
func (s *relationalAutoIncSetter) SetAutoIncrementValue(ctx *sqle.Context, value uint64) error {
	t := s.table
	if err := t.requireAutoIncrement(); err != nil {
		return err
	}
	if err := flushPendingWriteSet(ctx); err != nil {
		return err
	}
	if value == 0 {
		if err := t.seqSvc.Reset(ctx, t.def.Database, t.autoIncSequenceName()); err != nil {
			return err
		}
	} else if err := t.seqSvc.Advance(ctx, t.def.Database, t.autoIncSequenceName(), value-1); err != nil {
		return err
	}
	t.autoInc.discard()
	return nil
}

// AcquireAutoIncrementLock is a no-op returning a no-op release function:
// relationalWriter's block allocation (sequenceBlock) already guarantees no
// two callers are ever handed the same value regardless of
// @@innodb_autoinc_lock_mode, so there is no additional serialization to
// acquire here. QRDB does not offer the "consecutive" guarantee
// innodb_autoinc_lock_mode 0/1 provide for a single multi-row statement
// (see the package doc comment on the gaps-acceptable correctness bar);
// this is the MySQL 8.0-default innodb_autoinc_lock_mode=2 (interleaved)
// behavior.
func (s *relationalAutoIncSetter) AcquireAutoIncrementLock(_ *sqle.Context) (func(), error) {
	return func() {}, nil
}

// Close finalizes the setter. No-op: SetAutoIncrementValue is durable as
// soon as it returns.
func (s *relationalAutoIncSetter) Close(_ *sqle.Context) error { return nil }
