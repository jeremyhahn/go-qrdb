// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Indexes an ALTER TABLE rewrite changes (go-qrdb#547)
//
// DROP COLUMN narrows every index on the column and empties a single-column
// one; a layout-changing MODIFY COLUMN changes the encoding every index on
// the column was built with. Such an index is rebuilt under a new
// incarnation by the rewrite, and every incarnation the definition stops
// listing is reclaimed after quiescence.
// ---------------------------------------------------------------------------

// indexedWidgets creates shop.widgets with idx_ab (a, b), idx_b (b) and
// idx_c (c), and four rows.
func indexedWidgets(t *testing.T) (*ddlHarness, *Provider, *service.TableDef) {
	t.Helper()
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, a VARCHAR(16), b VARCHAR(16), c INT, "+
		"INDEX idx_ab (a, b), INDEX idx_b (b), INDEX idx_c (c))")
	mustExecQuery(t, prov, "shop", "INSERT INTO widgets VALUES (1, 'x', 'p', 10), (2, 'x', 'q', 20), (3, 'y', 'p', 30), (4, 'z', NULL, 40)")
	def, err := h.tableSvc.GetTable(context.Background(), "shop", "widgets")
	require.NoError(t, err)
	return h, prov, def
}

func requireEmptyPrefix(t *testing.T, h *ddlHarness, prefix, what string) {
	t.Helper()
	left, err := h.kvSvc.Scan(context.Background(), prefix)
	require.NoError(t, err)
	assert.Empty(t, left, what)
}

// TestRewrite_DropColumnRebuildsNarrowedIndexAndReclaimsTheOld: after DROP
// COLUMN b, idx_ab is a new incarnation holding one entry per row encoded
// over (a) alone, lookups through it are exact, and -- past quiescence --
// the old idx_ab incarnation and the emptied idx_b are gone from storage.
func TestRewrite_DropColumnRebuildsNarrowedIndexAndReclaimsTheOld(t *testing.T) {
	h, prov, before := indexedWidgets(t)
	mustExecQuery(t, prov, "shop", "ALTER TABLE widgets DROP COLUMN b")

	after, err := h.tableSvc.GetTable(context.Background(), "shop", "widgets")
	require.NoError(t, err)
	require.NotEqual(t, incarnationOf(before, "idx_ab"), incarnationOf(after, "idx_ab"), "a narrowed index is a new incarnation")
	assert.Len(t, indexKeysOf(t, h, "widgets", "idx_ab"), 4, "rebuilt: one entry per row")

	_, rows := mustExecQuery(t, prov, "shop", "SELECT id FROM widgets WHERE a = 'x' ORDER BY id")
	require.Len(t, rows, 2)
	assert.EqualValues(t, 1, rows[0][0])
	assert.EqualValues(t, 2, rows[1][0])

	mustExecQuery(t, prov, "shop", "INSERT INTO widgets VALUES (5, 'x', 50)")
	_, rows = mustExecQuery(t, prov, "shop", "SELECT id FROM widgets WHERE a = 'x' ORDER BY id")
	assert.Len(t, rows, 3)

	sweepPastQuiescence(t, h)
	requireEmptyPrefix(t, h, service.TableIndexPrefix("shop", before.TableID, 0, incarnationOf(before, "idx_ab")), "the narrowed index's old entries are reclaimed")
	requireEmptyPrefix(t, h, service.TableIndexPrefix("shop", before.TableID, 0, incarnationOf(before, "idx_b")), "the emptied index's entries are reclaimed")
	assert.Len(t, indexKeysOf(t, h, "widgets", "idx_ab"), 5, "the live incarnation is untouched by the sweep")
	assert.Len(t, indexKeysOf(t, h, "widgets", "idx_c"), 5)
}

// TestRewrite_ModifyColumnRebuildsItsIndexes: a MODIFY that changes the
// column's type rebuilds every index on it under a new incarnation.
func TestRewrite_ModifyColumnRebuildsItsIndexes(t *testing.T) {
	h, prov, before := indexedWidgets(t)
	mustExecQuery(t, prov, "shop", "ALTER TABLE widgets MODIFY COLUMN c VARCHAR(8)")

	after, err := h.tableSvc.GetTable(context.Background(), "shop", "widgets")
	require.NoError(t, err)
	require.NotEqual(t, incarnationOf(before, "idx_c"), incarnationOf(after, "idx_c"))
	assert.Equal(t, incarnationOf(before, "idx_ab"), incarnationOf(after, "idx_ab"), "an index not on the column keeps its entries")
	assert.Len(t, indexKeysOf(t, h, "widgets", "idx_c"), 4)

	_, rows := mustExecQuery(t, prov, "shop", "SELECT id FROM widgets WHERE c = '30'")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 3, rows[0][0])

	sweepPastQuiescence(t, h)
	requireEmptyPrefix(t, h, service.TableIndexPrefix("shop", before.TableID, 0, incarnationOf(before, "idx_c")), "the old encoding's entries are reclaimed")
}

// TestRewrite_NarrowingAUniqueIndexOntoDuplicatesFails: dropping b from
// UNIQUE (a, b) where a repeats cannot be satisfied. The ALTER fails, the
// table keeps its pre-ALTER definition, and nothing the attempt built
// outlives the sweep.
func TestRewrite_NarrowingAUniqueIndexOntoDuplicatesFails(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY, a VARCHAR(16), b VARCHAR(16), UNIQUE INDEX u_ab (a, b))")
	mustExecQuery(t, prov, "shop", "INSERT INTO widgets VALUES (1, 'x', 'p'), (2, 'x', 'q')")
	before, err := h.tableSvc.GetTable(context.Background(), "shop", "widgets")
	require.NoError(t, err)

	_, _, err = execQuery(prov, "shop", "ALTER TABLE widgets DROP COLUMN b")
	require.Error(t, err)

	after, err := h.tableSvc.GetTable(context.Background(), "shop", "widgets")
	require.NoError(t, err, "the aborted rewrite leaves the table servable")
	assert.Len(t, after.Columns, 3, "under its pre-ALTER definition")
	assert.Equal(t, incarnationOf(before, "u_ab"), incarnationOf(after, "u_ab"))

	sweepPastQuiescence(t, h)
	all, err := h.kvSvc.Scan(context.Background(), service.TableIndexGenerationPrefix("shop", before.TableID, 0))
	require.NoError(t, err)
	assert.Len(t, all, 2, "only the live index's two entries remain")
}
