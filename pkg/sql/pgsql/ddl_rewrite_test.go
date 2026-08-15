// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_pgsql

package pgsql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRewriteAlterColumnNullability covers the one ALTER form that cannot be
// translated by string rewriting alone: MySQL has no statement changing a
// column's nullability by itself, so MODIFY has to restate the column's
// current type, which only the catalog knows.
func TestRewriteAlterColumnNullability(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)
	catalog := h.sqlEngine.Analyzer.Catalog

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "DROP NOT NULL restates the column as nullable",
			query: "ALTER TABLE `people` ALTER COLUMN `name` DROP NOT NULL",
			want:  "ALTER TABLE `people` MODIFY `name` varchar(64) NULL",
		},
		{
			name:  "SET NOT NULL restates it as not nullable",
			query: "ALTER TABLE `people` ALTER COLUMN `name` SET NOT NULL",
			want:  "ALTER TABLE `people` MODIFY `name` varchar(64) NOT NULL",
		},
		{
			// PostgreSQL allows the COLUMN keyword to be omitted.
			name:  "the COLUMN keyword is optional",
			query: "ALTER TABLE `people` ALTER `name` DROP NOT NULL",
			want:  "ALTER TABLE `people` MODIFY `name` varchar(64) NULL",
		},
		{
			name:  "bare identifiers resolve too",
			query: "ALTER TABLE people ALTER COLUMN name DROP NOT NULL",
			want:  "ALTER TABLE people MODIFY name varchar(64) NULL",
		},
		{
			// Returned unchanged so the engine reports what it could not
			// find, against the statement the caller actually wrote.
			name:  "an unknown column is left for the engine to reject",
			query: "ALTER TABLE `people` ALTER COLUMN `nope` DROP NOT NULL",
			want:  "ALTER TABLE `people` ALTER COLUMN `nope` DROP NOT NULL",
		},
		{
			name:  "an unknown table is left alone",
			query: "ALTER TABLE `nope` ALTER COLUMN `name` DROP NOT NULL",
			want:  "ALTER TABLE `nope` ALTER COLUMN `name` DROP NOT NULL",
		},
		{
			name:  "an unrelated ALTER is untouched",
			query: "ALTER TABLE `people` ADD `nickname` varchar(32)",
			want:  "ALTER TABLE `people` ADD `nickname` varchar(32)",
		},
		{
			name:  "an unrelated statement is untouched",
			query: "SELECT * FROM `people`",
			want:  "SELECT * FROM `people`",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteAlterColumnNullability(ctx, catalog, "resultcolumnstest", tt.query)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestRewrittenAlterColumnNullabilityExecutes is the point of the rewrite:
// the statement it produces has to be one go-mysql-server actually accepts.
func TestRewrittenAlterColumnNullabilityExecutes(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)

	rewritten := rewriteAlterColumnNullability(ctx, h.sqlEngine.Analyzer.Catalog,
		"resultcolumnstest", "ALTER TABLE `people` ALTER COLUMN `name` DROP NOT NULL")
	require.NotContains(t, rewritten, "ALTER COLUMN", "the PostgreSQL form must be gone")

	_, iter, _, err := h.sqlEngine.Query(ctx, rewritten)
	require.NoError(t, err, "the rewritten statement must execute")
	require.NoError(t, iter.Close(ctx))

	schema, iter, _, err := h.sqlEngine.Query(ctx, "SELECT name FROM people")
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))
	assert.True(t, schema[0].Nullable, "the column is nullable after the rewrite executed")
}

