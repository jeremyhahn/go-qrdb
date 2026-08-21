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

package sdk_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// uncacheableStore is a KVStore whose dynamic type is a map, so it satisfies
// the interface and cannot be a map key. Nothing in the KVStore contract
// forbids this shape, which is the whole reason the predicate exists.
type uncacheableStore map[string][]byte

func (s uncacheableStore) Put(_ context.Context, key string, value []byte) error {
	s[key] = value
	return nil
}
func (s uncacheableStore) Get(_ context.Context, key string) ([]byte, error) {
	return s[key], nil
}
func (s uncacheableStore) Delete(_ context.Context, key string) error {
	delete(s, key)
	return nil
}
func (s uncacheableStore) Scan(_ context.Context, _ string, _ func(string, []byte) error) error {
	return nil
}
func (s uncacheableStore) List(_ context.Context, _ string) ([]string, error) { return nil, nil }
func (s uncacheableStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := s[key]
	return ok, nil
}
func (s uncacheableStore) RegisterEntityIndexes(_ string, _ []sdk.EntityIndex) error {
	return sdk.ErrKVIndexUnsupported
}
func (s uncacheableStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, sdk.ErrKVIndexUnsupported
}
func (s uncacheableStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, sdk.ErrKVIndexUnsupported
}
func (s uncacheableStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, sdk.ErrKVIndexUnsupported
}
func (s uncacheableStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, sdk.ErrKVIndexUnsupported
}

// The predicate is reachable from the SDK, and it answers both ways.
//
// This is written from OUTSIDE the sdk package, using only what it exports,
// because reachability is the thing under test: a consumer building
// tenant-routed stores on sdk.TenantStoreResolver may not import pkg/kvstore,
// and before this re-export the only options were reimplementing a predicate
// that must have one definition, or shipping the cache unguarded (go-qrdb#55).
func TestUsableAsMapKey_IsReachableAndDiscriminates(t *testing.T) {
	comparable := sdk.NewMemoryKVStore()
	if !sdk.UsableAsMapKey(comparable) {
		t.Fatal("the memory store is a pointer type and must be usable as a key")
	}

	if sdk.UsableAsMapKey(uncacheableStore{}) {
		t.Fatal("a map-typed store was reported as usable; caching by it panics " +
			"with \"hash of unhashable type\" on the first operation")
	}

	if sdk.UsableAsMapKey(nil) {
		t.Fatal("a nil store was reported as usable as a key")
	}
}

// The predicate's answer is the one that keeps a real cache from panicking.
// Guarding on it, a cache built over both store shapes stays correct: the
// comparable one is cached, the other is rebuilt, and neither crashes.
func TestUsableAsMapKey_GuardsARealCache(t *testing.T) {
	var cache sync.Map
	built := 0

	stateFor := func(store sdk.KVStore) string {
		if !sdk.UsableAsMapKey(store) {
			built++
			return "uncached"
		}
		if v, ok := cache.Load(store); ok {
			return v.(string)
		}
		built++
		cache.Store(store, "cached")
		return "cached"
	}

	comparable := sdk.NewMemoryKVStore()
	uncacheable := uncacheableStore{}

	// Twice each. Without the guard the second call on the uncacheable store
	// would not be reached, because the first Load panics.
	for i := 0; i < 2; i++ {
		if got := stateFor(comparable); got != "cached" {
			t.Fatalf("comparable store state = %q, want cached", got)
		}
		if got := stateFor(uncacheable); got != "uncached" {
			t.Fatalf("uncacheable store state = %q, want uncached", got)
		}
	}

	// The comparable store was built once and served from the cache after;
	// the uncacheable one was rebuilt each time. Three builds, not four.
	if built != 3 {
		t.Fatalf("built %d times, want 3 — one for the cached store and one per "+
			"call for the uncacheable one", built)
	}
}

