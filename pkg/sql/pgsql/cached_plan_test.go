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
	"errors"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	sqltypes "github.com/dolthub/go-mysql-server/sql/types"
	wire "github.com/jeroenrinzema/psql-wire"
	"github.com/jeroenrinzema/psql-wire/codes"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// TestEnsureStableResultType covers the condition PostgreSQL calls "cached
// plan must not change result type": a client PARSEs once, keeps the
// RowDescription it was given, and BINDs/EXECUTEs many times against it. If
// DDL changes the result shape in between, the rows no longer match the
// descriptor the client is still decoding with.
//
// Verified against PostgreSQL 16: the same sequence (warm a statement, drop
// and recreate the table wider, re-execute the identical SQL) yields
// "cached plan must not change result type (SQLSTATE 0A000)" there and
// succeeds on the client's retry, because pgx drops the statement from its
// cache on any error. Matching both the message and the SQLSTATE is what
// makes that recovery work here too.
func TestEnsureStableResultType(t *testing.T) {
	ctx := sql.NewContext(context.Background())

	column := func(name string, typ sql.Type) *sql.Column {
		return &sql.Column{Name: name, Type: typ}
	}
	declaredFor := func(schema sql.Schema) wire.Columns {
		return schemaToColumns(ctx, schema)
	}

	base := sql.Schema{column("id", sqltypes.Int64), column("a", sqltypes.LongText)}

	tests := []struct {
		name     string
		declared wire.Columns
		schema   sql.Schema
		wantErr  bool
	}{
		{
			name:     "identical shape is accepted",
			declared: declaredFor(base),
			schema:   sql.Schema{column("id", sqltypes.Int64), column("a", sqltypes.LongText)},
		},
		{
			name:     "a widened table is refused",
			declared: declaredFor(base),
			schema:   sql.Schema{column("id", sqltypes.Int64), column("a", sqltypes.LongText), column("b", sqltypes.LongText)},
			wantErr:  true,
		},
		{
			name:     "a narrowed table is refused",
			declared: declaredFor(base),
			schema:   sql.Schema{column("id", sqltypes.Int64)},
			wantErr:  true,
		},
		{
			// The dangerous case: the width still agrees, so nothing
			// downstream would notice, and a client scanning by name would
			// land each value in the wrong column.
			name:     "a renamed column at the same width is refused",
			declared: declaredFor(base),
			schema:   sql.Schema{column("id", sqltypes.Int64), column("renamed", sqltypes.LongText)},
			wantErr:  true,
		},
		{
			// Equally silent: same width, same names, different types. The
			// client decodes each value with the codec for the OID it was
			// described with.
			name:     "a retyped column at the same width is refused",
			declared: declaredFor(base),
			schema:   sql.Schema{column("id", sqltypes.Int64), column("a", sqltypes.Int64)},
			wantErr:  true,
		},
		{
			// Every DML statement without RETURNING: its one-cell OkResult
			// row is not a result set and was never described as one.
			name:     "a statement that described no rows is exempt",
			declared: nil,
			schema:   sql.Schema{column("__ok_result__", sqltypes.Int64)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ensureStableResultType(ctx, tt.declared, tt.schema)

			if !tt.wantErr {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)

			var cachedPlanErr *CachedPlanResultTypeError
			require.ErrorAs(t, err, &cachedPlanErr)

			assert.Equal(t, "cached plan must not change result type", cachedPlanErr.Error(),
				"the message is PostgreSQL's own and clients match on it")
			assert.Equal(t, codes.FeatureNotSupported, psqlerr.GetCode(err),
				"SQLSTATE must be 0A000, the code PostgreSQL raises for this condition")
			assert.Equal(t, len(tt.declared), cachedPlanErr.Declared)
			assert.Equal(t, len(tt.schema), cachedPlanErr.Actual)
		})
	}
}

// TestExecuteDeferredCallbackRefusesAStaleDescription drives the check
// through the callback a cached statement actually re-enters, rather than
// calling the comparison directly: a statement described with one shape and
// executed against another must be refused before a single row reaches the
// client.
func TestExecuteDeferredCallbackRefusesAStaleDescription(t *testing.T) {
	h := newCachedPlanHandler()
	sqlCtx := h.buildSQLContext(context.Background())

	const query = "SELECT * FROM kv"

	declared := h.resultColumns(sqlCtx, query, nil)
	require.Len(t, declared, 2, "the kv table describes key and value")

	// Describe the statement as producing a single column, then execute the
	// two-column query against that description -- the shape a cached
	// statement is left in when DDL changes its table underneath it.
	stale := declared[:1]

	writer := &mockDataWriter{}
	err := h.executeDeferredCallback(context.Background(), writer, nil, query, query, nil, nil, stale)

	require.Error(t, err)
	var cachedPlanErr *CachedPlanResultTypeError
	require.True(t, errors.As(err, &cachedPlanErr))
	assert.Equal(t, codes.FeatureNotSupported, psqlerr.GetCode(err))
	assert.Zero(t, writer.rowCount, "no row may reach a client decoding against a stale descriptor")
}

// TestExecuteDeferredCallbackAcceptsItsOwnDescription is the companion
// guarantee, and the one that would break every ordinary query if the
// comparison were too strict: a statement executing against the description
// it was just given must always be accepted.
func TestExecuteDeferredCallbackAcceptsItsOwnDescription(t *testing.T) {
	h := newCachedPlanHandler()
	sqlCtx := h.buildSQLContext(context.Background())

	queries := []string{
		"SELECT * FROM kv",
		"SELECT `key` FROM kv",
		"SELECT `key`, `value` FROM kv",
	}

	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			declared := h.resultColumns(sqlCtx, query, nil)
			require.NotEmpty(t, declared)

			writer := &mockDataWriter{}
			err := h.executeDeferredCallback(context.Background(), writer, nil, query, query, nil, nil, declared)
			require.NoError(t, err)
		})
	}
}

// newCachedPlanHandler builds a ParseHandler over the mock KV service the
// rest of this package's handler tests use, so executeDeferredCallback's own
// buildSQLContext resolves the same provider it then executes against.
func newCachedPlanHandler() *ParseHandler {
	kvSvc := &mockKVService{scanData: map[string][]byte{"cached-key": []byte("cached-val")}}
	dbProvider := provider.NewProvider("qrdb", kvSvc)
	sqlEngine := engine.New(engine.Config{Provider: dbProvider})
	return NewParseHandler(sqlEngine, engine.NewTranslator(), "qrdb")
}
