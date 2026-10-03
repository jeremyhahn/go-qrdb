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
	"context"
	"testing"
	"time"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPgSleep waits its argument and ends early when its context is
// cancelled.
func TestPgSleep(t *testing.T) {
	fn, ok := NewPgSleep().(sqle.Function1)
	require.True(t, ok)
	assert.Equal(t, "pg_sleep", NewPgSleep().FunctionName())

	ctx := sqle.NewEmptyContext()
	start := time.Now()
	_, err := fn.Fn(ctx, expression.NewLiteral(0.05, types.Float64)).Eval(ctx, nil)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 50*time.Millisecond)

	cctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	sctx := sqle.NewContext(cctx)
	start = time.Now()
	_, err = fn.Fn(sctx, expression.NewLiteral(5.0, types.Float64)).Eval(sctx, nil)
	require.Error(t, err)
	assert.Less(t, time.Since(start), 2*time.Second, "pg_sleep must end with its context")
}
