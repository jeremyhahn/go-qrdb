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

package statemachine

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// Registry: Resolve
// ---------------------------------------------------------------------------

func TestRegistry_Resolve_RegisteredType(t *testing.T) {
	r := NewRegistry()
	r.Register("test", func() sm.CreateFunc {
		return NewRegularCreateFunc()
	})

	createFn, err := r.Resolve("test")
	if err != nil {
		t.Fatalf("Resolve(test) returned unexpected error: %v", err)
	}
	if createFn == nil {
		t.Fatal("Resolve(test) returned nil CreateFunc")
	}
}

func TestRegistry_Resolve_UnknownType(t *testing.T) {
	r := NewRegistry()

	_, err := r.Resolve("nonexistent")
	if err == nil {
		t.Fatal("Resolve(nonexistent) expected error, got nil")
	}
	var unknownErr *UnknownSMTypeError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("error type = %T, want *UnknownSMTypeError", err)
	}
	if unknownErr.Name != "nonexistent" {
		t.Fatalf("UnknownSMTypeError.Name = %q, want %q", unknownErr.Name, "nonexistent")
	}
}

func TestRegistry_Resolve_OverriddenType(t *testing.T) {
	r := NewRegistry()

	callCount := 0
	original := func() sm.CreateFunc {
		callCount++
		return NewRegularCreateFunc()
	}
	override := func() sm.CreateFunc {
		callCount += 10
		return NewRegularCreateFunc()
	}

	r.Register("custom", original)
	r.Register("custom", override)

	_, err := r.Resolve("custom")
	if err != nil {
		t.Fatalf("Resolve(custom) error: %v", err)
	}
	// The override factory adds 10, the original adds 1. If override
	// was used, callCount should be 10.
	if callCount != 10 {
		t.Fatalf("callCount = %d, want 10 (override factory should be used)", callCount)
	}
}

func TestRegistry_Resolve_ReturnsNewCreateFuncPerCall(t *testing.T) {
	r := NewRegistry()

	callCount := 0
	r.Register("counter", func() sm.CreateFunc {
		callCount++
		return NewRegularCreateFunc()
	})

	_, err1 := r.Resolve("counter")
	_, err2 := r.Resolve("counter")
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected errors: %v, %v", err1, err2)
	}
	if callCount != 2 {
		t.Fatalf("factory called %d times, want 2 (fresh factory per Resolve)", callCount)
	}
}

// ---------------------------------------------------------------------------
// Registry: Register
// ---------------------------------------------------------------------------

func TestRegistry_Register_NilFactory(t *testing.T) {
	r := NewRegistry()

	// Registering nil is allowed (caller's responsibility), but Resolve
	// will panic when calling it. Verify Register doesn't panic.
	r.Register("nil-factory", nil)

	if !r.Has("nil-factory") {
		t.Fatal("expected Has(nil-factory) = true after Register")
	}
}

func TestRegistry_Register_MultipleTypes(t *testing.T) {
	r := NewRegistry()
	r.Register("a", func() sm.CreateFunc { return NewRegularCreateFunc() })
	r.Register("b", func() sm.CreateFunc { return NewRegularCreateFunc() })
	r.Register("c", func() sm.CreateFunc { return NewRegularCreateFunc() })

	if r.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", r.Len())
	}
}

// ---------------------------------------------------------------------------
// Registry: Names
// ---------------------------------------------------------------------------

func TestRegistry_Names_Empty(t *testing.T) {
	r := NewRegistry()
	names := r.Names()
	if len(names) != 0 {
		t.Fatalf("Names() = %v, want empty slice", names)
	}
}