// deceptiveStore is the shape that broke the type-level predicate: a STRUCT
// with an `any` field and value receivers. The struct type is comparable, so
// reflect.Type.Comparable answers true — and using the value as a key panics
// anyway whenever that field holds a slice or a map.
//
// Nothing in the KVStore contract forbids this either, which is the point: the
// predicate has to answer for the value in hand, not for its type.
type deceptiveStore struct {
	payload any
	data    *sync.Map
}

func newDeceptiveStore(payload any) deceptiveStore {
	return deceptiveStore{payload: payload, data: &sync.Map{}}
}

func (s deceptiveStore) Put(_ context.Context, key string, value []byte) error {
	s.data.Store(key, value)
	return nil
}
func (s deceptiveStore) Get(_ context.Context, key string) ([]byte, error) {
	if v, ok := s.data.Load(key); ok {
		return v.([]byte), nil
	}
	return nil, nil
}
func (s deceptiveStore) Delete(_ context.Context, key string) error {
	s.data.Delete(key)
	return nil
}
func (s deceptiveStore) Scan(_ context.Context, _ string, _ func(string, []byte) error) error {
	return nil
}
func (s deceptiveStore) List(_ context.Context, _ string) ([]string, error) { return nil, nil }
func (s deceptiveStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := s.data.Load(key)
	return ok, nil
}
func (s deceptiveStore) RegisterEntityIndexes(_ string, _ []sdk.EntityIndex) error {
	return sdk.ErrKVIndexUnsupported
}
func (s deceptiveStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, sdk.ErrKVIndexUnsupported
}
func (s deceptiveStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, sdk.ErrKVIndexUnsupported
}
func (s deceptiveStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, sdk.ErrKVIndexUnsupported
}
func (s deceptiveStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, sdk.ErrKVIndexUnsupported
}

// A COMPARABLE TYPE carrying an UNCOMPARABLE VALUE must be refused, and the
// real cache path must survive it.
//
// reflect.Type.Comparable answers true here and the value panics as a key, so a
// type-level predicate is fail-open in exactly the case a consumer cannot see
// coming. The store shape is legal, the panic is at the first Load, and the
// caller is relying on this predicate precisely so it does not have to know any
// of that (go-qrdb#55).
func TestUsableAsMapKey_RefusesAComparableTypeHoldingAnUncomparableValue(t *testing.T) {
	deceptive := newDeceptiveStore(map[string]int{"a": 1})

	if reflect.TypeOf(deceptive).Comparable() != true {
		t.Fatal("fixture is wrong: this type must be comparable, or it does not " +
			"exercise the gap between the type-level and value-level questions")
	}
	if sdk.UsableAsMapKey(deceptive) {
		t.Fatal("a comparable TYPE holding an uncomparable VALUE was reported as " +
			"usable; caching by it panics with \"hash of unhashable type\"")
	}

	// And the guard has to hold up on the real path, not just the predicate:
	// without it the first Load panics and the test does not complete.
	var cache sync.Map
	stateFor := func(store sdk.KVStore) string {
		if !sdk.UsableAsMapKey(store) {
			return "uncached"
		}
		if v, ok := cache.Load(store); ok {
			return v.(string)
		}
		cache.Store(store, "cached")
		return "cached"
	}
	for i := 0; i < 2; i++ {
		if got := stateFor(deceptive); got != "uncached" {
			t.Fatalf("deceptive store state = %q, want uncached", got)
		}
	}
}

// The same struct shape with a comparable value in the field IS usable, so the
// fix is a distinction rather than a blanket refusal of struct-backed stores.
func TestUsableAsMapKey_AcceptsTheSameShapeHoldingAComparableValue(t *testing.T) {
	if !sdk.UsableAsMapKey(newDeceptiveStore("plain")) {
		t.Fatal("a struct store whose field holds a comparable value was refused; " +
			"the predicate must answer for the value, not blanket-refuse the shape")
	}
}
