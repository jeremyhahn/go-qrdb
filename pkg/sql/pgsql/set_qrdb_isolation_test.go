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

	wire "github.com/jeroenrinzema/psql-wire"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// TestPGWire_SetQrdbIsolation_TakesEffect is the regression test for the PG
// wire swallowing every SET as a no-op: SET qrdb_isolation reached nothing,
// so a session that asked for written+reads silently ran at the default and
// its range read was served instead of refused.
func TestPGWire_SetQrdbIsolation_TakesEffect(t *testing.T) {
	hs, closeFn, err := sqltest.NewLockingHarness()
	require.NoError(t, err)
	t.Cleanup(closeFn)
	prov := provider.NewProvider("qrdb", hs.KVSvc, provider.WithRegistry(hs.Registry))
	eng := engine.New(engine.Config{Provider: prov, Dialect: engine.DialectPostgreSQL})
	t.Cleanup(func() { _ = eng.Close() })
	h := NewParseHandler(eng, engine.NewTranslator(), "qrdb")

	ctx, err := newConnectionSessionHandler()(context.Background())
	require.NoError(t, err)
	run := func(q string) error {
		_, err := h.HandleParse(ctx, wire.Query{Query: q})
		return err
	}

	require.NoError(t, run("CREATE TABLE notes (id BIGINT PRIMARY KEY, body VARCHAR(64))"))
	require.NoError(t, run("SET qrdb_isolation TO 'written+reads'"))
	require.NoError(t, run("BEGIN"))
	err = run("SELECT * FROM notes")
	var rangeRead *provider.IsolationRangeReadError
	require.ErrorAs(t, err, &rangeRead, "written+reads refuses a range read; the SET must have reached the session")
	require.NoError(t, run("ROLLBACK"))
}
