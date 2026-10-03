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

package query

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	sqle "github.com/dolthub/go-mysql-server"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

// mockKV is an in-memory service.KVServicer. Methods the SQL engine does
// not exercise are inherited from the embedded nil interface and panic if
// called, which keeps the mock honest about what the engine touches.
type mockKV struct {
	service.KVServicer
	store map[string][]byte
}

func newMockKV() *mockKV {
	return &mockKV{store: make(map[string][]byte)}
}

func (m *mockKV) Put(_ context.Context, key string, value []byte) error {
	m.store[key] = value
	return nil
}

func (m *mockKV) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *mockKV) Delete(_ context.Context, key string) error {
	delete(m.store, key)
	return nil
}

func (m *mockKV) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *mockKV) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.store {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}

func (m *mockKV) GetStale(ctx context.Context, key string) ([]byte, error) {
	return m.Get(ctx, key)
}

func (m *mockKV) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}

func (m *mockKV) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}

// mockDB implements the Get method of service.DatabaseServicer; all other
// methods are inherited from the embedded nil interface.
type mockDB struct {
	service.DatabaseServicer
	getFn func(name string) (*database.DatabaseInfo, error)
}

// Get reads the catalog record by name, as database.Manager.Get does: the
// service uses it only for the shared default SQL database.
func (m *mockDB) Get(name string) (*database.DatabaseInfo, error) {
	return m.getFn(name)
}

// GetForTenant is the owner-checked read, held to the same rule as the
// real DatabaseService.GetForTenant: a database another tenant owns is
// answered as missing (database.OwnedBy).
func (m *mockDB) GetForTenant(ctx context.Context, name string) (*database.DatabaseInfo, error) {
	info, err := m.getFn(name)
	if err != nil {
		return nil, err
	}
	if !database.OwnedBy(info.TenantID, tenant.FromContext(ctx)) {
		return nil, &database.NotFoundError{Name: name}
	}
	return info, nil
}

// ownedKV returns a catalog in which every name is a KV database owned by
// owner -- the fixture for a tenant querying its own databases.
func ownedKV(owner string) func(string) (*database.DatabaseInfo, error) {
	return func(name string) (*database.DatabaseInfo, error) {
		info, err := kvDB(name)
		if err != nil {
			return nil, err
		}
		info.TenantID = owner
		return info, nil
	}
}

func kvDB(name string) (*database.DatabaseInfo, error) {
	return &database.DatabaseInfo{
		Name:    name,
		Engine:  database.EngineKV,
		Backend: database.BackendMemory,
		Status:  database.StatusOpen,
	}, nil
}

// seed stores a key-value pair under the composite (system tenant, dbName)
// namespace, matching the prefix the SQL KV table scans with (see
// tenant.ComposeNamespace).
func seed(t *testing.T, kv *mockKV, dbName, key, value string) {
	t.Helper()
	prefixed := tenant.ComposeNamespace(tenantid.System, dbName) + key
	if err := kv.Put(context.Background(), prefixed, []byte(value)); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// authedContext returns a context carrying both the auth identity and the
// canonical tenant binding for tenantID, mirroring exactly what every
// transport's auth middleware installs on ctx before a data-plane handler
// runs (see pkg/auth/middleware/middleware.go authenticate): auth.WithIdentity
// followed by tenant.WithTenantID derived from the same identity.
func authedContext(tenantID string) context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "alice",
		TenantID: tenantID,
	})
	return tenant.WithTenantID(ctx, tenantID)
}

// mockRegistry implements service.Registry for tenant-scoping tests. Only
// KVService is exercised by the ad-hoc SQL query path; every other method
// is inherited from the embedded nil interface and panics if called, which
// keeps the mock honest about what the engine touches.
type mockRegistry struct {
	service.Registry
	kvServiceFn func(tenantID, db string) (service.KVServicer, error)

	// privilegeServiceFn resolves the SQL privilege catalog. A non-system
	// tenant's every statement resolves it (pkg/sql/privilege), so it must
	// be answered even by a test that is not about privileges; the harness
	// serves an empty, unlatched catalog by default, which is the
	// unrestricted posture.
	privilegeServiceFn func(tenantID, db string) (service.PrivilegeServicer, error)
}

