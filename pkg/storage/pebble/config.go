// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package pebble provides a PebbleDB implementation of the storage.Storage interface.
package pebble

import (
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/bloom"
	"github.com/cockroachdb/pebble/v2/sstable"
)

// CompressionType represents the compression algorithm used by PebbleDB.
type CompressionType string

const (
	// CompressionNone disables compression.
	CompressionNone CompressionType = "none"

	// CompressionSnappy uses Snappy compression (fast, moderate ratio).
	CompressionSnappy CompressionType = "snappy"

	// CompressionZstd uses Zstandard compression (slower, better ratio).
	CompressionZstd CompressionType = "zstd"
)

// ConfigError is returned when a Config value is invalid.
type ConfigError struct {
	Field   string
	Message string
}

// Error implements the error interface.
func (e *ConfigError) Error() string {
	return "pebble config: " + e.Field + ": " + e.Message
}

// Config holds PebbleDB configuration options.
type Config struct {
	// DataDir is the directory where data files are stored.
	DataDir string

	// WALDir is the directory where write-ahead log files are stored.
	// If empty, WAL files are stored in DataDir.
	WALDir string

	// CacheSize is the size of the block cache in bytes.
	// Default: 128 MB.
	CacheSize int64

	// MaxOpenFiles is the maximum number of open file descriptors.
	// Default: 1000.
	MaxOpenFiles int

	// MaxConcurrentCompactions is the maximum number of concurrent compactions.
	// Default: 1. Note: PebbleDB v2 manages compaction scheduling internally.
	MaxConcurrentCompactions int

	// DisableWAL disables the write-ahead log. Improves write throughput
	// at the cost of durability. Default: false.
	DisableWAL bool

	// SyncWrites ensures writes are synced to disk before returning.
	// Default: false (Raft WAL provides durability).
	SyncWrites bool

	// BytesPerSync controls how often file writes are synced to disk.
	// Default: 512 KB.
	BytesPerSync int

	// MemTableSize is the maximum size of a memtable in bytes.
	// Default: 64 MB.
	MemTableSize int64

	// MemTableStopWritesThreshold is the number of memtables that trigger
	// write stalls until compaction catches up. Default: 2.
	MemTableStopWritesThreshold int

	// L0CompactionThreshold is the number of L0 files that trigger compaction.
	// Default: 4.
	L0CompactionThreshold int

	// L0StopWritesThreshold is the number of L0 files that trigger write stalls.
	// Default: 12.
	L0StopWritesThreshold int

	// BlockSize is the target block size for sstable data blocks.
	// Default: 4 KB.
	BlockSize int

	// Compression specifies the compression algorithm. Default: snappy.
	Compression CompressionType

	// MaxConcurrentReads is the maximum number of concurrent read operations.
	// Default: 100.
	MaxConcurrentReads int

	// MaxConcurrentWrites is the maximum number of concurrent write operations.
	// Default: 100.
	MaxConcurrentWrites int

	// ReadOnly opens the database in read-only mode. Default: false.
	ReadOnly bool

	// ErrorIfNotExists causes Open to fail if the database does not exist.
	// Default: false.
	ErrorIfNotExists bool

	// WALMinSyncInterval is the minimum time between WAL syncs. Default: 0.
	WALMinSyncInterval time.Duration
}

// Validate checks the configuration for invalid values.
func (c *Config) Validate() error {
	if c.DataDir == "" {
		return &ConfigError{Field: "DataDir", Message: "is required"}
	}
	if c.CacheSize < 0 {
		return &ConfigError{Field: "CacheSize", Message: "must be non-negative"}
	}
	if c.MaxOpenFiles < 0 {
		return &ConfigError{Field: "MaxOpenFiles", Message: "must be non-negative"}
	}
	if c.MaxConcurrentCompactions < 0 {
		return &ConfigError{Field: "MaxConcurrentCompactions", Message: "must be non-negative"}
	}
	if c.BytesPerSync < 0 {
		return &ConfigError{Field: "BytesPerSync", Message: "must be non-negative"}
	}
	if c.MemTableSize < 0 {
		return &ConfigError{Field: "MemTableSize", Message: "must be non-negative"}
	}
	if c.MemTableStopWritesThreshold < 0 {
		return &ConfigError{Field: "MemTableStopWritesThreshold", Message: "must be non-negative"}
	}
	if c.L0CompactionThreshold < 0 {
		return &ConfigError{Field: "L0CompactionThreshold", Message: "must be non-negative"}
	}
	if c.L0StopWritesThreshold < 0 {
		return &ConfigError{Field: "L0StopWritesThreshold", Message: "must be non-negative"}
	}
	if c.BlockSize < 0 {
		return &ConfigError{Field: "BlockSize", Message: "must be non-negative"}
	}
	if c.MaxConcurrentReads < 0 {
		return &ConfigError{Field: "MaxConcurrentReads", Message: "must be non-negative"}
	}
	if c.MaxConcurrentWrites < 0 {
		return &ConfigError{Field: "MaxConcurrentWrites", Message: "must be non-negative"}
	}

	switch c.Compression {
	case CompressionNone, CompressionSnappy, CompressionZstd, "":
		// valid
	default:
		return &ConfigError{Field: "Compression", Message: "unsupported type: " + string(c.Compression)}
	}

	return nil
}

