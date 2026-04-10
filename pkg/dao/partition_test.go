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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// mockPartitionManager implements PartitionManager for testing.
type mockPartitionManager struct {
	partitions       map[string]*mockStorage
	defaultPartition string
	mu               sync.RWMutex
}

// mockStorage implements storage.Storage for testing.
type mockStorage struct {
	data map[string][]byte
	mu   sync.RWMutex
}

func newMockPartitionManager(partitionNames []string, defaultName string) *mockPartitionManager {
	pm := &mockPartitionManager{
		partitions:       make(map[string]*mockStorage),
		defaultPartition: defaultName,
	}
	for _, name := range partitionNames {
		pm.partitions[name] = newMockStorage()
	}
	return pm
}

func newMockStorage() *mockStorage {
	return &mockStorage{
		data: make(map[string][]byte),
	}
}

func (m *mockPartitionManager) GetFromPartition(partitionName string, key []byte) ([]byte, error) {
	m.mu.RLock()
	store, ok := m.partitions[partitionName]
	m.mu.RUnlock()
	if !ok {
		return nil, &PartitionNotFoundError{Partition: partitionName}
	}
	return store.Get(key)
}

func (m *mockPartitionManager) SetToPartition(partitionName string, key, value []byte) error {
	m.mu.RLock()
	store, ok := m.partitions[partitionName]
	m.mu.RUnlock()
	if !ok {
		return &PartitionNotFoundError{Partition: partitionName}
	}
	return store.Set(key, value)
}

func (m *mockPartitionManager) DeleteFromPartition(partitionName string, key []byte) error {
	m.mu.RLock()
	store, ok := m.partitions[partitionName]
	m.mu.RUnlock()
	if !ok {
		return &PartitionNotFoundError{Partition: partitionName}
	}
	return store.Delete(key)
}

func (m *mockPartitionManager) GetPartition(name string) (storage.Storage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	store, ok := m.partitions[name]
	if !ok {
		return nil, &PartitionNotFoundError{Partition: name}
	}
	return store, nil
}

func (m *mockPartitionManager) DefaultPartition() string {
	return m.defaultPartition
}

func (m *mockPartitionManager) PartitionNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.partitions))
	for name := range m.partitions {
		names = append(names, name)
	}
	return names
}

// mockStorage implements storage.Storage

func (s *mockStorage) Get(key []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[string(key)]
	if !ok {
		return nil, &PartitionNotFoundError{Partition: string(key)}
	}
	return v, nil
}

func (s *mockStorage) Set(key, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[string(key)] = value
	return nil
}

func (s *mockStorage) Delete(key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, string(key))
	return nil
}

func (s *mockStorage) Exists(key []byte) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[string(key)]
	return ok, nil
}

func (s *mockStorage) List(prefix []byte) ([][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var keys [][]byte
	p := string(prefix)
	for k := range s.data {
		if len(p) == 0 || (len(k) >= len(p) && k[:len(p)] == p) {
			keys = append(keys, []byte(k))
		}
	}
	return keys, nil
}

func (s *mockStorage) Batch() storage.Batch {
	return nil
}

func (s *mockStorage) NewIterator(_ *storage.IteratorOptions) storage.Iterator {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		keys = append(keys, k)
	}

	dataCopy := make(map[string][]byte, len(s.data))
	for k, v := range s.data {
		dataCopy[k] = v
	}

	return &mockIterator{
		keys:    keys,
		data:    dataCopy,
		current: -1,
	}
}

func (s *mockStorage) Snapshot() (storage.Snapshot, error) {
	return nil, nil
}

func (s *mockStorage) Close() error {
	return nil
}

// mockIterator implements storage.Iterator for testing.
type mockIterator struct {
	keys    []string
	data    map[string][]byte
	current int
}

func (i *mockIterator) SeekToFirst() bool {
	i.current = 0
	return i.Valid()
}

func (i *mockIterator) SeekToLast() bool {
	i.current = len(i.keys) - 1
	return i.Valid()
}

