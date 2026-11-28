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

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

func TestVectorNotFoundError(t *testing.T) {
	err := &VectorNotFoundError{Namespace: "embeddings", ID: "vec-42"}
	expected := "service: vector not found: embeddings/vec-42"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestVectorNotFoundErrorEmptyFields(t *testing.T) {
	err := &VectorNotFoundError{}
	expected := "service: vector not found: /"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestNamespaceNotFoundError(t *testing.T) {
	err := &NamespaceNotFoundError{Namespace: "documents"}
	expected := "service: namespace not found: documents"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestNamespaceNotFoundErrorEmptyNamespace(t *testing.T) {
	err := &NamespaceNotFoundError{}
	expected := "service: namespace not found: "
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestNamespaceExistsError(t *testing.T) {
	err := &NamespaceExistsError{Namespace: "images"}
	expected := "service: namespace already exists: images"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestNamespaceExistsErrorEmptyNamespace(t *testing.T) {
	err := &NamespaceExistsError{}
	expected := "service: namespace already exists: "
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestDimensionMismatchError(t *testing.T) {
	err := &DimensionMismatchError{Expected: 384, Actual: 768}
	expected := "service: dimension mismatch: expected 384, got 768"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestDimensionMismatchErrorZeroDimensions(t *testing.T) {
	err := &DimensionMismatchError{Expected: 0, Actual: 0}
	expected := "service: dimension mismatch: expected 0, got 0"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestInvalidVectorError(t *testing.T) {
	err := &InvalidVectorError{Reason: "contains NaN values"}
	expected := "service: invalid vector: contains NaN values"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestInvalidVectorErrorEmptyReason(t *testing.T) {
	err := &InvalidVectorError{}
	expected := "service: invalid vector: "
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestInvalidNamespaceNameError(t *testing.T) {
	err := &InvalidNamespaceNameError{Name: "bad\x1fname", Reason: "contains illegal control character 0x1f"}
	got := err.Error()
	if got == "" {
		t.Error("Error() must return non-empty string")
	}
}

// ---------------------------------------------------------------------------
// validateNamespaceName tests
// ---------------------------------------------------------------------------

func TestValidateNamespaceName_ValidNames(t *testing.T) {
	valid := []string{
		"docs",
		"my-vectors",
		"namespace_with_underscore",
		"ns123",
		"a",
		"UPPERCASE",
		"with spaces", // spaces (0x20) are allowed
		"unicode-αβγ", // multi-byte UTF-8 is fine
	}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			if err := validateNamespaceName(name); err != nil {
				t.Errorf("validateNamespaceName(%q) = %v, want nil", name, err)
			}
		})
	}
}

func TestValidateNamespaceName_RejectsEmpty(t *testing.T) {
	err := validateNamespaceName("")
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
	var target *InvalidNamespaceNameError
	if !errors.As(err, &target) {
		t.Fatalf("expected InvalidNamespaceNameError, got %T: %v", err, err)
	}
}

func TestValidateNamespaceName_RejectsControlBytes(t *testing.T) {
	controls := []struct {
		name string
		b    byte
	}{
		{"null byte", 0x00},
		{"unit separator (namespaceSep)", 0x1f},
		{"tab", 0x09},
		{"newline", 0x0a},
		{"carriage return", 0x0d},
		{"escape", 0x1b},
	}
	for _, tc := range controls {
		t.Run(tc.name, func(t *testing.T) {
			name := "valid" + string([]byte{tc.b}) + "suffix"
			err := validateNamespaceName(name)
			if err == nil {
				t.Fatalf("validateNamespaceName(%q) = nil, want error for control byte 0x%02x", name, tc.b)
			}
			var target *InvalidNamespaceNameError
			if !errors.As(err, &target) {
				t.Fatalf("expected InvalidNamespaceNameError, got %T: %v", err, err)
			}
		})
	}
}

func TestValidateNamespaceName_RejectsTooLong(t *testing.T) {
	name := ""
	for i := 0; i <= maxNamespaceNameLen; i++ {
		name += "a"
	}
	err := validateNamespaceName(name)
	if err == nil {
		t.Fatal("validateNamespaceName: expected error for name exceeding max length, got nil")
	}
	var target *InvalidNamespaceNameError
	if !errors.As(err, &target) {
		t.Fatalf("expected InvalidNamespaceNameError, got %T: %v", err, err)
	}
}

func TestValidateNamespaceName_MaxLengthExactlyAccepted(t *testing.T) {
	name := ""
	for i := 0; i < maxNamespaceNameLen; i++ {
		name += "a"
	}
	if err := validateNamespaceName(name); err != nil {
		t.Errorf("validateNamespaceName: expected nil for exactly-max-length name, got %v", err)
	}
}

var errHomeFault = errors.New("injected home fault")

// routedHomes is a TenantHomeResolver whose answers each test sets.
type routedHomes struct {
	inHome     bool
	inHomeErr  error
	resolveErr error
	store      kvstore.KVStore
}

func (h *routedHomes) InHome(context.Context, string) (bool, error) {
	return h.inHome, h.inHomeErr
}

func (h *routedHomes) Resolve(context.Context, string) (kvstore.KVStore, error) {
	return h.store, h.resolveErr
}

func newRoutedStorage(t *testing.T, homes TenantHomeResolver) (*tenantRoutedVectorStorage, *vectorService) {
	t.Helper()
	svc, err := NewVectorService(vector.NewMemoryVectorStorage(), newVecTestLogger())
	require.NoError(t, err)
	vs, ok := svc.(*vectorService)
	require.True(t, ok)
	if homes != nil {
		vs.SetTenantHomeResolver(homes)
	}
	return &tenantRoutedVectorStorage{v: vs}, vs
}

// TestTenantRoutedVectorStorage_HomeRoundTrip drives every routed method
// against a tenant's home store.
func TestTenantRoutedVectorStorage_HomeRoundTrip(t *testing.T) {
	homeKV := kvstoretest.NewGuardedMemoryStore()
	r, vs := newRoutedStorage(t, &routedHomes{inHome: true, store: homeKV})
	ctx := context.Background()
	ns := scopedNamespace("acme", "docs")

	require.NoError(t, r.CreateNamespace(ctx, &vector.NamespaceInfo{Name: ns, Dimension: 2, DistanceMetric: "cosine"}))
	require.NoError(t, r.UpsertVector(ctx, ns, &vector.VectorValue{ID: 1, Embedding: []float32{1, 0}, Metadata: map[string]string{"k": "v"}}))
	require.NoError(t, r.BatchUpsertVectors(ctx, ns, []*vector.VectorValue{
		{ID: 2, Embedding: []float32{0, 1}, Metadata: map[string]string{"k": "w"}},
		{ID: 3, Embedding: []float32{1, 1}},
	}))
	got, err := r.GetVector(ctx, ns, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), got.ID)

	_, err = r.ScanByMetadata(ctx, ns, "k", "v", nil)
	require.NoError(t, err)
	_, err = r.ScanByMultipleFilters(ctx, ns, []vector.MetadataFilter{{Field: "k", Value: "w"}}, nil)
	require.NoError(t, err)
	all, err := r.ScanNamespace(ctx, ns, nil)
	require.NoError(t, err)
	require.Len(t, all, 3)

	_, err = r.UpdateNamespace(ctx, ns, &vector.NamespaceUpdate{Metadata: map[string]string{"m": "1"}})
	require.NoError(t, err)
	info, err := r.GetNamespaceInfo(ctx, ns)
	require.NoError(t, err)
	require.Equal(t, ns, info.Name)
	_, err = r.GetStats(ctx, ns)
	require.NoError(t, err)
	_, err = r.ListNamespaces(ctx)
	require.NoError(t, err)

	_, err = r.NextID(ns)
	require.NoError(t, err)
	ids, err := r.BatchAllocateIDs(ns, 2)
	require.NoError(t, err)
	require.Len(t, ids, 2)

	vs.noteHomeWrite(ctx, mustStorageFor(t, vs, "acme"), ns)
	require.NoError(t, r.DeleteVector(ctx, ns, 1))
	require.NoError(t, r.BatchDeleteVectors(ctx, ns, []uint64{2}))
	require.NoError(t, r.Compact(ctx, ns))
	require.NoError(t, r.CleanupTombstones(ctx, time.Hour))
	require.NoError(t, r.DeleteNamespace(ctx, ns))

	nodeNS, err := vs.storage.ListNamespaces(ctx)
	require.NoError(t, err)
	require.Empty(t, nodeNS, "home writes never reach the node-wide store")
	require.NoError(t, r.Close())
}

func mustStorageFor(t *testing.T, vs *vectorService, tenantID string) vector.Storage {
	t.Helper()
	st, err := vs.storageFor(context.Background(), tenantID)
	require.NoError(t, err)
	return st
}

// TestTenantRoutedVectorStorage_RefusesEveryMethod checks each routed method
// fails closed with a TenantVectorHomeError when the home cannot be reached.
func TestTenantRoutedVectorStorage_RefusesEveryMethod(t *testing.T) {
	ns := scopedNamespace("acme", "docs")
	calls := map[string]func(context.Context, *tenantRoutedVectorStorage) error{
		"upsert": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			return r.UpsertVector(ctx, ns, &vector.VectorValue{ID: 1, Embedding: []float32{1, 0}})
		},
		"get": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.GetVector(ctx, ns, 1)
			return err
		},
		"delete": func(ctx context.Context, r *tenantRoutedVectorStorage) error { return r.DeleteVector(ctx, ns, 1) },
		"batch upsert": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			return r.BatchUpsertVectors(ctx, ns, []*vector.VectorValue{{ID: 1, Embedding: []float32{1, 0}}})
		},
		"batch delete": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			return r.BatchDeleteVectors(ctx, ns, []uint64{1})
		},
		"scan metadata": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.ScanByMetadata(ctx, ns, "k", "v", nil)
			return err
		},
		"scan filters": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.ScanByMultipleFilters(ctx, ns, nil, nil)
			return err
		},
		"scan namespace": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.ScanNamespace(ctx, ns, nil)
			return err
		},
		"create": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			return r.CreateNamespace(ctx, &vector.NamespaceInfo{Name: ns, Dimension: 2})
		},
		"update": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.UpdateNamespace(ctx, ns, &vector.NamespaceUpdate{})
			return err
		},
		"delete namespace": func(ctx context.Context, r *tenantRoutedVectorStorage) error { return r.DeleteNamespace(ctx, ns) },
		"info": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.GetNamespaceInfo(ctx, ns)
			return err
		},
		"stats": func(ctx context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.GetStats(ctx, ns)
			return err
		},
		"next id": func(_ context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.NextID(ns)
			return err
		},
		"batch ids": func(_ context.Context, r *tenantRoutedVectorStorage) error {
			_, err := r.BatchAllocateIDs(ns, 2)
			return err
		},
		"compact": func(ctx context.Context, r *tenantRoutedVectorStorage) error { return r.Compact(ctx, ns) },
	}
	homes := map[string]TenantHomeResolver{
		"no resolver":   nil,
		"in-home fault": &routedHomes{inHomeErr: errHomeFault},
		"resolve fault": &routedHomes{inHome: true, resolveErr: errHomeFault},
		"nil store":     &routedHomes{inHome: true},
	}
	for homeName, h := range homes {
		for callName, call := range calls {
			t.Run(homeName+"/"+callName, func(t *testing.T) {
				r, _ := newRoutedStorage(t, h)
				err := call(context.Background(), r)
				homeErr, ok := errors.AsType[*TenantVectorHomeError](err)
				require.True(t, ok, "got %v", err)
				require.Equal(t, "acme", homeErr.TenantID)
			})
		}
	}
}

