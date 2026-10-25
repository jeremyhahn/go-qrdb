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

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// declaredForeignKeys returns the foreign keys the named table declares.
func declaredForeignKeys(t *testing.T, ctx *sqle.Context, catalog sqle.Catalog, db, table string) []sqle.ForeignKeyConstraint {
	t.Helper()
	tbl, ok := lookupTable(ctx, catalog, db, table)
	require.True(t, ok)
	fkTable, ok := tbl.(sqle.ForeignKeyTable)
	require.True(t, ok)
	fks, err := fkTable.GetDeclaredForeignKeys(ctx)
	require.NoError(t, err)
	return fks
}

// runDropTableCascade runs the effect dropTableCascadeEffect returns for raw,
// as an Execute would; a statement with no effect runs nothing.
func runDropTableCascade(t *testing.T, ctx *sqle.Context, catalog sqle.Catalog, raw string) {
	t.Helper()
	if effect := dropTableCascadeEffect(catalog, raw); effect != nil {
		require.NoError(t, effect(ctx))
	}
}

// TestDropTableCascadeEffectIsBuiltNotRun pins the split the extended
// protocol relies on: building the effect, which Parse does, touches no
// foreign key, and only a statement with CASCADE has an effect at all.
func TestDropTableCascadeEffectIsBuiltNotRun(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)
	catalog := h.sqlEngine.Analyzer.Catalog
	_, iter, _, err := h.sqlEngine.Query(ctx,
		"CREATE TABLE pets (id BIGINT PRIMARY KEY, owner_id BIGINT, "+
			"CONSTRAINT fk_pets_owner FOREIGN KEY (owner_id) REFERENCES people (id))")
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))

	assert.Nil(t, dropTableCascadeEffect(catalog, `DROP TABLE IF EXISTS "people"`),
		"a DROP without CASCADE has no side effect")
	assert.Nil(t, dropTableCascadeEffect(catalog, `SELECT 1`))

	effect := dropTableCascadeEffect(catalog, `DROP TABLE IF EXISTS "people" CASCADE`)
	require.NotNil(t, effect)
	assert.Len(t, declaredForeignKeys(t, ctx, catalog, "resultcolumnstest", "pets"), 1,
		"building the effect drops nothing")

	require.NoError(t, effect(ctx))
	assert.Empty(t, declaredForeignKeys(t, ctx, catalog, "resultcolumnstest", "pets"))
	require.NoError(t, effect(ctx), "a second run finds nothing left to drop")
}

// TestDropForeignKeysToAnUnknownDatabase covers the database lookup: a
// session whose current database is gone drops nothing and does not fail,
// leaving the DROP itself to report the missing table.
func TestDropForeignKeysToAnUnknownDatabase(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)
	catalog := h.sqlEngine.Analyzer.Catalog
	_, iter, _, err := h.sqlEngine.Query(ctx,
		"CREATE TABLE pets (id BIGINT PRIMARY KEY, owner_id BIGINT, "+
			"CONSTRAINT fk_pets_owner FOREIGN KEY (owner_id) REFERENCES people (id))")
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))

	dropForeignKeysTo(ctx, catalog, "no_such_db", map[string]bool{"people": true})
	assert.Len(t, declaredForeignKeys(t, ctx, catalog, "resultcolumnstest", "pets"), 1,
		"another database's DROP leaves this one's keys")

	dropForeignKeysTo(ctx, catalog, "resultcolumnstest", map[string]bool{"no_such_table": true})
	assert.Len(t, declaredForeignKeys(t, ctx, catalog, "resultcolumnstest", "pets"), 1,
		"a key to a table not being dropped is kept")
}