func TestRegistry_Names_Sorted(t *testing.T) {
	r := NewRegistry()
	r.Register("zeta", func() sm.CreateFunc { return NewRegularCreateFunc() })
	r.Register("alpha", func() sm.CreateFunc { return NewRegularCreateFunc() })
	r.Register("middle", func() sm.CreateFunc { return NewRegularCreateFunc() })

	names := r.Names()
	if !sort.StringsAreSorted(names) {
		t.Fatalf("Names() = %v, expected sorted order", names)
	}
	expected := []string{"alpha", "middle", "zeta"}
	if len(names) != len(expected) {
		t.Fatalf("Names() has %d entries, want %d", len(names), len(expected))
	}
	for i, want := range expected {
		if names[i] != want {
			t.Fatalf("Names()[%d] = %q, want %q", i, names[i], want)
		}
	}
}

// ---------------------------------------------------------------------------
// Registry: Has
// ---------------------------------------------------------------------------

func TestRegistry_Has_Exists(t *testing.T) {
	r := NewRegistry()
	r.Register("present", func() sm.CreateFunc { return NewRegularCreateFunc() })

	if !r.Has("present") {
		t.Fatal("Has(present) = false, want true")
	}
}

func TestRegistry_Has_NotExists(t *testing.T) {
	r := NewRegistry()

	if r.Has("absent") {
		t.Fatal("Has(absent) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// Registry: Len
// ---------------------------------------------------------------------------

func TestRegistry_Len_Empty(t *testing.T) {
	r := NewRegistry()
	if r.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", r.Len())
	}
}

func TestRegistry_Len_AfterRegistrations(t *testing.T) {
	r := NewRegistry()
	r.Register("one", func() sm.CreateFunc { return NewRegularCreateFunc() })
	r.Register("two", func() sm.CreateFunc { return NewRegularCreateFunc() })

	if r.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", r.Len())
	}
}

func TestRegistry_Len_AfterOverride(t *testing.T) {
	r := NewRegistry()
	r.Register("same", func() sm.CreateFunc { return NewRegularCreateFunc() })
	r.Register("same", func() sm.CreateFunc { return NewRegularCreateFunc() })

	if r.Len() != 1 {
		t.Fatalf("Len() = %d, want 1 (override should not increase count)", r.Len())
	}
}

// ---------------------------------------------------------------------------
// Registry: Thread safety
// ---------------------------------------------------------------------------

func TestRegistry_ConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	factory := func() sm.CreateFunc { return NewRegularCreateFunc() }

	var wg sync.WaitGroup
	const goroutines = 50

	// Concurrent Register calls.
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := "type-" + string(rune('a'+idx%26))
			r.Register(name, factory)
		}(i)
	}

	// Concurrent Resolve, Has, Names, Len calls.
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Has("type-a")
			r.Names()
			r.Len()
			_, _ = r.Resolve("type-a")
		}()
	}

	wg.Wait()

	// Verify consistency.
	if r.Len() == 0 {
		t.Fatal("expected at least 1 registration after concurrent writes")
	}
	names := r.Names()
	if !sort.StringsAreSorted(names) {
		t.Fatalf("Names() = %v, expected sorted", names)
	}
}

// ---------------------------------------------------------------------------
// Default registry: pre-populated types
// ---------------------------------------------------------------------------

func TestDefaultRegistry_HasAllBuiltinTypes(t *testing.T) {
	expected := []string{"concurrent", "disk", "fast", "memory", "regular", "standard"}
	for _, name := range expected {
		if !Has(name) {
			t.Fatalf("default registry missing type %q", name)
		}
	}
}

func TestDefaultRegistry_MemoryAndStandardAreAliases(t *testing.T) {
	memFn, err := Resolve("memory")
	if err != nil {
		t.Fatalf("Resolve(memory) error: %v", err)
	}
	stdFn, err := Resolve("standard")
	if err != nil {
		t.Fatalf("Resolve(standard) error: %v", err)
	}

	if memFn == nil {
		t.Fatal("memory CreateFunc is nil")
	}
	if stdFn == nil {
		t.Fatal("standard CreateFunc is nil")
	}

	// Both should create StandardAdapter instances.
	memSM := memFn(1, 1)
	stdSM := stdFn(1, 1)

	if _, ok := memSM.(*StandardAdapter); !ok {
		t.Fatalf("memory CreateFunc produced %T, want *StandardAdapter", memSM)
	}
	if _, ok := stdSM.(*StandardAdapter); !ok {
		t.Fatalf("standard CreateFunc produced %T, want *StandardAdapter", stdSM)
	}
}

