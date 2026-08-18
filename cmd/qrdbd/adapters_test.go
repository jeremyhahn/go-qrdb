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
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// AuthBootstrapError tests
// ---------------------------------------------------------------------------

func TestAuthBootstrapError_Error(t *testing.T) {
	inner := errors.New("connect failed")
	err := &AuthBootstrapError{Step: "user store", Err: inner}
	want := `cli: auth bootstrap failed at "user store": connect failed`
	assert.Equal(t, want, err.Error())
}

func TestAuthBootstrapError_Unwrap(t *testing.T) {
	inner := errors.New("underlying cause")
	err := &AuthBootstrapError{Step: "role store", Err: inner}
	assert.Equal(t, inner, err.Unwrap())
	assert.True(t, errors.Is(err, inner))
}

func TestAuthBootstrapError_ErrorsAs(t *testing.T) {
	inner := errors.New("db timeout")
	err := &AuthBootstrapError{Step: "audit logger", Err: inner}
	var target *AuthBootstrapError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, "audit logger", target.Step)
}

// ---------------------------------------------------------------------------
// AdminCertError tests
// ---------------------------------------------------------------------------

func TestAdminCertError_Error(t *testing.T) {
	inner := errors.New("key generation failed")
	err := &AdminCertError{Err: inner}
	want := "cli: admin client certificate generation failed: key generation failed"
	assert.Equal(t, want, err.Error())
}

func TestAdminCertError_Unwrap(t *testing.T) {
	inner := errors.New("rand source exhausted")
	err := &AdminCertError{Err: inner}
	assert.Equal(t, inner, err.Unwrap())
	assert.True(t, errors.Is(err, inner))
}

// ---------------------------------------------------------------------------
// AdminCertWriteError tests
// ---------------------------------------------------------------------------

func TestAdminCertWriteError_Error(t *testing.T) {
	inner := errors.New("permission denied")
	err := &AdminCertWriteError{Path: "/data/admin-cert.pem", Err: inner}
	want := `cli: failed to write admin certificate to "/data/admin-cert.pem": permission denied`
	assert.Equal(t, want, err.Error())
}

func TestAdminCertWriteError_Unwrap(t *testing.T) {
	inner := errors.New("disk full")
	err := &AdminCertWriteError{Path: "/data/admin-key.pem", Err: inner}
	assert.Equal(t, inner, err.Unwrap())
	assert.True(t, errors.Is(err, inner))
}

func TestAdminCertWriteError_ErrorsAs(t *testing.T) {
	inner := errors.New("no space left")
	err := &AdminCertWriteError{Path: "/tmp/cert.pem", Err: inner}
	var target *AdminCertWriteError
	require.True(t, errors.As(err, &target))
	assert.Equal(t, "/tmp/cert.pem", target.Path)
}

// ---------------------------------------------------------------------------
// mockKVServicer for kvServiceAdapter tests
// ---------------------------------------------------------------------------

type mockKVServicer struct {
	putFn         func(ctx context.Context, key string, value []byte) error
	putIfAbsentFn func(ctx context.Context, key string, value []byte) (bool, error)
	getFn         func(ctx context.Context, key string) ([]byte, error)
	deleteFn      func(ctx context.Context, key string) error
	scanFn        func(ctx context.Context, prefix string) (map[string][]byte, error)
	listFn        func(ctx context.Context, prefix string) ([]string, error)
}

func (m *mockKVServicer) Put(ctx context.Context, key string, value []byte) error {
	if m.putFn != nil {
		return m.putFn(ctx, key, value)
	}
	return nil
}

func (m *mockKVServicer) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if m.putIfAbsentFn != nil {
		return m.putIfAbsentFn(ctx, key, value)
	}
	return true, nil
}

func (m *mockKVServicer) Get(ctx context.Context, key string) ([]byte, error) {
	if m.getFn != nil {
		return m.getFn(ctx, key)
	}
	return nil, nil
}

func (m *mockKVServicer) Delete(ctx context.Context, key string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, key)
	}
	return nil
}

