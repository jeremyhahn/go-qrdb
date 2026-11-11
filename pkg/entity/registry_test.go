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

package entity_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/entity"
)

// testUser is a minimal entity struct used across all registry tests.
type testUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Age   int    `json:"age"`
}

// testProduct is a second entity struct used for conflict testing.
type testProduct struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Price int64  `json:"price"`
}

// userFields returns the canonical FieldMeta slice for testUser.
func userFields() []entity.FieldMeta {
	return []entity.FieldMeta{
		{
			GoName:       "ID",
			JSONName:     "id",
			GoType:       reflect.TypeOf(""),
			GoKind:       reflect.String,
			IsPrimaryKey: true,
		},
		{
			GoName:   "Email",
			JSONName: "email",
			GoType:   reflect.TypeOf(""),
			GoKind:   reflect.String,
			Indexed:  true,
			Unique:   true,
		},
		{
			GoName:   "Age",
			JSONName: "age",
			GoType:   reflect.TypeOf(int(0)),
			GoKind:   reflect.Int,
		},
	}
}

// newUserRegistration returns a fully-populated Registration for testUser.
func newUserRegistration() *entity.Registration {
	return &entity.Registration{
		EntityType:  "users",
		StructType:  reflect.TypeOf(testUser{}),
		Fields:      userFields(),
		IDFieldJSON: "id",
		IDFieldGo:   "ID",
		Database:    "app",
		CodecType:   "json",
		NewFunc:     func() any { return &testUser{} },
		MarshalFunc: func(v any) ([]byte, error) { return json.Marshal(v) },
		UnmarshalFunc: func(data []byte) (any, error) {
			u := &testUser{}
			return u, json.Unmarshal(data, u)
		},
	}
}

// newProductRegistration returns a fully-populated Registration for testProduct.
func newProductRegistration() *entity.Registration {
	return &entity.Registration{
		EntityType: "products",
		StructType: reflect.TypeOf(testProduct{}),
		Fields: []entity.FieldMeta{
			{GoName: "ID", JSONName: "id", GoType: reflect.TypeOf(""), GoKind: reflect.String, IsPrimaryKey: true},
			{GoName: "Name", JSONName: "name", GoType: reflect.TypeOf(""), GoKind: reflect.String},
			{GoName: "Price", JSONName: "price", GoType: reflect.TypeOf(int64(0)), GoKind: reflect.Int64},
		},
		IDFieldJSON: "id",
		IDFieldGo:   "ID",
		Database:    "app",
		CodecType:   "json",
		NewFunc:     func() any { return &testProduct{} },
		MarshalFunc: func(v any) ([]byte, error) { return json.Marshal(v) },
		UnmarshalFunc: func(data []byte) (any, error) {
			p := &testProduct{}
			return p, json.Unmarshal(data, p)
		},
	}
}

func TestRegistry_Register_Valid(t *testing.T) {
	r := entity.NewRegistry()
	if err := r.Register(newUserRegistration()); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	_, ok := r.Get("users")
	if !ok {
		t.Error("Get() should find registered entity")
	}
}

func TestRegistry_Register_Idempotent(t *testing.T) {
	r := entity.NewRegistry()
	reg := newUserRegistration()
	if err := r.Register(reg); err != nil {
		t.Fatalf("first Register() unexpected error: %v", err)
	}
	// Registering the same StructType again must be a no-op.
	if err := r.Register(reg); err != nil {
		t.Fatalf("second Register() (idempotent) unexpected error: %v", err)
	}
}

func TestRegistry_Register_ConflictDifferentType(t *testing.T) {
	r := entity.NewRegistry()
	if err := r.Register(newUserRegistration()); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}

	// Re-register "users" with a different StructType.
	conflict := newUserRegistration()
	conflict.StructType = reflect.TypeOf(testProduct{})

	err := r.Register(conflict)
	if err == nil {
		t.Fatal("Register() should return error for conflicting StructType")
	}

	var are *entity.AlreadyRegisteredError
	if !errors.As(err, &are) {
		t.Fatalf("expected AlreadyRegisteredError, got %T: %v", err, err)
	}
	if are.EntityType != "users" {
		t.Errorf("AlreadyRegisteredError.EntityType = %q, want %q", are.EntityType, "users")
	}
}

// requiredFieldTestCase describes a single missing-field validation scenario.
type requiredFieldTestCase struct {
	name   string
	mutate func(r *entity.Registration)
	field  string
}