func (i *mockIterator) Seek(target []byte) bool {
	for idx, k := range i.keys {
		if k >= string(target) {
			i.current = idx
			return true
		}
	}
	i.current = len(i.keys)
	return false
}

func (i *mockIterator) Next() bool {
	if i.current >= len(i.keys)-1 {
		i.current = len(i.keys)
		return false
	}
	i.current++
	return true
}

func (i *mockIterator) Prev() bool {
	if i.current <= 0 {
		i.current = -1
		return false
	}
	i.current--
	return true
}

func (i *mockIterator) Valid() bool {
	return i.current >= 0 && i.current < len(i.keys)
}

func (i *mockIterator) Key() []byte {
	if !i.Valid() {
		return nil
	}
	return []byte(i.keys[i.current])
}

func (i *mockIterator) Value() []byte {
	if !i.Valid() {
		return nil
	}
	return i.data[i.keys[i.current]]
}

func (i *mockIterator) Error() error {
	return nil
}

func (i *mockIterator) Close() error {
	return nil
}

// TestWithPartitionOption validates the WithPartition option function.
func TestWithPartitionOption(t *testing.T) {
	t.Run("SetPartition", func(t *testing.T) {
		cfg := ParseOptions(WithPartition("users"))
		assert.Equal(t, "users", cfg.Partition)
	})

	t.Run("DefaultPartitionEmpty", func(t *testing.T) {
		cfg := ParseOptions()
		assert.Equal(t, "", cfg.Partition)
	})

	t.Run("PartitionWithOtherOptions", func(t *testing.T) {
		cfg := ParseOptions(
			WithPartition("users"),
			WithShard(5),
			WithCodec(CodecMsgPack),
		)
		assert.Equal(t, "users", cfg.Partition)
		assert.Equal(t, uint64(5), cfg.ShardID)
		assert.Equal(t, CodecMsgPack, cfg.CodecType)
	})
}

// TestNewFactoryWithConfig validates the NewFactoryWithConfig function.
func TestNewFactoryWithConfig(t *testing.T) {
	t.Run("NilConfig", func(t *testing.T) {
		factory := NewFactoryWithConfig(nil)
		require.NotNil(t, factory)
		assert.NotNil(t, factory.entityPartitions)
	})

	t.Run("WithAllOptions", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}
		partProvider := func(partition string, shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}

		factory := NewFactoryWithConfig(&FactoryConfig{
			KVStoreProvider:     provider,
			PartitionedProvider: partProvider,
			DefaultPartition:    "default",
			EntityPartitions: EntityPartitionMapping{
				"users":    "users",
				"sessions": "sessions",
			},
		})

		require.NotNil(t, factory)
		assert.Equal(t, "default", factory.DefaultPartition())
		assert.True(t, factory.HasPartitionedProvider())
		assert.Equal(t, "users", factory.GetEntityPartition("users"))
		assert.Equal(t, "sessions", factory.GetEntityPartition("sessions"))
	})

	t.Run("EntityPartitionsNil", func(t *testing.T) {
		factory := NewFactoryWithConfig(&FactoryConfig{
			DefaultPartition: "default",
			EntityPartitions: nil,
		})

		require.NotNil(t, factory)
		assert.NotNil(t, factory.entityPartitions)
		assert.Equal(t, "", factory.GetEntityPartition("users"))
	})
}

// TestFactoryEntityPartitionMapping validates entity-to-partition mapping.
func TestFactoryEntityPartitionMapping(t *testing.T) {
	t.Run("SetAndGet", func(t *testing.T) {
		factory := NewFactory(func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		})

		factory.SetEntityPartition("users", "users-partition")
		factory.SetEntityPartition("sessions", "sessions-partition")

		assert.Equal(t, "users-partition", factory.GetEntityPartition("users"))
		assert.Equal(t, "sessions-partition", factory.GetEntityPartition("sessions"))
		assert.Equal(t, "", factory.GetEntityPartition("unknown"))
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		factory := NewFactory(func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		})

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(2)
			go func(idx int) {
				defer wg.Done()
				factory.SetEntityPartition("entity"+string(rune('0'+idx%10)), "partition")
			}(i)
			go func(idx int) {
				defer wg.Done()
				factory.GetEntityPartition("entity" + string(rune('0'+idx%10)))
			}(i)
		}
		wg.Wait()
	})
}

