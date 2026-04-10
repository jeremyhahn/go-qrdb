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

package engine

import (
	"testing"

	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew_ReturnsEngine(t *testing.T) {
	provider := memory.NewDBProvider()
	eng := New(Config{Provider: provider})
	require.NotNil(t, eng)
	defer func() { _ = eng.Close() }()

	assert.NotNil(t, eng.Analyzer)
	assert.NotNil(t, eng.ProcessList)
	assert.NotNil(t, eng.MemoryManager)
}

func TestNew_CanExecuteQuery(t *testing.T) {
	provider := memory.NewDBProvider(
		memory.NewDatabase("testdb"),
	)
	eng := New(Config{Provider: provider})
	require.NotNil(t, eng)
	defer func() { _ = eng.Close() }()

	ctx := sql.NewEmptyContext()
	ctx.SetCurrentDatabase("testdb")

	schema, iter, _, err := eng.Query(ctx, "SELECT 1 AS result")
	require.NoError(t, err)
	require.NotNil(t, iter)
	defer func() { _ = iter.Close(ctx) }()

	assert.Len(t, schema, 1)
	assert.Equal(t, "result", schema[0].Name)

	row, err := iter.Next(ctx)
	require.NoError(t, err)
	assert.Equal(t, int8(1), row[0])
}

func TestNew_InvalidQueryReturnsError(t *testing.T) {
	provider := memory.NewDBProvider(
		memory.NewDatabase("testdb"),
	)
	eng := New(Config{Provider: provider})
	require.NotNil(t, eng)
	defer func() { _ = eng.Close() }()

	ctx := sql.NewEmptyContext()
	ctx.SetCurrentDatabase("testdb")

	_, _, _, err := eng.Query(ctx, "SELECTX INVALID SYNTAX")
	assert.Error(t, err)
}
