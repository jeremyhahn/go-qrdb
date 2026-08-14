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
	"errors"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// fakeTableService
// ---------------------------------------------------------------------------

// fakeTableService implements service.TableServicer for Database-wiring
// tests. Only GetTable and ListTables are exercised by RelationalTable
// resolution; the remaining (DDL) methods are unused stubs.
type fakeTableService struct {
	getTableFn        func(ctx context.Context, database, name string) (*service.TableDef, error)
	listTablesFn      func(ctx context.Context, database string) ([]string, error)
	rewriteJournalFn  func(ctx context.Context, database, name string) (*service.TableDDLJournal, *service.TableDef, error)
	completeRewriteFn func(ctx context.Context, database, name string) (*service.TableDef, error)

	referencingForeignKeysFn func(ctx context.Context, database, parent string) ([]service.TableForeignKey, error)
}

var _ service.TableServicer = (*fakeTableService)(nil)

func (f *fakeTableService) CreateTable(context.Context, *service.CreateTableRequest) (*service.TableDef, error) {
	return nil, nil
}
func (f *fakeTableService) DropTable(context.Context, *service.DropTableRequest) error { return nil }
func (f *fakeTableService) RenameTable(context.Context, *service.RenameTableRequest) error {
	return nil
}
func (f *fakeTableService) GetTable(ctx context.Context, database, name string) (*service.TableDef, error) {
	if f.getTableFn != nil {
		return f.getTableFn(ctx, database, name)
	}
	return nil, &service.TableNotFoundError{Database: database, Name: name}
}
func (f *fakeTableService) ListTables(ctx context.Context, database string) ([]string, error) {
	if f.listTablesFn != nil {
		return f.listTablesFn(ctx, database)
	}
	return nil, nil
}
func (f *fakeTableService) AlterTable(context.Context, *service.AlterTableRequest) (*service.TableDef, error) {
	return nil, nil
}
func (f *fakeTableService) CreateIndex(context.Context, *service.CreateTableIndexRequest) error {
	return nil
}
func (f *fakeTableService) DropIndex(context.Context, string, string, string) error { return nil }
func (f *fakeTableService) AddForeignKey(context.Context, *service.CreateTableForeignKeyRequest) error {
	return nil
}
func (f *fakeTableService) DropForeignKey(context.Context, string, string, string) error { return nil }
func (f *fakeTableService) UpdateForeignKey(context.Context, *service.UpdateTableForeignKeyRequest) error {
	return nil
}
func (f *fakeTableService) ReferencingForeignKeys(ctx context.Context, database, parent string) ([]service.TableForeignKey, error) {
	if f.referencingForeignKeysFn != nil {
		return f.referencingForeignKeysFn(ctx, database, parent)
	}
	return nil, nil
}
func (f *fakeTableService) RewriteJournal(ctx context.Context, database, name string) (*service.TableDDLJournal, *service.TableDef, error) {
	if f.rewriteJournalFn != nil {
		return f.rewriteJournalFn(ctx, database, name)
	}
	return nil, nil, &service.TableNoRewriteInProgressError{Database: database, Name: name}
}
func (f *fakeTableService) CompleteRewrite(ctx context.Context, database, name string) (*service.TableDef, error) {
	if f.completeRewriteFn != nil {
		return f.completeRewriteFn(ctx, database, name)
	}
	return nil, &service.TableNoRewriteInProgressError{Database: database, Name: name}
}

// ---------------------------------------------------------------------------
// fakeRelationalRegistry
// ---------------------------------------------------------------------------

// relationalScopeCall records one (service, tenant, database) resolution
// request made against fakeRelationalRegistry.
type relationalScopeCall struct {
	service string
	tenant  string
	db      string
}

// fakeRelationalRegistry implements service.Registry, returning
// preconfigured per-tenant TableServicer/KVServicer/MVCCKVServicer results
// and recording every (service, tenant, database) resolution it was asked
// to perform. Every method not exercised by RelationalTable resolution
// returns (nil, nil).
type fakeRelationalRegistry struct {
	tableSvcFn func(tenantID, db string) (service.TableServicer, error)
	kvSvc      service.KVServicer
	kvErr      error
	mvccSvc    service.MVCCKVServicer
	mvccErr    error
	batchSvc   service.BatchServicer
	batchErr   error
	txnSvc     service.TransactionServicer
	seqSvc     service.SequenceServicer

	calls []relationalScopeCall
}

var _ service.Registry = (*fakeRelationalRegistry)(nil)

