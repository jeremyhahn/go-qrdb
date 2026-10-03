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
// mockKVServicer for kvServiceAdapter tests
// ---------------------------------------------------------------------------

// newMockKVStore lives HERE, beside the type it constructs and with no build
// constraint. It used to sit in coverage_backfill_test.go, which is
// `!integration`, while seal_disabled_routing_test.go consumes it under no
// constraint at all -- so under BUILD_TAGS+integration the constructor vanished
// and this package stopped typechecking (#203). A test helper's constraint must
// be a SUPERSET of every caller's; the union of this one's callers is
// unconstrained, so it belongs in an unconstrained file.

// newMockKVStore creates a mockKVServicer backed by the provided store map,
// with Put/Get/Delete/Scan/List all wired to the in-memory store.
func newMockKVStore(store map[string][]byte) *mockKVServicer {
	return &mockKVServicer{
		putFn: func(_ context.Context, key string, value []byte) error {
			store[key] = value
			return nil
		},
		// The guarded write and its release must act on the SAME map the
		// plain ones do. The zero-value mock reports PutIfAbsent success
		// WITHOUT storing, which makes a claim look taken and then vanish —
		// every by-id route then falls back to the caller's own tenant and a
		// test over this mock cannot exercise routing at all.
		putIfAbsentFn: func(_ context.Context, key string, value []byte) (bool, error) {
			if _, exists := store[key]; exists {
				return false, nil
			}
			store[key] = value
			return true, nil
		},
		compareAndDeleteFn: func(_ context.Context, key string, expected []byte) (bool, error) {
			current, exists := store[key]
			if !exists || !bytes.Equal(current, expected) {
				return false, nil
			}
			delete(store, key)
			return true, nil
		},
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if v, ok := store[key]; ok {
				return v, nil
			}
			return nil, &service.KeyNotFoundError{Key: key}
		},
		deleteFn: func(_ context.Context, key string) error {
			delete(store, key)
			return nil
		},
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			result := make(map[string][]byte)
			for k, v := range store {
				if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
					result[k] = v
				}
			}
			return result, nil
		},
		listFn: func(_ context.Context, prefix string) ([]string, error) {
			var keys []string
			for k := range store {
				if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
					keys = append(keys, k)
				}
			}
			return keys, nil
		},
	}
}

// mockTransactionServicer implements service.TransactionServicer over the
// SAME store map a mockKVServicer wraps (newMockKVStore's own store
// parameter), so a guarded write via PutAllIfUnchanged and a plain Get/Put
// through the KVServicer mock observe one consistent view — exactly as the
// real guardedKVServiceAdapter and kvServiceAdapter do over one
// Raft-applied state machine. Without this, go-qrdb#555's atomic
// role/user creation path would write into a transaction plane the rest of
// the mock stack never sees.
func newMockTransactionServicer(store map[string][]byte) *mockTransactionServicer {
	return &mockTransactionServicer{store: store}
}

type mockTransactionServicer struct {
	store map[string][]byte
}

// Execute evaluates every compare against the shared store and applies
// successOps when all pass, failureOps otherwise. Value-equality and
// version compares — what go-qrdb#555's guarded batch actually uses, since
// a nil Guard is expressed as a version-0 compare rather than a value
// compare against nil/empty (go-qrdb#555 review round 3), and a non-nil
// Guard now ADDS a version-greater-than-zero existence compare ahead of its
// value compare (go-qrdb#555 review round 4) — are implemented; anything
// else is unreachable through this package's tests.
func (m *mockTransactionServicer) Execute(_ context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	ok := true
	for _, c := range compares {
		switch c.Target {
		case service.CompareValue:
			if c.Result != service.CompareEqual || !bytes.Equal(m.store[string(c.Key)], c.Value) {
				ok = false
			}
		case service.CompareVersion:
			// This mock keeps no real version counter, only presence — which
			// is all a version-0 ("absent") or version->0 ("present") compare
			// needs. A present key with an EMPTY value must still count as
			// version 1, matching every real engine (pkg/mvcc's
			// RaftCompare.evaluate, pkg/store's pebbleEvaluateCompare): a
			// value-based mock here would make this exact defect (an empty
			// value satisfying a nil guard) invisible again.
			_, exists := m.store[string(c.Key)]
			version := int64(0)
			if exists {
				version = 1
			}
			switch c.Result {
			case service.CompareEqual:
				ok = version == c.Version
			case service.CompareGreater:
				// existsGuard: the key must be present, i.e. version > 0.
				ok = version > c.Version
			default:
				ok = false
			}
		default:
			ok = false
		}
		if !ok {
			break
		}
	}
	ops := failureOps
	if ok {
		ops = successOps
	}
	for _, op := range ops {
		switch op.Type {
		case service.OpPut:
			m.store[string(op.Key)] = op.Value
		case service.OpDelete:
			delete(m.store, string(op.Key))
		}
	}
	return &service.TransactionResponse{Succeeded: ok}, nil
}

