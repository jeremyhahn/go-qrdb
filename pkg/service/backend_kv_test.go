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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	memorystorage "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// newBackendKVService creates a BackendKVService backed by in-memory storage
// for use in unit tests. Each call returns a fresh, independent instance.
func newBackendKVService(t *testing.T) (*BackendKVService, func()) {
	t.Helper()
	store := memorystorage.NewStorage()
	backend := database.NewStandaloneBackend(store)
	svc := NewBackendKVService(backend)
	cleanup := func() {
		_ = store.Close()
	}
	return svc, cleanup
}

// ---------------------------------------------------------------------------
// NewBackendKVService
// ---------------------------------------------------------------------------

func TestNewBackendKVService_ReturnsNonNil(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()
	assert.NotNil(t, svc)
}

func TestNewBackendKVService_ImplementsKVServicer(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()
	var _ KVServicer = svc
}

// ---------------------------------------------------------------------------
// Put / Get
// ---------------------------------------------------------------------------

func TestPut_StoresValue(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "key1", []byte("value1")))

	got, err := svc.Get(ctx, "key1")
	require.NoError(t, err)
	assert.Equal(t, []byte("value1"), got)
}

func TestPut_OverwritesExistingKey(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "key1", []byte("original")))
	require.NoError(t, svc.Put(ctx, "key1", []byte("updated")))

	got, err := svc.Get(ctx, "key1")
	require.NoError(t, err)
	assert.Equal(t, []byte("updated"), got)
}

func TestGet_KeyNotFound(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	_, err := svc.Get(context.Background(), "nonexistent")
	require.Error(t, err)

	var knf *KeyNotFoundError
	assert.True(t, errors.As(err, &knf), "expected KeyNotFoundError, got %T: %v", err, err)
	assert.Equal(t, "nonexistent", knf.Key)
}

func TestGet_ReturnsCorrectValue(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value []byte
	}{
		{"simple string value", "mykey", []byte("hello")},
		{"binary value", "binkey", []byte{0x00, 0xFF, 0x42}},
		{"empty value", "emptyval", []byte{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, cleanup := newBackendKVService(t)
			defer cleanup()

			ctx := context.Background()
			require.NoError(t, svc.Put(ctx, tc.key, tc.value))

			got, err := svc.Get(ctx, tc.key)
			require.NoError(t, err)
			assert.Equal(t, tc.value, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestDelete_RemovesExistingKey(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "delme", []byte("val")))
	require.NoError(t, svc.Delete(ctx, "delme"))

	_, err := svc.Get(ctx, "delme")
	var knf *KeyNotFoundError
	assert.True(t, errors.As(err, &knf))
}

func TestDelete_NonExistentKeyIsNoOp(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	// Deleting a key that does not exist must not return an error.
	err := svc.Delete(context.Background(), "ghost")
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestList_WithPrefix(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "app/a", []byte("1")))
	require.NoError(t, svc.Put(ctx, "app/b", []byte("2")))
	require.NoError(t, svc.Put(ctx, "other/c", []byte("3")))

	keys, err := svc.List(ctx, "app/")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"app/a", "app/b"}, keys)
}

func TestList_EmptyPrefix_ReturnsAllUserKeys(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "x", []byte("1")))
	require.NoError(t, svc.Put(ctx, "y", []byte("2")))

	keys, err := svc.List(ctx, "")
	require.NoError(t, err)
	assert.Contains(t, keys, "x")
	assert.Contains(t, keys, "y")
}

func TestList_FiltersSystemKeys(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "user/1", []byte("u1")))
	require.NoError(t, svc.Put(ctx, InternalKeyPrefixSystem+"role/admin", []byte("r1")))

	keys, err := svc.List(ctx, "")
	require.NoError(t, err)

	for _, k := range keys {
		assert.False(t, len(k) >= len(InternalKeyPrefixSystem) && k[:len(InternalKeyPrefixSystem)] == InternalKeyPrefixSystem,
			"system key %q must be filtered from List results", k)
	}
}

