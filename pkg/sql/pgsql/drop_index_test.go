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

// TestRewriteDropIndex covers the translation of PostgreSQL's schema-scoped
// DROP INDEX -- including the DROP INDEX CURRENT_SCHEMA()."name" form
// gorm.io/driver/postgres emits -- into MySQL's table-scoped ALTER TABLE
// ... DROP INDEX, with the owning table resolved from the catalog.
func TestRewriteDropIndex(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)
	catalog := h.sqlEngine.Analyzer.Catalog

	_, iter, _, err := h.sqlEngine.Query(ctx, "CREATE INDEX idx_people_name ON people (name)")
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))

	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "gorm's CURRENT_SCHEMA()-qualified form",
			query: `DROP INDEX CURRENT_SCHEMA()."idx_people_name"`,
			want:  "ALTER TABLE `people` DROP INDEX `idx_people_name`",
		},
		{
			name:  "bare name",
			query: `DROP INDEX idx_people_name`,
			want:  "ALTER TABLE `people` DROP INDEX `idx_people_name`",
		},
		{
			name:  "IF EXISTS and CONCURRENTLY accepted",
			query: `DROP INDEX CONCURRENTLY IF EXISTS "idx_people_name"`,
			want:  "ALTER TABLE `people` DROP INDEX `idx_people_name`",
		},
		{
			name:  "unknown index left alone",
			query: `DROP INDEX "no_such_index"`,
			want:  `DROP INDEX "no_such_index"`,
		},
		{
			name:  "non-DROP-INDEX statement untouched",
			query: `DROP TABLE "people"`,
			want:  `DROP TABLE "people"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rewriteDropIndex(ctx, catalog, "resultcolumnstest", tt.query)
			assert.Equal(t, tt.want, got)
		})
	}

	// The produced statement must actually execute and remove the index.
	rewritten := rewriteDropIndex(ctx, catalog, "resultcolumnstest",
		`DROP INDEX CURRENT_SCHEMA()."idx_people_name"`)
	_, iter, _, err = h.sqlEngine.Query(ctx, rewritten)
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))
	assert.Equal(t, `DROP INDEX "idx_people_name"`,
		rewriteDropIndex(ctx, catalog, "resultcolumnstest", `DROP INDEX "idx_people_name"`),
		"a dropped index must no longer resolve")
}

// TestRewriteAlterIndexRename covers PostgreSQL's ALTER INDEX ... RENAME TO
// translation, and proves the produced MySQL statement executes.
func TestRewriteAlterIndexRename(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)
	catalog := h.sqlEngine.Analyzer.Catalog

	_, iter, _, err := h.sqlEngine.Query(ctx, "CREATE INDEX idx_old ON people (name)")
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))

	got := rewriteAlterIndexRename(ctx, catalog, "resultcolumnstest",
		`ALTER INDEX CURRENT_SCHEMA()."idx_old" RENAME TO "idx_new"`)
	require.Equal(t, "ALTER TABLE `people` RENAME INDEX `idx_old` TO `idx_new`", got)

	_, iter, _, err = h.sqlEngine.Query(ctx, got)
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))

	// The rename must be visible: the old name no longer resolves, the new
	// one does.
	assert.Equal(t, `DROP INDEX "idx_old"`,
		rewriteDropIndex(ctx, catalog, "resultcolumnstest", `DROP INDEX "idx_old"`))
	assert.Equal(t, "ALTER TABLE `people` DROP INDEX `idx_new`",
		rewriteDropIndex(ctx, catalog, "resultcolumnstest", `DROP INDEX "idx_new"`))

	// Unknown index and non-matching statements pass through.
	assert.Equal(t, `ALTER INDEX "nope" RENAME TO "x"`,
		rewriteAlterIndexRename(ctx, catalog, "resultcolumnstest", `ALTER INDEX "nope" RENAME TO "x"`))
	assert.Equal(t, `ALTER TABLE "people" RENAME TO "folks"`,
		rewriteAlterIndexRename(ctx, catalog, "resultcolumnstest", `ALTER TABLE "people" RENAME TO "folks"`))
}
