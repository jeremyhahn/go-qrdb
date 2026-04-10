// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"sort"
	"strings"
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
}

// Compile-time interface check.
var _ StorageBackend = (*StorageAdapter)(nil)

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
	return a.inner.Set([]byte(key), data)
}

// Delete removes the key from storage.
func (a *StorageAdapter) Delete(_ context.Context, key string) error {
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
