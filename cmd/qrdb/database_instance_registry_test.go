//go:build qrdb_embedded

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

package main

import (
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

func newTestRegistry(t *testing.T) *DatabaseInstanceRegistry {
	t.Helper()
	return NewDatabaseInstanceRegistry(testLogger(t))
}

func newNilInstance() *embeddedDatabaseInstance {
	return &embeddedDatabaseInstance{}
}

func TestDatabaseInstanceRegistry_Register_Success(t *testing.T) {
	reg := newTestRegistry(t)

	err := reg.Register("mydb", newNilInstance())
	if err != nil {
		t.Fatalf("Register returned unexpected error: %v", err)
	}
	if got := reg.Count(); got != 1 {
		t.Fatalf("Count: got %d, want 1", got)
	}
}

func TestDatabaseInstanceRegistry_Register_Duplicate(t *testing.T) {
	reg := newTestRegistry(t)

	if err := reg.Register("mydb", newNilInstance()); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	err := reg.Register("mydb", newNilInstance())
	if err == nil {
		t.Fatal("expected error on duplicate Register, got nil")
	}

	var target *InstanceAlreadyExistsError
	if !errors.As(err, &target) {
		t.Fatalf("error type: got %T, want *InstanceAlreadyExistsError", err)
	}
	if target.Name != "mydb" {
		t.Fatalf("error Name: got %q, want %q", target.Name, "mydb")
	}
	if got := reg.Count(); got != 1 {
		t.Fatalf("Count after duplicate: got %d, want 1", got)
	}
}

func TestDatabaseInstanceRegistry_Get_Success(t *testing.T) {
	reg := newTestRegistry(t)
	inst := newNilInstance()

	if err := reg.Register("mydb", inst); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := reg.Get("mydb")
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if got != inst {
		t.Fatal("Get returned different instance pointer")
	}
}

func TestDatabaseInstanceRegistry_Get_NotFound(t *testing.T) {
	reg := newTestRegistry(t)

	_, err := reg.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error on Get for missing instance, got nil")
	}

	var target *InstanceNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("error type: got %T, want *InstanceNotFoundError", err)
	}
	if target.Name != "nonexistent" {
		t.Fatalf("error Name: got %q, want %q", target.Name, "nonexistent")
	}
}

func TestDatabaseInstanceRegistry_Stop_Success(t *testing.T) {
	reg := newTestRegistry(t)

	if err := reg.Register("mydb", newNilInstance()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := reg.Stop("mydb"); err != nil {
		t.Fatalf("Stop returned unexpected error: %v", err)
	}

	if got := reg.Count(); got != 0 {
		t.Fatalf("Count after Stop: got %d, want 0", got)
	}

	_, err := reg.Get("mydb")
	var target *InstanceNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("Get after Stop: expected InstanceNotFoundError, got %v", err)
	}
}

func TestDatabaseInstanceRegistry_Stop_NotFound(t *testing.T) {
	reg := newTestRegistry(t)

	err := reg.Stop("nonexistent")
	if err == nil {
		t.Fatal("expected error on Stop for missing instance, got nil")
	}

	var target *InstanceNotFoundError
	if !errors.As(err, &target) {
		t.Fatalf("error type: got %T, want *InstanceNotFoundError", err)
	}
	if target.Name != "nonexistent" {
		t.Fatalf("error Name: got %q, want %q", target.Name, "nonexistent")
	}
}

func TestDatabaseInstanceRegistry_StopAll_Success(t *testing.T) {
	reg := newTestRegistry(t)

	for _, name := range []string{"db1", "db2", "db3"} {
		if err := reg.Register(name, newNilInstance()); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
	}

	if got := reg.Count(); got != 3 {
		t.Fatalf("Count before StopAll: got %d, want 3", got)
	}

	if err := reg.StopAll(); err != nil {
		t.Fatalf("StopAll returned unexpected error: %v", err)
	}

	if got := reg.Count(); got != 0 {
		t.Fatalf("Count after StopAll: got %d, want 0", got)
	}

	if names := reg.Names(); len(names) != 0 {
		t.Fatalf("Names after StopAll: got %v, want empty", names)
	}
}

func TestDatabaseInstanceRegistry_StopAll_Empty(t *testing.T) {
	reg := newTestRegistry(t)

	if err := reg.StopAll(); err != nil {
		t.Fatalf("StopAll on empty registry returned unexpected error: %v", err)
	}

	if got := reg.Count(); got != 0 {
		t.Fatalf("Count: got %d, want 0", got)
	}
}

func TestDatabaseInstanceRegistry_Names_Success(t *testing.T) {
	reg := newTestRegistry(t)

	expected := []string{"alpha", "bravo", "charlie"}
	for _, name := range expected {
		if err := reg.Register(name, newNilInstance()); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
	}

	got := reg.Names()
	sort.Strings(got)
	sort.Strings(expected)

	if len(got) != len(expected) {
		t.Fatalf("Names length: got %d, want %d", len(got), len(expected))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("Names[%d]: got %q, want %q", i, got[i], expected[i])
		}
	}
}

func TestDatabaseInstanceRegistry_Count_Empty(t *testing.T) {
	reg := newTestRegistry(t)

	if got := reg.Count(); got != 0 {
		t.Fatalf("Count on new registry: got %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// embeddedDatabaseInstance.Close — branch coverage
// ---------------------------------------------------------------------------

// TestEmbeddedDatabaseInstance_Close_AllNil verifies Close succeeds when all
// fields are nil (no-op path).
func TestEmbeddedDatabaseInstance_Close_AllNil(t *testing.T) {
	inst := &embeddedDatabaseInstance{}
	if err := inst.Close(); err != nil {
		t.Fatalf("Close with all-nil fields: %v", err)
	}
}

// TestEmbeddedDatabaseInstance_Close_WithWatchHub verifies Close calls
// watchHub.Close() when watchHub is non-nil.
func TestEmbeddedDatabaseInstance_Close_WithWatchHub(t *testing.T) {
	hub := watch.NewHub()
	inst := &embeddedDatabaseInstance{watchHub: hub}
	if err := inst.Close(); err != nil {
		t.Fatalf("Close with watchHub: %v", err)
	}
}

// TestEmbeddedDatabaseInstance_Close_WithWatchHubAndNilRest verifies the
// nil-guard for vectorStorage, host, and localBackend when only watchHub is set.
func TestEmbeddedDatabaseInstance_Close_NilVectorStorage(t *testing.T) {
	hub := watch.NewHub()
	inst := &embeddedDatabaseInstance{watchHub: hub, vectorStorage: nil, host: nil, localBackend: nil}
	if err := inst.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestDatabaseInstanceRegistry_ConcurrentAccess(t *testing.T) {
	reg := newTestRegistry(t)

	const numGoroutines = 50
	var wg sync.WaitGroup
	wg.Add(numGoroutines * 2)

	// Half the goroutines register unique instances.
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			name := "db-" + string(rune('A'+id))
			_ = reg.Register(name, newNilInstance())
		}(i)
	}

	// Half the goroutines attempt Get operations (may or may not find entries).
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			name := "db-" + string(rune('A'+id))
			_, _ = reg.Get(name)
		}(i)
	}

	wg.Wait()

	// Verify the count is consistent with the number of successfully
	// registered instances.
	count := reg.Count()
	names := reg.Names()
	if int64(len(names)) != count {
		t.Fatalf("Count/Names mismatch: Count=%d, len(Names)=%d", count, len(names))
	}
}