func TestDefaultRegistry_RegularIsLegacy(t *testing.T) {
	regFn, err := Resolve("regular")
	if err != nil {
		t.Fatalf("Resolve(regular) error: %v", err)
	}
	regSM := regFn(1, 1)
	if _, ok := regSM.(*RegularStateMachine); !ok {
		t.Fatalf("regular CreateFunc produced %T, want *RegularStateMachine", regSM)
	}
}

func TestDefaultRegistry_NamesAreSorted(t *testing.T) {
	names := Names()
	if !sort.StringsAreSorted(names) {
		t.Fatalf("Names() = %v, expected sorted order", names)
	}
}

func TestDefaultRegistry_Len(t *testing.T) {
	n := Len()
	// At minimum: memory, standard, regular, concurrent, fast, disk
	if n < 6 {
		t.Fatalf("Len() = %d, want >= 6", n)
	}
}

func TestDefaultRegistry_Resolve_Unknown(t *testing.T) {
	_, err := Resolve("nonexistent-sm-type")
	if err == nil {
		t.Fatal("Resolve(nonexistent-sm-type) expected error, got nil")
	}
	var unknownErr *UnknownSMTypeError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("error type = %T, want *UnknownSMTypeError", err)
	}
}

func TestDefaultRegistry_Default_ReturnsGlobalInstance(t *testing.T) {
	r := Default()
	if r == nil {
		t.Fatal("Default() returned nil")
	}
	// Should be the same instance as the package-level defaultRegistry.
	if r != defaultRegistry {
		t.Fatal("Default() did not return the package-level defaultRegistry")
	}
}

// ---------------------------------------------------------------------------
// Default registry: Register extends the global registry
// ---------------------------------------------------------------------------

func TestDefaultRegistry_Register_ExtendsGlobal(t *testing.T) {
	name := "test-custom-type-extend"

	// Clean up after test to avoid polluting other tests.
	defer func() {
		defaultRegistry.mu.Lock()
		delete(defaultRegistry.entries, name)
		defaultRegistry.mu.Unlock()
	}()

	Register(name, func() sm.CreateFunc {
		return NewRegularCreateFunc()
	})

	if !Has(name) {
		t.Fatalf("Has(%q) = false after Register", name)
	}

	createFn, err := Resolve(name)
	if err != nil {
		t.Fatalf("Resolve(%q) error: %v", name, err)
	}
	if createFn == nil {
		t.Fatalf("Resolve(%q) returned nil CreateFunc", name)
	}
}

// ---------------------------------------------------------------------------
// Default registry: Resolve produces working state machines
// ---------------------------------------------------------------------------

func TestDefaultRegistry_Resolve_Memory_ProducesStateMachine(t *testing.T) {
	createFn, err := Resolve("memory")
	if err != nil {
		t.Fatalf("Resolve(memory) error: %v", err)
	}

	rawSM := createFn(1, 1)
	adapter, ok := rawSM.(*StandardAdapter)
	if !ok {
		t.Fatalf("memory CreateFunc produced %T, want *StandardAdapter", rawSM)
	}

	// Verify the SM is functional with a simple Update + Lookup.
	cmd, err := kv.EncodePut([]byte("key1"), []byte("value1"))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := adapter.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update error: %v", err)
	}

	val, err := adapter.Lookup(context.Background(), []byte("key1"))
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	if string(val.([]byte)) != "value1" {
		t.Fatalf("Lookup = %q, want %q", val, "value1")
	}
}