func (m *mockRegistry) KVService(tenantID, db string) (service.KVServicer, error) {
	return m.kvServiceFn(tenantID, db)
}

// TableService reports the relational catalog unavailable, which the
// provider's view resolution treats as "no views" -- the embedded nil
// service.Registry would nil-panic instead of erroring.
func (m *mockRegistry) TableService(tenantID, db string) (service.TableServicer, error) {
	return nil, &service.SubsystemNotConfiguredError{Subsystem: "table"}
}

// CollectionService is implemented explicitly rather than left to the embedded
// nil interface: the SQL provider's table lookup calls it on every unresolved
// name, and a method promoted off a nil embedded interface compiles and then
// dereferences nil. This engine serves no collections, which is a nil servicer
// and a nil error.
func (m *mockRegistry) CollectionService(string, string) (service.CollectionServicer, error) {
	return nil, nil
}

func (m *mockRegistry) PrivilegeService(tenantID, db string) (service.PrivilegeServicer, error) {
	if m.privilegeServiceFn != nil {
		return m.privilegeServiceFn(tenantID, db)
	}
	return sqltest.NewHarness().PrivSvc, nil
}

// ---------------------------------------------------------------------------
// Execute: happy paths
// ---------------------------------------------------------------------------

func TestExecute_SelectLiteral(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	result, err := svc.Execute(context.Background(), "testdb", "SELECT 1 AS val")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Columns) != 1 || result.Columns[0] != "val" {
		t.Fatalf("columns = %v, want [val]", result.Columns)
	}
	if result.RowCount != 1 {
		t.Fatalf("rowCount = %d, want 1", result.RowCount)
	}
	if result.Rows[0]["val"] != "1" {
		t.Fatalf("rows[0][val] = %v, want %q", result.Rows[0]["val"], "1")
	}
	if result.Duration == "" {
		t.Fatal("expected non-empty duration")
	}
}

func TestExecute_SelectSeededKVData(t *testing.T) {
	kv := newMockKV()
	seed(t, kv, "testdb", "greeting", "hello world")
	svc := NewService(kv, &mockDB{getFn: kvDB})

	result, err := svc.Execute(context.Background(), "testdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RowCount != 1 {
		t.Fatalf("rowCount = %d, want 1", result.RowCount)
	}
	row := result.Rows[0]
	if row["key"] != "greeting" {
		t.Fatalf("row[key] = %v, want %q", row["key"], "greeting")
	}
	// []byte values must be decoded as UTF-8 strings, not base64.
	if row["value"] != "hello world" {
		t.Fatalf("row[value] = %v, want %q", row["value"], "hello world")
	}
}

func TestExecute_EmptyResultSet(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	result, err := svc.Execute(context.Background(), "testdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RowCount != 0 {
		t.Fatalf("rowCount = %d, want 0", result.RowCount)
	}
	if result.Rows == nil {
		t.Fatal("rows must be an empty slice, not nil")
	}
}

func TestExecute_NilDatabaseService(t *testing.T) {
	// Legacy single-database mode: no database service, all tables exposed.
	kv := newMockKV()
	seed(t, kv, "qrdb", "k1", "v1")
	svc := NewService(kv, nil)

	result, err := svc.Execute(context.Background(), "qrdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RowCount != 1 {
		t.Fatalf("rowCount = %d, want 1", result.RowCount)
	}
}

// ---------------------------------------------------------------------------
// Execute: validation and error paths
// ---------------------------------------------------------------------------

func TestExecute_EmptyQuery(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	_, err := svc.Execute(context.Background(), "testdb", "")
	var eq *service.SQLEmptyQueryError
	if !errors.As(err, &eq) {
		t.Fatalf("expected SQLEmptyQueryError, got %T: %v", err, err)
	}
}

