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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/entity"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// With a registry the kv table is served from the servicer the registry
// resolves for (session tenant, database), with no second prefix, and a
// refused resolution -- a sealed database -- fails the lookup.
func TestDatabase_KVTable_ResolvedThroughRegistry(t *testing.T) {
	resolved := &mockKVService{}
	reg := &fakeRelationalRegistry{kvSvc: resolved}
	p := NewProvider("shop", &mockKVService{}, WithRegistry(reg))
	db, err := p.Database(sessionContext("acme"), "shop")
	require.NoError(t, err)

	tbl, found, err := db.(*Database).GetTableInsensitive(sessionContext("acme"), tableName)
	require.NoError(t, err)
	require.True(t, found)
	kvTbl, ok := tbl.(*KVTable)
	require.True(t, ok)
	require.Same(t, resolved, kvTbl.kvSvc)
	require.Empty(t, kvTbl.prefix)
	require.Equal(t, []relationalScopeCall{{service: "kv", tenant: "acme", db: "shop"}}, reg.calls)

	reg.kvErr = &service.BarrierSealedError{TenantID: "acme", Database: "shop"}
	_, _, err = db.(*Database).GetTableInsensitive(sessionContext("acme"), tableName)
	var sealed *service.BarrierSealedError
	require.True(t, errors.As(err, &sealed), "err = %v, want the registry's refusal", err)
}

// Entity tables resolve the same way.
func TestDatabase_EntityTable_ResolutionRefused(t *testing.T) {
	const entityType = "kvresolutionwidget"
	require.NoError(t, entity.Global().Register(providerEntityRegistration(entityType)))
	t.Cleanup(func() { entity.Global().Unregister(entityType) })

	reg := &fakeRelationalRegistry{kvErr: &service.BarrierSealedError{Database: "shop"}}
	p := NewProvider("shop", &mockKVService{}, WithRegistry(reg))
	db, err := p.Database(sessionContext("acme"), "shop")
	require.NoError(t, err)
	_, _, err = db.(*Database).GetTableInsensitive(sessionContext("acme"), entityType)
	var sealed *service.BarrierSealedError
	require.True(t, errors.As(err, &sealed), "err = %v, want the registry's refusal", err)
}
