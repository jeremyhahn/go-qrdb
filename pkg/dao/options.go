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
	"time"

	"github.com/jeremyhahn/go-codec"
)

// CodecType wraps codec.Type for DAO configuration.
// Use SupportedCodecs() to discover available codecs at runtime.
type CodecType = codec.Type

// Codec type constants matching go-codec types.
// Availability depends on go-codec version and build tags.
// Use dao.SupportedCodecs() or dao.IsCodecSupported() to check availability.
const (
	CodecJSON     CodecType = codec.JSON     // Standard JSON (always available)
	CodecYAML     CodecType = codec.YAML     // YAML format
	CodecTOML     CodecType = codec.TOML     // TOML format
	CodecMsgPack  CodecType = codec.MsgPack  // MessagePack binary
	CodecProtoBuf CodecType = codec.ProtoBuf // Protocol Buffers (requires proto.Message)
	CodecBSON     CodecType = codec.BSON     // BSON binary (MongoDB compatible)
	CodecCBOR     CodecType = codec.CBOR     // CBOR binary (when available)
	CodecAvro     CodecType = codec.Avro     // Avro binary (when available)
)

// Default configuration values.
const (
	DefaultShardID = uint64(1)
	DefaultTimeout = 30 * time.Second
)

// Config holds DAO configuration options.
type Config struct {
	// ShardID specifies which Raft shard to use for storage.
	// Only relevant in cluster mode. Defaults to 1.
	ShardID uint64

	// CodecType specifies the serialization format.
	// Defaults to JSON.
	CodecType CodecType

	// Timeout is the maximum duration for DAO operations.
	// Defaults to 30 seconds.
	Timeout time.Duration

	// Partition specifies which storage partition to use for this DAO.
	// Empty string uses the default partition.
	Partition string

	// IDGen specifies the ID generation strategy for new entities.
	// If nil, UnixNanoGenerator is used.
	IDGen IDGenerator

	// Database is the optional database namespace prefix applied to
	// index names during QueryByIndex, ScanByIndex, PageByIndex, and
	// CountByIndex. When non-empty, index lookups use the form
	// "{Database}.{entityType}.{fieldName}". Leave empty for the legacy
	// "{entityType}.{fieldName}" format (backward compatible).
	Database string
}

// DefaultConfig returns a Config with default values.
func DefaultConfig() *Config {
	return &Config{
		ShardID:   DefaultShardID,
		CodecType: CodecJSON,
		Timeout:   DefaultTimeout,
		Partition: "",
	}
}

// Option is a functional option for configuring a DAO.
type Option func(*Config)

// WithShard sets the Raft shard ID for the DAO.
// In cluster mode, entities are stored in the specified shard.
// This option is ignored in local (single-node) and memory modes.
func WithShard(shardID uint64) Option {
	return func(c *Config) {
		c.ShardID = shardID
	}
}

// WithCodec sets the serialization codec for the DAO.
// Supported codecs: CodecJSON, CodecYAML, CodecTOML, CodecMsgPack,
// CodecProtoBuf, CodecBSON, CodecCBOR.
//
// Performance considerations:
//   - CodecJSON: Human-readable, good for debugging, moderate performance
//   - CodecMsgPack: Binary, excellent performance, compact size
//   - CodecCBOR: Binary, good performance, schema-less
//   - CodecProtoBuf: Binary, excellent performance, requires schema
func WithCodec(ct CodecType) Option {
	return func(c *Config) {
		c.CodecType = ct
	}
}

// WithTimeout sets the operation timeout for the DAO.
// All DAO operations will use this timeout for context deadlines.
func WithTimeout(d time.Duration) Option {
	return func(c *Config) {
		c.Timeout = d
	}
}

// WithPartition sets the storage partition for this DAO.
// The partition name must correspond to a partition registered with the Factory.
// If empty or not set, the factory's default partition is used.
//
// Example:
//
//	userDAO, err := dao.NewDAO(factory, "users", newUser,
//	    dao.WithPartition("users"),
//	    dao.WithShard(1),
//	)
func WithPartition(partition string) Option {
	return func(c *Config) {
		c.Partition = partition
	}
}

// WithIDGenerator sets the ID generation strategy for the DAO.
// Built-in generators: UnixNanoGenerator, AtomicIncrementGenerator, FieldHashGenerator.
// If not set, UnixNanoGenerator is used by default.
func WithIDGenerator(gen IDGenerator) Option {
	return func(c *Config) {
		c.IDGen = gen
	}
}

// WithDatabase sets the database namespace prefix for index name resolution.
// When set, index lookups in QueryByIndex, ScanByIndex, PageByIndex, and
// CountByIndex use the form "{database}.{entityType}.{fieldName}" instead of
// the legacy "{entityType}.{fieldName}". This enables per-database index
// isolation when multiple databases share the same storage shard.
// Leave unset for backward-compatible single-database behaviour.
func WithDatabase(database string) Option {
	return func(c *Config) {
		c.Database = database
	}
}

// ParseOptions applies options to a default Config and returns it.
func ParseOptions(opts ...Option) *Config {
	cfg := DefaultConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}