func (m *mockKVServicer) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, prefix)
	}
	return nil, nil
}

func (m *mockKVServicer) List(ctx context.Context, prefix string) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx, prefix)
	}
	return nil, nil
}

func (m *mockKVServicer) GetStale(ctx context.Context, key string) ([]byte, error) {
	return m.Get(ctx, key)
}

func (m *mockKVServicer) ListStale(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}

func (m *mockKVServicer) ScanStale(ctx context.Context, prefix string) (map[string][]byte, error) {
	return m.Scan(ctx, prefix)
}

// Stub methods for the rest of KVServicer interface that are not used
// by kvServiceAdapter.
func (m *mockKVServicer) PutEntity(_ context.Context, _ *service.PutEntityRequest) (*service.PutEntityResponse, error) {
	return nil, nil
}
func (m *mockKVServicer) GetEntity(_ context.Context, _ *service.GetEntityRequest) (*service.GetEntityResponse, error) {
	return nil, nil
}
func (m *mockKVServicer) DeleteEntity(_ context.Context, _ *service.DeleteEntityRequest) (*service.DeleteEntityResponse, error) {
	return nil, nil
}
func (m *mockKVServicer) ListEntities(_ context.Context, _ *service.ListEntitiesRequest) (*service.ListEntitiesResponse, error) {
	return nil, nil
}

func (m *mockKVServicer) PutWithMode(_ context.Context, _ string, _ []byte, _ writemode.Mode) error {
	return nil
}

func (m *mockKVServicer) DeleteWithMode(_ context.Context, _ string, _ writemode.Mode) error {
	return nil
}

func (m *mockKVServicer) GetWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]byte, error) {
	return nil, nil
}

func (m *mockKVServicer) ListWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) ([]string, error) {
	return nil, nil
}

func (m *mockKVServicer) ScanWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (map[string][]byte, error) {
	return nil, nil
}

func (m *mockKVServicer) ExistsWithConsistency(_ context.Context, _ string, _ consistency.ReadOptions) (bool, error) {
	return false, nil
}

// ---------------------------------------------------------------------------
// kvServiceAdapter.Put tests
// ---------------------------------------------------------------------------

