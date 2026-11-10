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
	objstoreOpener ObjstoreOpener
}

// ObjstoreOpener opens the objstore engine of tenantID's database name, whose
// own directory is dataDir. tenantID is empty for a database no tenant owns.
// The node supplies the opener, because only the node holds the
// object-storage provider configuration and its credential files.
//
// The database's objects outlive its directory, so the opener also returns
// the ObjstoreReclaim that deletes them; an opener that returns none fails the
// open, because such a database could never be dropped.
type ObjstoreOpener func(tenantID, name, dataDir string) (storage.Storage, ObjstoreReclaim, error)

// ObjstoreReclaim deletes every object an objstore database holds in its
// namespace: records, format marker and commit journal. Drop calls it after
// the database is closed and before it reports the drop, so a database
// recreated in the namespace finds none of the old keys. It must succeed on
// a namespace already partly or wholly reclaimed, because a failed drop is
// retried.
type ObjstoreReclaim func() error

// storeReclaimer is a Backend whose data lives outside its database
// directory. Drop calls ReclaimStore to delete that data, and fails when it
// cannot.
type storeReclaimer interface {
	ReclaimStore() error
}

// objstoreBackend is the Backend of an objstore database: a StandaloneBackend
// that carries the reclaim of its objects.
type objstoreBackend struct {
	*StandaloneBackend
	reclaim ObjstoreReclaim
}

// ReclaimStore deletes the database's objects.
func (b *objstoreBackend) ReclaimStore() error {
	return b.reclaim()
}

// EmbeddedBackendOption configures an EmbeddedBackendFactory.
type EmbeddedBackendOption func(*EmbeddedBackendFactory)

// WithObjstoreOpener sets the opener a backend: objstore database is opened
// through. Without one such a database is refused with
// ObjstoreNotConfiguredError.
func WithObjstoreOpener(open ObjstoreOpener) EmbeddedBackendOption {
	return func(f *EmbeddedBackendFactory) {
		f.objstoreOpener = open
	}
}

// NewEmbeddedBackendFactory creates a new EmbeddedBackendFactory.
func NewEmbeddedBackendFactory(opts ...EmbeddedBackendOption) *EmbeddedBackendFactory {
	f := &EmbeddedBackendFactory{}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// Create creates a StandaloneBackend for the given configuration.
func (f *EmbeddedBackendFactory) Create(cfg DatabaseConfig, dataDir string) (Backend, error) {
	if cfg.Backend == BackendObjstore {
		return f.openObjstore(cfg.TenantID, cfg.Name, dataDir)
	}
	store, err := openStorage(cfg.Name, cfg.Backend, dataDir)
	if err != nil {
		return nil, err
	}
	return NewStandaloneBackend(store), nil
}

// errObjstoreNoReclaim fails the open of an objstore database whose opener
// returned no reclaim: Drop could not delete its objects.
var errObjstoreNoReclaim = errors.New("database: the objstore opener returned no reclaim for the database's objects")

// openObjstore opens a backend: objstore database through the node's opener.
// The Backend carries the opener's reclaim, which Drop calls to delete the
// database's objects.
func (f *EmbeddedBackendFactory) openObjstore(tenantID, name, dataDir string) (Backend, error) {
	if f.objstoreOpener == nil {
		return nil, &ObjstoreNotConfiguredError{Name: name}
	}
	store, reclaim, err := f.objstoreOpener(tenantID, name, dataDir)
	if err != nil {
		return nil, &StorageCreateError{Name: name, Backend: string(BackendObjstore), Err: err}
	}
	if reclaim == nil {
		_ = store.Close()
		return nil, &StorageCreateError{Name: name, Backend: string(BackendObjstore), Err: errObjstoreNoReclaim}
	}
	return &objstoreBackend{StandaloneBackend: NewStandaloneBackend(store), reclaim: reclaim}, nil
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
func openStorage(name string, backend BackendType, dataDir string) (storage.Storage, error) {
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
