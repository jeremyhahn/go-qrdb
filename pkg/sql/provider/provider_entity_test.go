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
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/entity"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// providerEntityRegistration constructs a minimal Registration for
// registrationProviderEntity. The entityType parameter allows each test to
// use a unique type name so global registry state does not bleed across tests.
func providerEntityRegistration(entityType string) *entity.Registration {
	t := reflect.TypeOf(registrationProviderEntity{})
	fields := []entity.FieldMeta{
		{
			GoName:       "ID",
			JSONName:     "id",
			GoType:       reflect.TypeOf(uint64(0)),
			GoKind:       reflect.Uint64,
			IsPrimaryKey: true,
			Indexed:      true,
			Unique:       true,
		},
		{
			GoName:   "Name",
			JSONName: "name",
			GoType:   reflect.TypeOf(""),
			GoKind:   reflect.String,
		},
	}
	return &entity.Registration{
		EntityType:  entityType,
		StructType:  t,
		Fields:      fields,
		IDFieldJSON: "id",
		IDFieldGo:   "ID",
		Database:    "",
		CodecType:   "json",
		NewFunc:     func() any { return &registrationProviderEntity{} },
		MarshalFunc: func(v any) ([]byte, error) { return json.Marshal(v) },
		UnmarshalFunc: func(data []byte) (any, error) {
			e := &registrationProviderEntity{}
			if err := json.Unmarshal(data, e); err != nil {
				return nil, err
			}
			return e, nil
		},
	}
}

// providerEntityRegistrationForDB is like providerEntityRegistration but with
// a non-empty Database field for scoped-entity tests.
func providerEntityRegistrationForDB(entityType, db string) *entity.Registration {
	reg := providerEntityRegistration(entityType)
	reg.Database = db
	return reg
}

// registrationProviderEntity is a minimal entity used only by this test file.
type registrationProviderEntity struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

// ---------------------------------------------------------------------------
// TestGetTableInsensitive_EntityTable
// ---------------------------------------------------------------------------

// TestGetTableInsensitive_EntityTable verifies that GetTableInsensitive returns
// an EntityTable (found=true) when the requested name matches an entity type
// registered in the global entity registry.
func TestGetTableInsensitive_EntityTable(t *testing.T) {
	const entityType = "provider_entity_exact"
	reg := providerEntityRegistration(entityType)
	if err := entity.Global().Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	defer entity.Global().Unregister(entityType)

	svc := &mockKVService{}
	db := &Database{name: "qrdb", kvSvc: svc}
	ctx := newTestContext()

	tbl, found, err := db.GetTableInsensitive(ctx, entityType)
	if err != nil {
		t.Fatalf("GetTableInsensitive() error = %v", err)
	}
	if !found {
		t.Fatal("GetTableInsensitive() found = false, want true")
	}
	if tbl == nil {
		t.Fatal("GetTableInsensitive() returned nil table")
	}
	if tbl.Name() != entityType {
		t.Errorf("table Name() = %q, want %q", tbl.Name(), entityType)
	}

	// Verify the returned table is an EntityTable, not a KVTable or CollectionTable.
	if _, ok := tbl.(*EntityTable); !ok {
		t.Errorf("table is %T, want *EntityTable", tbl)
	}
}

// ---------------------------------------------------------------------------
// TestGetTableInsensitive_EntityTable_CaseInsensitive
// ---------------------------------------------------------------------------

// TestGetTableInsensitive_EntityTable_CaseInsensitive verifies that the lookup
// is case-insensitive: registering "products" and requesting "Products" or
// "PRODUCTS" both resolve to the EntityTable.
func TestGetTableInsensitive_EntityTable_CaseInsensitive(t *testing.T) {
	const entityType = "provider_entity_ci_products"
	reg := providerEntityRegistration(entityType)
	if err := entity.Global().Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	defer entity.Global().Unregister(entityType)

	svc := &mockKVService{}
	db := &Database{name: "qrdb", kvSvc: svc}
	ctx := newTestContext()

	// The registry stores keys in lowercase because GetTableInsensitive calls
	// strings.ToLower before the lookup. Verify the registered lowercase name
	// is retrievable under mixed-case lookup inputs.
	for _, lookupName := range []string{
		entityType,
		strings.ToUpper(entityType),
		"Provider_Entity_CI_Products",
	} {
		tbl, found, err := db.GetTableInsensitive(ctx, lookupName)
		if err != nil {
			t.Errorf("GetTableInsensitive(%q) error = %v", lookupName, err)
			continue
		}
		if !found {
			t.Errorf("GetTableInsensitive(%q) found = false, want true", lookupName)
			continue
		}
		if tbl == nil {
			t.Errorf("GetTableInsensitive(%q) returned nil table", lookupName)
		}
	}
}

// ---------------------------------------------------------------------------
// TestGetTableNames_IncludesEntityTypes
// ---------------------------------------------------------------------------

