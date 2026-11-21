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

package shard

import (
	"sync"
	"testing"
)

func TestNewRegistry(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry() returned nil")
	}
	if r.Count() != 0 {
		t.Errorf("Count() = %d, want 0", r.Count())
	}
}

func TestNewRegistryEmptyList(t *testing.T) {
	r := NewRegistry()
	list := r.List()
	if len(list) != 0 {
		t.Errorf("List() length = %d, want 0", len(list))
	}
}

// --- Register ---

func TestRegisterSuccess(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ShardKV, TypeKV); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	if r.Count() != 1 {
		t.Errorf("Count() = %d, want 1", r.Count())
	}
}

func TestRegisterMultipleShards(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ShardKV, TypeKV); err != nil {
		t.Fatalf("Register(KV) unexpected error: %v", err)
	}
	if err := r.Register(ShardVector, TypeVector); err != nil {
		t.Fatalf("Register(Vector) unexpected error: %v", err)
	}
	if err := r.Register(ShardTimeSeries, TypeTimeSeries); err != nil {
		t.Fatalf("Register(TimeSeries) unexpected error: %v", err)
	}
	if r.Count() != 3 {
		t.Errorf("Count() = %d, want 3", r.Count())
	}
}

func TestRegisterDuplicateReturnsError(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ShardKV, TypeKV); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	err := r.Register(ShardKV, TypeKV)
	if err == nil {
		t.Fatal("Register() expected error for duplicate, got nil")
	}
	if _, ok := err.(*ShardAlreadyRegisteredError); !ok {
		t.Errorf("Register() error type = %T, want *ShardAlreadyRegisteredError", err)
	}
}

func TestRegisterZeroIDReturnsError(t *testing.T) {
	r := NewRegistry()
	err := r.Register(0, TypeKV)
	if err == nil {
		t.Fatal("Register(0) expected error, got nil")
	}
	if _, ok := err.(*InvalidShardIDError); !ok {
		t.Errorf("Register(0) error type = %T, want *InvalidShardIDError", err)
	}
}

// --- Unregister ---

func TestUnregisterSuccess(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ShardKV, TypeKV); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	if err := r.Unregister(ShardKV); err != nil {
		t.Fatalf("Unregister() unexpected error: %v", err)
	}
	if r.Count() != 0 {
		t.Errorf("Count() = %d, want 0", r.Count())
	}
}

func TestUnregisterNotRegisteredReturnsError(t *testing.T) {
	r := NewRegistry()
	err := r.Unregister(ShardKV)
	if err == nil {
		t.Fatal("Unregister() expected error, got nil")
	}
	if _, ok := err.(*ShardNotRegisteredError); !ok {
		t.Errorf("Unregister() error type = %T, want *ShardNotRegisteredError", err)
	}
}

// --- Get ---

func TestGetSuccess(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ShardVector, TypeVector); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	got, err := r.Get(ShardVector)
	if err != nil {
		t.Fatalf("Get() unexpected error: %v", err)
	}
	if got != TypeVector {
		t.Errorf("Get() = %q, want %q", got, TypeVector)
	}
}

func TestGetNotRegisteredReturnsError(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get(ShardKV)
	if err == nil {
		t.Fatal("Get() expected error, got nil")
	}
	if _, ok := err.(*ShardNotRegisteredError); !ok {
		t.Errorf("Get() error type = %T, want *ShardNotRegisteredError", err)
	}
}

// --- List ---

func TestListReturnsAllShards(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ShardKV, TypeKV); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	if err := r.Register(ShardVector, TypeVector); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	list := r.List()
	if len(list) != 2 {
		t.Fatalf("List() length = %d, want 2", len(list))
	}
	if list[ShardKV] != TypeKV {
		t.Errorf("List()[%d] = %q, want %q", ShardKV, list[ShardKV], TypeKV)
	}
	if list[ShardVector] != TypeVector {
		t.Errorf("List()[%d] = %q, want %q", ShardVector, list[ShardVector], TypeVector)
	}
}

func TestListReturnsCopy(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ShardKV, TypeKV); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	list := r.List()
	// Mutate the returned map.
	list[ShardKV] = TypeSystem
	list[999] = TypeUser

	// Verify the registry is unaffected.
	got, err := r.Get(ShardKV)
	if err != nil {
		t.Fatalf("Get() unexpected error: %v", err)
	}
	if got != TypeKV {
		t.Errorf("Get() = %q, want %q after mutating List() copy", got, TypeKV)
	}
	if r.Count() != 1 {
		t.Errorf("Count() = %d, want 1 after mutating List() copy", r.Count())
	}
}