func TestExecute_EmptyDatabase(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	_, err := svc.Execute(context.Background(), "", "SELECT 1")
	var ed *service.SQLEmptyDatabaseError
	if !errors.As(err, &ed) {
		t.Fatalf("expected SQLEmptyDatabaseError, got %T: %v", err, err)
	}
}

func TestExecute_DatabaseNotFound(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{
		getFn: func(name string) (*database.DatabaseInfo, error) {
			return nil, &database.NotFoundError{Name: name}
		},
	})

	_, err := svc.Execute(context.Background(), "missing", "SELECT 1")
	var nf *database.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("expected database.NotFoundError, got %T: %v", err, err)
	}
	if nf.Name != "missing" {
		t.Fatalf("name = %q, want %q", nf.Name, "missing")
	}
}

func TestExecute_InvalidSyntax(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	_, err := svc.Execute(context.Background(), "testdb", "SELECTX BAD SYNTAX")
	var ee *service.SQLExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("expected SQLExecutionError, got %T: %v", err, err)
	}
	if ee.Query != "SELECTX BAD SYNTAX" {
		t.Fatalf("query = %q, want original query", ee.Query)
	}
	if ee.Unwrap() == nil {
		t.Fatal("expected wrapped engine error")
	}
}

func TestExecute_UnknownTable(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	_, err := svc.Execute(context.Background(), "testdb", "SELECT * FROM nonexistent")
	var ee *service.SQLExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("expected SQLExecutionError, got %T: %v", err, err)
	}
}

