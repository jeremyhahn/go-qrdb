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

//go:build qrdb_pgsql

package pgsql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRewriteAlterColumnType covers the translation of PostgreSQL's
// ALTER COLUMN ... TYPE ... USING into MySQL's MODIFY — the statement
// gorm.io/driver/postgres emits for every column-type migration and the
// single largest ALTER-family failure on the pgsql conformance target.
func TestRewriteAlterColumnType(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)
	catalog := h.sqlEngine.Analyzer.Catalog

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "plain cast USING becomes MODIFY",
			query: `ALTER TABLE "people" ALTER COLUMN "name" TYPE varchar(20) USING "name"::varchar(20)`,
			want:  "ALTER TABLE `people` MODIFY `name` varchar(20) NULL",
		},
		{
			name:  "USING is optional",
			query: `ALTER TABLE "people" ALTER COLUMN "name" TYPE varchar(30)`,
			want:  "ALTER TABLE `people` MODIFY `name` varchar(30) NULL",
		},
		{
			name:  "SET DATA TYPE spelling",
			query: `ALTER TABLE "people" ALTER COLUMN "name" SET DATA TYPE varchar(40)`,
			want:  "ALTER TABLE `people` MODIFY `name` varchar(40) NULL",
		},
		{
			name:  "CAST-form USING accepted",
			query: `ALTER TABLE "people" ALTER COLUMN "name" TYPE varchar(50) USING CAST("name" AS varchar(50))`,
			want:  "ALTER TABLE `people` MODIFY `name` varchar(50) NULL",
		},
		{
			// text is a PostgreSQL-only spelling; the substituted type must
			// come from the same table CREATE TABLE translation uses.
			name:  "PostgreSQL-only type is translated",
			query: `ALTER TABLE "people" ALTER COLUMN "name" TYPE text USING "name"::text`,
			want:  "ALTER TABLE `people` MODIFY `name` LONGTEXT NULL",
		},
		{
			// A chained plain cast of the same column carries the same
			// semantics as MODIFY's own conversion -- gorm emits this shape
			// for smallint-to-boolean migrations.
			name:  "chained plain cast accepted",
			query: `ALTER TABLE "people" ALTER COLUMN "name" TYPE varchar(20) USING "name"::text::varchar(20)`,
			want:  "ALTER TABLE `people` MODIFY `name` varchar(20) NULL",
		},
		{
			// A USING expression that transforms the value cannot be
			// expressed as MODIFY; leave it for the engine to reject rather
			// than silently dropping the transformation.
			name:  "value-transforming USING left alone",
			query: `ALTER TABLE "people" ALTER COLUMN "name" TYPE varchar(20) USING UPPER("name")::varchar(20)`,
			want:  `ALTER TABLE "people" ALTER COLUMN "name" TYPE varchar(20) USING UPPER("name")::varchar(20)`,
		},
		{
			name:  "USING casting a DIFFERENT column left alone",
			query: `ALTER TABLE "people" ALTER COLUMN "name" TYPE varchar(20) USING "id"::varchar(20)`,
			want:  `ALTER TABLE "people" ALTER COLUMN "name" TYPE varchar(20) USING "id"::varchar(20)`,
		},
		{
			name:  "unknown table left alone",
			query: `ALTER TABLE "nope" ALTER COLUMN "name" TYPE varchar(20)`,
			want:  `ALTER TABLE "nope" ALTER COLUMN "name" TYPE varchar(20)`,
		},
		{
			name:  "unknown column left alone",
			query: `ALTER TABLE "people" ALTER COLUMN "nope" TYPE varchar(20)`,
			want:  `ALTER TABLE "people" ALTER COLUMN "nope" TYPE varchar(20)`,
		},
		{
			name:  "nullability ALTERs are not captured by the TYPE rewrite",
			query: `ALTER TABLE "people" ALTER COLUMN "name" DROP NOT NULL`,
			want:  `ALTER TABLE "people" ALTER COLUMN "name" DROP NOT NULL`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteAlterColumnType(ctx, catalog, "resultcolumnstest", tt.query)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestRewriteCommentOnColumn covers COMMENT ON COLUMN, which MySQL spells as
// a MODIFY carrying a COMMENT attribute.
func TestRewriteCommentOnColumn(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)
	catalog := h.sqlEngine.Analyzer.Catalog

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "comment is restated as MODIFY ... COMMENT",
			query: `COMMENT ON COLUMN "people"."name" IS 'my name'`,
			want:  "ALTER TABLE `people` MODIFY `name` varchar(64) NULL COMMENT 'my name'",
		},
		{
			name:  "IS NULL clears the comment",
			query: `COMMENT ON COLUMN "people"."name" IS NULL`,
			want:  "ALTER TABLE `people` MODIFY `name` varchar(64) NULL",
		},
		{
			name:  "embedded quotes survive verbatim",
			query: `COMMENT ON COLUMN "people"."name" IS 'it''s mine'`,
			want:  "ALTER TABLE `people` MODIFY `name` varchar(64) NULL COMMENT 'it''s mine'",
		},
		{
			name:  "unknown table left alone",
			query: `COMMENT ON COLUMN "nope"."name" IS 'x'`,
			want:  `COMMENT ON COLUMN "nope"."name" IS 'x'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteCommentOnColumn(ctx, catalog, "resultcolumnstest", tt.query)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestRewrittenAlterColumnTypeExecutes proves the produced MODIFY is a
// statement go-mysql-server actually accepts and applies.
func TestRewrittenAlterColumnTypeExecutes(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)

	rewritten := rewriteAlterColumnType(ctx, h.sqlEngine.Analyzer.Catalog,
		"resultcolumnstest", `ALTER TABLE "people" ALTER COLUMN "name" TYPE varchar(20) USING "name"::varchar(20)`)
	require.NotContains(t, rewritten, "USING", "the PostgreSQL form must be gone")

	_, iter, _, err := h.sqlEngine.Query(ctx, rewritten)
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))
}

// TestRewriteAlterColumnDefault covers the SET/DROP DEFAULT re-quoting. The
// engine's own grammar accepts the statement; only the translator's
// reserved-word mangling stood in the way.
func TestRewriteAlterColumnDefault(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)
	catalog := h.sqlEngine.Analyzer.Catalog

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "SET DEFAULT literal",
			query: `ALTER TABLE "column_structs" ALTER COLUMN "code" SET DEFAULT 'hello'`,
			want:  "ALTER TABLE `column_structs` ALTER `code` SET DEFAULT 'hello'",
		},
		{
			name:  "DROP DEFAULT",
			query: `ALTER TABLE "column_structs" ALTER COLUMN "code" DROP DEFAULT`,
			want:  "ALTER TABLE `column_structs` ALTER `code` DROP DEFAULT",
		},
		{
			name:  "COLUMN keyword optional",
			query: `ALTER TABLE "t" ALTER "c" SET DEFAULT 42`,
			want:  "ALTER TABLE `t` ALTER `c` SET DEFAULT 42",
		},
		{
			name:  "nullability form not captured",
			query: `ALTER TABLE "t" ALTER COLUMN "c" DROP NOT NULL`,
			want:  `ALTER TABLE "t" ALTER COLUMN "c" DROP NOT NULL`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteAlterColumnDefault(ctx, catalog, "resultcolumnstest", tt.query)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestRewrittenAlterColumnDefaultExecutes proves the produced statement is
// one the engine accepts and applies.
func TestRewrittenAlterColumnDefaultExecutes(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)

	rewritten := rewriteAlterColumnDefault(ctx, h.sqlEngine.Analyzer.Catalog,
		"resultcolumnstest", `ALTER TABLE "people" ALTER COLUMN "name" SET DEFAULT 'anon'`)
	_, iter, _, err := h.sqlEngine.Query(ctx, rewritten)
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))
}