// --- Count ---

func TestCountAfterRegisterAndUnregister(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ShardKV, TypeKV); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	if err := r.Register(ShardVector, TypeVector); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	if r.Count() != 2 {
		t.Errorf("Count() = %d, want 2", r.Count())
	}
	if err := r.Unregister(ShardKV); err != nil {
		t.Fatalf("Unregister() unexpected error: %v", err)
	}
	if r.Count() != 1 {
		t.Errorf("Count() = %d, want 1 after Unregister", r.Count())
	}
}

// --- IsRegistered ---

func TestIsRegisteredTrue(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ShardKV, TypeKV); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	if !r.IsRegistered(ShardKV) {
		t.Error("IsRegistered() = false, want true")
	}
}

func TestIsRegisteredFalse(t *testing.T) {
	r := NewRegistry()
	if r.IsRegistered(ShardKV) {
		t.Error("IsRegistered() = true, want false")
	}
}

func TestIsRegisteredAfterUnregister(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ShardKV, TypeKV); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	if err := r.Unregister(ShardKV); err != nil {
		t.Fatalf("Unregister() unexpected error: %v", err)
	}
	if r.IsRegistered(ShardKV) {
		t.Error("IsRegistered() = true after Unregister, want false")
	}
}

// --- Concurrency ---

func TestRegistryConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	const goroutines = 100

	// Concurrent registrations of unique shard IDs.
	wg.Add(goroutines)
	for i := uint64(1); i <= goroutines; i++ {
		go func(id uint64) {
			defer wg.Done()
			_ = r.Register(id, TypeUser)
		}(i)
	}
	wg.Wait()

	if r.Count() != goroutines {
		t.Errorf("Count() = %d, want %d after concurrent registrations", r.Count(), goroutines)
	}

	// Concurrent reads.
	wg.Add(goroutines)
	for i := uint64(1); i <= goroutines; i++ {
		go func(id uint64) {
			defer wg.Done()
			_, _ = r.Get(id)
			r.IsRegistered(id)
			r.List()
			r.Count()
		}(i)
	}
	wg.Wait()

	// Concurrent unregistrations.
	wg.Add(goroutines)
	for i := uint64(1); i <= goroutines; i++ {
		go func(id uint64) {
			defer wg.Done()
			_ = r.Unregister(id)
		}(i)
	}
	wg.Wait()

	if r.Count() != 0 {
		t.Errorf("Count() = %d, want 0 after concurrent unregistrations", r.Count())
	}
}

func TestRegistryConcurrentMixedOperations(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup

	// Pre-populate some shards.
	for i := uint64(1); i <= 10; i++ {
		if err := r.Register(i, TypeKV); err != nil {
			t.Fatalf("Register(%d) unexpected error: %v", i, err)
		}
	}

	// Run mixed concurrent operations.
	wg.Add(40)
	for i := uint64(0); i < 10; i++ {
		go func(id uint64) {
			defer wg.Done()
			_, _ = r.Get(id + 1)
		}(i)
		go func(id uint64) {
			defer wg.Done()
			r.IsRegistered(id + 1)
		}(i)
		go func(id uint64) {
			defer wg.Done()
			r.List()
		}(i)
		go func(id uint64) {
			defer wg.Done()
			_ = r.Register(id+100, TypeUser)
		}(i)
	}
	wg.Wait()
}

// --- Re-register after unregister ---

func TestRegisterAfterUnregister(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ShardKV, TypeKV); err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	if err := r.Unregister(ShardKV); err != nil {
		t.Fatalf("Unregister() unexpected error: %v", err)
	}
	// Re-register with a different type.
	if err := r.Register(ShardKV, TypeSystem); err != nil {
		t.Fatalf("re-Register() unexpected error: %v", err)
	}
	got, err := r.Get(ShardKV)
	if err != nil {
		t.Fatalf("Get() unexpected error: %v", err)
	}
	if got != TypeSystem {
		t.Errorf("Get() = %q, want %q after re-register", got, TypeSystem)
	}
}