// TestFactoryResolvePartition validates partition resolution logic.
func TestFactoryResolvePartition(t *testing.T) {
	t.Run("ExplicitOptionPriority", func(t *testing.T) {
		factory := NewFactoryWithConfig(&FactoryConfig{
			DefaultPartition: "default",
			EntityPartitions: EntityPartitionMapping{
				"users": "users-mapped",
			},
		})

		cfg := &Config{Partition: "explicit"}
		partition := factory.resolvePartition("users", cfg)
		assert.Equal(t, "explicit", partition)
	})

	t.Run("EntityMappingSecondPriority", func(t *testing.T) {
		factory := NewFactoryWithConfig(&FactoryConfig{
			DefaultPartition: "default",
			EntityPartitions: EntityPartitionMapping{
				"users": "users-mapped",
			},
		})

		cfg := &Config{}
		partition := factory.resolvePartition("users", cfg)
		assert.Equal(t, "users-mapped", partition)
	})

	t.Run("DefaultPartitionFallback", func(t *testing.T) {
		factory := NewFactoryWithConfig(&FactoryConfig{
			DefaultPartition: "default",
		})

		cfg := &Config{}
		partition := factory.resolvePartition("users", cfg)
		assert.Equal(t, "default", partition)
	})

	t.Run("EmptyWhenNoConfig", func(t *testing.T) {
		factory := NewFactory(func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		})

		cfg := &Config{}
		partition := factory.resolvePartition("users", cfg)
		assert.Equal(t, "", partition)
	})
}

// TestPartitionedKVStore validates the PartitionedKVStore wrapper.
func TestPartitionedKVStore(t *testing.T) {
	t.Run("Put", func(t *testing.T) {
		mgr := newMockPartitionManager([]string{"users"}, "users")
		store := NewPartitionedKVStore(mgr, "users")

		err := store.Put(context.Background(), "key1", []byte("value1"))
		require.NoError(t, err)

		val, err := store.Get(context.Background(), "key1")
		require.NoError(t, err)
		assert.Equal(t, []byte("value1"), val)
	})

	t.Run("Get", func(t *testing.T) {
		mgr := newMockPartitionManager([]string{"users"}, "users")
		store := NewPartitionedKVStore(mgr, "users")

		err := store.Put(context.Background(), "key1", []byte("value1"))
		require.NoError(t, err)

		val, err := store.Get(context.Background(), "key1")
		require.NoError(t, err)
		assert.Equal(t, []byte("value1"), val)
	})

	t.Run("Delete", func(t *testing.T) {
		mgr := newMockPartitionManager([]string{"users"}, "users")
		store := NewPartitionedKVStore(mgr, "users")

		err := store.Put(context.Background(), "key1", []byte("value1"))
		require.NoError(t, err)

		err = store.Delete(context.Background(), "key1")
		require.NoError(t, err)

		_, err = store.Get(context.Background(), "key1")
		require.Error(t, err)
	})

	t.Run("List", func(t *testing.T) {
		mgr := newMockPartitionManager([]string{"users"}, "users")
		store := NewPartitionedKVStore(mgr, "users")

		err := store.Put(context.Background(), "users/1", []byte("user1"))
		require.NoError(t, err)
		err = store.Put(context.Background(), "users/2", []byte("user2"))
		require.NoError(t, err)

		keys, err := store.List(context.Background(), "users/")
		require.NoError(t, err)
		assert.Len(t, keys, 2)
	})

	t.Run("PartitionName", func(t *testing.T) {
		mgr := newMockPartitionManager([]string{"users"}, "users")
		store := NewPartitionedKVStore(mgr, "users")

		assert.Equal(t, "users", store.PartitionName())
	})

	t.Run("NilManager", func(t *testing.T) {
		store := NewPartitionedKVStore(nil, "users")

		err := store.Put(context.Background(), "key", []byte("value"))
		require.Error(t, err)
		var pncErr *PartitionNotConfiguredError
		assert.ErrorAs(t, err, &pncErr)

		_, err = store.Get(context.Background(), "key")
		require.Error(t, err)
		assert.ErrorAs(t, err, &pncErr)

		err = store.Delete(context.Background(), "key")
		require.Error(t, err)
		assert.ErrorAs(t, err, &pncErr)

		_, err = store.List(context.Background(), "prefix")
		require.Error(t, err)
		assert.ErrorAs(t, err, &pncErr)
	})

	t.Run("PartitionNotFound", func(t *testing.T) {
		mgr := newMockPartitionManager([]string{"users"}, "users")
		store := NewPartitionedKVStore(mgr, "nonexistent")

		err := store.Put(context.Background(), "key", []byte("value"))
		require.Error(t, err)
		var pnfErr *PartitionNotFoundError
		assert.ErrorAs(t, err, &pnfErr)
	})
}

