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

// Per-(tenant, database) resolution of the COLLECTION half of a SQL database
// (go-qrdb#441).
//
// The relational half of a database has resolved through the registry since
// relationalTable was written. The collection half was served from a
// process-wide servicer built over the raw, un-namespaced engines, so two
// tenants connected to the same database name over the MySQL or PostgreSQL
// wire protocol read and wrote each other's documents -- a leak reachable
// through exactly the same connection that was already tenant-scoped for
// CREATE TABLE.

package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestDatabase_CollectionServiceResolvesPerSessionTenant pins that the
// collection servicer is resolved for the session's tenant and this database's
// name, exactly as relationalTable resolves the table catalog.
func TestDatabase_CollectionServiceResolvesPerSessionTenant(t *testing.T) {
	scoped := &mockCollectionService{}
	reg := &fakeRelationalRegistry{collectionSvc: scoped}
	singleton := &mockCollectionService{}

	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg, collectionSvc: singleton}

	got, err := db.collectionService(sessionContext("acme"))
	require.NoError(t, err)
	assert.Same(t, scoped, got, "the registry-resolved servicer must win over the process-wide one")

	require.Len(t, reg.calls, 1)
	assert.Equal(t, "collection", reg.calls[0].service)
	assert.Equal(t, "acme", reg.calls[0].tenant)
	assert.Equal(t, "shop", reg.calls[0].db)
}

// TestDatabase_CollectionServiceFallsBackWithoutARegistry covers the legacy,
// single-tenant provider composition, where the process-wide servicer is the
// correct answer.
func TestDatabase_CollectionServiceFallsBackWithoutARegistry(t *testing.T) {
	singleton := &mockCollectionService{}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, collectionSvc: singleton}

	got, err := db.collectionService(sessionContext("acme"))
	require.NoError(t, err)
	assert.Same(t, singleton, got)
}

// TestDatabase_CollectionServiceRefusedResolutionIsReported pins the
// fail-closed half. A registry that refuses the (tenant, database) pair --
// unknown tenant, unknown database, sealed barrier -- must surface that, not
// fall back to the un-namespaced servicer the caller was refused, and not
// swallow it into "there is no such table".
//
// The swallowing shape is what an earlier draft did and it is the fail-OPEN
// answer: a keyspace that was never resolved reporting that it holds nothing.
func TestDatabase_CollectionServiceRefusedResolutionIsReported(t *testing.T) {
	singleton := &mockCollectionService{}
	refusal := &service.DatabaseNotFoundError{TenantID: "acme", Database: "shop"}
	reg := &fakeRelationalRegistry{collectionErr: refusal}

	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg, collectionSvc: singleton}

	got, err := db.collectionService(sessionContext("acme"))
	assert.Nil(t, got)
	require.ErrorIs(t, err, error(refusal))
}

// TestDatabase_GetTableInsensitiveReportsARefusedCollectionResolution is the
// same refusal through the caller that matters: a table lookup must fail
// rather than answer "no such table" for a keyspace it was refused.
func TestDatabase_GetTableInsensitiveReportsARefusedCollectionResolution(t *testing.T) {
	refusal := &service.DatabaseNotFoundError{TenantID: "acme", Database: "shop"}
	reg := &fakeRelationalRegistry{
		collectionErr: refusal,
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{}, nil // GetTable defaults to TableNotFoundError.
		},
		kvSvc: &mockKVService{},
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}

	tbl, found, err := db.GetTableInsensitive(sessionContext("acme"), "orders")
	require.ErrorIs(t, err, error(refusal))
	assert.False(t, found)
	assert.Nil(t, tbl)
}

// TestDatabase_GetTableNamesReportsARefusedCollectionResolution covers the
// listing side: a silently incomplete SHOW TABLES is the same fail-open.
func TestDatabase_GetTableNamesReportsARefusedCollectionResolution(t *testing.T) {
	refusal := &service.DatabaseNotFoundError{TenantID: "acme", Database: "shop"}
	reg := &fakeRelationalRegistry{
		collectionErr: refusal,
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{}, nil
		},
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}

	names, err := db.GetTableNames(sessionContext("acme"))
	require.ErrorIs(t, err, error(refusal))
	assert.Nil(t, names)
}

// TestDatabase_GetTableNamesResolvesCollectionsPerTenant is the same property
// through SHOW TABLES, which is the other place a database enumerates its
// collections.
func TestDatabase_GetTableNamesResolvesCollectionsPerTenant(t *testing.T) {
	scoped := &mockCollectionService{
		listColFn: func(_ context.Context, req *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
			assert.Equal(t, "shop", req.Database)
			return &service.ListCollectionsResponse{
				Collections: []service.CollectionInfo{{Name: "orders", Database: "shop"}},
			}, nil
		},
	}
	reg := &fakeRelationalRegistry{
		collectionSvc: scoped,
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{}, nil
		},
	}
	singleton := &mockCollectionService{
		listColFn: func(context.Context, *service.ListCollectionsRequest) (*service.ListCollectionsResponse, error) {
			t.Fatal("the process-wide servicer was consulted")
			return nil, nil
		},
	}

	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg, collectionSvc: singleton}

	names, err := db.GetTableNames(sessionContext("acme"))
	require.NoError(t, err)
	assert.Contains(t, names, "orders")
}