func TestKVServiceAdapter_Put_Success(t *testing.T) {
	var capturedKey string
	var capturedVal []byte
	mock := &mockKVServicer{
		putFn: func(_ context.Context, key string, value []byte) error {
			capturedKey = key
			capturedVal = value
			return nil
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	err := adapter.Put(context.Background(), "mykey", []byte("myval"))
	require.NoError(t, err)
	assert.Equal(t, "mykey", capturedKey)
	assert.Equal(t, []byte("myval"), capturedVal)
}

func TestKVServiceAdapter_Put_Error(t *testing.T) {
	sentinel := errors.New("put failed")
	mock := &mockKVServicer{
		putFn: func(_ context.Context, _ string, _ []byte) error {
			return sentinel
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	err := adapter.Put(context.Background(), "k", []byte("v"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

// ---------------------------------------------------------------------------
// kvServiceAdapter.PutIfAbsent tests
// ---------------------------------------------------------------------------

// TestKVServiceAdapter_SatisfiesAtomicKVStore pins the capability the adapter
// advertises. Callers that need exactly-once semantics — custodian
// registration claiming a username, invite redemption — discover it by type
// assertion and refuse a store without it, so losing this assertion would fail
// those services at startup rather than anywhere near this file.
func TestKVServiceAdapter_SatisfiesAtomicKVStore(t *testing.T) {
	adapter := newKVServiceAdapter(&mockKVServicer{}, nil, nil)
	_, ok := adapter.(kvstore.AtomicKVStore)
	assert.True(t, ok, "the Raft-backed adapter must carry the atomic conditional write")
}

func TestKVServiceAdapter_PutIfAbsent_Stored(t *testing.T) {
	var capturedKey string
	var capturedVal []byte
	mock := &mockKVServicer{
		putIfAbsentFn: func(_ context.Context, key string, value []byte) (bool, error) {
			capturedKey = key
			capturedVal = value
			return true, nil
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil).(kvstore.AtomicKVStore)

	stored, err := adapter.PutIfAbsent(context.Background(), "mykey", []byte("myval"))
	require.NoError(t, err)
	assert.True(t, stored)
	assert.Equal(t, "mykey", capturedKey)
	assert.Equal(t, []byte("myval"), capturedVal)
}

// TestKVServiceAdapter_PutIfAbsent_NotStored covers the outcome the capability
// exists for: the key was already present, which is reported as stored=false
// with a nil error and must not be flattened into success.
func TestKVServiceAdapter_PutIfAbsent_NotStored(t *testing.T) {
	mock := &mockKVServicer{
		putIfAbsentFn: func(_ context.Context, _ string, _ []byte) (bool, error) {
			return false, nil
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil).(kvstore.AtomicKVStore)

	stored, err := adapter.PutIfAbsent(context.Background(), "k", []byte("v"))
	require.NoError(t, err)
	assert.False(t, stored)
}

func TestKVServiceAdapter_PutIfAbsent_Error(t *testing.T) {
	sentinel := errors.New("conditional put failed")
	mock := &mockKVServicer{
		putIfAbsentFn: func(_ context.Context, _ string, _ []byte) (bool, error) {
			return false, sentinel
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil).(kvstore.AtomicKVStore)

	stored, err := adapter.PutIfAbsent(context.Background(), "k", []byte("v"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
	assert.False(t, stored)
}

// ---------------------------------------------------------------------------
// kvServiceAdapter.Get tests (augments existing 71.4% coverage)
// ---------------------------------------------------------------------------

func TestKVServiceAdapter_Get_KeyNotFound(t *testing.T) {
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, &service.KeyNotFoundError{Key: "missing"}
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	val, err := adapter.Get(context.Background(), "missing")
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestKVServiceAdapter_Get_OtherError(t *testing.T) {
	sentinel := errors.New("storage failure")
	mock := &mockKVServicer{
		getFn: func(_ context.Context, _ string) ([]byte, error) {
			return nil, sentinel
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	val, err := adapter.Get(context.Background(), "key")
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
	assert.Nil(t, val)
}

func TestKVServiceAdapter_Get_Success(t *testing.T) {
	mock := &mockKVServicer{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if key == "found" {
				return []byte("value"), nil
			}
			return nil, &service.KeyNotFoundError{Key: key}
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	val, err := adapter.Get(context.Background(), "found")
	require.NoError(t, err)
	assert.Equal(t, []byte("value"), val)
}

// ---------------------------------------------------------------------------
// kvServiceAdapter.Delete tests
// ---------------------------------------------------------------------------

func TestKVServiceAdapter_Delete_Success(t *testing.T) {
	var capturedKey string
	mock := &mockKVServicer{
		deleteFn: func(_ context.Context, key string) error {
			capturedKey = key
			return nil
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	err := adapter.Delete(context.Background(), "deletekey")
	require.NoError(t, err)
	assert.Equal(t, "deletekey", capturedKey)
}

func TestKVServiceAdapter_Delete_Error(t *testing.T) {
	sentinel := errors.New("delete refused")
	mock := &mockKVServicer{
		deleteFn: func(_ context.Context, _ string) error {
			return sentinel
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	err := adapter.Delete(context.Background(), "key")
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

// ---------------------------------------------------------------------------
// kvServiceAdapter.Scan tests
// ---------------------------------------------------------------------------

func TestKVServiceAdapter_Scan_Success(t *testing.T) {
	mock := &mockKVServicer{
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			return map[string][]byte{
				prefix + "a": []byte("va"),
				prefix + "b": []byte("vb"),
			}, nil
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	collected := make(map[string][]byte)
	err := adapter.Scan(context.Background(), "pfx/", func(key string, value []byte) error {
		collected[string(key)] = value
		return nil
	})
	require.NoError(t, err)
	assert.Len(t, collected, 2)
	assert.Equal(t, []byte("va"), collected["pfx/a"])
	assert.Equal(t, []byte("vb"), collected["pfx/b"])
}

func TestKVServiceAdapter_Scan_ServiceError(t *testing.T) {
	sentinel := errors.New("scan failed")
	mock := &mockKVServicer{
		scanFn: func(_ context.Context, _ string) (map[string][]byte, error) {
			return nil, sentinel
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	err := adapter.Scan(context.Background(), "pfx/", func(_ string, _ []byte) error {
		t.Fatal("callback should not be called on service error")
		return nil
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

func TestKVServiceAdapter_Scan_CallbackError(t *testing.T) {
	mock := &mockKVServicer{
		scanFn: func(_ context.Context, _ string) (map[string][]byte, error) {
			return map[string][]byte{"k1": []byte("v1")}, nil
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	sentinel := errors.New("callback abort")
	err := adapter.Scan(context.Background(), "", func(_ string, _ []byte) error {
		return sentinel
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
}

// ---------------------------------------------------------------------------
// kvServiceAdapter.List tests
// ---------------------------------------------------------------------------

func TestKVServiceAdapter_List_Success(t *testing.T) {
	mock := &mockKVServicer{
		listFn: func(_ context.Context, prefix string) ([]string, error) {
			return []string{prefix + "alpha", prefix + "beta"}, nil
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	result, err := adapter.List(context.Background(), "ns/")
	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, "ns/alpha", result[0])
	assert.Equal(t, "ns/beta", result[1])
}

func TestKVServiceAdapter_List_Error(t *testing.T) {
	sentinel := errors.New("list failed")
	mock := &mockKVServicer{
		listFn: func(_ context.Context, _ string) ([]string, error) {
			return nil, sentinel
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	result, err := adapter.List(context.Background(), "pfx")
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel))
	assert.Nil(t, result)
}

func TestKVServiceAdapter_List_Empty(t *testing.T) {
	mock := &mockKVServicer{
		listFn: func(_ context.Context, _ string) ([]string, error) {
			return []string{}, nil
		},
	}
	adapter := newKVServiceAdapter(mock, nil, nil)

	result, err := adapter.List(context.Background(), "empty/")
	require.NoError(t, err)
	assert.Empty(t, result)
}

// ---------------------------------------------------------------------------
// kvServiceAdapter.QueryCompoundIndex tests
// ---------------------------------------------------------------------------

func TestKVServiceAdapter_QueryCompoundIndex_NoOp(t *testing.T) {
	adapter := newKVServiceAdapter(&mockKVServicer{}, nil, nil)
	result, err := adapter.QueryCompoundIndex(context.Background(), "idx", [][]byte{[]byte("v1")})
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestKVServiceAdapter_QueryCompoundIndex_EmptyValues(t *testing.T) {
	adapter := newKVServiceAdapter(&mockKVServicer{}, nil, nil)
	result, err := adapter.QueryCompoundIndex(context.Background(), "", nil)
	require.NoError(t, err)
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// kvServiceAdapter.ScanCompoundIndex tests
// ---------------------------------------------------------------------------

func TestKVServiceAdapter_ScanCompoundIndex_NoOp(t *testing.T) {
	adapter := newKVServiceAdapter(&mockKVServicer{}, nil, nil)
	result, err := adapter.ScanCompoundIndex(context.Background(), "idx", [][]byte{[]byte("v1")}, []byte("start"), []byte("end"))
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestKVServiceAdapter_ScanCompoundIndex_EmptyValues(t *testing.T) {
	adapter := newKVServiceAdapter(&mockKVServicer{}, nil, nil)
	result, err := adapter.ScanCompoundIndex(context.Background(), "", nil, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// kvServiceAdapter compile-time interface check
// ---------------------------------------------------------------------------

func TestKVServiceAdapter_ImplementsKVStore(t *testing.T) {
	mock := &mockKVServicer{}
	adapter := newKVServiceAdapter(mock, nil, nil)
	// Confirm the adapter satisfies kvstore.KVStore at runtime.
	require.NotNil(t, adapter)
}