func TestList_SystemPrefixQueryPassesThrough(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	sysKey := InternalKeyPrefixSystem + "role/admin"
	require.NoError(t, svc.Put(ctx, sysKey, []byte("data")))

	keys, err := svc.List(ctx, InternalKeyPrefixSystem)
	require.NoError(t, err)
	assert.Contains(t, keys, sysKey)
}

func TestList_ReturnsSortedOrder(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "z", []byte("1")))
	require.NoError(t, svc.Put(ctx, "a", []byte("2")))
	require.NoError(t, svc.Put(ctx, "m", []byte("3")))

	keys, err := svc.List(ctx, "")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(keys), 3)

	// Verify the three inserted keys appear in sorted order.
	var got []string
	for _, k := range keys {
		if k == "a" || k == "m" || k == "z" {
			got = append(got, k)
		}
	}
	assert.Equal(t, []string{"a", "m", "z"}, got)
}

func TestList_NoMatchingPrefix_ReturnsEmptySlice(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "aaa", []byte("v")))

	keys, err := svc.List(ctx, "zzz")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// ---------------------------------------------------------------------------
// Scan
// ---------------------------------------------------------------------------

func TestScan_WithPrefix(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "ns/a", []byte("va")))
	require.NoError(t, svc.Put(ctx, "ns/b", []byte("vb")))
	require.NoError(t, svc.Put(ctx, "other/c", []byte("vc")))

	result, err := svc.Scan(ctx, "ns/")
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{
		"ns/a": []byte("va"),
		"ns/b": []byte("vb"),
	}, result)
}

func TestScan_EmptyResult(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	result, err := svc.Scan(context.Background(), "nope/")
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestScan_FiltersSystemKeys(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "data/k", []byte("v")))
	require.NoError(t, svc.Put(ctx, InternalKeyPrefixSystem+"internal", []byte("secret")))

	result, err := svc.Scan(ctx, "")
	require.NoError(t, err)

	for k := range result {
		assert.False(t, len(k) >= len(InternalKeyPrefixSystem) && k[:len(InternalKeyPrefixSystem)] == InternalKeyPrefixSystem,
			"system key %q must be filtered from Scan results", k)
	}
}

// ---------------------------------------------------------------------------
// GetStale / ListStale / ScanStale
// ---------------------------------------------------------------------------

func TestGetStale_DelegatesToGet(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "stale/k", []byte("sv")))

	got, err := svc.GetStale(ctx, "stale/k")
	require.NoError(t, err)
	assert.Equal(t, []byte("sv"), got)
}

func TestGetStale_NonExistentReturnsKeyNotFoundError(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	_, err := svc.GetStale(context.Background(), "missing")
	var knf *KeyNotFoundError
	assert.True(t, errors.As(err, &knf))
}

func TestListStale_DelegatesToList(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "sl/x", []byte("1")))
	require.NoError(t, svc.Put(ctx, "sl/y", []byte("2")))

	keys, err := svc.ListStale(ctx, "sl/")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"sl/x", "sl/y"}, keys)
}

func TestListStale_EmptyPrefix(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "alpha", []byte("a")))

	keys, err := svc.ListStale(ctx, "")
	require.NoError(t, err)
	assert.Contains(t, keys, "alpha")
}

func TestScanStale_DelegatesToScan(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "ss/1", []byte("one")))
	require.NoError(t, svc.Put(ctx, "ss/2", []byte("two")))

	result, err := svc.ScanStale(ctx, "ss/")
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{
		"ss/1": []byte("one"),
		"ss/2": []byte("two"),
	}, result)
}

func TestScanStale_EmptyResult(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	result, err := svc.ScanStale(context.Background(), "absent/")
	require.NoError(t, err)
	assert.Empty(t, result)
}

// ---------------------------------------------------------------------------
// PutWithMode / DeleteWithMode
// ---------------------------------------------------------------------------