func TestExecute_EngineScoping(t *testing.T) {
	// A timeseries-engine database must not expose the kv table.
	svc := NewService(newMockKV(), &mockDB{
		getFn: func(name string) (*database.DatabaseInfo, error) {
			return &database.DatabaseInfo{
				Name:    name,
				Engine:  database.EngineTimeSeries,
				Backend: database.BackendMemory,
				Status:  database.StatusOpen,
			}, nil
		},
	})

	_, err := svc.Execute(context.Background(), "tsdb", "SELECT * FROM kv")
	var ee *service.SQLExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("expected SQLExecutionError for out-of-engine table, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Execute: tenant scoping (no Registry configured)
//
// Regression coverage for the SYSTEM-tenant escalation: before the fix,
// run() built a bare sql.Context with no QRDBSession, so
// provider.Database.GetTableInsensitive always fell back to the system
// tenant regardless of who the caller actually was — any tenant's SQL
// query silently read and wrote system/<db>/, the SHARED store, no matter
// what tenant.WithTenantID/auth.WithIdentity carried on ctx.
// ---------------------------------------------------------------------------

// TestExecute_TenantScoping_ReadsOwnNamespaceNotSystem proves that a query
// issued by an authenticated non-system tenant is scoped to that tenant's
// own composite namespace and does not see data seeded under the system
// tenant's namespace for the same database.
func TestExecute_TenantScoping_ReadsOwnNamespaceNotSystem(t *testing.T) {
	kv := newMockKV()
	seed(t, kv, "testdb", "secret", "system-owned") // lands under system/testdb/
	acmeKey := tenant.ComposeNamespace("acme", "testdb") + "greeting"
	if err := kv.Put(context.Background(), acmeKey, []byte("acme-owned")); err != nil {
		t.Fatalf("seed acme: %v", err)
	}

	svc := NewService(kv, &mockDB{getFn: kvDB}, WithSharedDefaultDatabase("testdb"))

	result, err := svc.Execute(authedContext("acme"), "testdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RowCount != 1 {
		t.Fatalf("rowCount = %d, want 1 (only the acme tenant's own row)", result.RowCount)
	}
	row := result.Rows[0]
	if row["key"] != "greeting" || row["value"] != "acme-owned" {
		t.Fatalf("row = %+v, want the acme-scoped row (SYSTEM-tenant escalation regression)", row)
	}
}

// TestExecute_TenantScoping_WritesOwnNamespaceNotSystem proves that a
// non-system tenant's INSERT lands under that tenant's own composite
// namespace, not system/<db>/.
func TestExecute_TenantScoping_WritesOwnNamespaceNotSystem(t *testing.T) {
	kv := newMockKV()
	svc := NewService(kv, &mockDB{getFn: kvDB}, WithSharedDefaultDatabase("testdb"))

	_, err := svc.Execute(authedContext("acme"), "testdb",
		"INSERT INTO kv (`key`, `value`) VALUES ('k1', 'v1')")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	acmeKey := tenant.ComposeNamespace("acme", "testdb") + "k1"
	if _, err := kv.Get(context.Background(), acmeKey); err != nil {
		t.Fatalf("write did not land under the acme tenant namespace: %v", err)
	}
	systemKey := tenant.ComposeNamespace(tenantid.System, "testdb") + "k1"
	if _, err := kv.Get(context.Background(), systemKey); err == nil {
		t.Fatal("write leaked into the system tenant namespace (SYSTEM-tenant escalation regression)")
	}
}

// TestExecute_TenantScoping_UnauthenticatedFallsBackToSystem verifies the
// unauthenticated / legacy-caller behavior is unchanged: with no identity
// or tenant bound to ctx, queries still resolve to the system tenant,
// exactly as before this fix (see TestExecute_SelectSeededKVData).
func TestExecute_TenantScoping_UnauthenticatedFallsBackToSystem(t *testing.T) {
	kv := newMockKV()
	seed(t, kv, "testdb", "greeting", "hello world")
	svc := NewService(kv, &mockDB{getFn: kvDB})

	result, err := svc.Execute(context.Background(), "testdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RowCount != 1 || result.Rows[0]["key"] != "greeting" {
		t.Fatalf("result = %+v, want the system-tenant seeded row", result)
	}
}

// ---------------------------------------------------------------------------
// Execute: Registry-scoped resolution
// ---------------------------------------------------------------------------

// TestExecute_WithRegistry_ResolvesPerTenantScope verifies that when a
// Registry is configured, Execute resolves the KV service through
// registry.KVService(tenant, db) using the tenant authenticated on ctx, and
// operates on bare keys against the returned (already-scoped) service
// rather than composing its own namespace prefix on top.
func TestExecute_WithRegistry_ResolvesPerTenantScope(t *testing.T) {
	scopedKV := newMockKV()
	if err := scopedKV.Put(context.Background(), "greeting", []byte("scoped-data")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var gotTenant, gotDB string
	reg := &mockRegistry{
		kvServiceFn: func(tenantID, db string) (service.KVServicer, error) {
			gotTenant, gotDB = tenantID, db
			return scopedKV, nil
		},
	}

	// The unscoped kvSvc must never be touched once a Registry resolves the
	// call; seed it with a decoy row to prove that.
	decoy := newMockKV()
	seed(t, decoy, "testdb", "decoy", "should-not-be-visible")

	svc := NewService(decoy, &mockDB{getFn: ownedKV("acme")}, WithRegistry(reg))

	result, err := svc.Execute(authedContext("acme"), "testdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "acme" || gotDB != "testdb" {
		t.Fatalf("registry.KVService called with (%q, %q), want (\"acme\", \"testdb\")", gotTenant, gotDB)
	}
	if result.RowCount != 1 || result.Rows[0]["key"] != "greeting" || result.Rows[0]["value"] != "scoped-data" {
		t.Fatalf("result = %+v, want the registry-scoped row", result)
	}
}

// TestExecute_WithRegistry_WritesThroughScopedService verifies that writes
// issued while a Registry is configured are applied to the registry-scoped
// service using bare keys (no additional namespace prefix on top of the
// scoping the registry already applied).
func TestExecute_WithRegistry_WritesThroughScopedService(t *testing.T) {
	scopedKV := newMockKV()
	reg := &mockRegistry{
		kvServiceFn: func(_, _ string) (service.KVServicer, error) {
			return scopedKV, nil
		},
	}

	svc := NewService(newMockKV(), &mockDB{getFn: ownedKV("acme")}, WithRegistry(reg))

	_, err := svc.Execute(authedContext("acme"), "testdb",
		"INSERT INTO kv (`key`, `value`) VALUES ('k1', 'v1')")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	v, err := scopedKV.Get(context.Background(), "k1")
	if err != nil {
		t.Fatalf("write did not land as a bare key on the registry-scoped service: %v", err)
	}
	if string(v) != "v1" {
		t.Fatalf("value = %q, want %q", v, "v1")
	}
}

// TestExecute_WithRegistry_TypedErrorPropagated verifies that a Registry
// resolution failure (unknown tenant, unknown database, sealed barrier, or
// an isolation backend that is not wired into this server) is propagated
// unwrapped rather than silently falling back to the shared store, which
// would downgrade the tenant's isolation guarantee. The ad-hoc SQL query
// path fails closed exactly like the KV/MVCC/Batch data-plane paths.
func TestExecute_WithRegistry_TypedErrorPropagated(t *testing.T) {
	wantErr := &service.TenantNotFoundError{TenantID: "ghost"}
	reg := &mockRegistry{
		kvServiceFn: func(_, _ string) (service.KVServicer, error) {
			return nil, wantErr
		},
	}
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB}, WithRegistry(reg))

	_, err := svc.Execute(authedContext("ghost"), "testdb", "SELECT 1")
	var tnf *service.TenantNotFoundError
	if !errors.As(err, &tnf) {
		t.Fatalf("expected TenantNotFoundError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Execute: read-only engine (write escalation)
//
// Regression coverage for the read-permission/write-capability mismatch:
// every transport maps SQLQuery/POST /sql/query to permission.KVRange (a
// read permission), so the served engine must not expose any write path
// regardless of what statement a caller submits.
// ---------------------------------------------------------------------------

func TestExecute_ReadOnly_RejectsInsert(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB}, WithReadOnly())

	_, err := svc.Execute(context.Background(), "testdb",
		"INSERT INTO kv (`key`, `value`) VALUES ('k1', 'v1')")
	var ee *service.SQLExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("expected SQLExecutionError rejecting INSERT on a read-only engine, got %T: %v", err, err)
	}
}

func TestExecute_ReadOnly_RejectsUpdate(t *testing.T) {
	kv := newMockKV()
	seed(t, kv, "testdb", "k1", "v1")
	svc := NewService(kv, &mockDB{getFn: kvDB}, WithReadOnly())

	_, err := svc.Execute(context.Background(), "testdb", "UPDATE kv SET `value` = 'v2' WHERE `key` = 'k1'")
	var ee *service.SQLExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("expected SQLExecutionError rejecting UPDATE on a read-only engine, got %T: %v", err, err)
	}
}

