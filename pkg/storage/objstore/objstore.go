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

// Package objstore provides a key-value storage engine backed by go-objstore,
// so any go-objstore provider (local, memory, s3, gcs, azure, minio, ...) can
// serve as a flat KV backend. It mirrors the file backend's contract — a simple
// KV engine with no MVCC — and is intended for blob/backup-oriented workloads
// rather than hot consensus paths. Keys are used as object keys verbatim, so
// callers must keep them within the key constraints of the chosen provider.
package objstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// Compile-time interface check.
var _ storage.Storage = (*Engine)(nil)

// Engine is a key-value storage engine backed by a go-objstore Storage.
type Engine struct {
	store    objcommon.Storage
	provider string
	closed   atomic.Bool
}

// New constructs an objstore-backed engine for the given go-objstore provider.
// Providers are resolved through go-objstore's factory: "local" and "memory" are
// always available, while cloud providers ("s3", "gcs", "azure", "minio",
// "glacier", "azurearchive") link their SDKs and are compiled in only when
// go-objstore's matching build tag is set (awss3, gcpstorage, azureblob, minio,
// glacier, azurearchive). Settings are passed verbatim to the provider.
func New(provider string, settings map[string]string) (*Engine, error) {
	if provider == "" {
		return nil, storage.ErrInvalidPath
	}
	store, err := objfactory.NewStorage(provider, settings)
	if err != nil {
		if errors.Is(err, objfactory.ErrUnknownBackend) {
			return nil, &UnsupportedProviderError{Provider: provider}
		}
		return nil, err
	}
	return &Engine{store: store, provider: provider}, nil
}

// UnsupportedProviderError indicates the requested go-objstore provider is not
// registered in this build. Cloud providers are compiled in only when
// go-objstore's matching build tag is set (e.g. -tags awss3 for "s3").
type UnsupportedProviderError struct {
	Provider string
}

func (e *UnsupportedProviderError) Error() string {
	return "objstore: unsupported provider " + e.Provider +
		` (cloud providers require go-objstore's build tag, e.g. -tags awss3 for s3)`
}

// Provider returns the go-objstore provider backing this engine.
func (e *Engine) Provider() string { return e.provider }

// notFound reports whether err signals a missing object.
func notFound(err error) bool {
	return errors.Is(err, objcommon.ErrKeyNotFound)
}

// Get retrieves the value for key. Returns nil, nil when the key does not exist,
// per the storage.Storage contract.
func (e *Engine) Get(key []byte) ([]byte, error) {
	if e.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	if len(key) == 0 {
		return nil, storage.ErrEmptyKey
	}
	rc, err := e.store.Get(string(key))
	if err != nil {
		if notFound(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Set stores a key-value pair as an object.
func (e *Engine) Set(key, value []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	return e.store.Put(string(key), bytes.NewReader(value))
}

// Delete removes a key. A missing key is not an error (idempotent).
func (e *Engine) Delete(key []byte) error {
	if e.closed.Load() {
		return &storage.ClosedError{}
	}
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	if err := e.store.Delete(string(key)); err != nil && !notFound(err) {
		return err
	}
	return nil
}

// List returns all keys matching prefix. An empty prefix returns all keys.
//
// Providers are only asked to narrow by the directory-aligned portion of the
// prefix (up to the last '/'): object-store providers use path semantics, and
// e.g. go-objstore's local provider stats the joined path and returns an empty
// list for a partial key component like "k0". The byte-prefix contract of
// storage.Storage is enforced by the client-side filter below.
func (e *Engine) List(prefix []byte) ([][]byte, error) {
	if e.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	providerPrefix := string(prefix)
	if i := strings.LastIndexByte(providerPrefix, '/'); i >= 0 {
		providerPrefix = providerPrefix[:i+1]
	} else {
		providerPrefix = ""
	}
	names, err := e.store.List(providerPrefix)
	if err != nil {
		return nil, err
	}
	keys := make([][]byte, 0, len(names))
	for _, name := range names {
		k := []byte(name)
		// Defend against providers that do not filter server-side.
		if len(prefix) > 0 && !bytes.HasPrefix(k, prefix) {
			continue
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// Exists reports whether key exists.
func (e *Engine) Exists(key []byte) (bool, error) {
	if e.closed.Load() {
		return false, &storage.ClosedError{}
	}
	if len(key) == 0 {
		return false, storage.ErrEmptyKey
	}
	ok, err := e.store.Exists(context.Background(), string(key))
	if err != nil {
		if notFound(err) {
			return false, nil
		}
		return false, err
	}
	return ok, nil
}

// Batch creates a new write batch for atomic multi-key operations. A closed
// engine returns a batch that reports the closure from every operation.
func (e *Engine) Batch() storage.Batch {
	if e.closed.Load() {
		return storage.NewClosedBatch()
	}
	return newBatch(e)
}

// NewIterator creates an iterator for scanning key ranges. A closed engine
// returns an iterator that reports the closure through Error.
func (e *Engine) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if e.closed.Load() {
		return storage.NewClosedIterator()
	}
	return newIterator(e, opts)
}

// Snapshot creates a point-in-time snapshot for consistent reads.
func (e *Engine) Snapshot() (storage.Snapshot, error) {
	if e.closed.Load() {
		return nil, &storage.ClosedError{}
	}
	return newSnapshot(e)
}

// Close marks the engine as closed. The underlying go-objstore Storage exposes
// no Close, so there is nothing further to release.
func (e *Engine) Close() error {
	e.closed.Store(true)
	return nil
}