func TestPutWithMode_IgnoresMode_DelegatesToPut(t *testing.T) {
	modes := []writemode.Mode{
		writemode.Sync,
		writemode.WALDurable,
		writemode.Batched,
	}
	for _, m := range modes {
		t.Run(m.String(), func(t *testing.T) {
			svc, cleanup := newBackendKVService(t)
			defer cleanup()

			ctx := context.Background()
			require.NoError(t, svc.PutWithMode(ctx, "wm/key", []byte("val"), m))

			got, err := svc.Get(ctx, "wm/key")
			require.NoError(t, err)
			assert.Equal(t, []byte("val"), got)
		})
	}
}

func TestDeleteWithMode_IgnoresMode_DelegatesToDelete(t *testing.T) {
	modes := []writemode.Mode{
		writemode.Sync,
		writemode.WALDurable,
		writemode.Batched,
	}
	for _, m := range modes {
		t.Run(m.String(), func(t *testing.T) {
			svc, cleanup := newBackendKVService(t)
			defer cleanup()

			ctx := context.Background()
			require.NoError(t, svc.Put(ctx, "dm/key", []byte("val")))
			require.NoError(t, svc.DeleteWithMode(ctx, "dm/key", m))

			_, err := svc.Get(ctx, "dm/key")
			var knf *KeyNotFoundError
			assert.True(t, errors.As(err, &knf))
		})
	}
}

// ---------------------------------------------------------------------------
// GetWithConsistency / ListWithConsistency / ScanWithConsistency
// ---------------------------------------------------------------------------

func TestGetWithConsistency_IgnoresConsistency_ReturnsValue(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "cons/k", []byte("cv")))

	opts := consistency.ReadOptions{AllowStale: false}
	got, err := svc.GetWithConsistency(ctx, "cons/k", opts)
	require.NoError(t, err)
	assert.Equal(t, []byte("cv"), got)
}

func TestGetWithConsistency_NonExistentKey(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	_, err := svc.GetWithConsistency(context.Background(), "nokey", consistency.ReadOptions{})
	var knf *KeyNotFoundError
	assert.True(t, errors.As(err, &knf))
}

func TestListWithConsistency_IgnoresConsistency_ReturnsList(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "lc/a", []byte("1")))
	require.NoError(t, svc.Put(ctx, "lc/b", []byte("2")))

	keys, err := svc.ListWithConsistency(ctx, "lc/", consistency.ReadOptions{AllowStale: true})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"lc/a", "lc/b"}, keys)
}

func TestListWithConsistency_EmptyResult(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	keys, err := svc.ListWithConsistency(context.Background(), "missing/", consistency.ReadOptions{})
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestScanWithConsistency_IgnoresConsistency_ReturnsScan(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "sc/p", []byte("pv")))
	require.NoError(t, svc.Put(ctx, "sc/q", []byte("qv")))

	result, err := svc.ScanWithConsistency(ctx, "sc/", consistency.ReadOptions{})
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{
		"sc/p": []byte("pv"),
		"sc/q": []byte("qv"),
	}, result)
}

func TestScanWithConsistency_EmptyResult(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	result, err := svc.ScanWithConsistency(context.Background(), "void/", consistency.ReadOptions{})
	require.NoError(t, err)
	assert.Empty(t, result)
}

// ---------------------------------------------------------------------------
// ExistsWithConsistency
// ---------------------------------------------------------------------------

func TestExistsWithConsistency_TrueForExistingKey(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "exists/k", []byte("yes")))

	ok, err := svc.ExistsWithConsistency(ctx, "exists/k", consistency.ReadOptions{})
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestExistsWithConsistency_FalseForMissingKey(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ok, err := svc.ExistsWithConsistency(context.Background(), "no-such-key", consistency.ReadOptions{})
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestExistsWithConsistency_FalseAfterDelete(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, svc.Put(ctx, "gone", []byte("data")))
	require.NoError(t, svc.Delete(ctx, "gone"))

	ok, err := svc.ExistsWithConsistency(ctx, "gone", consistency.ReadOptions{})
	require.NoError(t, err)
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// PutEntity
// ---------------------------------------------------------------------------

func TestPutEntity_StoresEntity(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	resp, err := svc.PutEntity(ctx, &PutEntityRequest{
		EntityType: "user",
		ID:         "u1",
		Data:       []byte(`{"name":"Alice"}`),
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, int64(1), resp.Version)
	assert.Equal(t, int64(1), resp.Revision)
	assert.Positive(t, resp.Timestamp)
}

func TestPutEntity_EmptyTypeReturnsError(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		ID:   "u1",
		Data: []byte("data"),
	})
	var e *EntityEmptyTypeError
	assert.True(t, errors.As(err, &e))
}

