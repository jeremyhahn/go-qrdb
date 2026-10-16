// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// KVStoreProvider is a function that returns a KVStore for a given shard ID.
// This abstraction allows the DAO factory to work with any storage backend
// without depending directly on the server package.
type KVStoreProvider func(shardID uint64) kvstore.KVStore

// PartitionedKVStoreProvider provides KV stores for specific partitions.
// This allows DAOs to be configured for different storage partitions.
type PartitionedKVStoreProvider func(partitionName string, shardID uint64) kvstore.KVStore

// EntityPartitionMapping maps entity types to partition names.
// This allows automatic routing of entity types to their designated partitions.
type EntityPartitionMapping map[string]string

// Factory creates GenericDAO instances for different entity types.
// It supports both simple KVStore providers and partitioned storage.
type Factory struct {
	kvStoreProvider     KVStoreProvider
	partitionedProvider PartitionedKVStoreProvider
	defaultPartition    string
	entityPartitions    EntityPartitionMapping
	mu                  sync.RWMutex
}

// FactoryConfig configures the DAO factory.
type FactoryConfig struct {
	// KVStoreProvider provides KVStore instances for shards.
	// This is used when no partition is specified or when partitions are not configured.
	KVStoreProvider KVStoreProvider

	// PartitionedProvider provides KVStore instances for specific partitions.
	// When set, DAOs can be configured to use specific storage partitions.
	PartitionedProvider PartitionedKVStoreProvider

	// DefaultPartition is the default partition name for operations
	// that don't specify a partition. Empty string means no partition.
	DefaultPartition string

	// EntityPartitions maps entity types to partition names.
	// When a DAO is created for an entity type listed here, it will
	// automatically use the mapped partition unless overridden.
	EntityPartitions EntityPartitionMapping
}

// NewFactory creates a new DAO factory with the given KVStore provider.
// This constructor maintains backward compatibility with existing code.
//
// Example usage with server:
//
//	factory := dao.NewFactory(server.GetKVStore)
//	userDAO, err := NewDAO[*User](factory, "users", func() *User { return &User{} })
func NewFactory(provider KVStoreProvider) *Factory {
	return &Factory{
		kvStoreProvider:  provider,
		entityPartitions: make(EntityPartitionMapping),
	}
}

// NewFactoryWithConfig creates a new DAO factory with full configuration.
// This constructor supports partitioned storage and entity-to-partition mapping.
//
// Example usage with partition manager:
//
//	factory := dao.NewFactoryWithConfig(&dao.FactoryConfig{
//	    KVStoreProvider:     server.GetKVStore,
//	    PartitionedProvider: myPartitionedProvider,
//	    DefaultPartition:    "default",
//	    EntityPartitions: dao.EntityPartitionMapping{
//	        "users":    "users",
//	        "sessions": "sessions",
//	    },
//	})
func NewFactoryWithConfig(cfg *FactoryConfig) *Factory {
	if cfg == nil {
		return &Factory{
			entityPartitions: make(EntityPartitionMapping),
		}
	}

	entityPartitions := cfg.EntityPartitions
	if entityPartitions == nil {
		entityPartitions = make(EntityPartitionMapping)
	}

	return &Factory{
		kvStoreProvider:     cfg.KVStoreProvider,
		partitionedProvider: cfg.PartitionedProvider,
		defaultPartition:    cfg.DefaultPartition,
		entityPartitions:    entityPartitions,
	}
}

// KVStore returns the underlying KVStore for the specified shard.
// This is useful for advanced use cases that need direct KV access.
func (f *Factory) KVStore(shardID uint64) kvstore.KVStore {
	return f.kvStoreProvider(shardID)
}

// KVStoreForPartition returns the KVStore for a specific partition and shard.
// Returns the default KVStore if partitioned provider is not configured.
func (f *Factory) KVStoreForPartition(partitionName string, shardID uint64) kvstore.KVStore {
	if f.partitionedProvider != nil && partitionName != "" {
		return f.partitionedProvider(partitionName, shardID)
	}
	if f.kvStoreProvider != nil {
		return f.kvStoreProvider(shardID)
	}
	return nil
}

// DatabaseKVStore returns the store of the named database for shardID. A
// database is the partition of its own name: the factory's
// PartitionedKVStoreProvider answers it. Unlike KVStoreForPartition it never
// falls back to the default provider, and it refuses with
// DatabaseStoreUnresolvedError when the name is empty, the factory has no
// partitioned provider, or the provider has no store for the database.
func (f *Factory) DatabaseKVStore(database string, shardID uint64) (kvstore.KVStore, error) {
	if database == "" {
		return nil, DatabaseStoreUnresolvedError{Reason: "no database named"}
	}
	if f.partitionedProvider == nil {
		return nil, DatabaseStoreUnresolvedError{Database: database, Reason: "the factory has no partitioned provider"}
	}
	store := f.partitionedProvider(database, shardID)
	if store == nil {
		return nil, DatabaseStoreUnresolvedError{Database: database, Reason: "the partitioned provider has no store for it"}
	}
	return store, nil
}

// SetEntityPartition maps an entity type to a partition name.
// This mapping is used when creating DAOs to determine which partition to use.
func (f *Factory) SetEntityPartition(entityType, partition string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entityPartitions[entityType] = partition
}

