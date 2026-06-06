// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import "context"

// failingBackend wraps an in-memory StorageBackend and returns a configured
// error from any operation on demand. It is a shared test fixture used across
// the seal test suite to exercise storage-failure paths.
type failingBackend struct {
	inner     StorageBackend
	getErr    error
	putErr    error
	deleteErr error
	listErr   error
	existErr  error
	scanErr   error
}

func newFailingBackend() *failingBackend {
	return &failingBackend{inner: NewMemoryBackend()}
}

func (f *failingBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.inner.Get(ctx, key)
}

func (f *failingBackend) Put(ctx context.Context, key string, data []byte) error {
	if f.putErr != nil {
		return f.putErr
	}
	return f.inner.Put(ctx, key, data)
}

func (f *failingBackend) Delete(ctx context.Context, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.inner.Delete(ctx, key)
}

func (f *failingBackend) List(ctx context.Context, prefix string) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.inner.List(ctx, prefix)
}

func (f *failingBackend) Exists(ctx context.Context, key string) (bool, error) {
	if f.existErr != nil {
		return false, f.existErr
	}
	return f.inner.Exists(ctx, key)
}

func (f *failingBackend) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	if f.scanErr != nil {
		return f.scanErr
	}
	return f.inner.Scan(ctx, prefix, fn)
}

func (f *failingBackend) Close() error {
	return f.inner.Close()
}