// TestRewriteDropConstraint covers PostgreSQL's single constraint namespace
// meeting MySQL's three. gorm.io/driver/postgres drops a unique index with
// DROP CONSTRAINT, which go-mysql-server routes to CHECK-constraint handling
// and refuses with "the table does not support check constraint operations".
func TestRewriteDropConstraint(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)
	catalog := h.sqlEngine.Analyzer.Catalog

	_, iter, _, err := h.sqlEngine.Query(ctx, "CREATE UNIQUE INDEX unique_name ON people (name)")
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "a unique index is dropped as an index",
			query: "ALTER TABLE `people` DROP CONSTRAINT `unique_name`",
			want:  "ALTER TABLE `people` DROP INDEX `unique_name`",
		},
		{
			name:  "the primary key resolves as an index too",
			query: "ALTER TABLE `people` DROP CONSTRAINT `PRIMARY`",
			want:  "ALTER TABLE `people` DROP INDEX `PRIMARY`",
		},
		{
			name:  "IF EXISTS is accepted",
			query: "ALTER TABLE `people` DROP CONSTRAINT IF EXISTS `unique_name`",
			want:  "ALTER TABLE `people` DROP INDEX `unique_name`",
		},
		{
			// It may be a real CHECK constraint, which the original
			// statement already spells the way MySQL expects.
			name:  "a name matching nothing is left alone",
			query: "ALTER TABLE `people` DROP CONSTRAINT `chk_age`",
			want:  "ALTER TABLE `people` DROP CONSTRAINT `chk_age`",
		},
		{
			name:  "an unknown table is left alone",
			query: "ALTER TABLE `nope` DROP CONSTRAINT `unique_name`",
			want:  "ALTER TABLE `nope` DROP CONSTRAINT `unique_name`",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, rewriteDropConstraint(ctx, catalog, "resultcolumnstest", tt.query))
		})
	}
}

// TestRewriteCreateIndexKeyLength covers MySQL's refusal to index a TEXT
// column without a prefix length. PostgreSQL's `text` maps to LONGTEXT, so a
// column indexed by a later statement -- rather than at CREATE TABLE time,
// where pkg/sql/engine already bounds it -- can only be indexed with one.
func TestRewriteCreateIndexKeyLength(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)
	catalog := h.sqlEngine.Analyzer.Catalog

	_, iter, _, err := h.sqlEngine.Query(ctx,
		"CREATE TABLE notes (id BIGINT PRIMARY KEY, body LONGTEXT, tag VARCHAR(32), weight BIGINT)")
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "a text column gains a prefix length",
			query: "CREATE INDEX `idx_body` ON `notes` (`body`)",
			want:  "CREATE INDEX `idx_body` ON `notes` ( `body`(768))",
		},
		{
			name:  "a non-text column is untouched",
			query: "CREATE INDEX `idx_weight` ON `notes` (`weight`)",
			want:  "CREATE INDEX `idx_weight` ON `notes` (`weight`)",
		},
		{
			name:  "a varchar column is untouched",
			query: "CREATE INDEX `idx_tag` ON `notes` (`tag`)",
			want:  "CREATE INDEX `idx_tag` ON `notes` (`tag`)",
		},
		{
			name:  "only the text column of a composite index gains one",
			query: "CREATE INDEX `idx_both` ON `notes` (`weight`,`body`)",
			want:  "CREATE INDEX `idx_both` ON `notes` (`weight`, `body`(768))",
		},
		{
			name:  "a UNIQUE index is handled the same way",
			query: "CREATE UNIQUE INDEX `idx_u` ON `notes` (`body`)",
			want:  "CREATE UNIQUE INDEX `idx_u` ON `notes` ( `body`(768))",
		},
		{
			name:  "an explicit prefix length is left as written",
			query: "CREATE INDEX `idx_body` ON `notes` (`body`(64))",
			want:  "CREATE INDEX `idx_body` ON `notes` (`body`(64))",
		},
		{
			name:  "an unknown table is left alone",
			query: "CREATE INDEX `idx_x` ON `nope` (`body`)",
			want:  "CREATE INDEX `idx_x` ON `nope` (`body`)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, rewriteCreateIndexKeyLength(ctx, catalog, "resultcolumnstest", tt.query))
		})
	}
}

// TestRewrittenCreateIndexExecutes is the point of the prefix length: the
// statement it produces has to be one go-mysql-server accepts.
func TestRewrittenCreateIndexExecutes(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)

	_, iter, _, err := h.sqlEngine.Query(ctx, "CREATE TABLE notes (id BIGINT PRIMARY KEY, body LONGTEXT)")
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))

	original := "CREATE INDEX `idx_body` ON `notes` (`body`)"
	_, _, _, err = h.sqlEngine.Query(ctx, original)
	require.Error(t, err, "MySQL refuses to index a text column without a prefix length")

	rewritten := rewriteCreateIndexKeyLength(ctx, h.sqlEngine.Analyzer.Catalog, "resultcolumnstest", original)
	_, iter, _, err = h.sqlEngine.Query(ctx, rewritten)
	require.NoError(t, err, "the rewritten statement must execute")
	require.NoError(t, iter.Close(ctx))
}