// TestCreatePartitionedProvider validates the CreatePartitionedProvider function.
func TestCreatePartitionedProvider(t *testing.T) {
	t.Run("CreateProvider", func(t *testing.T) {
		mgr := newMockPartitionManager([]string{"users", "sessions"}, "users")
		provider := CreatePartitionedProvider(mgr)

		store1 := provider("users", 1)
		require.NotNil(t, store1)

		store2 := provider("sessions", 1)
		require.NotNil(t, store2)

		// Verify stores are cached
		store1Again := provider("users", 1)
		assert.Equal(t, store1, store1Again)
	})

	t.Run("ProviderWithFactory", func(t *testing.T) {
		mgr := newMockPartitionManager([]string{"users", "sessions"}, "users")
		provider := CreatePartitionedProvider(mgr)

		factory := NewFactoryWithConfig(&FactoryConfig{
			PartitionedProvider: provider,
			DefaultPartition:    "users",
		})

		require.NotNil(t, factory)
		assert.True(t, factory.HasPartitionedProvider())
	})
}

// TestDAOWithPartition validates DAO creation with partition options.
func TestDAOWithPartition(t *testing.T) {
	t.Run("CreateDAOWithPartitionOption", func(t *testing.T) {
		stores := make(map[string]kvstore.KVStore)
		partProvider := func(partition string, shardID uint64) kvstore.KVStore {
			key := partition + "-" + string(rune('0'+shardID))
			if store, ok := stores[key]; ok {
				return store
			}
			store := newMemoryKVStore()
			stores[key] = store
			return store
		}

		factory := NewFactoryWithConfig(&FactoryConfig{
			PartitionedProvider: partProvider,
			DefaultPartition:    "default",
		})

		dao, err := NewDAO[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithPartition("users"),
		)
		require.NoError(t, err)
		require.NotNil(t, dao)
		assert.Equal(t, "users", dao.EntityType())
	})

	t.Run("DAOOperationsWithPartition", func(t *testing.T) {
		stores := make(map[string]kvstore.KVStore)
		partProvider := func(partition string, shardID uint64) kvstore.KVStore {
			if store, ok := stores[partition]; ok {
				return store
			}
			store := newMemoryKVStore()
			stores[partition] = store
			return store
		}

		factory := NewFactoryWithConfig(&FactoryConfig{
			PartitionedProvider: partProvider,
			DefaultPartition:    "default",
		})

		dao, err := NewDAO[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithPartition("users"),
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
		assert.Equal(t, entity.Email, retrieved.Email)

		// Count
		count, err := dao.Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, count)

		// Delete
		err = dao.Delete(ctx, entity)
		require.NoError(t, err)

		// Verify deleted
		_, err = dao.Get(ctx, 123)
		require.Error(t, err)
		assert.True(t, IsNotFound(err))
	})

	t.Run("IsolatedPartitions", func(t *testing.T) {
		stores := make(map[string]kvstore.KVStore)
		partProvider := func(partition string, shardID uint64) kvstore.KVStore {
			if store, ok := stores[partition]; ok {
				return store
			}
			store := newMemoryKVStore()
			stores[partition] = store
			return store
		}

		factory := NewFactoryWithConfig(&FactoryConfig{
			PartitionedProvider: partProvider,
		})

		// Create DAO for users partition
		usersDAO, err := NewDAO[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithPartition("users"),
		)
		require.NoError(t, err)

		// Create DAO for sessions partition
		sessionsDAO, err := NewDAO[*TestEntity](
			factory,
			"sessions",
			func() *TestEntity { return &TestEntity{} },
			WithPartition("sessions"),
		)
		require.NoError(t, err)

		ctx := context.Background()

		// Save to users partition
		userEntity := &TestEntity{ID: 1, Name: "User", Email: "user@example.com"}
		err = usersDAO.Save(ctx, userEntity)
		require.NoError(t, err)

		// Save to sessions partition
		sessionEntity := &TestEntity{ID: 1, Name: "Session", Email: "session@example.com"}
		err = sessionsDAO.Save(ctx, sessionEntity)
		require.NoError(t, err)

		// Verify isolation
		usersCount, err := usersDAO.Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, usersCount)

		sessionsCount, err := sessionsDAO.Count(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, sessionsCount)

		// Verify data is separate
		retrievedUser, err := usersDAO.Get(ctx, 1)
		require.NoError(t, err)
		assert.Equal(t, "User", retrievedUser.Name)

		retrievedSession, err := sessionsDAO.Get(ctx, 1)
		require.NoError(t, err)
		assert.Equal(t, "Session", retrievedSession.Name)
	})
}