// TestVectorService_StorageForRoutesAndCaches covers the node-wide fallback,
// the home cache and resolver replacement.
func TestVectorService_StorageForRoutesAndCaches(t *testing.T) {
	ctx := context.Background()
	homes := &routedHomes{}
	_, vs := newRoutedStorage(t, homes)

	st, err := vs.storageFor(ctx, "acme")
	require.NoError(t, err)
	require.Same(t, vs.storage, st, "a tenant outside its home uses the node-wide store")

	homes.inHome = true
	homes.store = kvstoretest.NewGuardedMemoryStore()
	first := mustStorageFor(t, vs, "acme")
	require.NotSame(t, vs.storage, first)
	require.Same(t, first, mustStorageFor(t, vs, "acme"), "the same home store is cached")

	homes.store = kvstoretest.NewGuardedMemoryStore()
	require.NotSame(t, first, mustStorageFor(t, vs, "acme"), "a replaced home store is rebuilt")

	vs.SetTenantHomeResolver(nil)
	_, err = vs.storageFor(ctx, "acme")
	_, ok := errors.AsType[*TenantVectorHomeError](err)
	require.True(t, ok, "got %v", err)

	// noteHomeWrite ignores the node-wide store.
	vs.noteHomeWrite(ctx, vs.storage, scopedNamespace("acme", "docs"))
}
