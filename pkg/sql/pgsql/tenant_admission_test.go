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
	"sync/atomic"
	"testing"

	wire "github.com/jeroenrinzema/psql-wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/provider"
)

// suspendableAdmitter is a provider.SessionAdmitter over one tenant that is
// active until suspended is set: from then on it refuses every admission the
// way the auth chain's tenant status gate does, with
// *authmw.TenantNotActiveError.
type suspendableAdmitter struct {
	suspended atomic.Bool
}

var _ provider.SessionAdmitter = (*suspendableAdmitter)(nil)

func (a *suspendableAdmitter) AdmitIdentity(context.Context, *auth.Identity) error {
	if a.suspended.Load() {
		return &authmw.TenantNotActiveError{TenantID: "acme", Status: "suspended"}
	}
	return nil
}

// newAdmittedHandler returns a handler and a connection context whose session
// re-admits its identity through the returned admitter, as Server builds it.
func newAdmittedHandler(t *testing.T) (*ParseHandler, context.Context, *suspendableAdmitter) {
	t.Helper()
	hs := sqltest.NewHarness()
	prov := provider.NewProvider("qrdb", hs.KVSvc, provider.WithRegistry(hs.Registry))
	eng := engine.New(engine.Config{Provider: prov, Dialect: engine.DialectPostgreSQL})
	t.Cleanup(func() { _ = eng.Close() })
	adm := &suspendableAdmitter{}
	ctx, err := newConnectionSessionHandler(provider.WithSessionAdmitter(adm))(context.Background())
	require.NoError(t, err)
	return NewParseHandler(eng, engine.NewTranslator(), "qrdb"), ctx, adm
}

func requireTenantNotActive(t *testing.T, err error) {
	t.Helper()
	var notActive *authmw.TenantNotActiveError
	require.ErrorAs(t, err, &notActive)
	assert.Equal(t, "acme", notActive.TenantID)
}

// Sequence DDL is served from the sequence catalog before the statement's
// command opens; a connection whose tenant was suspended is refused before it
// touches the catalog.
func TestHandleParse_SequenceDDLRefusedBeforeCatalogWhenTenantSuspended(t *testing.T) {
	h, ctx, adm := newAdmittedHandler(t)
	nextval := func() (any, error) {
		sqlCtx := h.buildSQLContext(ctx)
		_, iter, _, err := h.sqlEngine.Query(sqlCtx, "SELECT NEXTVAL('orders_seq')")
		if err != nil {
			return nil, err
		}
		defer func() { _ = iter.Close(sqlCtx) }()
		row, err := iter.Next(sqlCtx)
		if err != nil {
			return nil, err
		}
		return row[0], nil
	}

	adm.suspended.Store(true)
	_, err := h.HandleParse(ctx, wire.Query{Query: "CREATE SEQUENCE orders_seq", SimpleQuery: true})
	requireTenantNotActive(t, err)
	adm.suspended.Store(false)
	_, err = h.HandleParse(ctx, wire.Query{Query: "CREATE SEQUENCE orders_seq", SimpleQuery: true})
	require.NoError(t, err, "a refused CREATE SEQUENCE created the sequence")
	got, err := nextval()
	require.NoError(t, err)
	assert.EqualValues(t, 1, got)

	adm.suspended.Store(true)
	_, err = h.HandleParse(ctx, wire.Query{Query: "DROP SEQUENCE orders_seq", SimpleQuery: true})
	requireTenantNotActive(t, err)
	adm.suspended.Store(false)
	got, err = nextval()
	require.NoError(t, err, "a refused DROP SEQUENCE dropped the sequence")
	assert.EqualValues(t, 2, got, "a refused DROP SEQUENCE reset the sequence")
}

// DROP TABLE ... CASCADE drops the foreign keys aimed at the table before the
// statement's command opens; a connection whose tenant was suspended is
// refused before any of them is dropped.
func TestHandleParse_DropTableCascadeRefusedBeforeSideEffectWhenTenantSuspended(t *testing.T) {
	h, ctx, adm := newAdmittedHandler(t)
	for _, q := range []string{
		"CREATE TABLE people (id BIGINT PRIMARY KEY)",
		"CREATE TABLE pets (id BIGINT PRIMARY KEY, owner_id BIGINT, " +
			"CONSTRAINT fk_pets_owner FOREIGN KEY (owner_id) REFERENCES people (id))",
	} {
		_, err := h.HandleParse(ctx, wire.Query{Query: q, SimpleQuery: true})
		require.NoError(t, err, q)
	}
	catalog := h.sqlEngine.Analyzer.Catalog
	require.Len(t, declaredForeignKeys(t, h.buildSQLContext(ctx), catalog, "qrdb", "pets"), 1)

	adm.suspended.Store(true)
	_, err := h.HandleParse(ctx, wire.Query{Query: "DROP TABLE people CASCADE", SimpleQuery: true})
	requireTenantNotActive(t, err)

	adm.suspended.Store(false)
	sqlCtx := h.buildSQLContext(ctx)
	_, ok := lookupTable(sqlCtx, catalog, "qrdb", "people")
	assert.True(t, ok, "a refused DROP TABLE dropped the table")
	assert.Len(t, declaredForeignKeys(t, sqlCtx, catalog, "qrdb", "pets"), 1,
		"a refused DROP TABLE ... CASCADE dropped the foreign key aimed at the table")
}

// A connection with no session admitter -- HandleParse driven without the
// server's session middleware -- admits every statement.
func TestAdmitConnection_NoSessionAdmits(t *testing.T) {
	require.NoError(t, admitConnection(context.Background()))
	ctx, err := newConnectionSessionHandler()(context.Background())
	require.NoError(t, err)
	require.NoError(t, admitConnection(ctx))
}
