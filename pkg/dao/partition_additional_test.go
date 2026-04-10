//go:build codec_json

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// TestIsPartitionErrorVariants validates the IsPartitionError function with various inputs.
func TestIsPartitionErrorVariants(t *testing.T) {
	t.Run("Nil", func(t *testing.T) {
		assert.False(t, IsPartitionError(nil))
	})

	t.Run("PartitionNotConfiguredError", func(t *testing.T) {
		err := &PartitionNotConfiguredError{Operation: "Put"}
		assert.True(t, IsPartitionError(err))
	})

	t.Run("PartitionNotFoundError", func(t *testing.T) {
		err := &PartitionNotFoundError{Partition: "test"}
		assert.True(t, IsPartitionError(err))
	})

	t.Run("WrappedPartitionNotConfiguredError", func(t *testing.T) {
		innerErr := &PartitionNotConfiguredError{Operation: "Get"}
		wrappedErr := &dberrors.WrappedMessageError{Message: "wrapped", Cause: innerErr}
		assert.True(t, IsPartitionError(wrappedErr))
	})

	t.Run("WrappedPartitionNotFoundError", func(t *testing.T) {
		innerErr := &PartitionNotFoundError{Partition: "users"}
		wrappedErr := &dberrors.WrappedMessageError{Message: "outer", Cause: innerErr}
		assert.True(t, IsPartitionError(wrappedErr))
	})

	t.Run("DragonErrorNotFound", func(t *testing.T) {
		err := &dberrors.DragonError{Code: dberrors.ErrNotFound, Op: "partition.get"}
		assert.True(t, IsPartitionError(err))
	})

	t.Run("DragonErrorOther", func(t *testing.T) {
		err := &dberrors.DragonError{Code: dberrors.ErrInternal, Op: "partition.get"}
		assert.False(t, IsPartitionError(err))
	})

	t.Run("GenericError", func(t *testing.T) {
		err := errors.New("some generic error")
		assert.False(t, IsPartitionError(err))
	})
}

// TestPartitionedKVStoreScanErrors validates Scan error handling.
func TestPartitionedKVStoreScanErrors(t *testing.T) {
	t.Run("ScanWithNilManager", func(t *testing.T) {
		store := NewPartitionedKVStore(nil, "users")

		ctx := context.Background()
		err := store.Scan(ctx, "prefix", func(_ string, _ []byte) error {
			return nil
		})

		require.Error(t, err)
		var pncErr *PartitionNotConfiguredError
		assert.ErrorAs(t, err, &pncErr)
		assert.Equal(t, "Scan", pncErr.Operation)
	})

	t.Run("ScanWithContextCancellation", func(t *testing.T) {
		mgr := newMockPartitionManager([]string{"users"}, "users")

		// Add some data to scan
		err := mgr.SetToPartition("users", []byte("key1"), []byte("value1"))
		require.NoError(t, err)
		err = mgr.SetToPartition("users", []byte("key2"), []byte("value2"))
		require.NoError(t, err)

		store := NewPartitionedKVStore(mgr, "users")

		// Create a cancelled context
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err = store.Scan(ctx, "", func(_ string, _ []byte) error {
			return nil
		})
		// Context should be checked during scan
		require.Error(t, err)
		assert.Equal(t, context.Canceled, err)
	})

	t.Run("ScanPartitionNotFound", func(t *testing.T) {
		mgr := newMockPartitionManager([]string{"users"}, "users")
		store := NewPartitionedKVStore(mgr, "nonexistent")

		ctx := context.Background()
		err := store.Scan(ctx, "prefix", func(_ string, _ []byte) error {
			return nil
		})

		require.Error(t, err)
		var pnfErr *PartitionNotFoundError
		assert.ErrorAs(t, err, &pnfErr)
	})

	t.Run("ScanCallbackError", func(t *testing.T) {
		mgr := newMockPartitionManager([]string{"users"}, "users")

		// Add data to trigger callback
		err := mgr.SetToPartition("users", []byte("users/1"), []byte("value1"))
		require.NoError(t, err)

		store := NewPartitionedKVStore(mgr, "users")

		ctx := context.Background()
		callbackErr := errors.New("callback error")
		err = store.Scan(ctx, "users/", func(_ string, _ []byte) error {
			return callbackErr
		})

		require.Error(t, err)
		assert.Equal(t, callbackErr, err)
	})
}

// TestPartitionErrorMessages validates error message content.
func TestPartitionErrorMessages(t *testing.T) {
	t.Run("PartitionNotConfiguredErrorMessage", func(t *testing.T) {
		err := &PartitionNotConfiguredError{Operation: "Delete"}
		msg := err.Error()
		assert.Contains(t, msg, "partition manager not configured")
		assert.Contains(t, msg, "Delete")
	})

	t.Run("PartitionNotFoundErrorMessage", func(t *testing.T) {
		err := &PartitionNotFoundError{Partition: "sessions"}
		msg := err.Error()
		assert.Contains(t, msg, "partition not found")
		assert.Contains(t, msg, "sessions")
	})
}

// TestPartitionedKVStoreListWithNilManager validates List error handling.
func TestPartitionedKVStoreListWithNilManager(t *testing.T) {
	store := NewPartitionedKVStore(nil, "users")

	ctx := context.Background()
	keys, err := store.List(ctx, "prefix")

	require.Error(t, err)
	assert.Nil(t, keys)
	var pncErr *PartitionNotConfiguredError
	assert.ErrorAs(t, err, &pncErr)
	assert.Equal(t, "List", pncErr.Operation)
}

// TestCreatePartitionedProviderCaching validates that the provider caches stores.
func TestCreatePartitionedProviderCaching(t *testing.T) {
	mgr := newMockPartitionManager([]string{"users", "sessions"}, "users")
	provider := CreatePartitionedProvider(mgr)

	// Get store for same partition multiple times
	store1 := provider("users", 1)
	store2 := provider("users", 2) // Different shard, same partition
	store3 := provider("users", 1) // Same partition and shard

	// Stores for same partition should be cached
	require.NotNil(t, store1)
	require.NotNil(t, store2)
	require.NotNil(t, store3)

	// Same partition should return same store
	assert.Equal(t, store1, store2)
	assert.Equal(t, store1, store3)

	// Different partition should return different store
	store4 := provider("sessions", 1)
	require.NotNil(t, store4)
	assert.NotEqual(t, store1, store4)
}

// TestPrefixEndKeyEdgeCases validates prefixEndKey with edge cases.
func TestPrefixEndKeyEdgeCases(t *testing.T) {
	t.Run("SingleByte", func(t *testing.T) {
		end := prefixEndKey([]byte{0x00})
		assert.Equal(t, []byte{0x01}, end)
	})

	t.Run("SingleByteMax", func(t *testing.T) {
		end := prefixEndKey([]byte{0xFF})
		assert.Nil(t, end)
	})

	t.Run("MultiByteWithTrailingFF", func(t *testing.T) {
		end := prefixEndKey([]byte{0x01, 0x02, 0xFF})
		assert.Equal(t, []byte{0x01, 0x03}, end)
	})

	t.Run("MultiByteMiddleFF", func(t *testing.T) {
		end := prefixEndKey([]byte{0x01, 0xFF, 0x02})
		assert.Equal(t, []byte{0x01, 0xFF, 0x03}, end)
	})
}
