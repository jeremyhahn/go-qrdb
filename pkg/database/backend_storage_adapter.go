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
	"bytes"
	"context"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// BackendStorageAdapter wraps a database.Backend as a seal.StorageBackend.
// This enables the barrier system to use the database's backend for
// storing its root key and barrier metadata.
type BackendStorageAdapter struct {
	inner Backend

	// writeMu serializes the check-then-write performed by PutIfAbsent.
	// database.Backend implementations (StandaloneBackend, BarrierBackend) are
	// single-process local engines, so this mutex is the genuine atomicity
	// mechanism — there is no other process to race with.
	writeMu sync.Mutex
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
	// Takes writeMu so it cannot land BETWEEN a guarded operation's read and
	// its write. Without this the guards are advisory: PutIfAbsent could
	// overwrite a value written after its check, and CompareAndDelete could
	// delete a replacement it never compared against.
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.inner.Put(ctx, []byte(key), data)
}

// PutIfAbsent atomically stores data under the given key only if the key does
// not already exist. The check-then-write is serialized under putIfAbsentMu,
// which provides genuine atomicity for the single-process backends this
// adapter wraps. Backend.Get returns nil, nil for a missing key, so a nil
// value is the absence signal.
func (a *BackendStorageAdapter) PutIfAbsent(ctx context.Context, key string, data []byte) (bool, error) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()

	v, err := a.inner.Get(ctx, []byte(key))
	if err != nil {
		return false, err
	}
	if v != nil {
		return false, nil
	}
	if err := a.inner.Put(ctx, []byte(key), data); err != nil {
		return false, err
	}
	return true, nil
}

// CompareAndDelete removes key only when its stored value still equals
// expected. The same mutex that makes PutIfAbsent indivisible makes the
// compare and the delete one operation here.
func (a *BackendStorageAdapter) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()

	v, err := a.inner.Get(ctx, []byte(key))
	if err != nil {
		return false, err
	}
	if v == nil || !bytes.Equal(v, expected) {
		return false, nil
	}
	if err := a.inner.Delete(ctx, []byte(key)); err != nil {
		return false, err
	}
	return true, nil
}

// CompareAndSwap replaces the key's value only when it still equals expected,
// under the same write mutex the guarded delete uses — that lock is what makes
// the read-then-write atomic for this adapter. An absent key is left absent
// (go-qrdb#56).
func (a *BackendStorageAdapter) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()

	v, err := a.inner.Get(ctx, []byte(key))
	if err != nil {
		return false, err
	}
	if v == nil || !bytes.Equal(v, expected) {
		return false, nil
	}
	if err := a.inner.Put(ctx, []byte(key), next); err != nil {
		return false, err
	}
	return true, nil
}

// Delete removes the string key.
func (a *BackendStorageAdapter) Delete(ctx context.Context, key string) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.inner.Delete(ctx, []byte(key))
}

func (a *BackendStorageAdapter) DeleteRange(ctx context.Context, start, end []byte) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.inner.DeleteRange(ctx, start, end)
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