func TestDefaultRegistry_Resolve_Concurrent_ProducesStateMachine(t *testing.T) {
	createFn, err := Resolve("concurrent")
	if err != nil {
		t.Fatalf("Resolve(concurrent) error: %v", err)
	}
	rawSM := createFn(1, 1)
	if rawSM == nil {
		t.Fatal("concurrent CreateFunc returned nil")
	}

	adapter, ok := rawSM.(*ConcurrentAdapter)
	if !ok {
		t.Fatalf("concurrent CreateFunc produced %T, want *ConcurrentAdapter", rawSM)
	}

	// Verify the SM is functional.
	cmd, err := kv.EncodePut([]byte("key-c"), []byte("val-c"))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := adapter.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update error: %v", err)
	}

	val, err := adapter.Lookup(context.Background(), []byte("key-c"))
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	if string(val.([]byte)) != "val-c" {
		t.Fatalf("Lookup = %q, want %q", val, "val-c")
	}

	// Close the SM.
	if err := adapter.Close(context.Background()); err != nil {
		t.Fatalf("Close error: %v", err)
	}
}

func TestDefaultRegistry_Resolve_Fast_ProducesStateMachine(t *testing.T) {
	createFn, err := Resolve("fast")
	if err != nil {
		t.Fatalf("Resolve(fast) error: %v", err)
	}
	rawSM := createFn(1, 1)
	if rawSM == nil {
		t.Fatal("fast CreateFunc returned nil")
	}

	adapter, ok := rawSM.(*StandardAdapter)
	if !ok {
		t.Fatalf("fast CreateFunc produced %T, want *StandardAdapter", rawSM)
	}

	// Verify the SM is functional.
	cmd, err := kv.EncodePut([]byte("key-f"), []byte("val-f"))
	if err != nil {
		t.Fatalf("EncodePut failed: %v", err)
	}
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := adapter.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update error: %v", err)
	}

	val, err := adapter.Lookup(context.Background(), []byte("key-f"))
	if err != nil {
		t.Fatalf("Lookup error: %v", err)
	}
	if string(val.([]byte)) != "val-f" {
		t.Fatalf("Lookup = %q, want %q", val, "val-f")
	}

	// Close the SM.
	if err := adapter.Close(context.Background()); err != nil {
		t.Fatalf("Close error: %v", err)
	}
}

