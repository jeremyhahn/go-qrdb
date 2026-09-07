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

package gorm

// The `_, _ =` and `_ =` discards on builder.WriteString / WriteByte through
// this file and qrdb.go are deliberate and are the reason stated once here
// rather than at sixteen call sites. gorm's clause.Builder and clause.Writer
// declare error returns their implementations never use -- gorm.Statement
// writes into an in-memory buffer -- and every upstream gorm driver (mysql,
// postgres) calls them unchecked for that reason.
//
// They are written out rather than excluded in .golangci.yml because TWO
// linters see them: errcheck, and gosec as G104. An errcheck exclude-functions
// entry silences only the first and leaves the build red on the second, so the
// exclusion bought nothing and hid the reason from the file it applies to.

import (
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Clause builder keys, matching the names GORM's callbacks look up in
// db.ClauseBuilders.
const (
	// ClauseOnConflict is the "ON CONFLICT" builder key.
	ClauseOnConflict = "ON CONFLICT"
	// ClauseValues is the "VALUES" builder key.
	ClauseValues = "VALUES"
	// ClauseFor is the "FOR" (locking) builder key.
	ClauseFor = "FOR"
)

// ClauseBuilders returns the qrdb-specific rewrites Initialize merges into
// db.ClauseBuilders: ClauseOnConflict and ClauseValues unconditionally, and
// ClauseFor when Config.DontSupportForShareClause is set.
func (dialector Dialector) ClauseBuilders() map[string]clause.ClauseBuilder {
	builders := map[string]clause.ClauseBuilder{
		ClauseOnConflict: buildOnConflict,
		ClauseValues:     buildValues,
	}

	if dialector.Config != nil && dialector.Config.DontSupportForShareClause {
		builders[ClauseFor] = buildFor
	}

	return builders
}

// buildOnConflict rewrites a clause.OnConflict expression to MySQL's
// "ON DUPLICATE KEY UPDATE" syntax.
//
// GORM's own OnConflict.Build emits PostgreSQL's "ON CONFLICT (...) DO
// UPDATE SET ...", which qrdb's MySQL-dialect parser rejects outright, so
// this replaces it rather than delegating to it. An "excluded.col" value —
// the PostgreSQL name for the row that would have been inserted — is
// rewritten to MySQL's equivalent, "VALUES(col)". When the caller supplied
// no DoUpdates assignments at all, one self-assignment on the first
// available column (the primary key, or else the first field) is
// synthesized so the statement remains valid SQL and still returns a
// meaningful affected-row count instead of silently doing nothing.
func buildOnConflict(c clause.Clause, builder clause.Builder) {
	onConflict, ok := c.Expression.(clause.OnConflict)
	if !ok {
		c.Build(builder)
		return
	}

	_, _ = builder.WriteString("ON DUPLICATE KEY UPDATE ")

	if len(onConflict.DoUpdates) == 0 {
		if stmt, ok := builder.(*gorm.Statement); ok && stmt.Schema != nil {
			var column clause.Column
			switch {
			case stmt.Schema.PrioritizedPrimaryField != nil:
				column = clause.Column{Name: stmt.Schema.PrioritizedPrimaryField.DBName}
			case len(stmt.Schema.DBNames) > 0:
				column = clause.Column{Name: stmt.Schema.DBNames[0]}
			}

			if column.Name != "" {
				onConflict.DoNothing = false
				onConflict.DoUpdates = []clause.Assignment{{Column: column, Value: column}}
			}
		}
	}

	for idx, assignment := range onConflict.DoUpdates {
		if idx > 0 {
			_ = builder.WriteByte(',')
		}

		builder.WriteQuoted(assignment.Column)
		_ = builder.WriteByte('=')

		if column, ok := assignment.Value.(clause.Column); ok && column.Table == "excluded" {
			column.Table = ""
			_, _ = builder.WriteString("VALUES(")
			builder.WriteQuoted(column)
			_ = builder.WriteByte(')')
		} else {
			builder.AddVar(builder, assignment.Value)
		}
	}
}

// buildValues rewrites an empty-column clause.Values — an INSERT selecting
// no columns, used to insert a single all-default row — to the literal
// "VALUES()" MySQL requires. GORM's own Values.Build instead emits
// "DEFAULT VALUES", PostgreSQL/SQLite syntax that qrdb's MySQL-dialect
// parser rejects.
func buildValues(c clause.Clause, builder clause.Builder) {
	if values, ok := c.Expression.(clause.Values); ok && len(values.Columns) == 0 {
		_, _ = builder.WriteString("VALUES()")
		return
	}
	c.Build(builder)
}

// buildFor rewrites a clause.Locking with SHARE strength to MySQL's
// "LOCK IN SHARE MODE", for engines that do not support the standard
// "FOR SHARE" syntax. Any other locking strength (e.g. UPDATE) is left to
// the default builder.
func buildFor(c clause.Clause, builder clause.Builder) {
	if locking, ok := c.Expression.(clause.Locking); ok && strings.EqualFold(locking.Strength, "SHARE") {
		_, _ = builder.WriteString("LOCK IN SHARE MODE")
		return
	}
	c.Build(builder)
}