// TestDropTableCascadeEffect covers the CASCADE half of PostgreSQL's DROP
// TABLE: incoming foreign keys are dropped so the DROP itself can pass the
// engine's referenced-by validation. GORM's migrator drops every table with
// DROP TABLE IF EXISTS ... CASCADE, so without this no schema teardown works.
func TestDropTableCascadeEffect(t *testing.T) {
	newHarness := func(t *testing.T) (*ParseHandler, *sqle.Context, sqle.Catalog) {
		h, ctx := newResultColumnsHandler(t)
		catalog := h.sqlEngine.Analyzer.Catalog
		for _, q := range []string{
			"CREATE TABLE pets (id BIGINT PRIMARY KEY, owner_id BIGINT, " +
				"CONSTRAINT fk_pets_owner FOREIGN KEY (owner_id) REFERENCES people (id))",
		} {
			_, iter, _, err := h.sqlEngine.Query(ctx, q)
			require.NoError(t, err)
			require.NoError(t, iter.Close(ctx))
		}
		require.Len(t, declaredForeignKeys(t, ctx, catalog, "resultcolumnstest", "pets"), 1)
		return h, ctx, catalog
	}

	t.Run("CASCADE drops incoming foreign keys", func(t *testing.T) {
		h, ctx, catalog := newHarness(t)
		runDropTableCascade(t, ctx, catalog, `DROP TABLE IF EXISTS "people" CASCADE`)
		assert.Empty(t, declaredForeignKeys(t, ctx, catalog, "resultcolumnstest", "pets"))
		_ = h
	})

	t.Run("plain DROP TABLE is untouched", func(t *testing.T) {
		_, ctx, catalog := newHarness(t)
		runDropTableCascade(t, ctx, catalog, `DROP TABLE IF EXISTS "people"`)
		assert.Len(t, declaredForeignKeys(t, ctx, catalog, "resultcolumnstest", "pets"), 1)
	})

	t.Run("referencing table also being dropped is left alone", func(t *testing.T) {
		// Both ends of the FK die with the statement; nothing needs dropping
		// up front, and the helper must not touch a table that is going away.
		_, ctx, catalog := newHarness(t)
		runDropTableCascade(t, ctx, catalog, `DROP TABLE IF EXISTS "people", "pets" CASCADE`)
		assert.Len(t, declaredForeignKeys(t, ctx, catalog, "resultcolumnstest", "pets"), 1)
	})

	t.Run("unrelated CASCADE drop leaves other tables' keys", func(t *testing.T) {
		_, ctx, catalog := newHarness(t)
		runDropTableCascade(t, ctx, catalog, `DROP TABLE IF EXISTS "pets" CASCADE`)
		assert.Len(t, declaredForeignKeys(t, ctx, catalog, "resultcolumnstest", "pets"), 1)
	})
}

// TestDropTableCascadeExecutesEndToEnd proves the full statement -- side
// effect plus the DROP itself -- succeeds through the engine where it
// previously failed referenced-by validation.
func TestDropTableCascadeExecutesEndToEnd(t *testing.T) {
	h, ctx := newResultColumnsHandler(t)
	catalog := h.sqlEngine.Analyzer.Catalog
	_, iter, _, err := h.sqlEngine.Query(ctx,
		"CREATE TABLE pets (id BIGINT PRIMARY KEY, owner_id BIGINT, "+
			"CONSTRAINT fk_pets_owner FOREIGN KEY (owner_id) REFERENCES people (id))")
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))

	// Without the CASCADE side effect the engine refuses this DROP.
	_, iter, _, err = h.sqlEngine.Query(ctx, "DROP TABLE people")
	if err == nil {
		require.NoError(t, iter.Close(ctx))
		t.Fatal("engine no longer validates referenced tables; CASCADE helper may be dead code")
	}

	runDropTableCascade(t, ctx, catalog, `DROP TABLE IF EXISTS "people" CASCADE`)
	_, iter, _, err = h.sqlEngine.Query(ctx, "DROP TABLE IF EXISTS people CASCADE")
	require.NoError(t, err)
	require.NoError(t, iter.Close(ctx))
}
