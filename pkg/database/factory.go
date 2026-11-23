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

package database

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
)

// BackendFactory creates Backend instances for a given database configuration.
// Implementations select the appropriate storage backend based on the
// database mode and backend type.
type BackendFactory interface {
	// Create creates a new Backend for the given config and data directory.
	Create(cfg DatabaseConfig, dataDir string) (Backend, error)

	// Close releases any resources held by the factory.
	Close() error
}

// EmbeddedBackendFactory creates standalone backends backed by local storage
// engines (memory, file, pebble).
type EmbeddedBackendFactory struct {
	objectStorage          ObjectStorageOpener
	objectStorageValidate  ObjectStorageSettingsValidator
	objectStorageNamespace ObjectStorageNamespacer
}

// ObjectStorageSettingsValidator checks a database's Objstore provider
// settings before the database is written to the catalog, which replicates
// them to every node: it refuses an inline credential, returning
// *ObjstoreCredentialSettingError. pkg/database does not link go-objstore,
// which owns the set of credential setting names, so the node composer
// supplies it (pkg/database/objstoredb.ValidateSettings).
type ObjectStorageSettingsValidator func(settings map[string]string) error

// ObjectStorageOpener opens the storage of a `backend: objstore` database
// from its DatabaseConfig. pkg/database does not link an object-storage
// engine itself, so a lean embedder stays free of go-objstore; the node
// composer supplies the opener (pkg/database/objstoredb.Open).
type ObjectStorageOpener func(cfg DatabaseConfig) (storage.Storage, error)

// EmbeddedBackendFactoryOption configures an EmbeddedBackendFactory.
type EmbeddedBackendFactoryOption func(*EmbeddedBackendFactory)

// WithObjectStorage supplies the opener for `backend: objstore` databases.
// Without it the factory refuses such a database with
// *ObjectStorageNotWiredError.
func WithObjectStorage(open ObjectStorageOpener) EmbeddedBackendFactoryOption {
	return func(f *EmbeddedBackendFactory) {
		f.objectStorage = open
	}
}

// WithObjectStorageValidator supplies the settings validator for a
// database's Objstore block, whatever its backend. Without it the factory
// refuses any Objstore block with *ObjectStorageNotWiredError.
func WithObjectStorageValidator(validate ObjectStorageSettingsValidator) EmbeddedBackendFactoryOption {
	return func(f *EmbeddedBackendFactory) {
		f.objectStorageValidate = validate
	}
}

// ValidateObjectStorage checks cfg's Objstore block through the
// WithObjectStorageValidator validator. A config without one is valid; one
// with a block and no validator wired is refused with
// *ObjectStorageNotWiredError. The Manager calls it before the catalog write.
func (f *EmbeddedBackendFactory) ValidateObjectStorage(cfg DatabaseConfig) error {
	if cfg.Objstore == nil {
		return nil
	}
	if f.objectStorageValidate == nil {
		return &ObjectStorageNotWiredError{Name: cfg.Name}
	}
	return f.objectStorageValidate(cfg.Objstore.Settings)
}

// objectStorageValidating is a BackendFactory that validates a database's
// Objstore block (EmbeddedBackendFactory.ValidateObjectStorage).
type objectStorageValidating interface {
	ValidateObjectStorage(cfg DatabaseConfig) error
}

// validateObjectStorage checks cfg's Objstore block through factory. A
// factory that cannot validate one refuses it with *ObjectStorageNotWiredError,
// so an unchecked setting never reaches the catalog.
func validateObjectStorage(factory BackendFactory, cfg DatabaseConfig) error {
	if cfg.Objstore == nil {
		return nil
	}
	v, ok := factory.(objectStorageValidating)
	if !ok {
		return &ObjectStorageNotWiredError{Name: cfg.Name}
	}
	return v.ValidateObjectStorage(cfg)
}

