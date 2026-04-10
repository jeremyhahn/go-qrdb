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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureTimeout validates the ensureTimeout helper function.
func TestEnsureTimeout(t *testing.T) {
	t.Run("ContextWithoutDeadline", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithTimeout(5*time.Second),
		)
		require.NoError(t, err)

		// Get the internal DAO to access private method
		genericDAO := dao.(*genericDAO[*TestEntity])

		ctx := context.Background()
		newCtx, cancel := genericDAO.ensureTimeout(ctx)
		defer cancel()

		// Should have a deadline now
		_, hasDeadline := newCtx.Deadline()
		assert.True(t, hasDeadline)
	})

	t.Run("ContextWithDeadline", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithTimeout(5*time.Second),
		)
		require.NoError(t, err)

		genericDAO := dao.(*genericDAO[*TestEntity])

		// Create context with existing deadline
		ctx, ctxCancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer ctxCancel()
		newCtx, cancel := genericDAO.ensureTimeout(ctx)
		defer cancel()

		// Should still have the original deadline
		deadline, hasDeadline := newCtx.Deadline()
		assert.True(t, hasDeadline)

		// Should be the same context (not wrapped again)
		originalDeadline, _ := ctx.Deadline()
		assert.Equal(t, originalDeadline, deadline)
	})
}

// TestMakeKey validates the makeKey helper function.
func TestMakeKey(t *testing.T) {
	t.Run("KeyFormat", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		genericDAO := dao.(*genericDAO[*TestEntity])

		key := genericDAO.makeKey(123)
		assert.Equal(t, "users/123", key)
	})

	t.Run("DifferentEntityTypes", func(t *testing.T) {
		store := newMemoryKVStore()
		dao1, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		dao2, err := New[*TestEntity](
			store,
			"sessions",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		genericDAO1 := dao1.(*genericDAO[*TestEntity])
		genericDAO2 := dao2.(*genericDAO[*TestEntity])

		key1 := genericDAO1.makeKey(123)
		key2 := genericDAO2.makeKey(123)

		assert.NotEqual(t, key1, key2)
		assert.Equal(t, "users/123", key1)
		assert.Equal(t, "sessions/123", key2)
	})
}

// TestPrefixKey validates the prefixKey helper function.
func TestPrefixKey(t *testing.T) {
	t.Run("PrefixFormat", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		genericDAO := dao.(*genericDAO[*TestEntity])

		prefix := genericDAO.prefixKey()
		assert.Equal(t, "users/", prefix)
	})

	t.Run("DifferentEntityTypePrefixes", func(t *testing.T) {
		store := newMemoryKVStore()
		dao1, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		dao2, err := New[*TestEntity](
			store,
			"products",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		genericDAO1 := dao1.(*genericDAO[*TestEntity])
		genericDAO2 := dao2.(*genericDAO[*TestEntity])

		prefix1 := genericDAO1.prefixKey()
		prefix2 := genericDAO2.prefixKey()

		assert.NotEqual(t, prefix1, prefix2)
		assert.Equal(t, "users/", prefix1)
		assert.Equal(t, "products/", prefix2)
	})
}

// TestIsNotFoundError validates the isNotFoundError helper function.
func TestIsNotFoundError(t *testing.T) {
	t.Run("NilError", func(t *testing.T) {
		assert.False(t, isNotFoundError(nil))
	})

	// Note: We can't easily test the DragonError case without creating
	// a real DragonError, which would require importing from pkg/errors
	// and potentially creating circular dependencies. The function is
	// tested indirectly through Get operations that return not found errors.
}

// TestCodecSerialization validates codec serialization edge cases.
func TestCodecSerialization(t *testing.T) {
	t.Run("LargeEntity", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		// Create entity with large data
		entity := &TestEntity{
			ID:    1,
			Name:  string(make([]byte, 10000)), // 10KB string
			Email: "test@example.com",
		}

		ctx := context.Background()
		err = dao.Save(ctx, entity)
		require.NoError(t, err)

		retrieved, err := dao.Get(ctx, 1)
		require.NoError(t, err)
		assert.Equal(t, len(entity.Name), len(retrieved.Name))
	})
}

// TestAutoIDGeneration validates auto ID generation behavior.
func TestAutoIDGeneration(t *testing.T) {
	t.Run("ConsecutiveSavesGenerateDifferentIDs", func(t *testing.T) {
		store := newMemoryKVStore()
		dao, err := New[*TestEntity](
			store,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()

		entity1 := &TestEntity{Name: "User1", Email: "user1@example.com"}
		err = dao.Save(ctx, entity1)
		require.NoError(t, err)

		entity2 := &TestEntity{Name: "User2", Email: "user2@example.com"}
		err = dao.Save(ctx, entity2)
		require.NoError(t, err)

		assert.NotZero(t, entity1.ID)
		assert.NotZero(t, entity2.ID)
		assert.NotEqual(t, entity1.ID, entity2.ID)
	})
}