func TestRegistry_Register_MissingRequiredFields(t *testing.T) {
	cases := []requiredFieldTestCase{
		{
			name:   "missing EntityType",
			mutate: func(r *entity.Registration) { r.EntityType = "" },
			field:  "EntityType",
		},
		{
			name:   "missing StructType",
			mutate: func(r *entity.Registration) { r.StructType = nil },
			field:  "StructType",
		},
		{
			name:   "empty Fields",
			mutate: func(r *entity.Registration) { r.Fields = nil },
			field:  "Fields",
		},
		{
			name:   "missing IDFieldJSON",
			mutate: func(r *entity.Registration) { r.IDFieldJSON = "" },
			field:  "IDFieldJSON",
		},
		{
			name:   "missing IDFieldGo",
			mutate: func(r *entity.Registration) { r.IDFieldGo = "" },
			field:  "IDFieldGo",
		},
		{
			name:   "nil NewFunc",
			mutate: func(r *entity.Registration) { r.NewFunc = nil },
			field:  "NewFunc",
		},
		{
			name:   "nil MarshalFunc",
			mutate: func(r *entity.Registration) { r.MarshalFunc = nil },
			field:  "MarshalFunc",
		},
		{
			name:   "nil UnmarshalFunc",
			mutate: func(r *entity.Registration) { r.UnmarshalFunc = nil },
			field:  "UnmarshalFunc",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := entity.NewRegistry()
			reg := newUserRegistration()
			tc.mutate(reg)
			err := r.Register(reg)
			if err == nil {
				t.Fatal("Register() should return error for missing required field")
			}
			var ire *entity.InvalidRegistrationError
			if !errors.As(err, &ire) {
				t.Fatalf("expected InvalidRegistrationError, got %T: %v", err, err)
			}
			if ire.Field != tc.field {
				t.Errorf("InvalidRegistrationError.Field = %q, want %q", ire.Field, tc.field)
			}
		})
	}
}

func TestRegistry_Get_Existing(t *testing.T) {
	r := entity.NewRegistry()
	if err := r.Register(newUserRegistration()); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}

	got, ok := r.Get("users")
	if !ok {
		t.Fatal("Get() should return true for existing entity")
	}
	if got == nil {
		t.Fatal("Get() should return non-nil Registration")
	}
	if got.EntityType != "users" {
		t.Errorf("EntityType = %q, want %q", got.EntityType, "users")
	}
}

func TestRegistry_Get_NonExisting(t *testing.T) {
	r := entity.NewRegistry()
	got, ok := r.Get("nonexistent")
	if ok {
		t.Error("Get() should return false for unregistered entity")
	}
	if got != nil {
		t.Error("Get() should return nil for unregistered entity")
	}
}

func TestRegistry_List_Sorted(t *testing.T) {
	r := entity.NewRegistry()
	if err := r.Register(newProductRegistration()); err != nil {
		t.Fatalf("Register(product) unexpected error: %v", err)
	}
	if err := r.Register(newUserRegistration()); err != nil {
		t.Fatalf("Register(user) unexpected error: %v", err)
	}

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("List() len = %d, want 2", len(list))
	}
	// "products" < "users" alphabetically.
	if list[0].EntityType != "products" {
		t.Errorf("List()[0].EntityType = %q, want %q", list[0].EntityType, "products")
	}
	if list[1].EntityType != "users" {
		t.Errorf("List()[1].EntityType = %q, want %q", list[1].EntityType, "users")
	}
}

func TestRegistry_List_Empty(t *testing.T) {
	r := entity.NewRegistry()
	if list := r.List(); len(list) != 0 {
		t.Errorf("List() on empty registry returned %d elements, want 0", len(list))
	}
}

