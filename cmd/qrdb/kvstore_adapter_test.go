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
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Inline mock KV service for adapter tests
// ---------------------------------------------------------------------------

type adapterMockKVService struct {
	store     map[string][]byte
	scanPairs map[string][]byte
}

func newAdapterMockKVService() *adapterMockKVService {
	return &adapterMockKVService{
		store: make(map[string][]byte),
	}
}

func (m *adapterMockKVService) Put(_ context.Context, key string, value []byte) error {
	m.store[key] = value
	return nil
}

func (m *adapterMockKVService) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	if _, exists := m.store[key]; exists {
		return false, nil
	}
	m.store[key] = value
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *adapterMockKVService) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	current, ok := m.store[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(m.store, key)
	return true, nil
}

func (m *adapterMockKVService) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *adapterMockKVService) Delete(_ context.Context, key string) error {
	delete(m.store, key)
	return nil
}

func (m *adapterMockKVService) List(_ context.Context, _ string) ([]string, error) {
	keys := make([]string, 0, len(m.store))
	for k := range m.store {
		keys = append(keys, k)
	}
	return keys, nil
}

func (m *adapterMockKVService) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	result := make(map[string][]byte)
	for k, v := range m.store {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			result[k] = v
		}
	}
	return result, nil
}

// Remaining KVServicer stubs.
func (m *adapterMockKVService) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, nil
}
func (m *adapterMockKVService) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, nil
}
func (m *adapterMockKVService) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, nil
}
func (m *adapterMockKVService) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, nil
}
func (m *adapterMockKVService) GetStale(ctx context.Context, key string) ([]byte, error) {
	return m.Get(ctx, key)
}
func (m *adapterMockKVService) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}
func (m *adapterMockKVService) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}
func (m *adapterMockKVService) PutWithMode(_ context.Context, key string, value []byte, _ writemode.Mode) error {
	m.store[key] = value
	return nil
}
func (m *adapterMockKVService) DeleteWithMode(_ context.Context, key string, _ writemode.Mode) error {
	delete(m.store, key)
	return nil
}
func (m *adapterMockKVService) GetWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) ([]byte, error) {
	return m.Get(ctx, key)
}
func (m *adapterMockKVService) ListWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) ([]string, error) {
	return m.List(ctx, prefix)
}
func (m *adapterMockKVService) ScanWithConsistency(ctx context.Context, prefix string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}
func (m *adapterMockKVService) ExistsWithConsistency(ctx context.Context, key string, _ consistency.ReadOptions) (bool, error) {
	_, err := m.Get(ctx, key)
	return err == nil, nil
}

var _ service.KVServicer = (*adapterMockKVService)(nil)

// ---------------------------------------------------------------------------
// kvServiceAdapter tests
// ---------------------------------------------------------------------------