// ObjectStorageNamespacer names the provider namespace an object-storage
// database's configuration resolves to: two configurations with one key
// address one namespace (a symlinked local root and its target, two
// spellings of one endpoint). A seam, as ObjectStorageSettingsValidator is,
// so this package does not import the object-storage engine.
type ObjectStorageNamespacer func(cfg DatabaseConfig) (string, error)

// ErrObjectStorageNamespaceUnknown is returned by ObjectStorageNamespace
// when no ObjectStorageNamespacer is wired: the namespace of a configuration
// cannot be told, so nothing may be released on the strength of it.
var ErrObjectStorageNamespaceUnknown = errors.New("database: object-storage namespace unknown: no namespacer is wired")

// WithObjectStorageNamespace wires the namespacer the manager uses to tell
// whether a dropped database's namespace is the one a re-created database of
// its name holds (the namespace release retry).
func WithObjectStorageNamespace(ns ObjectStorageNamespacer) EmbeddedBackendFactoryOption {
	return func(f *EmbeddedBackendFactory) {
		f.objectStorageNamespace = ns
	}
}

// ObjectStorageNamespace returns the namespace key of cfg's object storage
// (ObjectStorageNamespacer), or ErrObjectStorageNamespaceUnknown when no
// namespacer is wired.
func (f *EmbeddedBackendFactory) ObjectStorageNamespace(cfg DatabaseConfig) (string, error) {
	if f.objectStorageNamespace == nil {
		return "", ErrObjectStorageNamespaceUnknown
	}
	return f.objectStorageNamespace(cfg)
}

// objectStorageNamespacing is the namespacer side of a BackendFactory.
type objectStorageNamespacing interface {
	ObjectStorageNamespace(cfg DatabaseConfig) (string, error)
}

// objectStorageNamespace returns the namespace key of cfg's object storage
// through factory, or ErrObjectStorageNamespaceUnknown when factory has no
// namespacer.
func objectStorageNamespace(factory BackendFactory, cfg DatabaseConfig) (string, error) {
	n, ok := factory.(objectStorageNamespacing)
	if !ok {
		return "", ErrObjectStorageNamespaceUnknown
	}
	return n.ObjectStorageNamespace(cfg)
}

