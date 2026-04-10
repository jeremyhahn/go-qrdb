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

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
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
type EmbeddedBackendFactory struct{}

// NewEmbeddedBackendFactory creates a new EmbeddedBackendFactory.
func NewEmbeddedBackendFactory() *EmbeddedBackendFactory {
	return &EmbeddedBackendFactory{}
}

// Create creates a StandaloneBackend for the given configuration.
func (f *EmbeddedBackendFactory) Create(cfg DatabaseConfig, dataDir string) (Backend, error) {
	store, err := openStorage(cfg.Name, cfg.Backend, dataDir)
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
		store, err := file.New(dataDir)
		if err != nil {
			return nil, &StorageCreateError{Name: name, Backend: string(backend), Err: err}
		}
		return store, nil
	case BackendPebble:
		cfg := pebble.DefaultConfig(dataDir)
		store, err := pebble.New(cfg)
		if err != nil {
			return nil, &StorageCreateError{Name: name, Backend: string(backend), Err: err}
		}
		return store, nil
	case BackendRaft:
		return nil, &StorageCreateError{
			Name:    name,
			Backend: string(backend),
			Err:     &InvalidBackendError{Backend: "raft (not yet implemented)"},
		}
	default:
		return nil, &InvalidBackendError{Backend: string(backend)}
	}
}
