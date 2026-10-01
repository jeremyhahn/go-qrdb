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
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// seedLegacyTable writes shop.users exactly as v0.20.0 through v0.21.0 left
// it: a catalog record with no layout and no index incarnations -- two
// indexes that would otherwise both read as incarnation 0 -- and rows and
// index entries under the old, generation-less prefixes.
func seedLegacyTable(t *testing.T, h *ddlHarness) {
	t.Helper()
	ctx := context.Background()
	legacy := `{"table_id":7,"name":"users","database":"shop",` +
		`"columns":[{"name":"id","type":"BIGINT UNSIGNED","nullable":false},` +
		`{"name":"email","type":"VARCHAR(64)","nullable":true},{"name":"code","type":"VARCHAR(16)","nullable":true}],` +
		`"primary_key":[{"name":"id"}],` +
		`"indexes":[{"name":"idx_email","columns":[{"name":"email"}],"unique":true},` +
		`{"name":"idx_code","columns":[{"name":"code"}]}],"schema_version":1}`
	require.NoError(t, h.kvSvc.Put(ctx, service.TableMetadataKey("shop", "users"), []byte(legacy)))
	for _, key := range []string{"__tbl:shop:7/row1", "__tbl:shop:7/row2", "__idx:shop:7:idx_email/a", "__idx:shop:7:idx_code/b"} {
		require.NoError(t, h.kvSvc.Put(ctx, key, []byte("v")))
	}
}

// requireLayoutRefusal asserts err is the table-layout refusal as the MySQL
// wire reports it -- ER_TABLE_NEEDS_UPGRADE, never an empty result or the
// generic 1105 -- with a message telling the operator what to do.
func requireLayoutRefusal(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	sqlErr := sqle.CastSQLError(err)
	require.NotNil(t, sqlErr)
	assert.Equal(t, ErrCodeTableLayoutUnsupported, sqlErr.Num, "err = %v", err)
	assert.Equal(t, "HY000", sqlErr.State)
	assert.Contains(t, sqlErr.Message, "shop/users")
	assert.Contains(t, sqlErr.Message, "re-create")
	// Past the engine's own statement wrappers (sql.UnwrapError, which
	// CastSQLError also looks through), the error classifies as the layout
	// refusal on every non-SQL transport too.
	assert.True(t, service.IsTableLayoutUnsupported(sqle.UnwrapError(err)), "err = %v", err)
}

