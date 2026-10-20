// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// LegacyStorage is the interface for storage backends that use []byte keys
// and do not accept context.Context. This matches the legacy storage.Storage
// interface from go-qrdb's storage package.
type LegacyStorage interface {
	Get(key []byte) ([]byte, error)
	Set(key []byte, value []byte) error
	Delete(key []byte) error
	List(prefix []byte) ([][]byte, error)
	Exists(key []byte) (bool, error)
	Close() error
}

// StorageAdapter wraps a LegacyStorage ([]byte keys, no ctx) as a
// StorageBackend (string keys + context.Context + Scan). This adapter
// enables go-qrdb's existing storage implementations to be used with
// the updated barrier interface without modifying the storage layer.
type StorageAdapter struct {
	inner LegacyStorage

	// writeMu serializes the Exists+Set performed by PutIfAbsent so the
	// check-then-write is atomic. LegacyStorage implementations are
	// single-process local engines, so this mutex is the genuine atomicity
	// mechanism — there is no other process to race with.
	writeMu sync.Mutex
}

// Compile-time interface check.
var (
	_ StorageBackend        = (*StorageAdapter)(nil)
	_ kvstore.FencedCreator = (*StorageAdapter)(nil)
	_ guardedBatchBackend   = (*StorageAdapter)(nil)
)

// batchingStorage is the capability PutAllIfAllUnchanged needs from the
// wrapped engine: a batch that commits several writes in one step. Every
// storage.Storage has it; a LegacyStorage need not.
type batchingStorage interface {
	Batch() storage.Batch
}

// NewStorageAdapter creates a StorageBackend adapter from a LegacyStorage.
func NewStorageAdapter(s LegacyStorage) StorageBackend {
	return &StorageAdapter{inner: s}
}

// Get retrieves the value for the given key.
func (a *StorageAdapter) Get(_ context.Context, key string) ([]byte, error) {
	return a.inner.Get([]byte(key))
}

// Put stores the value under the given key.
func (a *StorageAdapter) Put(_ context.Context, key string, data []byte) error {
	// Takes writeMu so it cannot land BETWEEN a guarded operation's read and
	// its write. Without this the guards are advisory: PutIfAbsent could
	// overwrite a value written after its Exists check, and CompareAndDelete
	// could delete a replacement it never compared against — a false
	// atomicity guarantee handed to every StorageBarrier over this adapter.
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.inner.Set([]byte(key), data)
}

// PutIfAbsent atomically stores the value under the given key only if the key
// does not already exist. The check-then-write is serialized under
// putIfAbsentMu, which provides genuine atomicity for the single-process
// legacy engines this adapter wraps.
func (a *StorageAdapter) PutIfAbsent(_ context.Context, key string, data []byte) (bool, error) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()

	exists, err := a.inner.Exists([]byte(key))
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if err := a.inner.Set([]byte(key), data); err != nil {
		return false, err
	}
	return true, nil
}

// PutIfAllAbsent stores the value under key only when key and every fence key
// are absent, under the same mutex that makes PutIfAbsent indivisible. See
// kvstore.FencedCreator.
func (a *StorageAdapter) PutIfAllAbsent(_ context.Context, key string, data []byte, fences ...string) (bool, error) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()

	for _, k := range append([]string{key}, fences...) {
		exists, err := a.inner.Exists([]byte(k))
		if err != nil {
			return false, err
		}
		if exists {
			return false, nil
		}
	}
	if err := a.inner.Set([]byte(key), data); err != nil {
		return false, err
	}
	return true, nil
}

// CompareAndDelete removes key only when its stored value still equals
// expected, under the same mutex that makes PutIfAbsent indivisible.
func (a *StorageAdapter) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()

	current, err := a.inner.Get([]byte(key))
	if err != nil {
		return false, err
	}
	if current == nil || !bytes.Equal(current, expected) {
		return false, nil
	}
	if err := a.inner.Delete([]byte(key)); err != nil {
		return false, err
	}
	return true, nil
}