func TestPutEntity_EmptyIDReturnsError(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "user",
		Data:       []byte("data"),
	})
	var e *EntityEmptyIDError
	assert.True(t, errors.As(err, &e))
	assert.Equal(t, EntityType("user"), e.EntityType)
}

func TestPutEntity_EmptyDataReturnsError(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "user",
		ID:         "u1",
		Data:       nil,
	})
	var e *EntityEmptyDataError
	assert.True(t, errors.As(err, &e))
}

func TestPutEntity_ZeroLengthDataReturnsError(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "user",
		ID:         "u1",
		Data:       []byte{},
	})
	var e *EntityEmptyDataError
	assert.True(t, errors.As(err, &e))
}

// ---------------------------------------------------------------------------
// GetEntity
// ---------------------------------------------------------------------------

func TestGetEntity_ReturnsStoredData(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	payload := []byte(`{"id":"e1"}`)
	_, err := svc.PutEntity(ctx, &PutEntityRequest{
		EntityType: "event",
		ID:         "e1",
		Data:       payload,
	})
	require.NoError(t, err)

	resp, err := svc.GetEntity(ctx, &GetEntityRequest{
		EntityType: "event",
		ID:         "e1",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, payload, resp.Data)
	assert.Equal(t, int64(1), resp.Version)
	assert.Equal(t, int64(1), resp.Revision)
	assert.Positive(t, resp.Timestamp)
}

func TestGetEntity_NonExistentReturnsKeyNotFoundError(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	_, err := svc.GetEntity(context.Background(), &GetEntityRequest{
		EntityType: "item",
		ID:         "missing",
	})
	var knf *KeyNotFoundError
	assert.True(t, errors.As(err, &knf))
}

func TestGetEntity_EmptyTypeReturnsError(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	_, err := svc.GetEntity(context.Background(), &GetEntityRequest{ID: "x"})
	var e *EntityEmptyTypeError
	assert.True(t, errors.As(err, &e))
}

func TestGetEntity_EmptyIDReturnsError(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	_, err := svc.GetEntity(context.Background(), &GetEntityRequest{EntityType: "config"})
	var e *EntityEmptyIDError
	assert.True(t, errors.As(err, &e))
	assert.Equal(t, EntityType("config"), e.EntityType)
}

// ---------------------------------------------------------------------------
// DeleteEntity
// ---------------------------------------------------------------------------

func TestDeleteEntity_RemovesEntity(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	_, err := svc.PutEntity(ctx, &PutEntityRequest{
		EntityType: "doc",
		ID:         "d1",
		Data:       []byte("content"),
	})
	require.NoError(t, err)

	resp, err := svc.DeleteEntity(ctx, &DeleteEntityRequest{
		EntityType: "doc",
		ID:         "d1",
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Deleted)
	assert.Equal(t, int64(1), resp.Revision)

	// Confirm entity is gone.
	_, err = svc.GetEntity(ctx, &GetEntityRequest{EntityType: "doc", ID: "d1"})
	var knf *KeyNotFoundError
	assert.True(t, errors.As(err, &knf))
}

func TestDeleteEntity_NonExistentEntityIsNoOp(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	resp, err := svc.DeleteEntity(context.Background(), &DeleteEntityRequest{
		EntityType: "doc",
		ID:         "ghost",
	})
	require.NoError(t, err)
	assert.True(t, resp.Deleted)
}

func TestDeleteEntity_EmptyTypeReturnsError(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	_, err := svc.DeleteEntity(context.Background(), &DeleteEntityRequest{ID: "x"})
	var e *EntityEmptyTypeError
	assert.True(t, errors.As(err, &e))
}

func TestDeleteEntity_EmptyIDReturnsError(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	_, err := svc.DeleteEntity(context.Background(), &DeleteEntityRequest{EntityType: "doc"})
	var e *EntityEmptyIDError
	assert.True(t, errors.As(err, &e))
}

// ---------------------------------------------------------------------------
// ListEntities
// ---------------------------------------------------------------------------

func TestListEntities_ReturnsStrippedIDs(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	for _, id := range []string{"alice", "bob", "carol"} {
		_, err := svc.PutEntity(ctx, &PutEntityRequest{
			EntityType: "user",
			ID:         id,
			Data:       []byte("data"),
		})
		require.NoError(t, err)
	}

	resp, err := svc.ListEntities(ctx, &ListEntitiesRequest{EntityType: "user"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"alice", "bob", "carol"}, resp.Keys)
	assert.Equal(t, int64(3), resp.TotalCount)
	assert.Empty(t, resp.NextPageToken)
}

func TestBackendListEntities_EmptyTypeReturnsError(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	_, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{})
	var e *EntityEmptyTypeError
	assert.True(t, errors.As(err, &e))
}

func TestBackendListEntities_EmptyResult(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	resp, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{EntityType: "empty"})
	require.NoError(t, err)
	assert.Empty(t, resp.Keys)
	assert.Equal(t, int64(0), resp.TotalCount)
}