// WithDefaults returns a copy of the config with zero-valued fields
// replaced by sensible defaults.
func (c *Config) WithDefaults() *Config {
	cfg := *c

	if cfg.CacheSize == 0 {
		cfg.CacheSize = 128 * 1024 * 1024 // 128 MB
	}
	if cfg.MaxOpenFiles == 0 {
		cfg.MaxOpenFiles = 1000
	}
	if cfg.MaxConcurrentCompactions == 0 {
		cfg.MaxConcurrentCompactions = 1
	}
	if cfg.BytesPerSync == 0 {
		cfg.BytesPerSync = 512 * 1024 // 512 KB
	}
	if cfg.MemTableSize == 0 {
		cfg.MemTableSize = 64 * 1024 * 1024 // 64 MB
	}
	if cfg.MemTableStopWritesThreshold == 0 {
		cfg.MemTableStopWritesThreshold = 2
	}
	if cfg.L0CompactionThreshold == 0 {
		cfg.L0CompactionThreshold = 4
	}
	if cfg.L0StopWritesThreshold == 0 {
		cfg.L0StopWritesThreshold = 12
	}
	if cfg.BlockSize == 0 {
		cfg.BlockSize = 4096 // 4 KB
	}
	if cfg.Compression == "" {
		cfg.Compression = CompressionSnappy
	}
	if cfg.MaxConcurrentReads == 0 {
		cfg.MaxConcurrentReads = 100
	}
	if cfg.MaxConcurrentWrites == 0 {
		cfg.MaxConcurrentWrites = 100
	}

	return &cfg
}

// DefaultConfig returns a Config with production-ready defaults.
// SyncWrites is false because Raft WAL provides durability guarantees.
func DefaultConfig(dataDir string) *Config {
	return &Config{
		DataDir:                     dataDir,
		CacheSize:                   512 * 1024 * 1024, // 512 MB
		MaxOpenFiles:                2000,
		MaxConcurrentCompactions:    2,
		SyncWrites:                  false,
		BytesPerSync:                1024 * 1024,       // 1 MB
		MemTableSize:                128 * 1024 * 1024, // 128 MB
		MemTableStopWritesThreshold: 4,
		L0CompactionThreshold:       4,
		L0StopWritesThreshold:       12,
		BlockSize:                   16384, // 16 KB
		Compression:                 CompressionSnappy,
		MaxConcurrentReads:          200,
		MaxConcurrentWrites:         200,
	}
}

// TestConfig returns a Config optimized for unit tests (minimal resources).
func TestConfig(dataDir string) *Config {
	return &Config{
		DataDir:                     dataDir,
		CacheSize:                   8 * 1024 * 1024, // 8 MB
		MaxOpenFiles:                100,
		MaxConcurrentCompactions:    1,
		SyncWrites:                  false,
		BytesPerSync:                1024 * 1024,     // 1 MB
		MemTableSize:                4 * 1024 * 1024, // 4 MB
		MemTableStopWritesThreshold: 2,
		L0CompactionThreshold:       2,
		L0StopWritesThreshold:       6,
		BlockSize:                   4096,
		Compression:                 CompressionNone,
		MaxConcurrentReads:          10,
		MaxConcurrentWrites:         10,
	}
}

// compressionProfile returns the sstable CompressionProfile for the given type.
func compressionProfile(ct CompressionType) *sstable.CompressionProfile {
	switch ct {
	case CompressionNone:
		return sstable.NoCompression
	case CompressionZstd:
		return sstable.ZstdCompression
	default:
		return sstable.SnappyCompression
	}
}

// ToPebbleOptions converts the config to PebbleDB v2's native Options struct.
func (c *Config) ToPebbleOptions() *pebble.Options {
	profile := compressionProfile(c.Compression)

	opts := &pebble.Options{
		Cache:                       pebble.NewCache(c.CacheSize),
		MaxOpenFiles:                c.MaxOpenFiles,
		DisableWAL:                  c.DisableWAL,
		BytesPerSync:                c.BytesPerSync,
		MemTableSize:                uint64(c.MemTableSize), //nolint:gosec // bounded config value
		MemTableStopWritesThreshold: c.MemTableStopWritesThreshold,
		L0CompactionThreshold:       c.L0CompactionThreshold,
		L0StopWritesThreshold:       c.L0StopWritesThreshold,
		ReadOnly:                    c.ReadOnly,
		ErrorIfNotExists:            c.ErrorIfNotExists,
		WALMinSyncInterval:          func() time.Duration { return c.WALMinSyncInterval },
	}

	if c.WALDir != "" {
		opts.WALDir = c.WALDir
	}

	for i := range opts.Levels {
		opts.Levels[i] = pebble.LevelOptions{
			BlockSize:    c.BlockSize,
			Compression:  func() *sstable.CompressionProfile { return profile },
			FilterPolicy: bloom.FilterPolicy(10),
			FilterType:   pebble.TableFilter,
		}
	}

	return opts
}