// CompareAndSwap replaces the key's value only when it still equals expected,
// under the same write mutex the guarded delete uses — that lock is what makes
// the read-then-write atomic for this adapter. An absent key is left absent
// (go-qrdb#56).
func (a *StorageAdapter) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()

	current, err := a.inner.Get([]byte(key))
	if err != nil {
		return false, err
	}
	if current == nil || !bytes.Equal(current, expected) {
		return false, nil
	}
	if err := a.inner.Set([]byte(key), next); err != nil {
		return false, err
	}
	return true, nil
}

// PutAllIfAllUnchanged applies every write in one committed batch, and only
// while every guard holds against the bytes stored now: a nil Guard.Value
// requires the key to be absent, any other value requires it to be present
// and equal. The guards are read and the batch committed under writeMu, the
// same mutex every other write here takes, so no write lands in between. An
// engine without batches is refused with ErrGuardedBatchUnsupported: applying
// the writes one at a time could leave a prefix of them behind.
func (a *StorageAdapter) PutAllIfAllUnchanged(_ context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	batcher, ok := a.inner.(batchingStorage)
	if !ok {
		return false, &kvstore.GuardedBatchUnsupportedError{Err: ErrGuardedBatchUnsupported}
	}

	a.writeMu.Lock()
	defer a.writeMu.Unlock()

	for _, g := range guards {
		current, err := a.inner.Get([]byte(g.Key))
		if err != nil {
			return false, err
		}
		if !guardHolds(g.Value, current) {
			return false, nil
		}
	}
	if len(writes) == 0 {
		return true, nil
	}
	// Closing an uncommitted batch discards it, so an early return below
	// leaves the store untouched.
	batch := batcher.Batch()
	defer func() { _ = batch.Close() }()
	for _, w := range writes {
		var err error
		if w.Delete {
			err = batch.Delete([]byte(w.Key))
		} else {
			err = batch.Set([]byte(w.Key), w.Value)
		}
		if err != nil {
			return false, err
		}
	}
	if err := batch.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// guardHolds reports whether a stored value satisfies a guard's expectation:
// absent when expected is nil, otherwise present and byte-equal. A present
// key holding an empty value is not absent, and an absent key never equals a
// non-nil empty expectation.
func guardHolds(expected, current []byte) bool {
	if expected == nil {
		return current == nil
	}
	return current != nil && bytes.Equal(current, expected)
}

// Delete removes the key from storage.
func (a *StorageAdapter) Delete(_ context.Context, key string) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.inner.Delete([]byte(key))
}

// List returns all keys matching the given prefix.
func (a *StorageAdapter) List(_ context.Context, prefix string) ([]string, error) {
	keys, err := a.inner.List([]byte(prefix))
	if err != nil {
		return nil, err
	}
	result := make([]string, len(keys))
	for i, k := range keys {
		result[i] = string(k)
	}
	sort.Strings(result)
	return result, nil
}

// Scan iterates over all key-value pairs matching the given prefix, invoking
// fn for each entry. This is implemented by listing keys and fetching each
// value individually.
func (a *StorageAdapter) Scan(_ context.Context, prefix string, fn func(key string, value []byte) error) error {
	keys, err := a.inner.List([]byte(prefix))
	if err != nil {
		return err
	}
	for _, k := range keys {
		key := string(k)
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		val, getErr := a.inner.Get(k)
		if getErr != nil {
			return getErr
		}
		if err := fn(key, val); err != nil {
			return err
		}
	}
	return nil
}

// Exists reports whether the given key exists in storage.
func (a *StorageAdapter) Exists(_ context.Context, key string) (bool, error) {
	return a.inner.Exists([]byte(key))
}

// Close releases resources held by the underlying storage.
func (a *StorageAdapter) Close() error {
	return a.inner.Close()
}
