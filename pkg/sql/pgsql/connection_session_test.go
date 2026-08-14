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

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

func TestNewConnectionSessionHandler_StoresSessionInContext(t *testing.T) {
	handler := newConnectionSessionHandler()
	ctx, err := handler(context.Background())
	require.NoError(t, err)

	sess := connectionSessionFromContext(ctx)
	require.NotNil(t, sess)
}

func TestNewConnectionSessionHandler_CarriesIdentity(t *testing.T) {
	identity := &auth.Identity{Subject: "alice", TenantID: "acme"}
	ctx := context.WithValue(context.Background(), identityContextKey{}, identity)

	handler := newConnectionSessionHandler()
	ctx, err := handler(ctx)
	require.NoError(t, err)

	sess := connectionSessionFromContext(ctx)
	require.NotNil(t, sess)
	assert.Equal(t, identity, sess.Identity())
	assert.Equal(t, "acme", sess.TenantID())
}

func TestConnectionSessionFromContext_MissingReturnsNil(t *testing.T) {
	assert.Nil(t, connectionSessionFromContext(context.Background()))
}

// TestBuildSQLContext_ReusesConnectionSessionAcrossStatements is the
// regression test for the bug this package's interactive-transaction
// support fixes: buildSQLContext used to construct a brand-new
// *provider.QRDBSession on every call (one per statement), which silently
// discarded any BEGIN/COMMIT/ROLLBACK state between statements even after
// QRDBSession itself gained a sql.TransactionSession implementation. Every
// call sharing the same connection-derived context (as psql-wire's
// SessionMiddleware chain guarantees -- see newConnectionSessionHandler's
// doc comment) must now observe the identical session instance.
func TestBuildSQLContext_ReusesConnectionSessionAcrossStatements(t *testing.T) {
	h := newTestParseHandler(t)

	connCtx, err := newConnectionSessionHandler()(context.Background())
	require.NoError(t, err)

	first := h.buildSQLContext(connCtx)
	second := h.buildSQLContext(connCtx)

	firstSess, ok := first.Session.(interface{ ID() uint32 })
	require.True(t, ok)
	secondSess, ok := second.Session.(interface{ ID() uint32 })
	require.True(t, ok)
	assert.Equal(t, firstSess.ID(), secondSess.ID(), "every statement on one connection must share the same session")
	assert.Same(t, first.Session, second.Session)
}

func TestBuildSQLContext_FallsBackWithoutConnectionSession(t *testing.T) {
	h := newTestParseHandler(t)

	// No newConnectionSessionHandler in the chain (e.g. HandleParse called
	// directly, as this package's other tests do): buildSQLContext must
	// still produce a usable, if throwaway, session rather than panicking.
	sqlCtx := h.buildSQLContext(context.Background())
	require.NotNil(t, sqlCtx.Session)
}