func TestDefaultRegistry_Resolve_Disk_ProducesStateMachine(t *testing.T) {
	createFn, err := Resolve("disk")
	if err != nil {
		t.Fatalf("Resolve(disk) error: %v", err)
	}
	rawSM := createFn(1, 1)
	if rawSM == nil {
		t.Fatal("disk CreateFunc returned nil")
	}

	adapter, ok := rawSM.(*DiskAdapter)
	if !ok {
		t.Fatalf("disk CreateFunc produced %T, want *DiskAdapter", rawSM)
	}

	// Close the SM.
	if err := adapter.Close(context.Background()); err != nil {
		t.Fatalf("Close error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// NewFastCreateFunc
// ---------------------------------------------------------------------------

func TestNewFastCreateFunc_ProducesFastStateMachine(t *testing.T) {
	store := newMemStorage()
	storeFactory := func(shardID, replicaID uint64) storage.Storage {
		return store
	}

	createFn := NewFastCreateFunc(storeFactory)
	if createFn == nil {
		t.Fatal("NewFastCreateFunc returned nil")
	}

	rawSM := createFn(10, 20)
	fsm, ok := rawSM.(*FastStateMachine)
	if !ok {
		t.Fatalf("CreateFunc produced %T, want *FastStateMachine", rawSM)
	}
	if fsm.shardID != 10 {
		t.Fatalf("shardID = %d, want 10", fsm.shardID)
	}
	if fsm.replicaID != 20 {
		t.Fatalf("replicaID = %d, want 20", fsm.replicaID)
	}
}

func TestNewFastCreateFunc_WithOptions(t *testing.T) {
	store := newMemStorage()
	storeFactory := func(_, _ uint64) storage.Storage {
		return store
	}
	wm := &testWatchManager{}

	createFn := NewFastCreateFunc(storeFactory, WithWatchManager(wm))
	rawSM := createFn(1, 1)
	fsm := rawSM.(*FastStateMachine)

	if fsm.watchManager != wm {
		t.Fatal("expected WatchManager to be set via option")
	}
}

// ---------------------------------------------------------------------------
// Registry: ResolveTyped
// ---------------------------------------------------------------------------

func TestRegistry_ResolveTyped_EmptyBackend_FallsBackToType(t *testing.T) {
	r := NewRegistry()
	r.Register("memory", func() sm.CreateFunc {
		return NewRegularCreateFunc()
	})

	createFn, err := r.ResolveTyped("memory", "")
	if err != nil {
		t.Fatalf("ResolveTyped(memory, \"\") returned unexpected error: %v", err)
	}
	if createFn == nil {
		t.Fatal("ResolveTyped(memory, \"\") returned nil CreateFunc")
	}
}

func TestRegistry_ResolveTyped_BackendOnly(t *testing.T) {
	r := NewRegistry()
	r.Register("concurrent", func() sm.CreateFunc {
		return NewRegularCreateFunc()
	})

	createFn, err := r.ResolveTyped("kv", "concurrent")
	if err != nil {
		t.Fatalf("ResolveTyped(kv, concurrent) returned unexpected error: %v", err)
	}
	if createFn == nil {
		t.Fatal("ResolveTyped(kv, concurrent) returned nil CreateFunc")
	}
}

func TestRegistry_ResolveTyped_CompositeKey(t *testing.T) {
	r := NewRegistry()

	compositeCallCount := 0
	r.Register("kv:standard", func() sm.CreateFunc {
		compositeCallCount++
		return NewRegularCreateFunc()
	})
	// Also register the plain backend to verify composite takes priority.
	r.Register("standard", func() sm.CreateFunc {
		return NewRegularCreateFunc()
	})

	createFn, err := r.ResolveTyped("kv", "standard")
	if err != nil {
		t.Fatalf("ResolveTyped(kv, standard) returned unexpected error: %v", err)
	}
	if createFn == nil {
		t.Fatal("ResolveTyped(kv, standard) returned nil CreateFunc")
	}
	if compositeCallCount != 1 {
		t.Fatalf("compositeCallCount = %d, want 1 (composite key should take priority)", compositeCallCount)
	}
}

func TestRegistry_ResolveTyped_UnknownTypeAndBackend(t *testing.T) {
	r := NewRegistry()

	_, err := r.ResolveTyped("nonexistent", "also-nonexistent")
	if err == nil {
		t.Fatal("ResolveTyped(nonexistent, also-nonexistent) expected error, got nil")
	}
	var unknownErr *UnknownSMTypeError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("error type = %T, want *UnknownSMTypeError", err)
	}
}

func TestRegistry_ResolveTyped_EmptyBackend_UnknownType(t *testing.T) {
	r := NewRegistry()

	_, err := r.ResolveTyped("nonexistent", "")
	if err == nil {
		t.Fatal("ResolveTyped(nonexistent, \"\") expected error, got nil")
	}
	var unknownErr *UnknownSMTypeError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("error type = %T, want *UnknownSMTypeError", err)
	}
}

func TestDefaultRegistry_ResolveTyped_PackageLevel(t *testing.T) {
	// Verify the package-level ResolveTyped function works.
	createFn, err := ResolveTyped("memory", "")
	if err != nil {
		t.Fatalf("ResolveTyped(memory, \"\") returned unexpected error: %v", err)
	}
	if createFn == nil {
		t.Fatal("ResolveTyped(memory, \"\") returned nil CreateFunc")
	}
}

func TestDefaultRegistry_ResolveTyped_WithBackend(t *testing.T) {
	// "concurrent" is a registered backend in the default registry.
	createFn, err := ResolveTyped("kv", "concurrent")
	if err != nil {
		t.Fatalf("ResolveTyped(kv, concurrent) returned unexpected error: %v", err)
	}
	if createFn == nil {
		t.Fatal("ResolveTyped(kv, concurrent) returned nil CreateFunc")
	}

	// Verify the returned SM is a ConcurrentAdapter.
	rawSM := createFn(1, 1)
	if _, ok := rawSM.(*ConcurrentAdapter); !ok {
		t.Fatalf("ResolveTyped(kv, concurrent) produced %T, want *ConcurrentAdapter", rawSM)
	}
}

// ---------------------------------------------------------------------------
// UnknownSMTypeError
// ---------------------------------------------------------------------------

func TestUnknownSMTypeError_Message(t *testing.T) {
	err := &UnknownSMTypeError{Name: "badtype"}
	want := `statemachine: unknown type "badtype"`
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestUnknownSMTypeError_ErrorsAs(t *testing.T) {
	var err error = &UnknownSMTypeError{Name: "missing"}
	var target *UnknownSMTypeError
	if !errors.As(err, &target) {
		t.Fatal("errors.As failed for *UnknownSMTypeError")
	}
	if target.Name != "missing" {
		t.Fatalf("Name = %q, want %q", target.Name, "missing")
	}
}

// ---------------------------------------------------------------------------
// NewRegistry
// ---------------------------------------------------------------------------

func TestNewRegistry_Empty(t *testing.T) {
	r := NewRegistry()
	if r == nil {
		t.Fatal("NewRegistry returned nil")
	}
	if r.Len() != 0 {
		t.Fatalf("new registry has %d entries, want 0", r.Len())
	}
	if len(r.Names()) != 0 {
		t.Fatalf("new registry Names() = %v, want empty", r.Names())
	}
}

// ---------------------------------------------------------------------------
// Registry: RegisterWithDescriptor
// ---------------------------------------------------------------------------

func TestRegistry_RegisterWithDescriptor_SetsMetadata(t *testing.T) {
	r := NewRegistry()
	desc := Descriptor{
		Description:  "test description",
		Persistence:  PersistenceDisk,
		Kind:         SMKindDisk,
		Capabilities: []Capability{CapabilityKV, CapabilityMVCC},
	}
	r.RegisterWithDescriptor("test-desc", func() sm.CreateFunc {
		return NewRegularCreateFunc()
	}, desc)

	got, err := r.Describe("test-desc")
	if err != nil {
		t.Fatalf("Describe error: %v", err)
	}
	if got.Name != "test-desc" {
		t.Fatalf("Name = %q, want %q", got.Name, "test-desc")
	}
	if got.Description != "test description" {
		t.Fatalf("Description = %q, want %q", got.Description, "test description")
	}
	if got.Persistence != PersistenceDisk {
		t.Fatalf("Persistence = %q, want %q", got.Persistence, PersistenceDisk)
	}
	if got.Kind != SMKindDisk {
		t.Fatalf("Kind = %q, want %q", got.Kind, SMKindDisk)
	}
	if len(got.Capabilities) != 2 {
		t.Fatalf("Capabilities len = %d, want 2", len(got.Capabilities))
	}
}

func TestRegistry_RegisterWithDescriptor_OverridesName(t *testing.T) {
	r := NewRegistry()
	desc := Descriptor{
		Name:        "wrong-name",
		Description: "test",
	}
	r.RegisterWithDescriptor("correct-name", func() sm.CreateFunc {
		return NewRegularCreateFunc()
	}, desc)

	got, err := r.Describe("correct-name")
	if err != nil {
		t.Fatalf("Describe error: %v", err)
	}
	if got.Name != "correct-name" {
		t.Fatalf("Name = %q, want %q (should override descriptor name)", got.Name, "correct-name")
	}
}

// ---------------------------------------------------------------------------
// Registry: Describe
// ---------------------------------------------------------------------------

func TestRegistry_Describe_Unknown(t *testing.T) {
	r := NewRegistry()

	_, err := r.Describe("nonexistent")
	if err == nil {
		t.Fatal("Describe(nonexistent) expected error, got nil")
	}
	var unknownErr *UnknownSMTypeError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("error type = %T, want *UnknownSMTypeError", err)
	}
}

func TestRegistry_Describe_DefaultDescriptor(t *testing.T) {
	r := NewRegistry()
	r.Register("simple", func() sm.CreateFunc {
		return NewRegularCreateFunc()
	})

	got, err := r.Describe("simple")
	if err != nil {
		t.Fatalf("Describe error: %v", err)
	}
	if got.Name != "simple" {
		t.Fatalf("Name = %q, want %q", got.Name, "simple")
	}
	if got.Description != "" {
		t.Fatalf("Description = %q, want empty (default descriptor)", got.Description)
	}
}

// ---------------------------------------------------------------------------
// Registry: Descriptors
// ---------------------------------------------------------------------------

func TestRegistry_Descriptors_Empty(t *testing.T) {
	r := NewRegistry()
	descs := r.Descriptors()
	if len(descs) != 0 {
		t.Fatalf("Descriptors() = %v, want empty slice", descs)
	}
}

func TestRegistry_Descriptors_Sorted(t *testing.T) {
	r := NewRegistry()
	r.RegisterWithDescriptor("zeta", func() sm.CreateFunc {
		return NewRegularCreateFunc()
	}, Descriptor{Description: "zeta sm"})
	r.RegisterWithDescriptor("alpha", func() sm.CreateFunc {
		return NewRegularCreateFunc()
	}, Descriptor{Description: "alpha sm"})

	descs := r.Descriptors()
	if len(descs) != 2 {
		t.Fatalf("Descriptors() len = %d, want 2", len(descs))
	}
	if descs[0].Name != "alpha" {
		t.Fatalf("Descriptors()[0].Name = %q, want %q", descs[0].Name, "alpha")
	}
	if descs[1].Name != "zeta" {
		t.Fatalf("Descriptors()[1].Name = %q, want %q", descs[1].Name, "zeta")
	}
}

func TestRegistry_Descriptors_PreservesMetadata(t *testing.T) {
	r := NewRegistry()
	r.RegisterWithDescriptor("test", func() sm.CreateFunc {
		return NewRegularCreateFunc()
	}, Descriptor{
		Description:  "test sm",
		Persistence:  PersistenceHybrid,
		Kind:         SMKindRegular,
		Capabilities: []Capability{CapabilityKV, CapabilityWatch},
	})

	descs := r.Descriptors()
	if len(descs) != 1 {
		t.Fatalf("Descriptors() len = %d, want 1", len(descs))
	}
	d := descs[0]
	if d.Persistence != PersistenceHybrid {
		t.Fatalf("Persistence = %q, want %q", d.Persistence, PersistenceHybrid)
	}
	if len(d.Capabilities) != 2 {
		t.Fatalf("Capabilities len = %d, want 2", len(d.Capabilities))
	}
}

// ---------------------------------------------------------------------------
// Default registry: Descriptor metadata for built-in types
// ---------------------------------------------------------------------------

func TestDefaultRegistry_BuiltinDescriptors(t *testing.T) {
	tests := []struct {
		name        string
		persistence PersistenceType
		kind        SMKind
	}{
		{"memory", PersistenceMemory, SMKindRegular},
		{"standard", PersistenceMemory, SMKindRegular},
		{"regular", PersistenceMemory, SMKindRegular},
		{"concurrent", PersistenceMemory, SMKindConcurrent},
		{"fast", PersistenceHybrid, SMKindRegular},
		{"disk", PersistenceDisk, SMKindDisk},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc, err := Describe(tt.name)
			if err != nil {
				t.Fatalf("Describe(%q) error: %v", tt.name, err)
			}
			if desc.Name != tt.name {
				t.Fatalf("Name = %q, want %q", desc.Name, tt.name)
			}
			if desc.Persistence != tt.persistence {
				t.Fatalf("Persistence = %q, want %q", desc.Persistence, tt.persistence)
			}
			if desc.Kind != tt.kind {
				t.Fatalf("Kind = %q, want %q", desc.Kind, tt.kind)
			}
			if desc.Description == "" {
				t.Fatal("Description should not be empty for built-in types")
			}
			if len(desc.Capabilities) == 0 {
				t.Fatal("Capabilities should not be empty for built-in types")
			}
		})
	}
}