// TestLegacyLayout_TableIsRefusedNotReadAsEmpty is the owner ruling
// ("break it"): a table created by v0.20.0 through v0.21.0 is refused by
// every read and write, typed, never answered with no rows.
func TestLegacyLayout_TableIsRefusedNotReadAsEmpty(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	seedLegacyTable(t, h)
	mustExecQuery(t, prov, "shop", "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY)")

	for _, query := range []string{
		"SELECT * FROM users",
		"SELECT id FROM users WHERE email = 'a@example.com'",
		"SELECT COUNT(*) FROM users",
		"INSERT INTO users VALUES (3, 'c@example.com', 'C')",
		"UPDATE users SET code = 'Z' WHERE id = 1",
		"DELETE FROM users WHERE id = 1",
		"TRUNCATE TABLE users",
		"ALTER TABLE users ADD COLUMN note VARCHAR(16)",
		"ALTER TABLE users DROP COLUMN code",
		"ALTER TABLE users MODIFY COLUMN code VARCHAR(32)",
		"CREATE INDEX idx_new ON users (code)",
		"ALTER TABLE users ADD CONSTRAINT fk_widget FOREIGN KEY (id) REFERENCES widgets (id)",
	} {
		t.Run(query, func(t *testing.T) {
			_, rows, err := execQuery(prov, "shop", query)
			assert.Empty(t, rows)
			requireLayoutRefusal(t, err)
		})
	}

	// The stand-in lists no indexes and declares no constraints (see
	// legacyLayoutTable.GetIndexes), so the engine refuses these itself,
	// before they reach the table: none may succeed or change the catalog.
	before, err := h.kvSvc.Get(context.Background(), service.TableMetadataKey("shop", "users"))
	require.NoError(t, err)
	for _, query := range []string{
		"ALTER TABLE users RENAME INDEX idx_code TO idx_c",
		"DROP INDEX idx_code ON users",
		"ALTER TABLE users DROP FOREIGN KEY fk_self",
	} {
		t.Run(query, func(t *testing.T) {
			_, _, err := execQuery(prov, "shop", query)
			require.Error(t, err)
		})
	}
	after, err := h.kvSvc.Get(context.Background(), service.TableMetadataKey("shop", "users"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "a refused statement leaves the legacy definition untouched")
}

// TestLegacyLayout_InformationSchemaNamesTheRemedy: one legacy table must
// not fail information_schema for the whole database, and where it is
// listed its comment is the refusal, naming the remedy.
func TestLegacyLayout_InformationSchemaNamesTheRemedy(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	seedLegacyTable(t, h)
	mustExecQuery(t, prov, "shop", "CREATE TABLE widgets (id BIGINT UNSIGNED PRIMARY KEY)")

	_, rows := mustExecQuery(t, prov, "shop",
		"SELECT table_name, table_comment FROM information_schema.tables WHERE table_schema = 'shop' AND table_name IN ('users', 'widgets') ORDER BY table_name")
	require.Len(t, rows, 2)
	assert.Equal(t, "users", rows[0][0])
	assert.Contains(t, rows[0][1], "re-create")
	assert.Equal(t, "widgets", rows[1][0])
}

// TestLegacyLayout_InterruptedRewriteIsDroppable: a legacy table whose
// v0.20.x ALTER rewrite was interrupted still resolves as a legacy table --
// the layout is checked before the journal -- so DROP TABLE, the remedy,
// still reaches it rather than being refused as a rewrite nothing can
// resume.
func TestLegacyLayout_InterruptedRewriteIsDroppable(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	seedLegacyTable(t, h)
	journal := service.TableMetadataKey("shop", "users") + "/__ddl"
	require.NoError(t, h.kvSvc.Put(context.Background(), journal, []byte(`{"kind":"rewrite"}`)))

	_, _, err := execQuery(prov, "shop", "SELECT * FROM users")
	requireLayoutRefusal(t, err)

	mustExecQuery(t, prov, "shop", "DROP TABLE users")
	_, err = h.kvSvc.Get(context.Background(), journal)
	var notFound *service.KeyNotFoundError
	require.ErrorAs(t, err, &notFound, "the drop takes the interrupted journal with it")
	sweepPastQuiescence(t, h)
	for _, prefix := range service.LegacyTableKeyPrefixes("shop", 7) {
		left, err := h.kvSvc.Scan(context.Background(), prefix)
		require.NoError(t, err)
		assert.Empty(t, left, "the legacy data under %s is reclaimed", prefix)
	}
}

// TestLegacyLayout_DropAndRecreateIsTheRemedy proves the remedy the error
// names works: DROP TABLE removes the legacy table and reclaims its data
// under the old prefixes, and the re-created table works.
func TestLegacyLayout_DropAndRecreateIsTheRemedy(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	seedLegacyTable(t, h)

	mustExecQuery(t, prov, "shop", "DROP TABLE users")
	sweepPastQuiescence(t, h)
	for _, prefix := range service.LegacyTableKeyPrefixes("shop", 7) {
		left, err := h.kvSvc.Scan(context.Background(), prefix)
		require.NoError(t, err)
		assert.Empty(t, left, "the legacy data under %s is reclaimed", prefix)
	}

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(64))")
	mustExecQuery(t, prov, "shop", "INSERT INTO users VALUES (1, 'a@example.com')")
	_, rows := mustExecQuery(t, prov, "shop", "SELECT id FROM users")
	require.Len(t, rows, 1)
}
