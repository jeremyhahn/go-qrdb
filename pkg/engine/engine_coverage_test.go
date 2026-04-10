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

package engine

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// ---------------------------------------------------------------------------
// WithKVDatabase — option function (0% coverage)
// ---------------------------------------------------------------------------

// TestWithKVDatabase_SetsDatabase verifies that the WithKVDatabase option
// stores the database name on the KVEngine.
func TestWithKVDatabase_SetsDatabase(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg, WithKVDatabase("mydb"))
	if eng.database != "mydb" {
		t.Errorf("expected database = %q, got %q", "mydb", eng.database)
	}
}

// TestWithKVDatabase_EmptyString verifies that an empty database string
// leaves the engine in backward-compatible mode.
func TestWithKVDatabase_EmptyString(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg, WithKVDatabase(""))
	if eng.database != "" {
		t.Errorf("expected empty database, got %q", eng.database)
	}
}

// ---------------------------------------------------------------------------
// buildIndexName — database-prefix path
// ---------------------------------------------------------------------------

// TestBuildIndexName_WithDatabase verifies that the database prefix is
// prepended when a database namespace is configured.
func TestBuildIndexName_WithDatabase(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg, WithKVDatabase("mydb"))
	name := eng.buildIndexName("users", "email")
	want := "mydb.users.email"
	if name != want {
		t.Errorf("buildIndexName = %q, want %q", name, want)
	}
}

// TestBuildIndexName_WithoutDatabase verifies the legacy format when no
// database namespace is configured.
func TestBuildIndexName_WithoutDatabase(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	name := eng.buildIndexName("users", "email")
	want := "users.email"
	if name != want {
		t.Errorf("buildIndexName = %q, want %q", name, want)
	}
}

// ---------------------------------------------------------------------------
// defaultLogger — non-nil logger path inside RegisterEntityType
// ---------------------------------------------------------------------------

// TestDefaultLogger_CreatesValidLogger verifies that defaultLogger returns
// a non-nil logger without panicking.
func TestDefaultLogger_CreatesValidLogger(t *testing.T) {
	log := defaultLogger()
	if log == nil {
		t.Fatal("defaultLogger() returned nil")
	}
}

// TestRegisterEntityType_UsesDefaultLogger verifies that RegisterEntityType
// creates a default logger when none is configured on the engine.
func TestRegisterEntityType_UsesDefaultLogger(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	// Create engine with no logger configured.
	eng := NewKVEngine(stg)

	// Register a simple entity type with one index — exercises the defaultLogger() path.
	err := eng.RegisterEntityType("products", []EntityIndex{
		{FieldName: "name", JSONFieldName: "name"},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType: unexpected error: %v", err)
	}
}

// TestRegisterEntityType_WithDatabase verifies that RegisterEntityType uses
// the database-qualified index name when WithKVDatabase is set.
func TestRegisterEntityType_WithDatabase(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg, WithKVDatabase("catalog"))
	err := eng.RegisterEntityType("items", []EntityIndex{
		{FieldName: "sku", JSONFieldName: "sku"},
	})
	if err != nil {
		t.Fatalf("RegisterEntityType with database: unexpected error: %v", err)
	}

	// The index should be registered under the qualified name.
	idxName := "catalog.items.sku"
	if _, ok := eng.indexes[idxName]; !ok {
		t.Errorf("expected index %q to be registered, got indexes: %v", idxName, eng.indexes)
	}
}
