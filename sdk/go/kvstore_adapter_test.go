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

package sdk

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock transport KVClient
// ---------------------------------------------------------------------------

type mockTransportKV struct {
	putFn    func(ctx context.Context, key string, value []byte, opts ...transport.WriteOption) error
	getFn    func(ctx context.Context, key string, opts ...transport.ReadOption) ([]byte, error)
	deleteFn func(ctx context.Context, key string, opts ...transport.WriteOption) error
	listFn   func(ctx context.Context, prefix string, opts ...transport.ReadOption) ([]string, error)
	scanFn   func(ctx context.Context, prefix string, opts ...transport.ReadOption) (map[string][]byte, error)
	existsFn func(ctx context.Context, key string, opts ...transport.ReadOption) (bool, error)
}

func (m *mockTransportKV) Put(ctx context.Context, key string, value []byte, opts ...transport.WriteOption) error {
	return m.putFn(ctx, key, value, opts...)
}

func (m *mockTransportKV) Get(ctx context.Context, key string, opts ...transport.ReadOption) ([]byte, error) {
	return m.getFn(ctx, key, opts...)
}

func (m *mockTransportKV) Delete(ctx context.Context, key string, opts ...transport.WriteOption) error {
	return m.deleteFn(ctx, key, opts...)
}

func (m *mockTransportKV) List(ctx context.Context, prefix string, opts ...transport.ReadOption) ([]string, error) {
	return m.listFn(ctx, prefix, opts...)
}

func (m *mockTransportKV) Scan(ctx context.Context, prefix string, opts ...transport.ReadOption) (map[string][]byte, error) {
	return m.scanFn(ctx, prefix, opts...)
}

func (m *mockTransportKV) Exists(ctx context.Context, key string, opts ...transport.ReadOption) (bool, error) {
	return m.existsFn(ctx, key, opts...)
}

// ---------------------------------------------------------------------------
// Mock transport IndexClient
// ---------------------------------------------------------------------------

type mockTransportIndex struct {
	createFn func(ctx context.Context, name, fieldName string, config transport.IndexConfig) error
	dropFn   func(ctx context.Context, name string) error
	queryFn  func(ctx context.Context, indexName, value string) (*transport.IndexQueryResponse, error)
	scanFn   func(ctx context.Context, indexName string, start, end *string) (*transport.IndexScanResponse, error)
	listFn   func(ctx context.Context) (*transport.IndexListResponse, error)
}

func (m *mockTransportIndex) IndexCreate(ctx context.Context, name, fieldName string, config transport.IndexConfig) error {
	return m.createFn(ctx, name, fieldName, config)
}

func (m *mockTransportIndex) IndexDrop(ctx context.Context, name string) error {
	return m.dropFn(ctx, name)
}

func (m *mockTransportIndex) IndexQuery(ctx context.Context, indexName, value string) (*transport.IndexQueryResponse, error) {
	return m.queryFn(ctx, indexName, value)
}

func (m *mockTransportIndex) IndexScan(ctx context.Context, indexName string, start, end *string) (*transport.IndexScanResponse, error) {
	return m.scanFn(ctx, indexName, start, end)
}

