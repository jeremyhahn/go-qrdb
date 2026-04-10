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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TestNewFactory validates the NewFactory function.
func TestNewFactory(t *testing.T) {
	t.Run("CreateFactory", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}

		factory := NewFactory(provider)
		require.NotNil(t, factory)
	})

	t.Run("FactoryKVStore", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}

		factory := NewFactory(provider)
		kvStore := factory.KVStore(1)
		require.NotNil(t, kvStore)
	})

	t.Run("DifferentShardsReturnDifferentStores", func(t *testing.T) {
		stores := make(map[uint64]kvstore.KVStore)
		provider := func(shardID uint64) kvstore.KVStore {
			if store, ok := stores[shardID]; ok {
				return store
			}
			store := newMemoryKVStore()
			stores[shardID] = store
			return store
		}

		factory := NewFactory(provider)
		store1 := factory.KVStore(1)
		store2 := factory.KVStore(2)

		// Stores should be different - test by writing to one and verifying isolation
		ctx := context.Background()
		err := store1.Put(ctx, "key1", []byte("value1"))
		require.NoError(t, err)

		// store2 should not have the key
		_, err = store2.Get(ctx, "key1")
		require.Error(t, err)

		// store1 should have the key
		val, err := store1.Get(ctx, "key1")
		require.NoError(t, err)
		assert.Equal(t, []byte("value1"), val)
	})
}

// TestNewDAO validates the NewDAO function with factory.
func TestNewDAO(t *testing.T) {
	t.Run("CreateDAOWithFactory", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}
		factory := NewFactory(provider)

		dao, err := NewDAO[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)

		require.NoError(t, err)
		require.NotNil(t, dao)
		assert.Equal(t, "users", dao.EntityType())
	})

	t.Run("CreateDAOWithOptions", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}
		factory := NewFactory(provider)

		dao, err := NewDAO[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithShard(5),
			WithCodec(CodecJSON),
		)

		require.NoError(t, err)
		require.NotNil(t, dao)
	})

	t.Run("DAOOperationsWork", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}
		factory := NewFactory(provider)

		dao, err := NewDAO[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		ctx := context.Background()
		entity := &TestEntity{
			ID:    123,
			Name:  "Test User",
			Email: "test@example.com",
		}

		// Save
		err = dao.Save(ctx, entity)
		require.NoError(t, err)

		// Get
		retrieved, err := dao.Get(ctx, 123)
		require.NoError(t, err)
		assert.Equal(t, entity.Name, retrieved.Name)
	})
}

// TestNewTypedFactory validates the NewTypedFactory function.
func TestNewTypedFactory(t *testing.T) {
	t.Run("CreateTypedFactory", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}
		factory := NewFactory(provider)

		typedFactory := NewTypedFactory[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)

		require.NotNil(t, typedFactory)
	})

	t.Run("CreateTypedFactoryWithOptions", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}
		factory := NewFactory(provider)

		typedFactory := NewTypedFactory[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithCodec(CodecMsgPack),
		)

		require.NotNil(t, typedFactory)
	})

	t.Run("TypedFactoryCreate", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}
		factory := NewFactory(provider)

		typedFactory := NewTypedFactory[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)

		dao, err := typedFactory.Create()
		require.NoError(t, err)
		require.NotNil(t, dao)
		assert.Equal(t, "users", dao.EntityType())
	})

	t.Run("TypedFactoryCreateWithAdditionalOptions", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}
		factory := NewFactory(provider)

		typedFactory := NewTypedFactory[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithCodec(CodecJSON),
		)

		dao, err := typedFactory.Create(WithShard(10))
		require.NoError(t, err)
		require.NotNil(t, dao)
	})

	t.Run("TypedFactoryMultipleDAOs", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}
		factory := NewFactory(provider)

		typedFactory := NewTypedFactory[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)

		dao1, err := typedFactory.Create()
		require.NoError(t, err)

		dao2, err := typedFactory.Create()
		require.NoError(t, err)

		// Both should work independently
		assert.NotNil(t, dao1)
		assert.NotNil(t, dao2)
	})
}

// TestFactoryIntegration validates factory integration scenarios.
func TestFactoryIntegration(t *testing.T) {
	t.Run("FactoryWithMultipleShards", func(t *testing.T) {
		stores := make(map[uint64]kvstore.KVStore)
		provider := func(shardID uint64) kvstore.KVStore {
			if store, ok := stores[shardID]; ok {
				return store
			}
			store := newMemoryKVStore()
			stores[shardID] = store
			return store
		}
		factory := NewFactory(provider)

		// Create DAOs for different shards
		dao1, err := NewDAO[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithShard(1),
		)
		require.NoError(t, err)

		dao2, err := NewDAO[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithShard(2),
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Save to shard 1
		entity1 := &TestEntity{ID: 1, Name: "User1", Email: "user1@example.com"}
		err = dao1.Save(ctx, entity1)
		require.NoError(t, err)

		// Save to shard 2
		entity2 := &TestEntity{ID: 1, Name: "User2", Email: "user2@example.com"}
		err = dao2.Save(ctx, entity2)
		require.NoError(t, err)

		// Data should be isolated
		count1, err := dao1.Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, count1)

		count2, err := dao2.Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, count2)
	})
}