// TestBackwardCompatibility validates that existing code works unchanged.
func TestBackwardCompatibility(t *testing.T) {
	t.Run("OriginalNewFactory", func(t *testing.T) {
		provider := func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		}

		factory := NewFactory(provider)
		require.NotNil(t, factory)

		dao, err := NewDAO[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)
		require.NotNil(t, dao)

		ctx := context.Background()
		entity := &TestEntity{ID: 1, Name: "User", Email: "user@example.com"}

		err = dao.Save(ctx, entity)
		require.NoError(t, err)

		retrieved, err := dao.Get(ctx, 1)
		require.NoError(t, err)
		assert.Equal(t, "User", retrieved.Name)
	})

	t.Run("OriginalWithOptions", func(t *testing.T) {
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

	t.Run("TypedFactoryUnchanged", func(t *testing.T) {
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
	})
}

// TestPartitionErrorTypes validates partition-related error types.
func TestPartitionErrorTypes(t *testing.T) {
	t.Run("PartitionNotConfiguredError", func(t *testing.T) {
		err := &PartitionNotConfiguredError{Operation: "Put"}
		assert.Contains(t, err.Error(), "Put")
		assert.Contains(t, err.Error(), "not configured")
	})

	t.Run("PartitionNotFoundError", func(t *testing.T) {
		err := &PartitionNotFoundError{Partition: "users"}
		assert.Contains(t, err.Error(), "users")
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("IsPartitionError", func(t *testing.T) {
		assert.True(t, IsPartitionError(&PartitionNotConfiguredError{Operation: "Put"}))
		assert.True(t, IsPartitionError(&PartitionNotFoundError{Partition: "users"}))
		assert.False(t, IsPartitionError(nil))
	})
}

// TestPrefixEndKey validates the prefixEndKey helper function.
func TestPrefixEndKey(t *testing.T) {
	t.Run("EmptyPrefix", func(t *testing.T) {
		end := prefixEndKey(nil)
		assert.Nil(t, end)

		end = prefixEndKey([]byte{})
		assert.Nil(t, end)
	})

	t.Run("NormalPrefix", func(t *testing.T) {
		end := prefixEndKey([]byte("users/"))
		assert.Equal(t, []byte("users0"), end)
	})

	t.Run("AllFFBytes", func(t *testing.T) {
		end := prefixEndKey([]byte{0xFF, 0xFF, 0xFF})
		assert.Nil(t, end)
	})

	t.Run("LastByteFF", func(t *testing.T) {
		end := prefixEndKey([]byte{0x01, 0xFF})
		assert.Equal(t, []byte{0x02}, end)
	})
}

// TestKVStoreForPartition validates the KVStoreForPartition method.
func TestKVStoreForPartition(t *testing.T) {
	t.Run("WithPartitionedProvider", func(t *testing.T) {
		stores := make(map[string]kvstore.KVStore)
		partProvider := func(partition string, shardID uint64) kvstore.KVStore {
			if store, ok := stores[partition]; ok {
				return store
			}
			store := newMemoryKVStore()
			stores[partition] = store
			return store
		}

		factory := NewFactoryWithConfig(&FactoryConfig{
			PartitionedProvider: partProvider,
		})

		store := factory.KVStoreForPartition("users", 1)
		require.NotNil(t, store)
	})

	t.Run("FallbackToKVStoreProvider", func(t *testing.T) {
		kvStore := newMemoryKVStore()
		factory := NewFactory(func(shardID uint64) kvstore.KVStore {
			return kvStore
		})

		store := factory.KVStoreForPartition("users", 1)
		require.NotNil(t, store)
		assert.Equal(t, kvStore, store)
	})

	t.Run("NoProviders", func(t *testing.T) {
		factory := NewFactoryWithConfig(&FactoryConfig{})

		store := factory.KVStoreForPartition("users", 1)
		assert.Nil(t, store)
	})

	t.Run("EmptyPartitionName", func(t *testing.T) {
		kvStore := newMemoryKVStore()
		factory := NewFactoryWithConfig(&FactoryConfig{
			KVStoreProvider: func(shardID uint64) kvstore.KVStore {
				return kvStore
			},
			PartitionedProvider: func(partition string, shardID uint64) kvstore.KVStore {
				return newMemoryKVStore()
			},
		})

		store := factory.KVStoreForPartition("", 1)
		require.NotNil(t, store)
		assert.Equal(t, kvStore, store)
	})
}

// TestSetDefaultPartition validates the SetDefaultPartition method.
func TestSetDefaultPartition(t *testing.T) {
	t.Run("SetAndGet", func(t *testing.T) {
		factory := NewFactory(func(shardID uint64) kvstore.KVStore {
			return newMemoryKVStore()
		})

		assert.Equal(t, "", factory.DefaultPartition())

		factory.SetDefaultPartition("default")
		assert.Equal(t, "default", factory.DefaultPartition())

		factory.SetDefaultPartition("new-default")
		assert.Equal(t, "new-default", factory.DefaultPartition())
	})
}

// TestEntityPartitionMappingInDAOCreation validates that entity mapping is used.
func TestEntityPartitionMappingInDAOCreation(t *testing.T) {
	t.Run("UseMappedPartition", func(t *testing.T) {
		usedPartitions := make(map[string]bool)
		partProvider := func(partition string, shardID uint64) kvstore.KVStore {
			usedPartitions[partition] = true
			return newMemoryKVStore()
		}

		factory := NewFactoryWithConfig(&FactoryConfig{
			PartitionedProvider: partProvider,
			EntityPartitions: EntityPartitionMapping{
				"users":    "users-partition",
				"sessions": "sessions-partition",
			},
		})

		_, err := NewDAO[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
		)
		require.NoError(t, err)

		assert.True(t, usedPartitions["users-partition"])
	})

	t.Run("ExplicitOverridesMapping", func(t *testing.T) {
		usedPartitions := make(map[string]bool)
		partProvider := func(partition string, shardID uint64) kvstore.KVStore {
			usedPartitions[partition] = true
			return newMemoryKVStore()
		}

		factory := NewFactoryWithConfig(&FactoryConfig{
			PartitionedProvider: partProvider,
			EntityPartitions: EntityPartitionMapping{
				"users": "users-partition",
			},
		})

		_, err := NewDAO[*TestEntity](
			factory,
			"users",
			func() *TestEntity { return &TestEntity{} },
			WithPartition("explicit-partition"),
		)
		require.NoError(t, err)

		assert.True(t, usedPartitions["explicit-partition"])
		assert.False(t, usedPartitions["users-partition"])
	})
}