func TestExecute_ReadOnly_RejectsDelete(t *testing.T) {
	kv := newMockKV()
	seed(t, kv, "testdb", "k1", "v1")
	svc := NewService(kv, &mockDB{getFn: kvDB}, WithReadOnly())

	_, err := svc.Execute(context.Background(), "testdb", "DELETE FROM kv WHERE `key` = 'k1'")
	var ee *service.SQLExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("expected SQLExecutionError rejecting DELETE on a read-only engine, got %T: %v", err, err)
	}
}

func TestExecute_ReadOnly_StillAllowsSelect(t *testing.T) {
	kv := newMockKV()
	seed(t, kv, "testdb", "greeting", "hello world")
	svc := NewService(kv, &mockDB{getFn: kvDB}, WithReadOnly())

	result, err := svc.Execute(context.Background(), "testdb", "SELECT * FROM kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RowCount != 1 {
		t.Fatalf("rowCount = %d, want 1", result.RowCount)
	}
}

// TestExecute_ReadOnly_RejectsCreateTable is the assertion that keeps a
// read-permission endpoint (POST /sql/query and its QUIC/Unix/MCP
// equivalents, all authorized as permission.KVRange) from creating tables.
// Phase 5 added CREATE TABLE support to provider.Database; without the
// read-only wrapper (provider.WithReadOnly, applied here via
// query.WithReadOnly) it would be reachable through this same ad-hoc SQL
// surface.
func TestExecute_ReadOnly_RejectsCreateTable(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB}, WithReadOnly())

	_, err := svc.Execute(context.Background(), "testdb", "CREATE TABLE users (id INT PRIMARY KEY)")
	var ee *service.SQLExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("expected SQLExecutionError rejecting CREATE TABLE on a read-only engine, got %T: %v", err, err)
	}
}

