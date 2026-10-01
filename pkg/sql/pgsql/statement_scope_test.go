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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// TestPGWire_StatementResolvesEachTableOnce: the PostgreSQL wire delivers
// statement boundaries, so a statement resolves each table definition once,
// as it does on the MySQL wire.
func TestPGWire_StatementResolvesEachTableOnce(t *testing.T) {
	hs := sqltest.NewHarness()
	counting := hs.CountTableReads()
	prov := provider.NewProviderWithEngine("qrdb", hs.KVSvc, "", provider.WithRegistry(hs.Registry))
	eng := engine.New(engine.Config{Provider: prov, Dialect: engine.DialectPostgreSQL})
	t.Cleanup(func() { _ = eng.Close() })
	h := NewParseHandler(eng, engine.NewTranslator(), "qrdb")

	ctx, err := newConnectionSessionHandler()(context.Background())
	require.NoError(t, err)
	run := func(q string) {
		t.Helper()
		_, err := h.HandleParse(ctx, q)
		require.NoError(t, err, q)
	}

	run("CREATE TABLE notes (id BIGINT PRIMARY KEY, body VARCHAR(64))")
	run("INSERT INTO notes (id, body) VALUES (1, 'a')")
	counting.Reset()
	run("UPDATE notes SET body = 'b' WHERE id = 1")
	assert.Equal(t, int32(1), counting.Gets(), "an UPDATE reads its table's definition once")

	run("ALTER TABLE notes ADD COLUMN extra INT")
	run("UPDATE notes SET extra = 1 WHERE id = 1")
}