func TestDefaultRegistry_Descriptors_ReturnsAll(t *testing.T) {
	descs := Descriptors()
	if len(descs) < 6 {
		t.Fatalf("Descriptors() len = %d, want >= 6", len(descs))
	}

	// Verify sorted order.
	for i := 1; i < len(descs); i++ {
		if descs[i].Name < descs[i-1].Name {
			t.Fatalf("Descriptors() not sorted: %q < %q at index %d", descs[i].Name, descs[i-1].Name, i)
		}
	}
}

func TestDefaultRegistry_Describe_Unknown(t *testing.T) {
	_, err := Describe("nonexistent-sm-type")
	if err == nil {
		t.Fatal("Describe(nonexistent-sm-type) expected error, got nil")
	}
	var unknownErr *UnknownSMTypeError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("error type = %T, want *UnknownSMTypeError", err)
	}
}

// ---------------------------------------------------------------------------
// Package-level RegisterWithDescriptor
// ---------------------------------------------------------------------------

func TestDefaultRegistry_RegisterWithDescriptor_ExtendsGlobal(t *testing.T) {
	name := "test-pkg-desc-extend"
	defer func() {
		defaultRegistry.mu.Lock()
		delete(defaultRegistry.entries, name)
		defaultRegistry.mu.Unlock()
	}()

	desc := Descriptor{
		Description:  "test pkg-level registration",
		Persistence:  PersistenceDisk,
		Kind:         SMKindDisk,
		Capabilities: []Capability{CapabilityMVCC},
	}
	RegisterWithDescriptor(name, func() sm.CreateFunc {
		return NewRegularCreateFunc()
	}, desc)

	if !Has(name) {
		t.Fatalf("Has(%q) = false after RegisterWithDescriptor", name)
	}

	got, err := Describe(name)
	if err != nil {
		t.Fatalf("Describe(%q) error: %v", name, err)
	}
	if got.Description != "test pkg-level registration" {
		t.Fatalf("Description = %q, want %q", got.Description, "test pkg-level registration")
	}
}

