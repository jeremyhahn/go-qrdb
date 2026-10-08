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

package sdk

import (
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// ---------------------------------------------------------------------------
// Storage interfaces
// ---------------------------------------------------------------------------

// Storage is the low-level storage engine interface.
type Storage = storage.Storage

// StorageBatch is a batch of writes applied atomically.
type StorageBatch = storage.Batch

// StorageIterator iterates over key-value pairs in a storage engine.
type StorageIterator = storage.Iterator

// StorageSnapshot is a consistent point-in-time view of storage.
type StorageSnapshot = storage.Snapshot

// StorageIteratorOptions configures iterator behavior.
type StorageIteratorOptions = storage.IteratorOptions

// ---------------------------------------------------------------------------
// Storage error types
// ---------------------------------------------------------------------------

// StorageError is the base storage error type.
type StorageError = storage.StorageError

// StorageOpError wraps a storage operation error.
type StorageOpError = storage.OpError

// StorageKeyNotFoundError is returned when a key does not exist.
type StorageKeyNotFoundError = storage.KeyNotFoundError

// StorageClosedError is returned when storage is closed. It carries no data;
// match ErrStorageClosed rather than this type, so that a backend returning
// the sentinel directly is recognized too.
type StorageClosedError = storage.ClosedError

// ErrStorageClosed is the sentinel every closed-store failure unwraps to,
// whichever backend produced it and however deeply it is wrapped:
//
//	if errors.Is(err, sdk.ErrStorageClosed) {
//		// the node closed the store under this call; conclude, do not crash
//	}
//
// It survives the embedded transport intact — the service layer's ProposeError
// and the transport's RequestError both unwrap. The network transports carry
// the CLASSIFICATION (gRPC Unavailable, HTTP 503) but not the value, because a
// status code is all that crosses the wire; a remote caller uses that instead.
var ErrStorageClosed = storage.ErrStorageClosed

// IsStorageClosed reports whether err is, or wraps, the store having been
// closed. Prefer it to comparing error strings.
var IsStorageClosed = storage.IsClosed

// StorageBatchError is returned when a batch operation fails.
type StorageBatchError = storage.BatchError

// StorageIteratorError is returned when an iterator operation fails.
type StorageIteratorError = storage.IteratorError

// StorageSnapshotError is returned when a snapshot operation fails.
type StorageSnapshotError = storage.SnapshotError

// ---------------------------------------------------------------------------
// Storage implementations
// ---------------------------------------------------------------------------

// MemoryStorage is an in-memory storage engine.
type MemoryStorage = memory.MemoryStorage

// FileEngine is a file-based storage engine.
type FileEngine = file.Engine

// PebbleStorage is a PebbleDB-backed storage engine.
type PebbleStorage = pebble.PebbleStorage

// ObjStoreEngine is an object-storage-backed storage engine. The "local" and
// "memory" providers are always available; cloud providers (s3, gcs, azure,
// glacier, azurearchive) are compiled in only when go-objstore's
// matching build tag is set.
type ObjStoreEngine = objstore.Engine

// ObjStoreUnsupportedProviderError is returned for a provider not compiled into
// this build (cloud providers require go-objstore build tags).
type ObjStoreUnsupportedProviderError = objstore.UnsupportedProviderError

// PebbleConfig holds PebbleDB configuration.
type PebbleConfig = pebble.Config

// PebbleCompressionType identifies the compression algorithm for PebbleDB.
type PebbleCompressionType = pebble.CompressionType

// PebbleCompressionType constants.
const (
	PebbleCompressionNone   = pebble.CompressionNone
	PebbleCompressionSnappy = pebble.CompressionSnappy
	PebbleCompressionZstd   = pebble.CompressionZstd
)

// ---------------------------------------------------------------------------
// Storage constructors
// ---------------------------------------------------------------------------

// NewMemoryStorage creates a new in-memory storage engine.
var NewMemoryStorage = memory.NewStorage

// NewFileStorage creates a new file-based storage engine at the given path.
var NewFileStorage = file.New

// NewPebbleStorage creates a new PebbleDB storage engine.
var NewPebbleStorage = pebble.New

// NewPebbleStorageFromDB wraps an existing PebbleDB instance.
var NewPebbleStorageFromDB = pebble.NewFromDB

// NewObjStoreStorage creates an object-storage-backed storage engine for the
// given go-objstore provider ("local", "memory", "s3", ...). Settings are passed
// verbatim to the provider (e.g. "path" for local, bucket/region for cloud).
var NewObjStoreStorage = objstore.New

// DefaultPebbleConfig returns the default PebbleDB configuration.
var DefaultPebbleConfig = pebble.DefaultConfig

// TestPebbleConfig returns a test-optimized PebbleDB configuration.
var TestPebbleConfig = pebble.TestConfig

// EncryptedPebbleConfig returns a DefaultPebbleConfig for the given dataDir with
// the Pebble vfs.FS pre-set to an encfs-wrapped FS backed by the supplied
// StorageBarrier. The barrier must be unsealed before pebble.Open is called.
// A nil barrier returns a plain DefaultPebbleConfig with no encryption.
func EncryptedPebbleConfig(dataDir string, barrier *seal.StorageBarrier) *pebble.Config {
	cfg := pebble.DefaultConfig(dataDir)
	if barrier != nil {
		cfg.FS = encfs.New(vfs.Default, barrier)
	}
	return cfg
}
