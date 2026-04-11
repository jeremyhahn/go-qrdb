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

package vector

import (
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/bloom"
	"github.com/cockroachdb/pebble/v2/sstable"
)

// Config contains configuration for the PebbleDB vector storage.
type Config struct {
	// DataDir is the directory for storage files
	DataDir string

	// MemTableSize is the size of in-memory write buffer (default: 64MB)
	MemTableSize int64

	// CacheSize is the size of the block cache (default: 512MB)
	CacheSize int64

	// MaxConcurrentCompactions limits compaction parallelism (default: 2)
	MaxConcurrentCompactions int

	// WALBytesPerSync controls WAL sync frequency (default: 1MB)
	WALBytesPerSync int64

	// TombstoneRetention is how long to keep tombstones (default: 24h)
	TombstoneRetention time.Duration

	// BloomFilterBitsPerKey controls bloom filter memory/accuracy trade-off (default: 10)
	BloomFilterBitsPerKey int

	// EnableMetrics enables Prometheus metrics (default: true)
	EnableMetrics bool

	// SyncWrites forces fsync on every write (default: false)
	SyncWrites bool

	// MaxConcurrentReads limits concurrent read operations (default: 1000)
	MaxConcurrentReads int

	// MaxConcurrentWrites limits concurrent write operations (default: 100)
	MaxConcurrentWrites int

	// CompactionFilterFunc is called during compaction to filter keys
	CompactionFilterFunc func(key, value []byte) bool
}

// DefaultConfig returns a production-ready configuration.
func DefaultConfig(dataDir string) *Config {
	return &Config{
		DataDir:                  dataDir,
		MemTableSize:             64 << 20,  // 64MB
		CacheSize:                512 << 20, // 512MB
		MaxConcurrentCompactions: 2,
		WALBytesPerSync:          1 << 20, // 1MB
		TombstoneRetention:       24 * time.Hour,
		BloomFilterBitsPerKey:    10,
		EnableMetrics:            true,
		SyncWrites:               false,
		MaxConcurrentReads:       1000,
		MaxConcurrentWrites:      100,
	}
}

// Validate validates the configuration.
func (c *Config) Validate() error {
	if c.DataDir == "" {
		return ErrInvalidConfig
	}
	if c.MemTableSize < 0 {
		return ErrInvalidConfig
	}
	if c.CacheSize < 0 {
		return ErrInvalidConfig
	}
	if c.MaxConcurrentCompactions < 0 {
		return ErrInvalidConfig
	}
	if c.WALBytesPerSync < 0 {
		return ErrInvalidConfig
	}
	if c.TombstoneRetention < 0 {
		return ErrInvalidConfig
	}
	if c.BloomFilterBitsPerKey < 0 {
		return ErrInvalidConfig
	}
	if c.MaxConcurrentReads <= 0 {
		c.MaxConcurrentReads = 1000
	}
	if c.MaxConcurrentWrites <= 0 {
		c.MaxConcurrentWrites = 100
	}
	return nil
}

// WithDefaults returns a copy of the config with defaults applied for zero values.
func (c *Config) WithDefaults() *Config {
	cfg := *c
	if cfg.MemTableSize == 0 {
		cfg.MemTableSize = 64 << 20
	}
	if cfg.CacheSize == 0 {
		cfg.CacheSize = 512 << 20
	}
	if cfg.MaxConcurrentCompactions == 0 {
		cfg.MaxConcurrentCompactions = 2
	}
	if cfg.WALBytesPerSync == 0 {
		cfg.WALBytesPerSync = 1 << 20
	}
	if cfg.TombstoneRetention == 0 {
		cfg.TombstoneRetention = 24 * time.Hour
	}
	if cfg.BloomFilterBitsPerKey == 0 {
		cfg.BloomFilterBitsPerKey = 10
	}
	if cfg.MaxConcurrentReads == 0 {
		cfg.MaxConcurrentReads = 1000
	}
	if cfg.MaxConcurrentWrites == 0 {
		cfg.MaxConcurrentWrites = 100
	}
	return &cfg
}

// ToPebbleOptions converts the config to PebbleDB options.
func (c *Config) ToPebbleOptions() *pebble.Options {
	cache := pebble.NewCache(c.CacheSize)
	opts := &pebble.Options{
		Cache:                  cache,
		MemTableSize:           uint64(c.MemTableSize), //nolint:gosec // G115: Safe conversion - memtable size from config
		WALBytesPerSync:        int(c.WALBytesPerSync),
		DisableWAL:             false,
		ErrorIfExists:          false,
		BytesPerSync:           512 << 10, // 512KB
		MaxOpenFiles:           1000,
		L0CompactionThreshold:  4,
		L0StopWritesThreshold:  12,
		LBaseMaxBytes:          64 << 20,  // 64MB
		MaxManifestFileSize:    128 << 20, // 128MB
		TargetByteDeletionRate: 0,
	}

	// Configure bloom filters for all levels
	for i := range opts.Levels {
		opts.Levels[i] = pebble.LevelOptions{
			BlockSize:      32 << 10,  // 32KB
			IndexBlockSize: 256 << 10, // 256KB
			FilterPolicy:   bloom.FilterPolicy(c.BloomFilterBitsPerKey),
			FilterType:     pebble.TableFilter,
			Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
		}
	}

	return opts
}
