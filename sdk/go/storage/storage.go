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

package storage

import (
	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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

// GuardedStorage is the optional interface a Storage backend implements to
// offer an atomic CompareAndSwap on raw bytes. Memory, File, Pebble and
// ObjStore storage all implement it; assert for it the way you would for a
// syncer:
//
//	guarded, ok := store.(sdkstorage.GuardedStorage)
//
// A losing swap reports false with a nil error, and an absent key is never
// created.
type GuardedStorage = storage.GuardedStorage

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

// ObjStoreOption configures NewObjStoreStorage beyond the provider settings.
type ObjStoreOption = objstore.Option

// ObjStoreCredentialFiles names the files holding an object-storage
// credential: KeyFile the access key, SecretFile the secret. Credentials are
// never passed inline in the provider settings; NewObjStoreStorage reads each
// file into protected memory, hands the provider handles, and destroys them
// when the open returns.
type ObjStoreCredentialFiles = objstore.CredentialFiles

// ObjStoreCredential names one half of an object-storage credential in a
// credential error.
type ObjStoreCredential = objstore.Credential

// ObjStoreCredential values.
const (
	ObjStoreCredentialKey    = objstore.CredentialKey
	ObjStoreCredentialSecret = objstore.CredentialSecret
)

// ErrObjStoreCredentialKeyWithoutSecret is returned for ObjStoreCredentialFiles
// naming a key file without a secret file.
var ErrObjStoreCredentialKeyWithoutSecret = objstore.ErrCredentialKeyWithoutSecret

// ObjStoreCredentialFileError is returned when a credential file cannot be
// read; it names the file, never its contents.
type ObjStoreCredentialFileError = objstore.CredentialFileError

// ObjStoreCredentialIgnoredError is returned when credential files are
// configured for a provider that does not read them.
type ObjStoreCredentialIgnoredError = objstore.CredentialIgnoredError

// ObjStoreCredentialUnavailableError is returned when a provider fails to
// read the credential handles it was given.
type ObjStoreCredentialUnavailableError = objstore.CredentialUnavailableError

// ObjStoreCredentialSettingError is returned when the provider settings carry
// a credential inline; it names the setting, never its value.
type ObjStoreCredentialSettingError = objstore.CredentialSettingError

// ObjStoreProviderCredentials declares which credential files a provider
// registered with RegisterObjStoreProvider reads.
type ObjStoreProviderCredentials = objstore.ProviderCredentials

// ObjStoreProviderRegisteredError is returned by RegisterObjStoreProvider for
// a name go-objstore ships or an earlier registration already holds.
type ObjStoreProviderRegisteredError = objstore.ProviderRegisteredError

// ErrInvalidObjStoreProviderRegistration is returned by
// RegisterObjStoreProvider for an empty name or a nil creator.
var ErrInvalidObjStoreProviderRegistration = objstore.ErrInvalidProviderRegistration

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

// WithObjStoreCredentialFiles makes NewObjStoreStorage hand the provider the
// credential the named files hold. A provider that does not read a configured
// file fails the open with ObjStoreCredentialIgnoredError.
var WithObjStoreCredentialFiles = objstore.WithCredentialFiles

// RejectObjStoreCredentialSettings returns ObjStoreCredentialSettingError when
// provider settings carry a credential inline.
var RejectObjStoreCredentialSettings = objstore.RejectCredentialSettings

// RegisterObjStoreProvider registers a go-objstore storage creator under name
// and declares which credential files it reads, so NewObjStoreStorage delivers
// them to it. It is safe for concurrent use and never replaces a shipped or
// earlier registration.
var RegisterObjStoreProvider = objstore.RegisterProvider

// DefaultPebbleConfig returns the default PebbleDB configuration.
var DefaultPebbleConfig = pebble.DefaultConfig

// TestPebbleConfig returns a test-optimized PebbleDB configuration.
var TestPebbleConfig = pebble.TestConfig

// EncryptedPebbleConfig returns a DefaultPebbleConfig for the given dataDir with
// the Pebble FS pre-set to an encfs-wrapped FS backed by the supplied
// StorageBarrier. The barrier must be unsealed before pebble.Open is called.
// A nil barrier returns a plain DefaultPebbleConfig with no encryption.
func EncryptedPebbleConfig(dataDir string, barrier *seal.StorageBarrier) *pebble.Config {
	cfg := pebble.DefaultConfig(dataDir)
	if barrier != nil {
		cfg.FS = pebble.WrapVFS(nil, atrest.New(barrier).Wrap)
	}
	return cfg
}
