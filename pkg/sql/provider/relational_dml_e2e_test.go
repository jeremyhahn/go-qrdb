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
	"context"
	"io"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
)

// ---------------------------------------------------------------------------
// End-to-end DML through pkg/sql/engine, driving real SQL text rather than
// direct writer calls -- the "merge criterion" scenario: CREATE TABLE with
// an AUTO_INCREMENT primary key, INSERT returning a generated
// LastInsertId, SELECT by primary key AND by a secondary index, UPDATE,
// DELETE, a uniqueIndex violation surfacing as MySQL error 1062, and
// TRUNCATE.
// ---------------------------------------------------------------------------

// execQueryCtx is execQuery but also returns the *sqle.Context the query
// ran under, so a caller can inspect session-scoped state such as
// LastInsertId after the statement completes.
func execQueryCtx(prov *Provider, dbName, query string) (*sqle.Context, sqle.Schema, []sqle.Row, error) {
	eng := engine.New(engine.Config{Provider: prov})
	defer func() { _ = eng.Close() }()

	sqlCtx := sqle.NewContext(context.Background(), sqle.WithSession(NewSession(sqle.NewBaseSession(), nil)))
	sqlCtx.SetCurrentDatabase(dbName)

	schema, iter, _, err := eng.Query(sqlCtx, query)
	if err != nil {
		return sqlCtx, nil, nil, err
	}
	var rows []sqle.Row
	for {
		row, nerr := iter.Next(sqlCtx)
		if nerr == io.EOF {
			break
		}
		if nerr != nil {
			_ = iter.Close(sqlCtx)
			return sqlCtx, nil, nil, nerr
		}
		rows = append(rows, row)
	}
	if cerr := iter.Close(sqlCtx); cerr != nil {
		return sqlCtx, nil, nil, cerr
	}
	return sqlCtx, schema, rows, nil
}

func TestRelationalDML_EndToEnd(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	// CREATE TABLE with an AUTO_INCREMENT primary key and a uniqueIndex
	// column.
	mustExecQuery(t, prov, "shop",
		`CREATE TABLE users (
			id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
			email VARCHAR(255),
			age INT
		)`)
	mustExecQuery(t, prov, "shop", "CREATE INDEX idx_email ON users (email)")

	// Re-issue CREATE INDEX as UNIQUE isn't directly expressible after the
	// fact in every dialect path this engine supports, so declare it
	// unique up front instead: drop and recreate via ALTER TABLE ADD
	// UNIQUE INDEX, the portable spelling go-mysql-server accepts.
	mustExecQuery(t, prov, "shop", "ALTER TABLE users DROP INDEX idx_email")
	mustExecQuery(t, prov, "shop", "ALTER TABLE users ADD UNIQUE INDEX idx_email (email)")

	// INSERT without an explicit id: LastInsertId must reflect the
	// generated AUTO_INCREMENT value.
	sqlCtx, _, _, err := execQueryCtx(prov, "shop", "INSERT INTO users (email, age) VALUES ('alice@example.com', 30)")
	require.NoError(t, err)
	aliceID := sqlCtx.GetLastQueryInfo().LastInsertId.Load()
	assert.Equal(t, int64(1), aliceID)

	sqlCtx2, _, _, err := execQueryCtx(prov, "shop", "INSERT INTO users (email, age) VALUES ('bob@example.com', 25)")
	require.NoError(t, err)
	bobID := sqlCtx2.GetLastQueryInfo().LastInsertId.Load()
	assert.Equal(t, int64(2), bobID)

	// SELECT by primary key.
	_, rows := mustExecQuery(t, prov, "shop", "SELECT email, age FROM users WHERE id = 1")
	require.Len(t, rows, 1)
	assert.Equal(t, "alice@example.com", rows[0][0])

	// SELECT by the unique secondary index.
	_, rows = mustExecQuery(t, prov, "shop", "SELECT id, age FROM users WHERE email = 'bob@example.com'")
	require.Len(t, rows, 1)
	assert.Equal(t, uint64(2), rows[0][0])

	// UPDATE.
	mustExecQuery(t, prov, "shop", "UPDATE users SET age = 31 WHERE id = 1")
	_, rows = mustExecQuery(t, prov, "shop", "SELECT age FROM users WHERE id = 1")
	require.Len(t, rows, 1)
	assert.Equal(t, int32(31), rows[0][0])

	// uniqueIndex violation surfaces as MySQL error 1062.
	_, _, _, err = execQueryCtx(prov, "shop", "INSERT INTO users (email, age) VALUES ('alice@example.com', 99)")
	require.Error(t, err)
	sqlErr := sqle.CastSQLError(err)
	require.NotNil(t, sqlErr, "a uniqueIndex violation must be classifiable as a go-mysql-server SQL error")
	assert.Equal(t, mysql.ERDupEntry, sqlErr.Num)
	assert.EqualValues(t, 1062, sqlErr.Num)

	// The rejected row must not exist.
	_, rows = mustExecQuery(t, prov, "shop", "SELECT COUNT(*) FROM users WHERE age = 99")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 0, rows[0][0])

	// DELETE.
	mustExecQuery(t, prov, "shop", "DELETE FROM users WHERE id = 2")
	_, rows = mustExecQuery(t, prov, "shop", "SELECT id FROM users")
	require.Len(t, rows, 1)
	assert.Equal(t, uint64(1), rows[0][0])

	// TRUNCATE.
	mustExecQuery(t, prov, "shop", "TRUNCATE TABLE users")
	_, rows = mustExecQuery(t, prov, "shop", "SELECT id FROM users")
	assert.Empty(t, rows)
}

func TestRelationalDML_EndToEnd_ReplaceInto(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(255))")
	mustExecQuery(t, prov, "shop", "INSERT INTO users (id, email) VALUES (1, 'alice@example.com')")

	mustExecQuery(t, prov, "shop", "REPLACE INTO users (id, email) VALUES (1, 'alice2@example.com')")

	_, rows := mustExecQuery(t, prov, "shop", "SELECT email FROM users WHERE id = 1")
	require.Len(t, rows, 1)
	assert.Equal(t, "alice2@example.com", rows[0][0])
}

func TestRelationalDML_EndToEnd_MultiRowInsertAutoIncrement(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop",
		"CREATE TABLE widgets (id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, name VARCHAR(64))")
	mustExecQuery(t, prov, "shop",
		"INSERT INTO widgets (name) VALUES ('a'), ('b'), ('c')")

	_, rows := mustExecQuery(t, prov, "shop", "SELECT id, name FROM widgets ORDER BY id")
	require.Len(t, rows, 3)
	seen := make(map[uint64]bool)
	for _, r := range rows {
		id := r[0].(uint64)
		assert.False(t, seen[id], "auto-increment values within a multi-row INSERT must be unique")
		seen[id] = true
	}
}