func (r *fakeRelationalRegistry) record(svc, tenantID, db string) {
	r.calls = append(r.calls, relationalScopeCall{service: svc, tenant: tenantID, db: db})
}

func (r *fakeRelationalRegistry) KVService(tenantID, db string) (service.KVServicer, error) {
	r.record("kv", tenantID, db)
	if r.kvErr != nil {
		return nil, r.kvErr
	}
	return r.kvSvc, nil
}

func (r *fakeRelationalRegistry) MVCCService(tenantID, db string) (service.MVCCKVServicer, error) {
	r.record("mvcc", tenantID, db)
	if r.mvccErr != nil {
		return nil, r.mvccErr
	}
	return r.mvccSvc, nil
}

func (r *fakeRelationalRegistry) WatchService(string, string) (service.WatchServicer, error) {
	return nil, nil
}
func (r *fakeRelationalRegistry) LeaseService(string, string) (service.LeaseServicer, error) {
	return nil, nil
}
func (r *fakeRelationalRegistry) IndexService(string, string) (service.IndexServicer, error) {
	return nil, nil
}
func (r *fakeRelationalRegistry) TimeSeriesService(string, string) (service.TimeSeriesServicer, error) {
	return nil, nil
}
func (r *fakeRelationalRegistry) VectorService(string, string) (service.VectorServicer, error) {
	return nil, nil
}
func (r *fakeRelationalRegistry) TransactionService(string, string) (service.TransactionServicer, error) {
	return r.txnSvc, nil
}
func (r *fakeRelationalRegistry) BatchService(tenantID, db string) (service.BatchServicer, error) {
	r.record("batch", tenantID, db)
	if r.batchErr != nil {
		return nil, r.batchErr
	}
	return r.batchSvc, nil
}

func (r *fakeRelationalRegistry) TableService(tenantID, db string) (service.TableServicer, error) {
	r.record("table", tenantID, db)
	if r.tableSvcFn != nil {
		return r.tableSvcFn(tenantID, db)
	}
	return &fakeTableService{}, nil
}

func (r *fakeRelationalRegistry) SequenceService(string, string) (service.SequenceServicer, error) {
	return r.seqSvc, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// sessionContext builds a *sqle.Context carrying a QRDBSession for the given
// tenant, mirroring how a MySQL/PostgreSQL wire-protocol connection binds
// its authenticated identity.
func sessionContext(tenantID string) *sqle.Context {
	base := sqle.NewBaseSession()
	sess := NewSession(base, &auth.Identity{Subject: "user", TenantID: tenantID})
	return sqle.NewContext(context.Background(), sqle.WithSession(sess))
}

// ---------------------------------------------------------------------------
// Resolution precedence / fallthrough
// ---------------------------------------------------------------------------

func TestDatabase_GetTableInsensitive_RelationalShadowsCollection(t *testing.T) {
	def := testUsersDef()
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{
				getTableFn: func(_ context.Context, _, name string) (*service.TableDef, error) {
					require.Equal(t, "users", name)
					return def, nil
				},
			}, nil
		},
		kvSvc: &mockKVService{},
	}
	collectionCalled := false
	collSvc := &mockCollectionService{
		getInfoFn: func(context.Context, string, string) (*service.CollectionInfo, error) {
			collectionCalled = true
			return &service.CollectionInfo{Name: "users"}, nil
		},
	}

	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg, collectionSvc: collSvc}

	tbl, found, err := db.GetTableInsensitive(newTestContext(), "users")
	require.NoError(t, err)
	require.True(t, found)

	_, isRelational := tbl.(*RelationalTable)
	assert.True(t, isRelational, "a relational table must shadow a same-named collection")
	assert.False(t, collectionCalled, "the collection service must not be consulted once the relational catalog resolves the name")
}

func TestDatabase_GetTableInsensitive_TableNotFoundFallsThroughToCollection(t *testing.T) {
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{}, nil // GetTable defaults to TableNotFoundError.
		},
		kvSvc: &mockKVService{},
	}
	collSvc := &mockCollectionService{
		getInfoFn: func(context.Context, string, string) (*service.CollectionInfo, error) {
			return &service.CollectionInfo{Name: "legacy_docs", Schema: map[string]string{"title": "VARCHAR"}}, nil
		},
	}

	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg, collectionSvc: collSvc}

	tbl, found, err := db.GetTableInsensitive(newTestContext(), "legacy_docs")
	require.NoError(t, err)
	require.True(t, found)

	_, isCollection := tbl.(*CollectionTable)
	assert.True(t, isCollection, "a missing relational table must fall through to the collection lookup")
}

