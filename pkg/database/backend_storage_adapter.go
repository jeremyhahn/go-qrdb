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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// BackendStorageAdapter wraps a database.Backend as a seal.StorageBackend.
// This enables the barrier system to use the database's backend for
// storing its root key and barrier metadata.
type BackendStorageAdapter struct {
	inner Backend
}

// Compile-time interface check.
var _ seal.StorageBackend = (*BackendStorageAdapter)(nil)

// NewBackendStorageAdapter creates a new adapter wrapping the given Backend.
func NewBackendStorageAdapter(b Backend) *BackendStorageAdapter {
	return &BackendStorageAdapter{inner: b}
}

// Get retrieves the value for the given string key.
func (a *BackendStorageAdapter) Get(ctx context.Context, key string) ([]byte, error) {
	return a.inner.Get(ctx, []byte(key))
}

// Put stores value under the given string key.
func (a *BackendStorageAdapter) Put(ctx context.Context, key string, data []byte) error {
	return a.inner.Put(ctx, []byte(key), data)
}

// Delete removes the string key.
func (a *BackendStorageAdapter) Delete(ctx context.Context, key string) error {
	return a.inner.Delete(ctx, []byte(key))
}

// List returns all keys matching the prefix as strings.
func (a *BackendStorageAdapter) List(ctx context.Context, prefix string) ([]string, error) {
	return a.inner.List(ctx, prefix)
}

// Scan iterates over all key-value pairs matching prefix, invoking fn for each
// entry. Implemented by listing keys and fetching each value.
func (a *BackendStorageAdapter) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	keys, err := a.inner.List(ctx, prefix)
	if err != nil {
		return err
	}
	for _, k := range keys {
		v, getErr := a.inner.Get(ctx, []byte(k))
		if getErr != nil {
			continue // key may have been deleted concurrently
		}
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

// Exists checks if a key exists by attempting a Get.
func (a *BackendStorageAdapter) Exists(ctx context.Context, key string) (bool, error) {
	v, err := a.inner.Get(ctx, []byte(key))
	if err != nil {
		return false, err
	}
	return v != nil, nil
}

// Close is a no-op; the underlying Backend is closed by the Database.
func (a *BackendStorageAdapter) Close() error {
	return nil
}