// GetEntityPartition returns the partition name for an entity type.
// Returns empty string if no mapping exists.
func (f *Factory) GetEntityPartition(entityType string) string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.entityPartitions[entityType]
}

// DefaultPartition returns the default partition name.
func (f *Factory) DefaultPartition() string {
	return f.defaultPartition
}

// SetDefaultPartition sets the default partition name.
func (f *Factory) SetDefaultPartition(partition string) {
	f.defaultPartition = partition
}

// HasPartitionedProvider returns true if a partitioned provider is configured.
func (f *Factory) HasPartitionedProvider() bool {
	return f.partitionedProvider != nil
}

// resolvePartition determines the partition to use for a DAO.
// Priority: explicit option > entity mapping > default partition > empty.
func (f *Factory) resolvePartition(entityType string, cfg *Config) string {
	// Explicit partition option takes priority
	if cfg.Partition != "" {
		return cfg.Partition
	}

	// Check entity-to-partition mapping
	f.mu.RLock()
	partition, ok := f.entityPartitions[entityType]
	f.mu.RUnlock()
	if ok {
		return partition
	}

	// Fall back to default partition
	return f.defaultPartition
}

// NewDAO creates a GenericDAO for the given entity type using this factory.
// This is a convenience function that combines Factory with New.
//
// Example:
//
//	factory := dao.NewFactory(server.GetKVStore)
//	userDAO, err := dao.NewDAO[*User](factory, "users", func() *User { return &User{} },
//	    dao.WithShard(1),
//	    dao.WithCodec(dao.CodecMsgPack))
//
// With partitions:
//
//	factory := dao.NewFactoryWithConfig(&dao.FactoryConfig{
//	    PartitionedProvider: myProvider,
//	    DefaultPartition:    "default",
//	})
//	userDAO, err := dao.NewDAO[*User](factory, "users", func() *User { return &User{} },
//	    dao.WithPartition("users"),
//	    dao.WithShard(1))
func NewDAO[E Entity](
	factory *Factory,
	entityType string,
	newEntity func() E,
	opts ...Option,
) (GenericDAO[E], error) {
	cfg := ParseOptions(opts...)

	// Resolve which partition to use
	partitionName := factory.resolvePartition(entityType, cfg)

	// Get KVStore for this partition/shard
	var kvStore kvstore.KVStore
	if factory.partitionedProvider != nil && partitionName != "" {
		kvStore = factory.partitionedProvider(partitionName, cfg.ShardID)
	} else if factory.kvStoreProvider != nil {
		kvStore = factory.kvStoreProvider(cfg.ShardID)
	}

	return New(kvStore, entityType, newEntity, opts...)
}

// NewDatabaseDAO creates a GenericDAO for the given entity type in the named
// database, its store resolved by factory.DatabaseKVStore for the shard the
// options select. It refuses with DatabaseStoreUnresolvedError rather than
// build a DAO over any other store.
//
// The database selects the store only. WithDatabase is a different setting:
// it qualifies index names, for databases that share one store.
func NewDatabaseDAO[E Entity](
	factory *Factory,
	database string,
	entityType string,
	newEntity func() E,
	opts ...Option,
) (GenericDAO[E], error) {
	store, err := databaseStore(factory, database, opts)
	if err != nil {
		return nil, err
	}
	return New(store, entityType, newEntity, opts...)
}

// NewDatabaseTransactional is NewDatabaseDAO for a TransactionalDAO.
func NewDatabaseTransactional[E Entity](
	factory *Factory,
	database string,
	entityType string,
	newEntity func() E,
	opts ...Option,
) (TransactionalDAO[E], error) {
	store, err := databaseStore(factory, database, opts)
	if err != nil {
		return nil, err
	}
	return NewTransactional(store, entityType, newEntity, opts...)
}

// databaseStore resolves the store a NewDatabase* constructor builds over.
func databaseStore(factory *Factory, database string, opts []Option) (kvstore.KVStore, error) {
	if factory == nil {
		return nil, InvalidConfigError{Field: "factory", Reason: "cannot be nil"}
	}
	return factory.DatabaseKVStore(database, ParseOptions(opts...).ShardID)
}

// TypedFactory provides type-safe DAO creation for a specific entity type.
// This is useful when you want to pre-configure a factory for a specific entity.
type TypedFactory[E Entity] struct {
	factory    *Factory
	entityType string
	newEntity  func() E
	opts       []Option
}

// NewTypedFactory creates a TypedFactory for a specific entity type.
func NewTypedFactory[E Entity](
	factory *Factory,
	entityType string,
	newEntity func() E,
	opts ...Option,
) *TypedFactory[E] {
	return &TypedFactory[E]{
		factory:    factory,
		entityType: entityType,
		newEntity:  newEntity,
		opts:       opts,
	}
}

// Create creates a new GenericDAO with the pre-configured settings.
// Additional options can be passed to override defaults.
func (tf *TypedFactory[E]) Create(opts ...Option) (GenericDAO[E], error) {
	allOpts := make([]Option, 0, len(tf.opts)+len(opts))
	allOpts = append(allOpts, tf.opts...)
	allOpts = append(allOpts, opts...)
	return NewDAO(tf.factory, tf.entityType, tf.newEntity, allOpts...)
}