// TestExecute_ReadOnly_RejectsDropTable is the DROP TABLE half of the same
// assertion as TestExecute_ReadOnly_RejectsCreateTable.
func TestExecute_ReadOnly_RejectsDropTable(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB}, WithReadOnly())

	_, err := svc.Execute(context.Background(), "testdb", "DROP TABLE kv")
	var ee *service.SQLExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("expected SQLExecutionError rejecting DROP TABLE on a read-only engine, got %T: %v", err, err)
	}
}

// TestExecute_NotReadOnly_DefaultAllowsWrite verifies that when WithReadOnly
// is not passed, DML continues to work exactly as before this fix — the
// default construction (used today by the Wails desktop binding and the
// embedded SDK transport, which run in-process without a separate
// network-authenticated caller) is unaffected.
func TestExecute_NotReadOnly_DefaultAllowsWrite(t *testing.T) {
	kv := newMockKV()
	svc := NewService(kv, &mockDB{getFn: kvDB})

	_, err := svc.Execute(context.Background(), "testdb",
		"INSERT INTO kv (`key`, `value`) VALUES ('k1', 'v1')")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	key := tenant.ComposeNamespace(tenantid.System, "testdb") + "k1"
	if _, err := kv.Get(context.Background(), key); err != nil {
		t.Fatalf("expected write to land: %v", err)
	}
}

// ---------------------------------------------------------------------------
// formatValue
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Execute: per-(tenant, database) engine caching
//
// Phase 9a: the ad-hoc SQL surface used to build a fresh provider AND
// sqle.Engine on every single Execute call, rebuilding the analyzer catalog
// from scratch each time. These tests pin the replacement behavior: the
// engine is now cached per (tenant, database) scope, reused by repeated
// queries against the same scope, and -- the isolation property that
// matters here -- never shared across two different tenants even when they
// target the same database name.
// ---------------------------------------------------------------------------

// cachedEngine returns the engine sqlService currently has cached for key,
// or nil if key has not been cached yet.
func cachedEngine(t *testing.T, svc service.SQLServicer, key engineKey) *sqle.Engine {
	t.Helper()
	s, ok := svc.(*sqlService)
	if !ok {
		t.Fatalf("svc is %T, want *sqlService", svc)
	}
	ptr := s.engines.Load()
	if ptr == nil {
		return nil
	}
	return (*ptr)[key]
}

func TestExecute_EngineCache_SameScopeReusesEngine(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	_, err := svc.Execute(context.Background(), "testdb", "SELECT 1")
	if err != nil {
		t.Fatalf("first Execute: unexpected error: %v", err)
	}
	key := engineKey{tenant: tenantid.System, db: "testdb"}
	first := cachedEngine(t, svc, key)
	if first == nil {
		t.Fatal("expected an engine cached for (system, testdb) after the first Execute")
	}

	_, err = svc.Execute(context.Background(), "testdb", "SELECT 2")
	if err != nil {
		t.Fatalf("second Execute: unexpected error: %v", err)
	}
	second := cachedEngine(t, svc, key)

	if first != second {
		t.Fatal("expected the second Execute against the same (tenant, database) scope to reuse the cached engine, got a rebuilt one")
	}
}

func TestExecute_EngineCache_DifferentDatabasesGetDistinctEngines(t *testing.T) {
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB})

	if _, err := svc.Execute(context.Background(), "db1", "SELECT 1"); err != nil {
		t.Fatalf("Execute(db1): unexpected error: %v", err)
	}
	if _, err := svc.Execute(context.Background(), "db2", "SELECT 1"); err != nil {
		t.Fatalf("Execute(db2): unexpected error: %v", err)
	}

	eng1 := cachedEngine(t, svc, engineKey{tenant: tenantid.System, db: "db1"})
	eng2 := cachedEngine(t, svc, engineKey{tenant: tenantid.System, db: "db2"})
	if eng1 == nil || eng2 == nil {
		t.Fatal("expected both scopes to be cached")
	}
	if eng1 == eng2 {
		t.Fatal("expected distinct databases to get distinct cached engines")
	}
}