// NewEmbeddedBackendFactory creates a new EmbeddedBackendFactory.
func NewEmbeddedBackendFactory(opts ...EmbeddedBackendFactoryOption) *EmbeddedBackendFactory {
	f := &EmbeddedBackendFactory{}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// Create creates a StandaloneBackend for the given configuration.
func (f *EmbeddedBackendFactory) Create(cfg DatabaseConfig, dataDir string) (Backend, error) {
	store, err := openStorage(cfg, dataDir, f.objectStorage)
	if err != nil {
		return nil, err
	}
	return NewStandaloneBackend(store), nil
}

// Close is a no-op for the embedded factory.
func (f *EmbeddedBackendFactory) Close() error {
	return nil
}

// RemoteBackend proxies Backend operations through an SDK transport to a
// remote qrdb server.
type RemoteBackend struct {
	transport RemoteTransport
}

// RemoteTransport defines the minimal interface that RemoteBackend requires
// to proxy operations to a remote qrdb server.
type RemoteTransport interface {
	Get(ctx context.Context, key []byte) ([]byte, error)
	Put(ctx context.Context, key, value []byte) error
	Delete(ctx context.Context, key []byte) error
	DeleteRange(ctx context.Context, start, end []byte) error
	List(ctx context.Context, prefix string) ([]string, error)
	Range(ctx context.Context, start, end []byte, limit int) ([]KeyValue, error)
	Batch(ctx context.Context, ops []BatchOp) error
	Close() error
}

// NewRemoteBackend creates a new RemoteBackend proxying through the given
// transport.
func NewRemoteBackend(transport RemoteTransport) *RemoteBackend {
	return &RemoteBackend{transport: transport}
}

// Get retrieves a value by key from the remote server.
func (b *RemoteBackend) Get(ctx context.Context, key []byte) ([]byte, error) {
	return b.transport.Get(ctx, key)
}

// Put stores a key-value pair on the remote server.
func (b *RemoteBackend) Put(ctx context.Context, key, value []byte) error {
	return b.transport.Put(ctx, key, value)
}

// Delete removes a key from the remote server.
func (b *RemoteBackend) Delete(ctx context.Context, key []byte) error {
	return b.transport.Delete(ctx, key)
}

func (b *RemoteBackend) DeleteRange(ctx context.Context, start, end []byte) error {
	return b.transport.DeleteRange(ctx, start, end)
}

// List returns all keys matching the prefix from the remote server.
func (b *RemoteBackend) List(ctx context.Context, prefix string) ([]string, error) {
	return b.transport.List(ctx, prefix)
}

// Range scans a key range on the remote server.
func (b *RemoteBackend) Range(ctx context.Context, start, end []byte, limit int) ([]KeyValue, error) {
	return b.transport.Range(ctx, start, end, limit)
}

// Batch executes a batch of operations on the remote server.
func (b *RemoteBackend) Batch(ctx context.Context, ops []BatchOp) error {
	return b.transport.Batch(ctx, ops)
}

// Close shuts down the remote transport connection.
func (b *RemoteBackend) Close() error {
	return b.transport.Close()
}

// openStorage creates a storage.Storage for the given backend type.
func openStorage(cfg DatabaseConfig, dataDir string, objectStorage ObjectStorageOpener) (storage.Storage, error) {
	name, backend := cfg.Name, cfg.Backend
	switch backend {
	case BackendMemory:
		return memory.NewStorage(), nil
	case BackendFile:
		store, err := file.New(dataDir, file.WithSyncObserver(storageSyncs(syncwatch.BackendFile, name, dataDir)))
		if err != nil {
			return nil, &StorageCreateError{Name: name, Backend: string(backend), Err: err}
		}
		return store, nil
	case BackendPebble:
		cfg := pebble.DefaultConfig(dataDir)
		store, err := pebble.New(cfg, pebble.WithSyncObserver(storageSyncs(syncwatch.BackendPebble, name, dataDir)))
		if err != nil {
			return nil, &StorageCreateError{Name: name, Backend: string(backend), Err: err}
		}
		return store, nil
	case BackendObjstore:
		// The objstore engine keeps its data in the provider, not under the
		// database's data root. The opener comes from the node composer, so
		// a lean embedder without one refuses the database.
		if objectStorage == nil {
			return nil, &ObjectStorageNotWiredError{Name: name}
		}
		store, err := objectStorage(cfg)
		if err != nil {
			return nil, &StorageCreateError{Name: name, Backend: string(backend), Err: err}
		}
		return store, nil
	case BackendRaft:
		// A dedicated-raft database's authoritative data plane is a per-database
		// Raft shard hosted on the shared system Host by the server-layer
		// RaftBackendFactory (IsolationRaft): the shard's state-machine data lives
		// under the tenant's DataRoot ({DataRoot}/databases/{db}/shard-<N>) and is
		// reached through the request resolver, not through this Backend. The
		// Manager still opens a Backend so the database's catalog record, open-map
		// membership, and shard-ID allocation are tracked uniformly with every
		// other backend; that bookkeeping role is served by a lightweight
		// in-memory placeholder. The placeholder is never used for the data plane.
		return memory.NewStorage(), nil
	default:
		return nil, &InvalidBackendError{Backend: string(backend)}
	}
}

// storageSyncs is the sync observer for database name's storage: its syncs
// record under backend, and a slow one warns naming the database and its
// data directory.
func storageSyncs(backend, name, dataDir string) *syncwatch.Observer {
	return syncwatch.ForStorage(backend, syncwatch.WithAttrs(slog.String("database", name), slog.String("dir", dataDir)))
}