func TestListEntities_WithPrefixFilter(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	for _, id := range []string{"admin-1", "admin-2", "user-1"} {
		_, err := svc.PutEntity(ctx, &PutEntityRequest{
			EntityType: "acct",
			ID:         id,
			Data:       []byte("data"),
		})
		require.NoError(t, err)
	}

	resp, err := svc.ListEntities(ctx, &ListEntitiesRequest{
		EntityType: "acct",
		Prefix:     "admin-",
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"admin-1", "admin-2"}, resp.Keys)
}

func TestListEntities_Pagination_PageSizeAndToken(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	ids := []string{"a", "b", "c", "d", "e"}
	for _, id := range ids {
		_, err := svc.PutEntity(ctx, &PutEntityRequest{
			EntityType: "item",
			ID:         id,
			Data:       []byte("v"),
		})
		require.NoError(t, err)
	}

	// First page: 2 items.
	page1, err := svc.ListEntities(ctx, &ListEntitiesRequest{
		EntityType: "item",
		PageSize:   2,
	})
	require.NoError(t, err)
	assert.Len(t, page1.Keys, 2)
	assert.NotEmpty(t, page1.NextPageToken)
	assert.Equal(t, int64(5), page1.TotalCount)

	// Second page: next 2 items.
	page2, err := svc.ListEntities(ctx, &ListEntitiesRequest{
		EntityType: "item",
		PageSize:   2,
		PageToken:  page1.NextPageToken,
	})
	require.NoError(t, err)
	assert.Len(t, page2.Keys, 2)
	assert.NotEmpty(t, page2.NextPageToken)

	// Third page: remaining 1 item.
	page3, err := svc.ListEntities(ctx, &ListEntitiesRequest{
		EntityType: "item",
		PageSize:   2,
		PageToken:  page2.NextPageToken,
	})
	require.NoError(t, err)
	assert.Len(t, page3.Keys, 1)
	assert.Empty(t, page3.NextPageToken)

	// All pages together must contain every inserted ID.
	all := append(append(page1.Keys, page2.Keys...), page3.Keys...)
	assert.ElementsMatch(t, ids, all)
}

func TestListEntities_PageTokenBeyondEnd_ReturnsEmpty(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	_, err := svc.PutEntity(ctx, &PutEntityRequest{
		EntityType: "thing",
		ID:         "only",
		Data:       []byte("v"),
	})
	require.NoError(t, err)

	// Page token offset beyond total count.
	resp, err := svc.ListEntities(ctx, &ListEntitiesRequest{
		EntityType: "thing",
		PageToken:  "999",
	})
	require.NoError(t, err)
	assert.Empty(t, resp.Keys)
}