func TestDatabase_GetTableInsensitive_RelationalNotFoundAndNoCollection(t *testing.T) {
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{}, nil
		},
		kvSvc: &mockKVService{},
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}

	tbl, found, err := db.GetTableInsensitive(newTestContext(), "nonexistent")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, tbl)
}

func TestDatabase_GetTableInsensitive_RewriteInProgressPropagates(t *testing.T) {
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{
				getTableFn: func(context.Context, string, string) (*service.TableDef, error) {
					return nil, &service.TableRewriteInProgressError{Database: "shop", Name: "users"}
				},
			}, nil
		},
		kvSvc: &mockKVService{},
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}

	_, found, err := db.GetTableInsensitive(newTestContext(), "users")
	assert.False(t, found)
	var rewriteErr *service.TableRewriteInProgressError
	assert.ErrorAs(t, err, &rewriteErr, "a genuine in-progress rewrite must not silently fall through to collections")
}

func TestDatabase_GetTableInsensitive_TableServiceError(t *testing.T) {
	tableErr := errors.New("registry unavailable")
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return nil, tableErr
		},
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}

	_, found, err := db.GetTableInsensitive(newTestContext(), "users")
	assert.False(t, found)
	assert.ErrorIs(t, err, tableErr)
}

func TestDatabase_GetTableInsensitive_KVServiceError(t *testing.T) {
	kvErr := errors.New("kv unavailable")
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{
				getTableFn: func(context.Context, string, string) (*service.TableDef, error) {
					return testUsersDef(), nil
				},
			}, nil
		},
		kvErr: kvErr,
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}

	_, found, err := db.GetTableInsensitive(newTestContext(), "users")
	assert.False(t, found)
	assert.ErrorIs(t, err, kvErr)
}

func TestDatabase_GetTableInsensitive_MVCCServiceError(t *testing.T) {
	mvccErr := errors.New("mvcc unavailable")
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{
				getTableFn: func(context.Context, string, string) (*service.TableDef, error) {
					return testUsersDef(), nil
				},
			}, nil
		},
		kvSvc:   &mockKVService{},
		mvccErr: mvccErr,
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}

	_, found, err := db.GetTableInsensitive(newTestContext(), "users")
	assert.False(t, found)
	assert.ErrorIs(t, err, mvccErr)
}

func TestDatabase_GetTableInsensitive_RelationalConstructionError(t *testing.T) {
	badDef := &service.TableDef{Name: "broken", Database: "shop", TableID: 1} // no columns
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{
				getTableFn: func(context.Context, string, string) (*service.TableDef, error) {
					return badDef, nil
				},
			}, nil
		},
		kvSvc: &mockKVService{},
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}

	_, found, err := db.GetTableInsensitive(newTestContext(), "broken")
	assert.False(t, found)
	assert.Error(t, err)
}

func TestDatabase_GetTableInsensitive_NoRegistry_SkipsRelationalStep(t *testing.T) {
	db := &Database{name: "shop", kvSvc: &mockKVService{}}
	tbl, found, err := db.GetTableInsensitive(newTestContext(), "kv")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "kv", tbl.Name())
}

// ---------------------------------------------------------------------------
// Tenant scoping
// ---------------------------------------------------------------------------

func TestDatabase_GetTableInsensitive_TenantScoping(t *testing.T) {
	acmeDef := &service.TableDef{
		TableID: 1, Name: "users", Database: "shop",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	}
	corpDef := &service.TableDef{
		TableID: 2, Name: "users", Database: "shop",
		Columns:    []service.ColumnDef{{Name: "id", Type: "BIGINT UNSIGNED"}},
		PrimaryKey: []service.PKColumn{{Name: "id"}},
	}

	reg := &fakeRelationalRegistry{
		tableSvcFn: func(tenantID, _ string) (service.TableServicer, error) {
			return &fakeTableService{
				getTableFn: func(_ context.Context, database, name string) (*service.TableDef, error) {
					switch tenantID {
					case "acme":
						return acmeDef, nil
					case "corp":
						return corpDef, nil
					default:
						return nil, &service.TableNotFoundError{Database: database, Name: name}
					}
				},
			}, nil
		},
		kvSvc: &mockKVService{},
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}

	acmeTbl, found, err := db.GetTableInsensitive(sessionContext("acme"), "users")
	require.NoError(t, err)
	require.True(t, found)

	corpTbl, found, err := db.GetTableInsensitive(sessionContext("corp"), "users")
	require.NoError(t, err)
	require.True(t, found)

	acmeRel := acmeTbl.(*RelationalTable)
	corpRel := corpTbl.(*RelationalTable)
	assert.Equal(t, uint64(1), acmeRel.def.TableID)
	assert.Equal(t, uint64(2), corpRel.def.TableID)

	// Every resolution call carried the session's tenant, never a
	// hardcoded/default one.
	sawAcme, sawCorp := false, false
	for _, c := range reg.calls {
		if c.tenant == "acme" {
			sawAcme = true
		}
		if c.tenant == "corp" {
			sawCorp = true
		}
	}
	assert.True(t, sawAcme)
	assert.True(t, sawCorp)
}

