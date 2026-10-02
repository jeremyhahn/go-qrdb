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
	"github.com/stretchr/testify/require"
)

// autocommitStatement returns a context on a fresh QRDB session with a
// transaction started, as go-mysql-server starts one before every
// statement, and the call that ends the statement the way go-mysql-server's
// autocommit does: it commits the statement's implicit transaction and
// starts the next, so a test driving table writers directly sees their
// writes in the store only once the statement ends -- as a client does.
func autocommitStatement(t *testing.T) (*sqle.Context, func() error) {
	t.Helper()
	sess := NewSession(sqle.NewBaseSession(), nil)
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(sess))
	_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
	require.NoError(t, err)
	end := func() error {
		if err := sess.CommitTransaction(ctx, nil); err != nil {
			return err
		}
		_, err := sess.StartTransaction(ctx, sqle.ReadWrite)
		return err
	}
	return ctx, end
}

// writeStatement runs write as one autocommit statement and ends it,
// returning the first error: the write's own, or its commit's.
func writeStatement(t *testing.T, write func(ctx *sqle.Context) error) error {
	t.Helper()
	ctx, end := autocommitStatement(t)
	if err := write(ctx); err != nil {
		return err
	}
	return end()
}