func TestKVServiceAdapter_PutGet(t *testing.T) {
	svc := newAdapterMockKVService()
	adapter := newKVServiceAdapter(svc, nil, nil)

	ctx := context.Background()
	if err := adapter.Put(ctx, "hello", []byte("world")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	val, err := adapter.Get(ctx, "hello")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(val) != "world" {
		t.Errorf("Get: expected %q, got %q", "world", string(val))
	}
}

func TestKVServiceAdapter_Get_NotFound_ReturnsNilNil(t *testing.T) {
	svc := newAdapterMockKVService()
	adapter := newKVServiceAdapter(svc, nil, nil)

	ctx := context.Background()
	val, err := adapter.Get(ctx, "missing")
	if err != nil {
		t.Fatalf("Get for missing key: expected nil error, got %v", err)
	}
	if val != nil {
		t.Errorf("Get for missing key: expected nil value, got %v", val)
	}
}

func TestKVServiceAdapter_Delete(t *testing.T) {
	svc := newAdapterMockKVService()
	adapter := newKVServiceAdapter(svc, nil, nil)

	ctx := context.Background()
	_ = adapter.Put(ctx, "key", []byte("val"))

	if err := adapter.Delete(ctx, "key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	val, err := adapter.Get(ctx, "key")
	if err != nil {
		t.Fatalf("Get after Delete: %v", err)
	}
	if val != nil {
		t.Error("expected nil after Delete")
	}
}

func TestKVServiceAdapter_Scan(t *testing.T) {
	svc := newAdapterMockKVService()
	adapter := newKVServiceAdapter(svc, nil, nil)

	ctx := context.Background()
	_ = adapter.Put(ctx, "pfx/a", []byte("1"))
	_ = adapter.Put(ctx, "pfx/b", []byte("2"))
	_ = adapter.Put(ctx, "other/c", []byte("3"))

	var collected []string
	err := adapter.Scan(ctx, "pfx/", func(k string, _ []byte) error {
		collected = append(collected, k)
		return nil
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(collected) != 2 {
		t.Errorf("expected 2 keys, got %d: %v", len(collected), collected)
	}
}

func TestKVServiceAdapter_Scan_FnError(t *testing.T) {
	svc := newAdapterMockKVService()
	adapter := newKVServiceAdapter(svc, nil, nil)

	ctx := context.Background()
	_ = adapter.Put(ctx, "pfx/a", []byte("1"))

	scanErr := errors.New("scan callback error")
	err := adapter.Scan(ctx, "pfx/", func(_ string, _ []byte) error {
		return scanErr
	})
	if !errors.Is(err, scanErr) {
		t.Fatalf("expected scan callback error, got: %v", err)
	}
}

func TestKVServiceAdapter_List(t *testing.T) {
	svc := newAdapterMockKVService()
	adapter := newKVServiceAdapter(svc, nil, nil)

	ctx := context.Background()
	_ = adapter.Put(ctx, "k1", []byte("v1"))
	_ = adapter.Put(ctx, "k2", []byte("v2"))

	keys, err := adapter.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

func TestKVServiceAdapter_Exists_Found(t *testing.T) {
	svc := newAdapterMockKVService()
	adapter := newKVServiceAdapter(svc, nil, nil)

	ctx := context.Background()
	_ = adapter.Put(ctx, "exists-key", []byte("v"))

	ok, err := adapter.Exists(ctx, "exists-key")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !ok {
		t.Error("expected Exists to return true")
	}
}

func TestKVServiceAdapter_Exists_NotFound(t *testing.T) {
	svc := newAdapterMockKVService()
	adapter := newKVServiceAdapter(svc, nil, nil)

	ctx := context.Background()
	ok, err := adapter.Exists(ctx, "ghost")
	if err != nil {
		t.Fatalf("Exists for missing key: unexpected error: %v", err)
	}
	if ok {
		t.Error("expected Exists to return false for missing key")
	}
}

// TestKVServiceAdapter_RegisterEntityIndexes_NilRegistererRefuses pins the
// typed refusal.
//
// It previously asserted a nil error, which told a DAO its indexes were
// registered when this adapter maintains none — every later index query then
// answered empty with no error. Registration is the only point at which the
// incapability can be reported.
func TestKVServiceAdapter_RegisterEntityIndexes_NilRegistererRefuses(t *testing.T) {
	svc := newAdapterMockKVService()
	adapter := newKVServiceAdapter(svc, nil, nil)

	err := adapter.RegisterEntityIndexes("users", []kvstore.EntityIndex{{FieldName: "Email"}})
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("RegisterEntityIndexes = %v; want kvstore.ErrIndexUnsupported", err)
	}
}

func TestKVServiceAdapter_RegisterEntityIndexes_WithRegisterer(t *testing.T) {
	svc := newAdapterMockKVService()
	var registeredType string
	var registeredIndexes []kvstore.EntityIndex
	registerer := &mockEntityRegisterer{
		registerFn: func(entityType string, indexes []kvstore.EntityIndex) error {
			registeredType = entityType
			registeredIndexes = indexes
			return nil
		},
	}
	adapter := newKVServiceAdapter(svc, registerer, noopLookup)

	idx := kvstore.EntityIndex{FieldName: "email", JSONFieldName: "email"}
	err := adapter.RegisterEntityIndexes("users", []kvstore.EntityIndex{idx})
	if err != nil {
		t.Fatalf("RegisterEntityIndexes with registerer: %v", err)
	}
	if registeredType != "users" {
		t.Errorf("entityType = %q, want users", registeredType)
	}
	if len(registeredIndexes) != 1 {
		t.Errorf("expected 1 index, got %d", len(registeredIndexes))
	}
}

// mockEntityRegisterer is a test-only EntityRegisterer.
type mockEntityRegisterer struct {
	registerFn func(entityType string, indexes []kvstore.EntityIndex) error
}

func (m *mockEntityRegisterer) RegisterEntityType(entityType string, indexes []kvstore.EntityIndex) error {
	if m.registerFn != nil {
		return m.registerFn(entityType, indexes)
	}
	return nil
}

// TestKVServiceAdapter_QueryIndex_NilLookupRefuses pins the typed refusal;
// see TestKVServiceAdapter_RegisterEntityIndexes_NilRegistererRefuses.
func TestKVServiceAdapter_QueryIndex_NilLookupRefuses(t *testing.T) {
	svc := newAdapterMockKVService()
	adapter := newKVServiceAdapter(svc, nil, nil)

	ctx := context.Background()
	keys, err := adapter.QueryIndex(ctx, "users.Email", []byte("test@example.com"))
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("QueryIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if keys != nil {
		t.Error("a refused query carries no results")
	}
}

// TestKVServiceAdapter_ScanIndex_NilLookupRefuses pins the typed refusal; see
// TestKVServiceAdapter_RegisterEntityIndexes_NilRegistererRefuses.
func TestKVServiceAdapter_ScanIndex_NilLookupRefuses(t *testing.T) {
	svc := newAdapterMockKVService()
	adapter := newKVServiceAdapter(svc, nil, nil)

	ctx := context.Background()
	result, err := adapter.ScanIndex(ctx, "users.Name", []byte("a"), []byte("z"))
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("ScanIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if result != nil {
		t.Error("a refused scan carries no results")
	}
}

func TestKVServiceAdapter_QueryIndex_LookupUnexpectedType(t *testing.T) {
	svc := newAdapterMockKVService()
	// lookup returns wrong type to exercise the type assertion failure branch.
	lookup := func(_ context.Context, _ interface{}) (interface{}, error) {
		return "not-a-slice", nil
	}
	adapter := newKVServiceAdapter(svc, nil, kvstore.LookupFunc(func(ctx context.Context, req interface{}) (interface{}, error) {
		switch req.(type) {
		case index.IndexQueryRequest:
			return "wrong-type", nil
		default:
			return nil, nil
		}
	}))
	_ = lookup

	ctx := context.Background()
	_, err := adapter.QueryIndex(ctx, "idx", []byte("val"))
	if err == nil {
		t.Fatal("expected error from unexpected result type")
	}
	var iqe *kvstore.IndexQueryError
	if !isIndexQueryError(err, &iqe) {
		t.Errorf("expected IndexQueryError, got %T: %v", err, err)
	}
}

// TestKVServiceAdapter_QueryCompoundIndex_Refuses pins the typed refusal. The
// adapter exposes no compound operations, and (nil, nil) is indistinguishable
// from a compound index with no matches.
func TestKVServiceAdapter_QueryCompoundIndex_Refuses(t *testing.T) {
	svc := newAdapterMockKVService()
	adapter := newKVServiceAdapter(svc, nil, nil)

	ctx := context.Background()
	keys, err := adapter.QueryCompoundIndex(ctx, "idx", [][]byte{[]byte("v")})
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("QueryCompoundIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if keys != nil {
		t.Error("a refused query carries no results")
	}
}

// TestKVServiceAdapter_ScanCompoundIndex_Refuses pins the typed refusal; see
// TestKVServiceAdapter_QueryCompoundIndex_Refuses.
func TestKVServiceAdapter_ScanCompoundIndex_Refuses(t *testing.T) {
	svc := newAdapterMockKVService()
	adapter := newKVServiceAdapter(svc, nil, nil)

	ctx := context.Background()
	result, err := adapter.ScanCompoundIndex(ctx, "idx", [][]byte{[]byte("v")}, nil, nil)
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("ScanCompoundIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if result != nil {
		t.Error("a refused scan carries no results")
	}
}

// TestKVServiceAdapter_ScanIndex_WithLookup exercises the non-nil lookup path
// that returns a valid map[string][][]byte result.
func TestKVServiceAdapter_ScanIndex_WithLookup(t *testing.T) {
	svc := newAdapterMockKVService()

	expected := map[string][][]byte{
		"user/1": {[]byte("alice")},
		"user/2": {[]byte("bob")},
	}

	adapter := newKVServiceAdapter(svc, nil, kvstore.LookupFunc(func(ctx context.Context, req interface{}) (interface{}, error) {
		return expected, nil
	}))

	ctx := context.Background()
	result, err := adapter.ScanIndex(ctx, "users.Name", []byte("a"), []byte("c"))
	if err != nil {
		t.Fatalf("ScanIndex: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 entries, got %d", len(result))
	}
}

// TestKVServiceAdapter_ScanIndex_LookupError verifies that a lookup error
// is propagated from ScanIndex.
func TestKVServiceAdapter_ScanIndex_LookupError(t *testing.T) {
	svc := newAdapterMockKVService()

	adapter := newKVServiceAdapter(svc, nil, kvstore.LookupFunc(func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, &kvstore.IndexScanError{IndexName: "users.Name", Reason: "scan failed"}
	}))

	ctx := context.Background()
	_, err := adapter.ScanIndex(ctx, "users.Name", []byte("a"), []byte("z"))
	if err == nil {
		t.Fatal("expected error from lookup, got nil")
	}
}

// TestKVServiceAdapter_ScanIndex_UnexpectedType verifies that a lookup
// returning an unexpected type produces an IndexScanError.
func TestKVServiceAdapter_ScanIndex_UnexpectedType(t *testing.T) {
	svc := newAdapterMockKVService()

	adapter := newKVServiceAdapter(svc, nil, kvstore.LookupFunc(func(ctx context.Context, req interface{}) (interface{}, error) {
		return "wrong-type", nil // not map[string][][]byte
	}))

	ctx := context.Background()
	_, err := adapter.ScanIndex(ctx, "users.Name", []byte("a"), []byte("z"))
	if err == nil {
		t.Fatal("expected error for unexpected result type")
	}
	var ise *kvstore.IndexScanError
	if !errors.As(err, &ise) {
		t.Errorf("expected IndexScanError, got %T: %v", err, err)
	}
}

func isIndexQueryError(err error, target **kvstore.IndexQueryError) bool {
	if e, ok := err.(*kvstore.IndexQueryError); ok {
		*target = e
		return true
	}
	return false
}

// noopLookup is a lookup that answers nothing. Its presence, not its answers,
// is what RegisterEntityIndexes requires: an adapter with a registerer but NO
// lookup can create indexes it could never read, so it refuses registration
// rather than reporting a capability it does not have.
func noopLookup(_ context.Context, _ interface{}) (interface{}, error) {
	return nil, nil
}

// TestKVServiceAdapter_RegisterEntityIndexes_RefusesCompound covers a review
// finding. QueryCompoundIndex and ScanCompoundIndex on this adapter refuse
// unconditionally — it exposes no compound operations at all — so forwarding a
// compound definition to the registerer and returning nil told the caller a
// compound index existed that it could never query. Registration success has
// to be an accurate capability statement or it is worse than no answer.
func TestKVServiceAdapter_RegisterEntityIndexes_RefusesCompound(t *testing.T) {
	var forwarded int
	registerer := &mockEntityRegisterer{
		registerFn: func(_ string, _ []kvstore.EntityIndex) error {
			forwarded++
			return nil
		},
	}
	adapter := newKVServiceAdapter(newAdapterMockKVService(), registerer, noopLookup)

	err := adapter.RegisterEntityIndexes("member", []kvstore.EntityIndex{{
		FieldNames:     []string{"Team", "Name"},
		JSONFieldNames: []string{"team", "name"},
	}})
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("RegisterEntityIndexes = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if forwarded != 0 {
		t.Error("a refused definition must not reach the registerer")
	}
}

// TestKVServiceAdapter_RegisterEntityIndexes_RefusesWithoutLookup covers the
// other half: a registerer with NO lookup can create indexes it can never read,
// because both query methods refuse when lookup is nil. Reporting successful
// registration there is a false capability statement.
func TestKVServiceAdapter_RegisterEntityIndexes_RefusesWithoutLookup(t *testing.T) {
	var forwarded int
	registerer := &mockEntityRegisterer{
		registerFn: func(_ string, _ []kvstore.EntityIndex) error {
			forwarded++
			return nil
		},
	}
	adapter := newKVServiceAdapter(newAdapterMockKVService(), registerer, nil)

	err := adapter.RegisterEntityIndexes("member", []kvstore.EntityIndex{
		{FieldName: "Team", JSONFieldName: "team"},
	})
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("RegisterEntityIndexes = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if forwarded != 0 {
		t.Error("a refused registration must not reach the registerer")
	}
}

// CompareAndSwap satisfies the guarded-swap half of the interface (go-qrdb#56).
func (*adapterMockKVService) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (m *adapterMockKVService) ListAll(a0 context.Context, a1 string) ([]string, error) {
	return m.List(a0, a1)
}