// ---------------------------------------------------------------------------
// PersistenceType and Capability constants
// ---------------------------------------------------------------------------

func TestPersistenceTypeValues(t *testing.T) {
	if PersistenceMemory != "memory" {
		t.Fatalf("PersistenceMemory = %q, want %q", PersistenceMemory, "memory")
	}
	if PersistenceDisk != "disk" {
		t.Fatalf("PersistenceDisk = %q, want %q", PersistenceDisk, "disk")
	}
	if PersistenceHybrid != "hybrid" {
		t.Fatalf("PersistenceHybrid = %q, want %q", PersistenceHybrid, "hybrid")
	}
}

func TestSMKindValues(t *testing.T) {
	if SMKindRegular != "regular" {
		t.Fatalf("SMKindRegular = %q, want %q", SMKindRegular, "regular")
	}
	if SMKindDisk != "disk" {
		t.Fatalf("SMKindDisk = %q, want %q", SMKindDisk, "disk")
	}
}

func TestCapabilityValues(t *testing.T) {
	caps := []struct {
		cap  Capability
		want string
	}{
		{CapabilityKV, "kv"},
		{CapabilityMVCC, "mvcc"},
		{CapabilityTimeSeries, "timeseries"},
		{CapabilityVector, "vector"},
		{CapabilityIndex, "index"},
		{CapabilityLease, "lease"},
		{CapabilityWatch, "watch"},
		{CapabilityTransaction, "transaction"},
		{CapabilityBatch, "batch"},
	}
	for _, tt := range caps {
		if string(tt.cap) != tt.want {
			t.Fatalf("Capability %q != %q", tt.cap, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// testWatchManager is a minimal WatchManager for option tests.
// ---------------------------------------------------------------------------

type testWatchManager struct {
	notifications int
}

func (w *testWatchManager) Notify(key string, value []byte, revision uint64, deleted bool) {
	w.notifications++
}
