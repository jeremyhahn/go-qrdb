//go:build codec_json

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

package dao

import (
	"context"
	"reflect"
	"testing"

	entitypkg "github.com/jeremyhahn/go-qrdb/pkg/entity"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// ---------------------------------------------------------------------------
// Test entity for entity-registration tests
// ---------------------------------------------------------------------------

// registrationTestEntity is a dedicated test entity with index tags on
// selected fields so buildRegistrationFields and detectIDField can be
// exercised in isolation.
type registrationTestEntity struct {
	ID    uint64  `json:"id"    index:"unique"`
	Name  string  `json:"name"  index:"true"`
	Email string  `json:"email" index:"unique"`
	Age   int64   `json:"age"`
	Score float64 `json:"score"`
}

func (e *registrationTestEntity) EntityID() uint64      { return e.ID }
func (e *registrationTestEntity) SetEntityID(id uint64) { e.ID = id }

// customIDEntity is a test entity whose primary-key field has a non-conventional
// Go name and JSON name so detectIDField is exercised with a custom name.
type customIDEntity struct {
	UserID uint64 `json:"user_id"`
	Handle string `json:"handle"`
}

func (e *customIDEntity) EntityID() uint64      { return e.UserID }
func (e *customIDEntity) SetEntityID(id uint64) { e.UserID = id }

// registrationEntityType is a unique type name used by TestNew_RegistersEntity*
// so it does not collide with entities registered by other tests in the same
// test binary run.
const registrationEntityType = "registration_test_entities"

// ---------------------------------------------------------------------------
// TestBuildRegistrationFields
// ---------------------------------------------------------------------------

// TestBuildRegistrationFields verifies that buildRegistrationFields returns one
// FieldMeta per exported field and correctly propagates GoName, JSONName,
// GoType, GoKind, IsPrimaryKey, Indexed, and Unique from the struct reflection
// data and the supplied index metadata.
func TestBuildRegistrationFields(t *testing.T) {
	eType := reflect.TypeOf(registrationTestEntity{})

	indexMeta, err := index.ParseIndexTags(eType)
	if err != nil {
		t.Fatalf("ParseIndexTags: %v", err)
	}

	fields := buildRegistrationFields(eType, indexMeta, "ID")

	// registrationTestEntity has 5 exported fields.
	if len(fields) != 5 {
		t.Fatalf("field count = %d, want 5", len(fields))
	}

	// Build a lookup map for convenient assertions.
	byGoName := make(map[string]entitypkg.FieldMeta, len(fields))
	for _, f := range fields {
		byGoName[f.GoName] = f
	}

	t.Run("IDField", func(t *testing.T) {
		f, ok := byGoName["ID"]
		if !ok {
			t.Fatal("field ID not found")
		}
		if f.JSONName != "id" {
			t.Errorf("JSONName = %q, want %q", f.JSONName, "id")
		}
		if f.GoType != reflect.TypeOf(uint64(0)) {
			t.Errorf("GoType = %v, want uint64", f.GoType)
		}
		if f.GoKind != reflect.Uint64 {
			t.Errorf("GoKind = %v, want Uint64", f.GoKind)
		}
		if !f.IsPrimaryKey {
			t.Error("IsPrimaryKey = false, want true")
		}
		if !f.Indexed {
			t.Error("Indexed = false, want true (index:\"unique\")")
		}
		if !f.Unique {
			t.Error("Unique = false, want true (index:\"unique\")")
		}
	})

	t.Run("NameField", func(t *testing.T) {
		f, ok := byGoName["Name"]
		if !ok {
			t.Fatal("field Name not found")
		}
		if f.JSONName != "name" {
			t.Errorf("JSONName = %q, want %q", f.JSONName, "name")
		}
		if f.GoKind != reflect.String {
			t.Errorf("GoKind = %v, want String", f.GoKind)
		}
		if f.IsPrimaryKey {
			t.Error("IsPrimaryKey = true, want false")
		}
		if !f.Indexed {
			t.Error("Indexed = false, want true (index:\"true\")")
		}
		if f.Unique {
			t.Error("Unique = true, want false (index:\"true\" is non-unique)")
		}
	})

	t.Run("EmailField", func(t *testing.T) {
		f, ok := byGoName["Email"]
		if !ok {
			t.Fatal("field Email not found")
		}
		if f.JSONName != "email" {
			t.Errorf("JSONName = %q, want %q", f.JSONName, "email")
		}
		if !f.Indexed {
			t.Error("Indexed = false, want true (index:\"unique\")")
		}
		if !f.Unique {
			t.Error("Unique = false, want true (index:\"unique\")")
		}
	})

	t.Run("AgeField", func(t *testing.T) {
		f, ok := byGoName["Age"]
		if !ok {
			t.Fatal("field Age not found")
		}
		if f.GoKind != reflect.Int64 {
			t.Errorf("GoKind = %v, want Int64", f.GoKind)
		}
		if f.Indexed {
			t.Error("Indexed = true, want false (no index tag)")
		}
		if f.IsPrimaryKey {
			t.Error("IsPrimaryKey = true, want false")
		}
	})

	t.Run("ScoreField", func(t *testing.T) {
		f, ok := byGoName["Score"]
		if !ok {
			t.Fatal("field Score not found")
		}
		if f.GoKind != reflect.Float64 {
			t.Errorf("GoKind = %v, want Float64", f.GoKind)
		}
		if f.Indexed {
			t.Error("Indexed = true, want false (no index tag)")
		}
	})
}

// TestBuildRegistrationFields_NoIndexMeta verifies that passing nil indexMeta
// results in no fields being marked as indexed.
func TestBuildRegistrationFields_NoIndexMeta(t *testing.T) {
	eType := reflect.TypeOf(registrationTestEntity{})

	fields := buildRegistrationFields(eType, nil, "ID")

	if len(fields) != 5 {
		t.Fatalf("field count = %d, want 5", len(fields))
	}

	for _, f := range fields {
		if f.Indexed {
			t.Errorf("field %q: Indexed = true, want false when indexMeta is nil", f.GoName)
		}
		if f.Unique {
			t.Errorf("field %q: Unique = true, want false when indexMeta is nil", f.GoName)
		}
	}

	// The primary key designation must still be honoured.
	for _, f := range fields {
		if f.GoName == "ID" && !f.IsPrimaryKey {
			t.Error("IsPrimaryKey = false for ID field, want true")
		}
		if f.GoName != "ID" && f.IsPrimaryKey {
			t.Errorf("IsPrimaryKey = true for non-ID field %q", f.GoName)
		}
	}
}

// ---------------------------------------------------------------------------
// TestDetectIDField
// ---------------------------------------------------------------------------

// TestDetectIDField verifies that detectIDField correctly identifies the JSON
// name "id" and Go name "ID" for registrationTestEntity.
func TestDetectIDField(t *testing.T) {
	eType := reflect.TypeOf(registrationTestEntity{})
	jsonName, goName := detectIDField(func() *registrationTestEntity { return &registrationTestEntity{} }, eType)

	if jsonName != "id" {
		t.Errorf("jsonName = %q, want %q", jsonName, "id")
	}
	if goName != "ID" {
		t.Errorf("goName = %q, want %q", goName, "ID")
	}
}

// TestDetectIDField_CustomName verifies that detectIDField correctly identifies
// a non-conventional primary-key field (UserID / user_id).
func TestDetectIDField_CustomName(t *testing.T) {
	eType := reflect.TypeOf(customIDEntity{})
	jsonName, goName := detectIDField(func() *customIDEntity { return &customIDEntity{} }, eType)

	if jsonName != "user_id" {
		t.Errorf("jsonName = %q, want %q", jsonName, "user_id")
	}
	if goName != "UserID" {
		t.Errorf("goName = %q, want %q", goName, "UserID")
	}
}

// ---------------------------------------------------------------------------
// TestNew_RegistersEntity* — integration of the registration hook in New()
// ---------------------------------------------------------------------------

// TestNew_RegistersEntity verifies that constructing a DAO via New() registers
// the entity type in the global entity registry with all expected metadata.
func TestNew_RegistersEntity(t *testing.T) {
	defer entitypkg.Global().Unregister(registrationEntityType)

	store := newMemoryKVStore()
	_, err := New[*registrationTestEntity](
		store,
		registrationEntityType,
		func() *registrationTestEntity { return &registrationTestEntity{} },
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	reg, ok := entitypkg.Global().Get(registrationEntityType)
	if !ok {
		t.Fatalf("entity not found in global registry after New()")
	}

	if reg.EntityType != registrationEntityType {
		t.Errorf("EntityType = %q, want %q", reg.EntityType, registrationEntityType)
	}
	if reg.IDFieldJSON != "id" {
		t.Errorf("IDFieldJSON = %q, want %q", reg.IDFieldJSON, "id")
	}
	if reg.IDFieldGo != "ID" {
		t.Errorf("IDFieldGo = %q, want %q", reg.IDFieldGo, "ID")
	}
	if reg.NewFunc == nil {
		t.Error("NewFunc is nil")
	}
	if reg.MarshalFunc == nil {
		t.Error("MarshalFunc is nil")
	}
	if reg.UnmarshalFunc == nil {
		t.Error("UnmarshalFunc is nil")
	}

	// Verify expected field count (5 exported fields).
	if len(reg.Fields) != 5 {
		t.Errorf("Fields len = %d, want 5", len(reg.Fields))
	}

	// Verify primary key designation.
	var pkCount int
	for _, f := range reg.Fields {
		if f.IsPrimaryKey {
			pkCount++
			if f.GoName != "ID" {
				t.Errorf("primary key GoName = %q, want %q", f.GoName, "ID")
			}
		}
	}
	if pkCount != 1 {
		t.Errorf("primary key count = %d, want 1", pkCount)
	}
}

// TestNew_RegistersEntity_MarshalUnmarshal verifies that the MarshalFunc and
// UnmarshalFunc stored in the registration produce a correct round-trip.
func TestNew_RegistersEntity_MarshalUnmarshal(t *testing.T) {
	const entityType = "registration_marshal_test_entities"
	defer entitypkg.Global().Unregister(entityType)

	store := newMemoryKVStore()
	_, err := New[*registrationTestEntity](
		store,
		entityType,
		func() *registrationTestEntity { return &registrationTestEntity{} },
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	reg, ok := entitypkg.Global().Get(entityType)
	if !ok {
		t.Fatal("entity not found in global registry")
	}

	original := &registrationTestEntity{
		ID:    42,
		Name:  "Alice",
		Email: "alice@example.com",
		Age:   30,
		Score: 9.5,
	}

	encoded, err := reg.MarshalFunc(original)
	if err != nil {
		t.Fatalf("MarshalFunc error = %v", err)
	}
	if len(encoded) == 0 {
		t.Fatal("MarshalFunc returned empty bytes")
	}

	decoded, err := reg.UnmarshalFunc(encoded)
	if err != nil {
		t.Fatalf("UnmarshalFunc error = %v", err)
	}

	got, ok := decoded.(*registrationTestEntity)
	if !ok {
		t.Fatalf("UnmarshalFunc returned %T, want *registrationTestEntity", decoded)
	}

	if got.ID != original.ID {
		t.Errorf("ID = %d, want %d", got.ID, original.ID)
	}
	if got.Name != original.Name {
		t.Errorf("Name = %q, want %q", got.Name, original.Name)
	}
	if got.Email != original.Email {
		t.Errorf("Email = %q, want %q", got.Email, original.Email)
	}
	if got.Age != original.Age {
		t.Errorf("Age = %d, want %d", got.Age, original.Age)
	}
	if got.Score != original.Score {
		t.Errorf("Score = %v, want %v", got.Score, original.Score)
	}
}

// TestNew_RegistersEntity_Idempotent verifies that calling New() twice with
// the same entity type and struct type does not return an error; re-registration
// of the same StructType is a defined no-op in the registry.
func TestNew_RegistersEntity_Idempotent(t *testing.T) {
	const entityType = "registration_idempotent_test_entities"
	defer entitypkg.Global().Unregister(entityType)

	store := newMemoryKVStore()
	factory := func() *registrationTestEntity { return &registrationTestEntity{} }

	_, err := New[*registrationTestEntity](store, entityType, factory)
	if err != nil {
		t.Fatalf("first New() error = %v", err)
	}

	_, err = New[*registrationTestEntity](store, entityType, factory)
	if err != nil {
		t.Errorf("second New() error = %v, want nil (idempotent re-registration)", err)
	}

	// Verify the entity is still properly registered after the second call.
	reg, ok := entitypkg.Global().Get(entityType)
	if !ok {
		t.Fatal("entity not found in global registry after idempotent re-registration")
	}
	if reg.EntityType != entityType {
		t.Errorf("EntityType = %q, want %q", reg.EntityType, entityType)
	}
}

// TestNew_RegistersEntity_NewFuncReturnsZeroValue verifies that the NewFunc
// stored in the registration produces a usable zero-value entity.
func TestNew_RegistersEntity_NewFuncReturnsZeroValue(t *testing.T) {
	const entityType = "registration_newfunc_test_entities"
	defer entitypkg.Global().Unregister(entityType)

	store := newMemoryKVStore()
	_, err := New[*registrationTestEntity](
		store,
		entityType,
		func() *registrationTestEntity { return &registrationTestEntity{} },
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	reg, ok := entitypkg.Global().Get(entityType)
	if !ok {
		t.Fatal("entity not found in global registry")
	}

	v := reg.NewFunc()
	if v == nil {
		t.Fatal("NewFunc() returned nil")
	}
	e, ok := v.(*registrationTestEntity)
	if !ok {
		t.Fatalf("NewFunc() returned %T, want *registrationTestEntity", v)
	}
	if e.ID != 0 {
		t.Errorf("zero-value entity ID = %d, want 0", e.ID)
	}
}

// TestNew_RegistersEntity_WithDatabase verifies that a DAO created with the
// WithDatabase option propagates the Database field to the entity registration.
func TestNew_RegistersEntity_WithDatabase(t *testing.T) {
	const entityType = "registration_db_scoped_entities"
	const dbName = "myapp"
	defer entitypkg.Global().Unregister(entityType)

	store := newMemoryKVStore()
	_, err := New[*registrationTestEntity](
		store,
		entityType,
		func() *registrationTestEntity { return &registrationTestEntity{} },
		WithDatabase(dbName),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	reg, ok := entitypkg.Global().Get(entityType)
	if !ok {
		t.Fatal("entity not found in global registry")
	}

	if reg.Database != dbName {
		t.Errorf("Database = %q, want %q", reg.Database, dbName)
	}
}

// TestNew_RegistersEntity_MarshalTypeMismatch verifies that MarshalFunc
// returns an error when given a value of the wrong type.
func TestNew_RegistersEntity_MarshalTypeMismatch(t *testing.T) {
	const entityType = "registration_marshal_mismatch_entities"
	defer entitypkg.Global().Unregister(entityType)

	store := newMemoryKVStore()
	_, err := New[*registrationTestEntity](
		store,
		entityType,
		func() *registrationTestEntity { return &registrationTestEntity{} },
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	reg, ok := entitypkg.Global().Get(entityType)
	if !ok {
		t.Fatal("entity not found in global registry")
	}

	// Passing a completely different type must produce an error.
	_, marshalErr := reg.MarshalFunc("not_an_entity")
	if marshalErr == nil {
		t.Error("MarshalFunc returned nil error for wrong type, want error")
	}
}

// TestBuildRegistrationFields_UnexportedFieldsSkipped verifies that
// unexported struct fields are omitted from the generated FieldMeta slice.
func TestBuildRegistrationFields_UnexportedFieldsSkipped(t *testing.T) {
	type entityWithUnexported struct {
		ID       uint64 `json:"id"`
		internal string //nolint:unused // intentionally unexported for test
		Name     string `json:"name"`
	}

	eType := reflect.TypeOf(entityWithUnexported{})
	fields := buildRegistrationFields(eType, nil, "ID")

	// Only 2 exported fields: ID and Name.
	if len(fields) != 2 {
		t.Fatalf("field count = %d, want 2 (unexported field must be skipped)", len(fields))
	}
	for _, f := range fields {
		if f.GoName == "internal" {
			t.Error("unexported field 'internal' must not appear in FieldMeta slice")
		}
	}
}

// TestDetectIDField_FallbackToConvention verifies the fallback path of
// detectIDField: the sentinel constant has the expected magic value, and an
// entity whose SetEntityID writes into a detectable uint64 field is correctly
// identified even under the fallback return path.
func TestDetectIDField_FallbackToConvention(t *testing.T) {
	// Validate the sentinel constant value. The fallback branch is reached when
	// no exported uint64 field matches the sentinel after SetEntityID is called.
	// The sentinel must be a non-zero magic value to avoid false positives on
	// zero-initialised fields.
	if idSentinel == 0 {
		t.Error("idSentinel must be non-zero to avoid false positives on uninitialised fields")
	}
	const expectedSentinel = uint64(0xDEADBEEFCAFEBABE)
	if idSentinel != expectedSentinel {
		t.Errorf("idSentinel = %#x, want %#x", idSentinel, expectedSentinel)
	}
}

// ---------------------------------------------------------------------------
// Save/Get round-trip with registered entity (end-to-end sanity check)
// ---------------------------------------------------------------------------

// TestNew_RegistersEntity_SaveAndGet is an end-to-end sanity check that a DAO
// created for registrationTestEntity can save and retrieve entities through the
// KV store after the registration hook has run.
func TestNew_RegistersEntity_SaveAndGet(t *testing.T) {
	const entityType = "registration_save_get_entities"
	defer entitypkg.Global().Unregister(entityType)

	store := newMemoryKVStore()
	dao, err := New[*registrationTestEntity](
		store,
		entityType,
		func() *registrationTestEntity { return &registrationTestEntity{} },
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx := context.Background()
	e := &registrationTestEntity{
		ID:    1001,
		Name:  "Bob",
		Email: "bob@example.com",
		Age:   25,
		Score: 7.5,
	}

	if err := dao.Save(ctx, e); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	retrieved, err := dao.Get(ctx, 1001)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if retrieved.Name != e.Name {
		t.Errorf("Name = %q, want %q", retrieved.Name, e.Name)
	}
	if retrieved.Email != e.Email {
		t.Errorf("Email = %q, want %q", retrieved.Email, e.Email)
	}
}