func TestRegistry_ListForDatabase_Filters(t *testing.T) {
	r := entity.NewRegistry()

	// "users" in database "app".
	if err := r.Register(newUserRegistration()); err != nil {
		t.Fatalf("Register(user) unexpected error: %v", err)
	}

	// "products" in database "app".
	if err := r.Register(newProductRegistration()); err != nil {
		t.Fatalf("Register(product) unexpected error: %v", err)
	}

	// "orders" in database "warehouse".
	orders := &entity.Registration{
		EntityType:  "orders",
		StructType:  reflect.TypeOf(struct{ ID string }{}),
		Fields:      []entity.FieldMeta{{GoName: "ID", JSONName: "id", GoType: reflect.TypeOf(""), GoKind: reflect.String, IsPrimaryKey: true}},
		IDFieldJSON: "id",
		IDFieldGo:   "ID",
		Database:    "warehouse",
		CodecType:   "json",
		NewFunc:     func() any { return &struct{ ID string }{} },
		MarshalFunc: func(v any) ([]byte, error) { return json.Marshal(v) },
		UnmarshalFunc: func(data []byte) (any, error) {
			o := &struct{ ID string }{}
			return o, json.Unmarshal(data, o)
		},
	}
	if err := r.Register(orders); err != nil {
		t.Fatalf("Register(orders) unexpected error: %v", err)
	}

	appRegs := r.ListForDatabase("app")
	if len(appRegs) != 2 {
		t.Fatalf("ListForDatabase(app) len = %d, want 2", len(appRegs))
	}
	if appRegs[0].EntityType != "products" {
		t.Errorf("appRegs[0].EntityType = %q, want %q", appRegs[0].EntityType, "products")
	}
	if appRegs[1].EntityType != "users" {
		t.Errorf("appRegs[1].EntityType = %q, want %q", appRegs[1].EntityType, "users")
	}

	warehouseRegs := r.ListForDatabase("warehouse")
	if len(warehouseRegs) != 1 {
		t.Fatalf("ListForDatabase(warehouse) len = %d, want 1", len(warehouseRegs))
	}
	if warehouseRegs[0].EntityType != "orders" {
		t.Errorf("warehouseRegs[0].EntityType = %q, want %q", warehouseRegs[0].EntityType, "orders")
	}

	// Database that has no registrations.
	if got := r.ListForDatabase("unknown"); len(got) != 0 {
		t.Errorf("ListForDatabase(unknown) len = %d, want 0", len(got))
	}
}

func TestRegistry_Unregister(t *testing.T) {
	r := entity.NewRegistry()
	if err := r.Register(newUserRegistration()); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}

	r.Unregister("users")

	_, ok := r.Get("users")
	if ok {
		t.Error("Get() should return false after Unregister()")
	}
}

func TestRegistry_Unregister_NotRegistered(t *testing.T) {
	r := entity.NewRegistry()
	// Unregistering an entity that was never registered must not panic.
	r.Unregister("nonexistent")
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	r := entity.NewRegistry()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			// Writers: alternate between user and product registrations.
			if n%2 == 0 {
				_ = r.Register(newUserRegistration())
			} else {
				_ = r.Register(newProductRegistration())
			}
			// Readers.
			r.Get("users")
			r.Get("products")
			r.List()
			r.ListForDatabase("app")
		}(i)
	}

	wg.Wait()

	// Both should be registered after all goroutines complete.
	if _, ok := r.Get("users"); !ok {
		t.Error("users should be registered after concurrent access")
	}
	if _, ok := r.Get("products"); !ok {
		t.Error("products should be registered after concurrent access")
	}
}

func TestGlobal_ReturnsSameInstance(t *testing.T) {
	g1 := entity.Global()
	g2 := entity.Global()
	if g1 != g2 {
		t.Error("Global() should return the same singleton instance on every call")
	}
}

func TestGlobal_IsUsable(t *testing.T) {
	g := entity.Global()
	if g == nil {
		t.Fatal("Global() must not return nil")
	}
	// The global registry accepts registrations without error.
	// Use a unique entity type to avoid cross-test pollution.
	reg := newUserRegistration()
	reg.EntityType = "global_test_entity"
	if err := g.Register(reg); err != nil {
		t.Fatalf("Global().Register() unexpected error: %v", err)
	}
	defer g.Unregister("global_test_entity")

	if _, ok := g.Get("global_test_entity"); !ok {
		t.Error("Global().Get() should find entity registered on Global()")
	}
}

func TestRegistry_FieldMeta_AllFieldsPreserved(t *testing.T) {
	r := entity.NewRegistry()
	if err := r.Register(newUserRegistration()); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}

	reg, _ := r.Get("users")
	if len(reg.Fields) != 3 {
		t.Fatalf("Fields len = %d, want 3", len(reg.Fields))
	}

	idField := reg.Fields[0]
	if !idField.IsPrimaryKey {
		t.Error("Fields[0].IsPrimaryKey should be true")
	}
	if idField.GoName != "ID" {
		t.Errorf("Fields[0].GoName = %q, want %q", idField.GoName, "ID")
	}
	if idField.JSONName != "id" {
		t.Errorf("Fields[0].JSONName = %q, want %q", idField.JSONName, "id")
	}

	emailField := reg.Fields[1]
	if !emailField.Indexed {
		t.Error("Fields[1].Indexed should be true")
	}
	if !emailField.Unique {
		t.Error("Fields[1].Unique should be true")
	}
}