func (m *mockTransportIndex) IndexList(ctx context.Context) (*transport.IndexListResponse, error) {
	return m.listFn(ctx)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestAdapter(kv *mockTransportKV, idx *mockTransportIndex) *kvStoreAdapter {
	return &kvStoreAdapter{kv: kv, index: idx}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestKVStoreAdapter_Put(t *testing.T) {
	ctx := context.Background()
	var capturedKey string
	var capturedValue []byte

	kv := &mockTransportKV{
		putFn: func(_ context.Context, key string, value []byte, _ ...transport.WriteOption) error {
			capturedKey = key
			capturedValue = value
			return nil
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	if err := adapter.Put(ctx, "mykey", []byte("myvalue")); err != nil {
		t.Fatalf("Put returned unexpected error: %v", err)
	}
	if capturedKey != "mykey" {
		t.Errorf("Put: expected key %q, got %q", "mykey", capturedKey)
	}
	if string(capturedValue) != "myvalue" {
		t.Errorf("Put: expected value %q, got %q", "myvalue", string(capturedValue))
	}
}

func TestKVStoreAdapter_Put_Error(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("transport error")

	kv := &mockTransportKV{
		putFn: func(_ context.Context, _ string, _ []byte, _ ...transport.WriteOption) error {
			return sentinel
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	if err := adapter.Put(ctx, "k", []byte("v")); !errors.Is(err, sentinel) {
		t.Fatalf("Put: expected sentinel error, got %v", err)
	}
}

func TestKVStoreAdapter_Get(t *testing.T) {
	ctx := context.Background()
	expected := []byte("storedvalue")

	kv := &mockTransportKV{
		getFn: func(_ context.Context, key string, _ ...transport.ReadOption) ([]byte, error) {
			if key != "getkey" {
				return nil, errors.New("unexpected key")
			}
			return expected, nil
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	got, err := adapter.Get(ctx, "getkey")
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if string(got) != string(expected) {
		t.Errorf("Get: expected %q, got %q", string(expected), string(got))
	}
}

func TestKVStoreAdapter_Get_Error(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("not found")

	kv := &mockTransportKV{
		getFn: func(_ context.Context, _ string, _ ...transport.ReadOption) ([]byte, error) {
			return nil, sentinel
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	if _, err := adapter.Get(ctx, "missing"); !errors.Is(err, sentinel) {
		t.Fatalf("Get: expected sentinel error, got %v", err)
	}
}

func TestKVStoreAdapter_Delete(t *testing.T) {
	ctx := context.Background()
	var deletedKey string

	kv := &mockTransportKV{
		deleteFn: func(_ context.Context, key string, _ ...transport.WriteOption) error {
			deletedKey = key
			return nil
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	if err := adapter.Delete(ctx, "delkey"); err != nil {
		t.Fatalf("Delete returned unexpected error: %v", err)
	}
	if deletedKey != "delkey" {
		t.Errorf("Delete: expected key %q, got %q", "delkey", deletedKey)
	}
}

func TestKVStoreAdapter_Delete_Error(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("delete failed")

	kv := &mockTransportKV{
		deleteFn: func(_ context.Context, _ string, _ ...transport.WriteOption) error {
			return sentinel
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	if err := adapter.Delete(ctx, "k"); !errors.Is(err, sentinel) {
		t.Fatalf("Delete: expected sentinel error, got %v", err)
	}
}

func TestKVStoreAdapter_Scan(t *testing.T) {
	ctx := context.Background()

	kv := &mockTransportKV{
		scanFn: func(_ context.Context, prefix string, _ ...transport.ReadOption) (map[string][]byte, error) {
			return map[string][]byte{
				prefix + "/a": []byte("alpha"),
				prefix + "/b": []byte("beta"),
			}, nil
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	collected := make(map[string]string)
	err := adapter.Scan(ctx, "scan/", func(key string, value []byte) error {
		collected[key] = string(value)
		return nil
	})
	if err != nil {
		t.Fatalf("Scan returned unexpected error: %v", err)
	}
	if len(collected) != 2 {
		t.Errorf("Scan: expected 2 entries, got %d", len(collected))
	}
	if collected["scan//a"] != "alpha" {
		t.Errorf("Scan: expected entry scan//a=alpha, got %q", collected["scan//a"])
	}
	if collected["scan//b"] != "beta" {
		t.Errorf("Scan: expected entry scan//b=beta, got %q", collected["scan//b"])
	}
}

// TestKVStoreAdapter_Scan_DeliversKeysInSortedOrder pins that Scan's
// delivery order is deterministic and lexicographic by key, not the
// randomized order Go's map iteration would otherwise produce.
//
// pkg/dao.GenericDAO.Page's default (no SortField) path issues one Scan
// call per page and relies on every call enumerating the SAME prefix in
// the SAME relative order for its offset/limit arithmetic to mean
// anything. Before this fix, Scan iterated the transport's map response
// directly: two Scan calls over identical data could (and, caught by a
// real multi-page pagination test over the real REST transport, did)
// deliver entries in two different orders, so an entity was skipped on
// one page and repeated on another.
func TestKVStoreAdapter_Scan_DeliversKeysInSortedOrder(t *testing.T) {
	ctx := context.Background()

	unordered := map[string][]byte{
		"prefix/zebra": []byte("z"),
		"prefix/apple": []byte("a"),
		"prefix/mango": []byte("m"),
		"prefix/kiwi":  []byte("k"),
	}
	kv := &mockTransportKV{
		scanFn: func(_ context.Context, _ string, _ ...transport.ReadOption) (map[string][]byte, error) {
			return unordered, nil
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	var gotKeys []string
	for i := 0; i < 20; i++ {
		gotKeys = nil
		if err := adapter.Scan(ctx, "prefix/", func(key string, _ []byte) error {
			gotKeys = append(gotKeys, key)
			return nil
		}); err != nil {
			t.Fatalf("Scan (iteration %d): %v", i, err)
		}
		want := []string{"prefix/apple", "prefix/kiwi", "prefix/mango", "prefix/zebra"}
		if !reflect.DeepEqual(gotKeys, want) {
			t.Fatalf("Scan delivery order (iteration %d) = %v, want %v", i, gotKeys, want)
		}
	}
}

func TestKVStoreAdapter_Scan_TransportError(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("scan transport error")

	kv := &mockTransportKV{
		scanFn: func(_ context.Context, _ string, _ ...transport.ReadOption) (map[string][]byte, error) {
			return nil, sentinel
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	if err := adapter.Scan(ctx, "", func(_ string, _ []byte) error { return nil }); !errors.Is(err, sentinel) {
		t.Fatalf("Scan: expected sentinel error, got %v", err)
	}
}

func TestKVStoreAdapter_Scan_CallbackError(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("callback error")

	kv := &mockTransportKV{
		scanFn: func(_ context.Context, _ string, _ ...transport.ReadOption) (map[string][]byte, error) {
			return map[string][]byte{"key": []byte("val")}, nil
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	if err := adapter.Scan(ctx, "", func(_ string, _ []byte) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("Scan: expected callback sentinel error, got %v", err)
	}
}

func TestKVStoreAdapter_List(t *testing.T) {
	ctx := context.Background()
	expected := []string{"prefix/a", "prefix/b", "prefix/c"}

	kv := &mockTransportKV{
		listFn: func(_ context.Context, prefix string, _ ...transport.ReadOption) ([]string, error) {
			return expected, nil
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	got, err := adapter.List(ctx, "prefix/")
	if err != nil {
		t.Fatalf("List returned unexpected error: %v", err)
	}
	if len(got) != len(expected) {
		t.Fatalf("List: expected %d keys, got %d", len(expected), len(got))
	}
	for i, k := range expected {
		if got[i] != k {
			t.Errorf("List[%d]: expected %q, got %q", i, k, got[i])
		}
	}
}

func TestKVStoreAdapter_List_Error(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("list error")

	kv := &mockTransportKV{
		listFn: func(_ context.Context, _ string, _ ...transport.ReadOption) ([]string, error) {
			return nil, sentinel
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	if _, err := adapter.List(ctx, ""); !errors.Is(err, sentinel) {
		t.Fatalf("List: expected sentinel error, got %v", err)
	}
}

func TestKVStoreAdapter_Exists(t *testing.T) {
	ctx := context.Background()

	kv := &mockTransportKV{
		existsFn: func(_ context.Context, key string, _ ...transport.ReadOption) (bool, error) {
			return key == "present", nil
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	if ok, err := adapter.Exists(ctx, "present"); err != nil || !ok {
		t.Errorf("Exists(present): expected true, nil; got %v, %v", ok, err)
	}
	if ok, err := adapter.Exists(ctx, "absent"); err != nil || ok {
		t.Errorf("Exists(absent): expected false, nil; got %v, %v", ok, err)
	}
}

func TestKVStoreAdapter_Exists_Error(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("exists error")

	kv := &mockTransportKV{
		existsFn: func(_ context.Context, _ string, _ ...transport.ReadOption) (bool, error) {
			return false, sentinel
		},
	}
	adapter := newTestAdapter(kv, &mockTransportIndex{})

	if _, err := adapter.Exists(ctx, "k"); !errors.Is(err, sentinel) {
		t.Fatalf("Exists: expected sentinel error, got %v", err)
	}
}

func TestKVStoreAdapter_RegisterEntityIndexes(t *testing.T) {
	var createdNames []string
	var createdFields []string
	var createdConfigs []transport.IndexConfig

	idx := &mockTransportIndex{
		createFn: func(_ context.Context, name, fieldName string, config transport.IndexConfig) error {
			createdNames = append(createdNames, name)
			createdFields = append(createdFields, fieldName)
			createdConfigs = append(createdConfigs, config)
			return nil
		},
	}
	adapter := newTestAdapter(&mockTransportKV{}, idx)

	indexes := []kvstore.EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", Unique: false, Sparse: false, CaseSensitive: true},
		{FieldName: "Email", JSONFieldName: "email", Unique: true, Sparse: true, CaseSensitive: true},
	}

	if err := adapter.RegisterEntityIndexes("user", indexes); err != nil {
		t.Fatalf("RegisterEntityIndexes returned unexpected error: %v", err)
	}
	if len(createdNames) != 2 {
		t.Fatalf("RegisterEntityIndexes: expected 2 IndexCreate calls, got %d", len(createdNames))
	}
	if createdNames[0] != "user.Name" {
		t.Errorf("RegisterEntityIndexes: expected index name %q, got %q", "user.Name", createdNames[0])
	}
	if createdNames[1] != "user.Email" {
		t.Errorf("RegisterEntityIndexes: expected index name %q, got %q", "user.Email", createdNames[1])
	}
	if createdFields[0] != "name" {
		t.Errorf("RegisterEntityIndexes: expected field %q, got %q", "name", createdFields[0])
	}
	if createdFields[1] != "email" {
		t.Errorf("RegisterEntityIndexes: expected field %q, got %q", "email", createdFields[1])
	}
	if createdConfigs[0].Unique || createdConfigs[0].Sparse {
		t.Errorf("RegisterEntityIndexes: Name index should not be unique or sparse")
	}
	if !createdConfigs[1].Unique || !createdConfigs[1].Sparse {
		t.Errorf("RegisterEntityIndexes: Email index should be unique and sparse")
	}
}

func TestKVStoreAdapter_RegisterEntityIndexes_Error(t *testing.T) {
	sentinel := errors.New("index create error")

	idx := &mockTransportIndex{
		createFn: func(_ context.Context, _, _ string, _ transport.IndexConfig) error {
			return sentinel
		},
	}
	adapter := newTestAdapter(&mockTransportKV{}, idx)

	indexes := []kvstore.EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", CaseSensitive: true},
	}

	if err := adapter.RegisterEntityIndexes("entity", indexes); !errors.Is(err, sentinel) {
		t.Fatalf("RegisterEntityIndexes: expected sentinel error, got %v", err)
	}
}

// TestKVStoreAdapter_RegisterEntityIndexes_TranslatesCapabilityRefusal pins
// that a server-side "this statemachine has no index subsystem at all"
// refusal -- decoded into *transport.CapabilityUnsupportedError, wrapped in
// *transport.RequestError exactly as the REST transport's decodeErrorResponse
// produces it for an HTTP 501 -- surfaces as kvstore.ErrIndexUnsupported.
// Before this translation, dao.New's errors.Is(err, kvstore.ErrIndexUnsupported)
// check never matched, so DAO construction against such a store failed
// outright instead of degrading to full scans.
func TestKVStoreAdapter_RegisterEntityIndexes_TranslatesCapabilityRefusal(t *testing.T) {
	wireErr := &transport.RequestError{
		Method:     "IndexCreate",
		StatusCode: 501,
		Err:        &transport.CapabilityUnsupportedError{Message: "statemachine: index not configured"},
	}

	idx := &mockTransportIndex{
		createFn: func(_ context.Context, _, _ string, _ transport.IndexConfig) error {
			return wireErr
		},
	}
	adapter := newTestAdapter(&mockTransportKV{}, idx)

	indexes := []kvstore.EntityIndex{
		{FieldName: "Name", JSONFieldName: "name", CaseSensitive: true},
	}

	err := adapter.RegisterEntityIndexes("entity", indexes)
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("RegisterEntityIndexes: expected kvstore.ErrIndexUnsupported, got %v", err)
	}
}

// TestKVStoreAdapter_QueryIndex_TranslatesCapabilityRefusal mirrors the
// RegisterEntityIndexes case for the direct QueryByIndex path, which must
// fail closed with the same sentinel rather than an opaque transport error.
func TestKVStoreAdapter_QueryIndex_TranslatesCapabilityRefusal(t *testing.T) {
	wireErr := &transport.RequestError{
		Method:     "IndexQuery",
		StatusCode: 501,
		Err:        &transport.CapabilityUnsupportedError{Message: "statemachine: index not configured"},
	}

	idx := &mockTransportIndex{
		queryFn: func(_ context.Context, _, _ string) (*transport.IndexQueryResponse, error) {
			return nil, wireErr
		},
	}
	adapter := newTestAdapter(&mockTransportKV{}, idx)

	_, err := adapter.QueryIndex(context.Background(), "idx", []byte("v"))
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("QueryIndex: expected kvstore.ErrIndexUnsupported, got %v", err)
	}
}

// TestKVStoreAdapter_ScanIndex_TranslatesCapabilityRefusal mirrors the same
// translation for ScanByIndex.
func TestKVStoreAdapter_ScanIndex_TranslatesCapabilityRefusal(t *testing.T) {
	wireErr := &transport.RequestError{
		Method:     "IndexScan",
		StatusCode: 501,
		Err:        &transport.CapabilityUnsupportedError{Message: "statemachine: index not configured"},
	}

	idx := &mockTransportIndex{
		scanFn: func(_ context.Context, _ string, _, _ *string) (*transport.IndexScanResponse, error) {
			return nil, wireErr
		},
	}
	adapter := newTestAdapter(&mockTransportKV{}, idx)

	_, err := adapter.ScanIndex(context.Background(), "idx", []byte("a"), []byte("b"))
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("ScanIndex: expected kvstore.ErrIndexUnsupported, got %v", err)
	}
}

func TestKVStoreAdapter_QueryIndex(t *testing.T) {
	ctx := context.Background()

	idx := &mockTransportIndex{
		queryFn: func(_ context.Context, indexName, value string) (*transport.IndexQueryResponse, error) {
			if indexName != "user.name" || value != "alice" {
				return nil, errors.New("unexpected args")
			}
			return &transport.IndexQueryResponse{Keys: []string{"users/1", "users/3"}}, nil
		},
	}
	adapter := newTestAdapter(&mockTransportKV{}, idx)

	got, err := adapter.QueryIndex(ctx, "user.name", []byte("alice"))
	if err != nil {
		t.Fatalf("QueryIndex returned unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("QueryIndex: expected 2 keys, got %d", len(got))
	}
	if string(got[0]) != "users/1" {
		t.Errorf("QueryIndex[0]: expected %q, got %q", "users/1", string(got[0]))
	}
	if string(got[1]) != "users/3" {
		t.Errorf("QueryIndex[1]: expected %q, got %q", "users/3", string(got[1]))
	}
}

func TestKVStoreAdapter_QueryIndex_Error(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("index query error")

	idx := &mockTransportIndex{
		queryFn: func(_ context.Context, _, _ string) (*transport.IndexQueryResponse, error) {
			return nil, sentinel
		},
	}
	adapter := newTestAdapter(&mockTransportKV{}, idx)

	if _, err := adapter.QueryIndex(ctx, "idx", []byte("val")); !errors.Is(err, sentinel) {
		t.Fatalf("QueryIndex: expected sentinel error, got %v", err)
	}
}

func TestKVStoreAdapter_ScanIndex(t *testing.T) {
	ctx := context.Background()

	idx := &mockTransportIndex{
		scanFn: func(_ context.Context, indexName string, start, end *string) (*transport.IndexScanResponse, error) {
			if indexName != "user.name" || start == nil || *start != "a" || end == nil || *end != "c" {
				return nil, errors.New("unexpected args")
			}
			return &transport.IndexScanResponse{
				Entries: []transport.IndexScanEntry{
					{Value: "alice", Keys: []string{"users/1", "users/3"}},
					{Value: "bob", Keys: []string{"users/2"}},
				},
			}, nil
		},
	}
	adapter := newTestAdapter(&mockTransportKV{}, idx)

	result, err := adapter.ScanIndex(ctx, "user.name", []byte("a"), []byte("c"))
	if err != nil {
		t.Fatalf("ScanIndex returned unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("ScanIndex: expected 2 entries, got %d", len(result))
	}
	aliceKeys, ok := result["alice"]
	if !ok {
		t.Fatal("ScanIndex: missing 'alice' entry")
	}
	if len(aliceKeys) != 2 {
		t.Errorf("ScanIndex: alice: expected 2 keys, got %d", len(aliceKeys))
	}
	if string(aliceKeys[0]) != "users/1" {
		t.Errorf("ScanIndex: alice[0]: expected %q, got %q", "users/1", string(aliceKeys[0]))
	}
	bobKeys, ok := result["bob"]
	if !ok {
		t.Fatal("ScanIndex: missing 'bob' entry")
	}
	if len(bobKeys) != 1 || string(bobKeys[0]) != "users/2" {
		t.Errorf("ScanIndex: bob: expected [users/2], got %v", bobKeys)
	}
}

func TestKVStoreAdapter_ScanIndex_Error(t *testing.T) {
	ctx := context.Background()
	sentinel := errors.New("index scan error")

	idx := &mockTransportIndex{
		scanFn: func(_ context.Context, _ string, _, _ *string) (*transport.IndexScanResponse, error) {
			return nil, sentinel
		},
	}
	adapter := newTestAdapter(&mockTransportKV{}, idx)

	if _, err := adapter.ScanIndex(ctx, "idx", []byte("a"), []byte("z")); !errors.Is(err, sentinel) {
		t.Fatalf("ScanIndex: expected sentinel error, got %v", err)
	}
}

// TestKVStoreAdapter_CompileTimeCheck verifies the compile-time interface
// assertion declared in kvstore_adapter.go remains valid.
func TestKVStoreAdapter_CompileTimeCheck(t *testing.T) {
	var _ kvstore.KVStore = (*kvStoreAdapter)(nil)
}

// TestKVStoreAdapter_RegisterEntityIndexes_RefusesACompoundDefinition pins the
// one definition this adapter still cannot honour.
//
// transport.IndexClient has no compound creation verb, and the compound query
// methods cannot serve one either. Previously this composed
// entityType + "." + ei.FieldName for a compound definition, where FieldName
// is empty — creating a garbage index named "entity." while telling the caller
// its compound index existed. Refusing makes pkg/dao plan a correct full scan.
//
// The case-insensitivity refusal that used to live here is gone: go-qrdb#34
// added CaseInsensitive to transport.IndexConfig and to every protocol
// underneath it. See
// TestKVStoreAdapter_RegisterEntityIndexes_ForwardsCaseInsensitivity.
func TestKVStoreAdapter_RegisterEntityIndexes_RefusesACompoundDefinition(t *testing.T) {
	var created int
	idx := &mockTransportIndex{
		createFn: func(_ context.Context, _, _ string, _ transport.IndexConfig) error {
			created++
			return nil
		},
	}
	adapter := newTestAdapter(&mockTransportKV{}, idx)

	compound := []kvstore.EntityIndex{{
		FieldNames:     []string{"Team", "Name"},
		JSONFieldNames: []string{"team", "name"},
		CaseSensitive:  true,
	}}
	if err := adapter.RegisterEntityIndexes("entity", compound); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("compound definition = %v; want kvstore.ErrIndexUnsupported", err)
	}

	if created != 0 {
		t.Errorf("created %d indexes; a refused definition must create none", created)
	}
}

// TestKVStoreAdapter_RegisterEntityIndexes_ForwardsCaseInsensitivity covers
// go-qrdb#34 on the SDK path, and replaces the refusal above.
//
// The polarity is inverted across this boundary on purpose:
// kvstore.EntityIndex names the default (CaseSensitive) while
// transport.IndexConfig marks the positive claim (CaseInsensitive). Both
// directions are asserted, because an adapter that hardcoded either one would
// pass a test of the other — and dropping the flag would create a
// case-SENSITIVE index on the server while reporting success, which is a
// lookup that answers empty with a nil error.
func TestKVStoreAdapter_RegisterEntityIndexes_ForwardsCaseInsensitivity(t *testing.T) {
	for _, tc := range []struct {
		name                string
		caseSensitive       bool
		wantCaseInsensitive bool
	}{
		{"case-insensitive definition", false, true},
		{"ordinary definition", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []transport.IndexConfig
			idx := &mockTransportIndex{
				createFn: func(_ context.Context, _, _ string, cfg transport.IndexConfig) error {
					got = append(got, cfg)
					return nil
				},
			}
			adapter := newTestAdapter(&mockTransportKV{}, idx)

			err := adapter.RegisterEntityIndexes("entity", []kvstore.EntityIndex{
				{FieldName: "Name", JSONFieldName: "name", CaseSensitive: tc.caseSensitive},
			})
			if err != nil {
				t.Fatalf("RegisterEntityIndexes = %v; want nil", err)
			}
			if len(got) != 1 {
				t.Fatalf("created %d indexes; want 1", len(got))
			}
			if got[0].CaseInsensitive != tc.wantCaseInsensitive {
				t.Errorf("forwarded CaseInsensitive=%v; want %v",
					got[0].CaseInsensitive, tc.wantCaseInsensitive)
			}
		})
	}
}

// TestKVStoreAdapter_RegisterEntityIndexes_ForwardsNumeric proves a numeric
// definition reaches the server as numeric, and an ordinary one as text.
// Dropping the flag would build text terms, whose range scans put "10" before
// "2" and answer `>= 2` without the record holding 10.
func TestKVStoreAdapter_RegisterEntityIndexes_ForwardsNumeric(t *testing.T) {
	for _, tc := range []struct {
		name    string
		numeric bool
	}{
		{"numeric definition", true},
		{"ordinary definition", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []transport.IndexConfig
			idx := &mockTransportIndex{
				createFn: func(_ context.Context, _, _ string, cfg transport.IndexConfig) error {
					got = append(got, cfg)
					return nil
				},
			}
			adapter := newTestAdapter(&mockTransportKV{}, idx)

			err := adapter.RegisterEntityIndexes("entity", []kvstore.EntityIndex{
				{FieldName: "Age", JSONFieldName: "age", CaseSensitive: true, Numeric: tc.numeric},
			})
			if err != nil {
				t.Fatalf("RegisterEntityIndexes = %v; want nil", err)
			}
			if len(got) != 1 {
				t.Fatalf("created %d indexes; want 1", len(got))
			}
			if got[0].Numeric != tc.numeric {
				t.Errorf("forwarded Numeric=%v; want %v", got[0].Numeric, tc.numeric)
			}
		})
	}
}

// TestKVStoreAdapter_ScanIndexThreadsOpenBounds is the go-qrdb#36 replacement
// for the earlier open-bound refusal. A nil []byte bound now reaches the
// transport as a nil *string — an OPEN bound — instead of being refused or
// collapsed to an explicit empty "". A one-sided range therefore returns its
// matches.
func TestKVStoreAdapter_ScanIndexThreadsOpenBounds(t *testing.T) {
	ctx := context.Background()

	type seen struct {
		start, end *string
	}
	for _, tc := range []struct {
		name       string
		start, end []byte
		wantStart  *string
		wantEnd    *string
	}{
		{"open upper", []byte("beta"), nil, strPtr("beta"), nil},
		{"open lower", nil, []byte("beta"), nil, strPtr("beta")},
		{"both open", nil, nil, nil, nil},
		{"both bounded", []byte("a"), []byte("z"), strPtr("a"), strPtr("z")},
		{"present empty upper", []byte("beta"), []byte{}, strPtr("beta"), strPtr("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got seen
			idx := &mockTransportIndex{
				scanFn: func(_ context.Context, _ string, start, end *string) (*transport.IndexScanResponse, error) {
					got = seen{start: start, end: end}
					return &transport.IndexScanResponse{
						Entries: []transport.IndexScanEntry{{Value: "beta", Keys: []string{"t/1"}}},
					}, nil
				},
			}
			adapter := newTestAdapter(&mockTransportKV{}, idx)

			result, err := adapter.ScanIndex(ctx, "member.Team", tc.start, tc.end)
			if err != nil {
				t.Fatalf("ScanIndex over a one-sided range must not error: %v", err)
			}
			if len(result) == 0 {
				t.Fatal("ScanIndex over an index holding matches must not return empty")
			}
			if !eqStrPtr(got.start, tc.wantStart) {
				t.Errorf("start threaded to transport = %v; want %v", derefStr(got.start), derefStr(tc.wantStart))
			}
			if !eqStrPtr(got.end, tc.wantEnd) {
				t.Errorf("end threaded to transport = %v; want %v", derefStr(got.end), derefStr(tc.wantEnd))
			}
		})
	}
}

func strPtr(s string) *string { return &s }

func eqStrPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func derefStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return "\"" + *p + "\""
}