func TestListEntities_InvalidPageToken_ReturnsError(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	_, err := svc.PutEntity(ctx, &PutEntityRequest{
		EntityType: "obj",
		ID:         "1",
		Data:       []byte("v"),
	})
	require.NoError(t, err)

	_, err = svc.ListEntities(ctx, &ListEntitiesRequest{
		EntityType: "obj",
		PageToken:  "not-a-number",
	})
	var e *InvalidPageTokenError
	require.True(t, errors.As(err, &e), "expected InvalidPageTokenError, got %T: %v", err, err)
	assert.Equal(t, "not-a-number", e.Token)
}

func TestListEntities_ZeroPageSizeReturnsAll(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	for _, id := range []string{"x", "y", "z"} {
		_, err := svc.PutEntity(ctx, &PutEntityRequest{
			EntityType: "ns",
			ID:         id,
			Data:       []byte("v"),
		})
		require.NoError(t, err)
	}

	resp, err := svc.ListEntities(ctx, &ListEntitiesRequest{
		EntityType: "ns",
		PageSize:   0, // zero = return all
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"x", "y", "z"}, resp.Keys)
	assert.Empty(t, resp.NextPageToken)
}

func TestListEntities_NegativePageSizeReturnsAll(t *testing.T) {
	svc, cleanup := newBackendKVService(t)
	defer cleanup()

	ctx := context.Background()
	for _, id := range []string{"p", "q"} {
		_, err := svc.PutEntity(ctx, &PutEntityRequest{
			EntityType: "ns2",
			ID:         id,
			Data:       []byte("v"),
		})
		require.NoError(t, err)
	}

	resp, err := svc.ListEntities(ctx, &ListEntitiesRequest{
		EntityType: "ns2",
		PageSize:   -1,
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"p", "q"}, resp.Keys)
}

// ---------------------------------------------------------------------------
// Error-path tests using a mock backend
// ---------------------------------------------------------------------------

// backendGetError is a typed error returned by the mock backend.
type backendGetError struct{}

func (e *backendGetError) Error() string { return "mock: get failed" }

// backendListError is a typed error returned by the mock backend.
type backendListError struct{}

func (e *backendListError) Error() string { return "mock: list failed" }

// backendPutError is a typed error returned by the mock backend.
type backendPutError struct{}

func (e *backendPutError) Error() string { return "mock: put failed" }

// backendDeleteError is a typed error returned by the mock backend.
type backendDeleteError struct{}

func (e *backendDeleteError) Error() string { return "mock: delete failed" }

// errBackend is a database.Backend that returns configurable errors on each
// operation. It is used to exercise the error-propagation paths in
// BackendKVService without requiring a real storage layer.
type errBackend struct {
	getErr    error
	listErr   error
	putErr    error
	deleteErr error
}

func (b *errBackend) Get(_ context.Context, _ []byte) ([]byte, error) {
	return nil, b.getErr
}

func (b *errBackend) Put(_ context.Context, _, _ []byte) error {
	return b.putErr
}

func (b *errBackend) Delete(_ context.Context, _ []byte) error {
	return b.deleteErr
}

func (b *errBackend) List(_ context.Context, _ string) ([]string, error) {
	return nil, b.listErr
}

func (b *errBackend) Range(_ context.Context, _, _ []byte, _ int) ([]database.KeyValue, error) {
	return nil, nil
}

func (b *errBackend) Batch(_ context.Context, _ []database.BatchOp) error {
	return nil
}

func (b *errBackend) Close() error { return nil }

// newErrBackendService creates a BackendKVService backed by an errBackend
// with the given pre-configured errors.
func newErrBackendService(eb *errBackend) *BackendKVService {
	return NewBackendKVService(eb)
}