func (m *mockTransactionServicer) ExecuteWithMode(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	return m.Execute(ctx, compares, successOps, failureOps)
}

func (m *mockTransactionServicer) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	return nil, errors.New("mockTransactionServicer: ExecuteEntity not implemented")
}

type mockKVServicer struct {
	putFn              func(ctx context.Context, key string, value []byte) error
	putIfAbsentFn      func(ctx context.Context, key string, value []byte) (bool, error)
	compareAndDeleteFn func(ctx context.Context, key string, expected []byte) (bool, error)
	getFn              func(ctx context.Context, key string) ([]byte, error)
	deleteFn           func(ctx context.Context, key string) error
	scanFn             func(ctx context.Context, prefix string) (map[string][]byte, error)
	listFn             func(ctx context.Context, prefix string) ([]string, error)
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

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *mockKVServicer) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if m.compareAndDeleteFn != nil {
		return m.compareAndDeleteFn(ctx, key, expected)
	}
	return false, nil
}

func (m *mockKVServicer) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
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

// TestKVServiceAdapter_QueryCompoundIndex_Refuses pins the typed refusal.
//
// These tests previously asserted a nil error and a nil result — the
// (nil, nil) answer the KVStore contract now forbids, because it is
// indistinguishable from a compound index with no matches. A DAO over this
// adapter would have reported "no records" for a lookup it never performed.
func TestKVServiceAdapter_QueryCompoundIndex_Refuses(t *testing.T) {
	adapter := newKVServiceAdapter(&mockKVServicer{}, nil, nil)
	result, err := adapter.QueryCompoundIndex(context.Background(), "idx", [][]byte{[]byte("v1")})
	require.ErrorIs(t, err, kvstore.ErrIndexUnsupported)
	assert.Nil(t, result, "a refused query carries no results")
}

func TestKVServiceAdapter_QueryCompoundIndex_RefusesEmptyValues(t *testing.T) {
	adapter := newKVServiceAdapter(&mockKVServicer{}, nil, nil)
	result, err := adapter.QueryCompoundIndex(context.Background(), "", nil)
	require.ErrorIs(t, err, kvstore.ErrIndexUnsupported)
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// kvServiceAdapter.ScanCompoundIndex tests
// ---------------------------------------------------------------------------

// TestKVServiceAdapter_ScanCompoundIndex_Refuses pins the typed refusal; see
// TestKVServiceAdapter_QueryCompoundIndex_Refuses.
func TestKVServiceAdapter_ScanCompoundIndex_Refuses(t *testing.T) {
	adapter := newKVServiceAdapter(&mockKVServicer{}, nil, nil)
	result, err := adapter.ScanCompoundIndex(context.Background(), "idx", [][]byte{[]byte("v1")}, []byte("start"), []byte("end"))
	require.ErrorIs(t, err, kvstore.ErrIndexUnsupported)
	assert.Nil(t, result, "a refused scan carries no results")
}

func TestKVServiceAdapter_ScanCompoundIndex_RefusesEmptyValues(t *testing.T) {
	adapter := newKVServiceAdapter(&mockKVServicer{}, nil, nil)
	result, err := adapter.ScanCompoundIndex(context.Background(), "", nil, nil, nil)
	require.ErrorIs(t, err, kvstore.ErrIndexUnsupported)
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

// ListAll mirrors List on this double: it applies no internal-key guard,
// so the filtered and unfiltered enumerations are the same call.
func (m *mockKVServicer) ListAll(ctx context.Context, prefix string) ([]string, error) {
	return m.List(ctx, prefix)
}