func TestDatabase_GetTableInsensitive_NoSessionUsesSystemTenant(t *testing.T) {
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(tenantID, _ string) (service.TableServicer, error) {
			assert.Equal(t, "system", tenantID)
			return &fakeTableService{}, nil
		},
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}

	_, _, err := db.GetTableInsensitive(newTestContext(), "users")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Read-only wrapping
// ---------------------------------------------------------------------------

func TestDatabase_GetTableInsensitive_RelationalTable_ReadOnlyWrapped(t *testing.T) {
	def := testUsersDef()
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{
				getTableFn: func(context.Context, string, string) (*service.TableDef, error) {
					return def, nil
				},
			}, nil
		},
		kvSvc: &mockKVService{},
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg, readOnly: true}

	tbl, found, err := db.GetTableInsensitive(newTestContext(), "users")
	require.NoError(t, err)
	require.True(t, found)

	_, ok := tbl.(*readOnlyTable)
	assert.True(t, ok, "a read-only database must wrap relational tables exactly like every other table kind")
}

func TestDatabase_GetTableInsensitive_RelationalTable_NotReadOnlyByDefault(t *testing.T) {
	def := testUsersDef()
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{
				getTableFn: func(context.Context, string, string) (*service.TableDef, error) {
					return def, nil
				},
			}, nil
		},
		kvSvc: &mockKVService{},
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}

	tbl, found, err := db.GetTableInsensitive(newTestContext(), "users")
	require.NoError(t, err)
	require.True(t, found)

	_, isRelational := tbl.(*RelationalTable)
	assert.True(t, isRelational)
}

// ---------------------------------------------------------------------------
// GetTableNames
// ---------------------------------------------------------------------------

func TestDatabase_GetTableNames_AppendsRelationalTables(t *testing.T) {
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{
				listTablesFn: func(_ context.Context, database string) ([]string, error) {
					assert.Equal(t, "shop", database)
					return []string{"users", "orders"}, nil
				},
			}, nil
		},
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}

	names, err := db.GetTableNames(newTestContext())
	require.NoError(t, err)
	assert.Contains(t, names, "users")
	assert.Contains(t, names, "orders")
	assert.Contains(t, names, "kv")
}

func TestDatabase_GetTableNames_TableServiceErrorIsIgnored(t *testing.T) {
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return nil, errors.New("unavailable")
		},
	}
	db := &Database{name: "shop", kvSvc: &mockKVService{}, registry: reg}

	names, err := db.GetTableNames(newTestContext())
	require.NoError(t, err)
	assert.Contains(t, names, "kv")
}

// ---------------------------------------------------------------------------
// WithRegistry option
// ---------------------------------------------------------------------------

func TestWithRegistry_PropagatesToDatabase(t *testing.T) {
	reg := &fakeRelationalRegistry{
		tableSvcFn: func(string, string) (service.TableServicer, error) {
			return &fakeTableService{
				getTableFn: func(context.Context, string, string) (*service.TableDef, error) {
					return testUsersDef(), nil
				},
			}, nil
		},
		kvSvc: &mockKVService{},
	}
	p := NewProviderWithEngine("shop", &mockKVService{}, "", WithRegistry(reg))

	sqlDB, err := p.Database(newTestContext(), "shop")
	require.NoError(t, err)

	tbl, found, err := sqlDB.GetTableInsensitive(newTestContext(), "users")
	require.NoError(t, err)
	require.True(t, found)
	_, ok := tbl.(*RelationalTable)
	assert.True(t, ok)
}

func TestWithRegistry_NilByDefault(t *testing.T) {
	p := NewProviderWithEngine("shop", &mockKVService{}, "")
	assert.Nil(t, p.registry)
}