// TestGetTableNames_IncludesEntityTypes verifies that GetTableNames includes
// entity type names from the global entity registry alongside the static table
// names.
func TestGetTableNames_IncludesEntityTypes(t *testing.T) {
	const entityType = "provider_entity_tablenames"
	reg := providerEntityRegistration(entityType)
	if err := entity.Global().Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	defer entity.Global().Unregister(entityType)

	svc := &mockKVService{}
	// Use a non-empty db name so the legacy (empty-Database) entity entries are
	// also included via the ListForDatabase("") call in GetTableNames.
	db := &Database{name: "qrdb", kvSvc: svc}
	ctx := newTestContext()

	names, err := db.GetTableNames(ctx)
	if err != nil {
		t.Fatalf("GetTableNames() error = %v", err)
	}

	found := false
	for _, n := range names {
		if n == entityType {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("GetTableNames() = %v, want to contain %q", names, entityType)
	}
}

// TestGetTableNames_IncludesEntityTypes_DBScoped verifies that an entity
// registered for a specific database appears in GetTableNames for that database
// but not for a different one.
func TestGetTableNames_IncludesEntityTypes_DBScoped(t *testing.T) {
	const entityType = "provider_entity_db_scoped"
	const dbName = "myapp"
	reg := providerEntityRegistrationForDB(entityType, dbName)
	if err := entity.Global().Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	defer entity.Global().Unregister(entityType)

	svc := &mockKVService{}

	// Should appear in the matching database.
	dbMatch := &Database{name: dbName, kvSvc: svc}
	ctx := newTestContext()

	names, err := dbMatch.GetTableNames(ctx)
	if err != nil {
		t.Fatalf("GetTableNames() error = %v", err)
	}
	found := false
	for _, n := range names {
		if n == entityType {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("GetTableNames() for db=%q = %v, want to contain %q", dbName, names, entityType)
	}

	// Should NOT appear in a different database (with a non-empty name).
	dbOther := &Database{name: "otherd", kvSvc: svc}
	namesOther, err := dbOther.GetTableNames(ctx)
	if err != nil {
		t.Fatalf("GetTableNames() error = %v", err)
	}
	for _, n := range namesOther {
		if n == entityType {
			t.Errorf("GetTableNames() for db=%q unexpectedly contains %q", "otherd", entityType)
		}
	}
}

// ---------------------------------------------------------------------------
// TestGetTableInsensitive_EntityTable_NotFound
// ---------------------------------------------------------------------------

// TestGetTableInsensitive_EntityTable_NotFound verifies that requesting an
// entity name that is not registered in the global entity registry returns
// found=false without an error, falling through to the collection/nil path.
func TestGetTableInsensitive_EntityTable_NotFound(t *testing.T) {
	svc := &mockKVService{}
	db := &Database{name: "qrdb", kvSvc: svc}
	ctx := newTestContext()

	// "completely_unknown_entity_xyz" is not registered in the global registry.
	tbl, found, err := db.GetTableInsensitive(ctx, "completely_unknown_entity_xyz")
	if err != nil {
		t.Fatalf("GetTableInsensitive() error = %v, want nil", err)
	}
	// No static table, no entity registration, no collection service → not found.
	if found {
		t.Errorf("GetTableInsensitive() found = true, want false")
	}
	if tbl != nil {
		t.Errorf("GetTableInsensitive() returned non-nil table %T, want nil", tbl)
	}
}

// ---------------------------------------------------------------------------
// TestGetTableInsensitive_EntityTable_TenantFromSession
// ---------------------------------------------------------------------------

// TestGetTableInsensitive_EntityTable_TenantFromSession verifies that when the
// sqle.Context carries a QRDBSession, the tenant ID from the session is
// propagated to the EntityTable.
func TestGetTableInsensitive_EntityTable_TenantFromSession(t *testing.T) {
	const entityType = "provider_entity_tenant_session"
	reg := providerEntityRegistration(entityType)
	if err := entity.Global().Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	defer entity.Global().Unregister(entityType)

	svc := &mockKVService{}
	db := &Database{name: "qrdb", kvSvc: svc}

	// Create a context with a QRDBSession carrying a non-default tenant.
	ctx := newTestContext()
	ctx.Session = NewSession(sqle.NewBaseSession(), &auth.Identity{TenantID: "tenant42"})

	tbl, found, err := db.GetTableInsensitive(ctx, entityType)
	if err != nil {
		t.Fatalf("GetTableInsensitive() error = %v", err)
	}
	if !found {
		t.Fatal("GetTableInsensitive() found = false, want true")
	}

	et, ok := tbl.(*EntityTable)
	if !ok {
		t.Fatalf("table is %T, want *EntityTable", tbl)
	}
	wantPrefix := tenant.ComposeNamespace("tenant42", "qrdb")
	if et.prefix != wantPrefix {
		t.Errorf("EntityTable prefix = %q, want %q", et.prefix, wantPrefix)
	}
}

// ---------------------------------------------------------------------------
// TestGetTableInsensitive_EntityTable_SystemTenant
// ---------------------------------------------------------------------------

// TestGetTableInsensitive_EntityTable_SystemTenant verifies that when the
// sqle.Context carries no QRDBSession, the EntityTable receives the system tenant id as
// the tenant ID.
func TestGetTableInsensitive_EntityTable_SystemTenant(t *testing.T) {
	const entityType = "provider_entity_default_tenant"
	reg := providerEntityRegistration(entityType)
	if err := entity.Global().Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	defer entity.Global().Unregister(entityType)

	svc := &mockKVService{}
	db := &Database{name: "qrdb", kvSvc: svc}
	// newTestContext() creates a plain context without a QRDBSession.
	ctx := newTestContext()

	tbl, found, err := db.GetTableInsensitive(ctx, entityType)
	if err != nil {
		t.Fatalf("GetTableInsensitive() error = %v", err)
	}
	if !found {
		t.Fatal("GetTableInsensitive() found = false, want true")
	}

	et, ok := tbl.(*EntityTable)
	if !ok {
		t.Fatalf("table is %T, want *EntityTable", tbl)
	}
	wantPrefix := tenant.ComposeNamespace("system", "qrdb")
	if et.prefix != wantPrefix {
		t.Errorf("EntityTable prefix = %q, want %q", et.prefix, wantPrefix)
	}
}