// TestExecute_EngineCache_TenantIsolation_DistinctEnginesNotShared is the
// isolation property the engine cache must uphold: two different tenants
// querying the SAME database name must never be served the same cached
// engine instance, even though the engine itself only holds a
// provider/catalog reference (no per-request state) -- sharing one across
// tenants would be the first step toward a cache design that silently
// blurs tenant boundaries as it grows more aggressive.
func TestExecute_EngineCache_TenantIsolation_DistinctEnginesNotShared(t *testing.T) {
	acmeKV := newMockKV()
	widgetsKV := newMockKV()
	reg := &mockRegistry{
		kvServiceFn: func(tenantID, _ string) (service.KVServicer, error) {
			if tenantID == "acme" {
				return acmeKV, nil
			}
			return widgetsKV, nil
		},
	}
	// One database name served to two tenants is the shared default SQL
	// database: each tenant has its own namespace under it.
	svc := NewService(newMockKV(), &mockDB{getFn: kvDB}, WithRegistry(reg), WithSharedDefaultDatabase("testdb"))

	if _, err := svc.Execute(authedContext("acme"), "testdb", "SELECT 1"); err != nil {
		t.Fatalf("Execute(acme): unexpected error: %v", err)
	}
	if _, err := svc.Execute(authedContext("widgets"), "testdb", "SELECT 1"); err != nil {
		t.Fatalf("Execute(widgets): unexpected error: %v", err)
	}

	acmeEngine := cachedEngine(t, svc, engineKey{tenant: "acme", db: "testdb"})
	widgetsEngine := cachedEngine(t, svc, engineKey{tenant: "widgets", db: "testdb"})
	if acmeEngine == nil || widgetsEngine == nil {
		t.Fatal("expected both tenant scopes to be cached")
	}
	if acmeEngine == widgetsEngine {
		t.Fatal("isolation violation: two different tenants querying the same database name were served the same cached engine")
	}

	// Re-querying acme's own scope must still hit its own cached entry, not
	// widgets'.
	if _, err := svc.Execute(authedContext("acme"), "testdb", "SELECT 2"); err != nil {
		t.Fatalf("second Execute(acme): unexpected error: %v", err)
	}
	if got := cachedEngine(t, svc, engineKey{tenant: "acme", db: "testdb"}); got != acmeEngine {
		t.Fatal("expected acme's repeat query to reuse acme's own cached engine")
	}
}

// TestExecute_EngineCache_ConcurrentAccess drives concurrent Execute calls
// across several (tenant, database) scopes and asserts every call
// completes with the expected result and the cache converges to exactly
// one engine per scope. Run with -race; the cache's copy-on-write swap
// (atomic.Pointer + CompareAndSwap) is the property under test.
func TestExecute_EngineCache_ConcurrentAccess(t *testing.T) {
	kv := newMockKV()
	tenants := []string{"acme", "widgets", "globex"}
	// Each tenant queries the shared default SQL database and one database
	// of its own ("<tenant>-db"): database names are one node-wide catalog,
	// so only the shared default is addressed by more than one tenant.
	dbs := []string{"shared", "-db"}
	catalog := &mockDB{getFn: func(name string) (*database.DatabaseInfo, error) {
		if name == "shared" {
			return kvDB(name)
		}
		return ownedKV(strings.TrimSuffix(name, "-db"))(name)
	}}
	svc := NewService(kv, catalog, WithSharedDefaultDatabase("shared"))

	var wg sync.WaitGroup
	const iterationsPerScope = 20
	for _, tid := range tenants {
		for _, db := range dbs {
			for i := 0; i < iterationsPerScope; i++ {
				wg.Add(1)
				if db == "-db" {
					db = tid + db
				}
				go func(tid, db string) {
					defer wg.Done()
					ctx := authedContext(tid)
					result, err := svc.Execute(ctx, db, "SELECT 1 AS val")
					if err != nil {
						t.Errorf("Execute(%s, %s): unexpected error: %v", tid, db, err)
						return
					}
					if result.RowCount != 1 || result.Rows[0]["val"] != "1" {
						t.Errorf("Execute(%s, %s): unexpected result %+v", tid, db, result)
					}
				}(tid, db)
			}
		}
	}
	wg.Wait()

	// The cache must have converged to exactly one engine per (tenant,
	// database) scope actually queried above.
	s := svc.(*sqlService)
	ptr := s.engines.Load()
	if ptr == nil {
		t.Fatal("expected the cache to be populated after concurrent Execute calls")
	}
	if got, want := len(*ptr), len(tenants)*len(dbs); got != want {
		t.Fatalf("cache has %d entries, want %d (one per (tenant, database) scope)", got, want)
	}
}