func TestGet_PropagatesBackendError(t *testing.T) {
	svc := newErrBackendService(&errBackend{getErr: &backendGetError{}})
	_, err := svc.Get(context.Background(), "k")
	var e *backendGetError
	assert.True(t, errors.As(err, &e))
}

func TestList_PropagatesBackendError(t *testing.T) {
	svc := newErrBackendService(&errBackend{listErr: &backendListError{}})
	_, err := svc.List(context.Background(), "pfx")
	var e *backendListError
	assert.True(t, errors.As(err, &e))
}

func TestScan_PropagatesListError(t *testing.T) {
	svc := newErrBackendService(&errBackend{listErr: &backendListError{}})
	_, err := svc.Scan(context.Background(), "pfx")
	var e *backendListError
	assert.True(t, errors.As(err, &e))
}

func TestScan_PropagatesGetErrorDuringValueFetch(t *testing.T) {
	// Use the real in-memory storage so that List succeeds but override Get
	// to fail by using a custom backend that returns keys from List but
	// fails on Get.
	store := memorystorage.NewStorage()
	realBackend := database.NewStandaloneBackend(store)
	defer func() { _ = store.Close() }()

	ctx := context.Background()
	require.NoError(t, realBackend.Put(ctx, []byte("p/k"), []byte("v")))

	// Wrap realBackend so List succeeds but Get fails.
	hybrid := &hybridErrBackend{
		listFn: realBackend.List,
		getErr: &backendGetError{},
	}
	svc := NewBackendKVService(hybrid)

	_, err := svc.Scan(ctx, "p/")
	var e *backendGetError
	assert.True(t, errors.As(err, &e))
}

func TestExistsWithConsistency_PropagatesBackendError(t *testing.T) {
	svc := newErrBackendService(&errBackend{getErr: &backendGetError{}})
	_, err := svc.ExistsWithConsistency(context.Background(), "k", consistency.ReadOptions{})
	var e *backendGetError
	assert.True(t, errors.As(err, &e))
}

func TestPutEntity_PropagatesPutError(t *testing.T) {
	svc := newErrBackendService(&errBackend{putErr: &backendPutError{}})
	_, err := svc.PutEntity(context.Background(), &PutEntityRequest{
		EntityType: "t",
		ID:         "1",
		Data:       []byte("data"),
	})
	var e *backendPutError
	assert.True(t, errors.As(err, &e))
}

func TestDeleteEntity_PropagatesDeleteError(t *testing.T) {
	svc := newErrBackendService(&errBackend{deleteErr: &backendDeleteError{}})
	_, err := svc.DeleteEntity(context.Background(), &DeleteEntityRequest{
		EntityType: "t",
		ID:         "1",
	})
	var e *backendDeleteError
	assert.True(t, errors.As(err, &e))
}

func TestListEntities_PropagatesListError(t *testing.T) {
	svc := newErrBackendService(&errBackend{listErr: &backendListError{}})
	_, err := svc.ListEntities(context.Background(), &ListEntitiesRequest{EntityType: "t"})
	var e *backendListError
	assert.True(t, errors.As(err, &e))
}

// hybridErrBackend delegates List to a real backend but returns a fixed
// error on Get. Used to trigger the per-key error path inside Scan.
type hybridErrBackend struct {
	listFn func(ctx context.Context, prefix string) ([]string, error)
	getErr error
}

func (b *hybridErrBackend) Get(_ context.Context, _ []byte) ([]byte, error) {
	return nil, b.getErr
}

func (b *hybridErrBackend) Put(_ context.Context, _, _ []byte) error { return nil }

func (b *hybridErrBackend) Delete(_ context.Context, _ []byte) error { return nil }

func (b *hybridErrBackend) List(ctx context.Context, prefix string) ([]string, error) {
	return b.listFn(ctx, prefix)
}

func (b *hybridErrBackend) Range(_ context.Context, _, _ []byte, _ int) ([]database.KeyValue, error) {
	return nil, nil
}

func (b *hybridErrBackend) Batch(_ context.Context, _ []database.BatchOp) error { return nil }

func (b *hybridErrBackend) Close() error { return nil }
