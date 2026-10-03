//go:build qrdb_sql

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

package pgcatalog

import (
	"regexp"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uuidV4Re matches the canonical text form of a version-4 UUID.
var uuidV4Re = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// evalUUIDFunction builds fn's expression with no arguments and evaluates
// it once.
func evalUUIDFunction(t *testing.T, fn sqle.Function) string {
	t.Helper()
	f0, ok := fn.(sqle.Function0)
	require.True(t, ok)
	ctx := sqle.NewEmptyContext()
	value, err := f0.Fn(ctx).Eval(ctx, nil)
	require.NoError(t, err)
	s, ok := value.(string)
	require.True(t, ok, "uuid functions return text")
	return s
}

// TestUUIDFunctions pins both PostgreSQL random-uuid spellings to emit
// well-formed version-4 UUIDs, fresh per call.
func TestUUIDFunctions(t *testing.T) {
	for _, fn := range []sqle.Function{NewGenRandomUUID(), NewUUIDGenerateV4()} {
		first := evalUUIDFunction(t, fn)
		second := evalUUIDFunction(t, fn)
		assert.Regexp(t, uuidV4Re, first)
		assert.Regexp(t, uuidV4Re, second)
		assert.NotEqual(t, first, second, "each call must generate a fresh uuid")
	}
}

// TestUUIDFunctionNames pins the registered names, which is what the
// engine's per-dialect registration keys on.
func TestUUIDFunctionNames(t *testing.T) {
	assert.Equal(t, "gen_random_uuid", NewGenRandomUUID().FunctionName())
	assert.Equal(t, "uuid_generate_v4", NewUUIDGenerateV4().FunctionName())
}