// ---------------------------------------------------------------------------
// Execute: WithRegistry resolves relational tables (CREATE TABLE-produced),
// and the read-only surface can read but never write them.
// ---------------------------------------------------------------------------

// TestExecute_WithRegistry_ResolvesRelationalTable proves that when a
// Registry is configured, Execute additionally resolves user-defined
// relational tables through it (provider.WithRegistry), not just the
// per-tenant KV service -- a table created through the real
// service.TableService is visible to a SELECT issued through the ad-hoc
// SQL surface.
func TestExecute_WithRegistry_ResolvesRelationalTable(t *testing.T) {
	h := sqltest.NewHarness()
	h.CreateTable(context.Background(), "testdb", "widgets", []service.ColumnDef{
		{Name: "id", Type: "INT"},
	})

	svc := NewService(newMockKV(), &mockDB{getFn: ownedKV("acme")}, WithRegistry(h.Registry))

	result, err := svc.Execute(authedContext("acme"), "testdb", "SELECT * FROM widgets")
	if err != nil {
		t.Fatalf("unexpected error querying a relational table through the registry: %v", err)
	}
	if result.RowCount != 0 {
		t.Fatalf("rowCount = %d, want 0 (empty table)", result.RowCount)
	}
	if len(result.Columns) != 1 || result.Columns[0] != "id" {
		t.Fatalf("columns = %v, want [id]", result.Columns)
	}
}

// TestExecute_WithRegistry_ReadOnly_RelationalTable_ReadableNotWritable is
// the item 3 assertion from the Phase 9a spec: adding the registry to the
// read-only ad-hoc surface must not open a write path. A relational table
// is readable (SELECT succeeds) but INSERT is still rejected exactly like
// the static kv table already is (TestExecute_ReadOnly_RejectsInsert).
func TestExecute_WithRegistry_ReadOnly_RelationalTable_ReadableNotWritable(t *testing.T) {
	h := sqltest.NewHarness()
	h.CreateTable(context.Background(), "testdb", "widgets", []service.ColumnDef{
		{Name: "id", Type: "INT"},
	})

	svc := NewService(newMockKV(), &mockDB{getFn: ownedKV("acme")}, WithRegistry(h.Registry), WithReadOnly())

	result, err := svc.Execute(authedContext("acme"), "testdb", "SELECT * FROM widgets")
	if err != nil {
		t.Fatalf("expected the relational table to be readable, got error: %v", err)
	}
	if result.RowCount != 0 {
		t.Fatalf("rowCount = %d, want 0", result.RowCount)
	}

	_, err = svc.Execute(authedContext("acme"), "testdb", "INSERT INTO widgets (id) VALUES (1)")
	var ee *service.SQLExecutionError
	if !errors.As(err, &ee) {
		t.Fatalf("expected SQLExecutionError rejecting INSERT into a relational table on a read-only engine, got %T: %v", err, err)
	}
}

func TestFormatValue(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"bytes", []byte("hello"), "hello"},
		{"nil", nil, ""},
		{"int", 42, "42"},
		{"string", "test", "test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatValue(tc.in); got != tc.want {
				t.Fatalf("formatValue(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
