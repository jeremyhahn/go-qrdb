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

package pebble

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// leakIterator opens an iterator on s and never closes it, so that the
// underlying pebble.DB.Close reports "leaked iterators" and fails.
func leakIterator(t *testing.T, s *PebbleStorage) {
	t.Helper()
	it := s.NewIterator(nil)
	if err := it.Error(); err != nil {
		t.Fatalf("NewIterator: %v", err)
	}
}

// ---------------------------------------------------------------------------
// pebble.go:NewIterator — closed-store branch
// ---------------------------------------------------------------------------

// TestNewIterator_AfterClose verifies a closed store yields an iterator that
// reports an error and is never valid.
func TestNewIterator_AfterClose(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	it := s.NewIterator(nil)
	defer it.Close()
	it.SeekToFirst()
	if it.Valid() {
		t.Fatal("iterator on closed store is valid, want invalid")
	}
	if it.Error() == nil {
		t.Fatal("iterator on closed store: Error() = nil, want non-nil")
	}
}

// TestNewIterator_AfterClose_Bounded verifies the closed-store branch is taken
// regardless of the iterator options supplied.
func TestNewIterator_AfterClose_Bounded(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	it := s.NewIterator(&storage.IteratorOptions{LowerBound: []byte("a"), UpperBound: []byte("z")})
	defer it.Close()
	if it.Error() == nil {
		t.Fatal("bounded iterator on closed store: Error() = nil, want non-nil")
	}
}

// ---------------------------------------------------------------------------
// pebble.go:Close — underlying pebble.DB.Close failure
// ---------------------------------------------------------------------------

// TestClose_LeakedIteratorReturnsOpError verifies a pebble close failure is
// wrapped in storage.OpError and that the store is still marked closed.
func TestClose_LeakedIteratorReturnsOpError(t *testing.T) {
	s := openTestStorage(t)
	leakIterator(t, s)

	err := s.Close()
	var opErr *storage.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("Close with leaked iterator: got %v, want *storage.OpError", err)
	}
	if opErr.Op != "close" {
		t.Fatalf("OpError.Op = %q, want %q", opErr.Op, "close")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: got %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// batch.go:NewIterator — store closed after the batch was created
// ---------------------------------------------------------------------------

// TestBatchNewIterator_StoreClosed verifies a batch iterator created after its
// store closed reports a BatchError wrapping ClosedError.
func TestBatchNewIterator_StoreClosed(t *testing.T) {
	s := openTestStorage(t)
	b := s.Batch()
	defer b.Close()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	it := b.NewIterator(nil)
	defer it.Close()
	err := it.Error()
	var batchErr *storage.BatchError
	if !errors.As(err, &batchErr) {
		t.Fatalf("batch iterator after store close: got %v, want *storage.BatchError", err)
	}
	if batchErr.Op != "iterate" {
		t.Fatalf("BatchError.Op = %q, want %q", batchErr.Op, "iterate")
	}
	var closed *storage.ClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("batch iterator after store close: got %v, want wrapped ClosedError", err)
	}
}

// TestBatchNewIterator_StoreClosed_NotValid verifies the store-closed batch
// iterator never positions on an entry.
func TestBatchNewIterator_StoreClosed_NotValid(t *testing.T) {
	s := openTestStorage(t)
	b := s.Batch()
	defer b.Close()
	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("batch Set: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	it := b.NewIterator(nil)
	defer it.Close()
	it.SeekToFirst()
	if it.Valid() {
		t.Fatal("batch iterator after store close is valid, want invalid")
	}
}

// ---------------------------------------------------------------------------
// registry.go:Open — opener failure
// ---------------------------------------------------------------------------

// TestPerTenantRegistry_Open_OpenerFailure verifies an opener error is wrapped
// in OpenError and leaves no entry behind.
func TestPerTenantRegistry_Open_OpenerFailure(t *testing.T) {
	cause := errors.New("opener refused")
	reg := NewPerTenantRegistry(WithRegistryOpener(func(string) (*PebbleStorage, error) {
		return nil, cause
	}))
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	dir := filepath.Join(t.TempDir(), "t")
	_, err := reg.Open(context.Background(), "t", dir)
	var openErr *OpenError
	if !errors.As(err, &openErr) {
		t.Fatalf("Open: got %v, want *OpenError", err)
	}
	if openErr.TenantID != "t" || openErr.Path != dir {
		t.Fatalf("OpenError = {%q, %q}, want {%q, %q}", openErr.TenantID, openErr.Path, "t", dir)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("Open: got %v, want to wrap %v", err, cause)
	}
	if got := reg.List(); len(got) != 0 {
		t.Fatalf("List after failed Open = %v, want empty", got)
	}
}

// TestPerTenantRegistry_Open_OpenerFailureRetry verifies a failed Open does
// not poison the tenant: a later Open through a working opener succeeds.
func TestPerTenantRegistry_Open_OpenerFailureRetry(t *testing.T) {
	fail := true
	reg := NewPerTenantRegistry(WithRegistryOpener(func(path string) (*PebbleStorage, error) {
		if fail {
			return nil, errors.New("transient")
		}
		return New(&Config{DataDir: path})
	}))
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	dir := filepath.Join(t.TempDir(), "t")
	if _, err := reg.Open(context.Background(), "t", dir); err == nil {
		t.Fatal("first Open: got nil, want error")
	}
	fail = false
	if _, err := reg.Open(context.Background(), "t", dir); err != nil {
		t.Fatalf("second Open: %v", err)
	}
}

// ---------------------------------------------------------------------------
// registry.go:Get — registered tenant
// ---------------------------------------------------------------------------

// TestPerTenantRegistry_Get_Registered verifies Get returns the same handle
// Open returned.
func TestPerTenantRegistry_Get_Registered(t *testing.T) {
	reg := newStubRegistry(t)
	opened, err := reg.Open(context.Background(), "t", filepath.Join(t.TempDir(), "t"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	got, err := reg.Get("t")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != opened {
		t.Fatal("Get returned a different handle than Open")
	}
}

// TestPerTenantRegistry_Get_AfterClose verifies Get reports NotRegisteredError
// once the tenant has been closed.
func TestPerTenantRegistry_Get_AfterClose(t *testing.T) {
	reg := newStubRegistry(t)
	if _, err := reg.Open(context.Background(), "t", filepath.Join(t.TempDir(), "t")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := reg.Close(context.Background(), "t"); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := reg.Get("t")
	var nr *NotRegisteredError
	if !errors.As(err, &nr) {
		t.Fatalf("Get after Close: got %v, want *NotRegisteredError", err)
	}
}

// ---------------------------------------------------------------------------
// registry.go:Close and CloseAll — storage close failure
// ---------------------------------------------------------------------------

// TestPerTenantRegistry_Close_StorageCloseFailure verifies a storage close
// failure is returned as CloseError and the entry is still removed.
func TestPerTenantRegistry_Close_StorageCloseFailure(t *testing.T) {
	reg := newStubRegistry(t)
	dir := filepath.Join(t.TempDir(), "t")
	s, err := reg.Open(context.Background(), "t", dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	leakIterator(t, s)

	err = reg.Close(context.Background(), "t")
	var closeErr *CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("Close: got %v, want *CloseError", err)
	}
	if closeErr.TenantID != "t" || closeErr.Path != dir {
		t.Fatalf("CloseError = {%q, %q}, want {%q, %q}", closeErr.TenantID, closeErr.Path, "t", dir)
	}
	var opErr *storage.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("Close: got %v, want wrapped *storage.OpError", err)
	}
	if got := reg.List(); len(got) != 0 {
		t.Fatalf("List after failed Close = %v, want empty", got)
	}
}

// TestPerTenantRegistry_CloseAll_StorageCloseFailure verifies CloseAll closes
// every tenant, returns a CloseError for the failing one, and marks the
// registry closed.
func TestPerTenantRegistry_CloseAll_StorageCloseFailure(t *testing.T) {
	reg := NewPerTenantRegistry()
	badDir := filepath.Join(t.TempDir(), "bad")
	bad, err := reg.Open(context.Background(), "bad", badDir)
	if err != nil {
		t.Fatalf("Open(bad): %v", err)
	}
	good, err := reg.Open(context.Background(), "good", filepath.Join(t.TempDir(), "good"))
	if err != nil {
		t.Fatalf("Open(good): %v", err)
	}
	leakIterator(t, bad)

	err = reg.CloseAll(context.Background())
	var closeErr *CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("CloseAll: got %v, want *CloseError", err)
	}
	if closeErr.TenantID != "bad" || closeErr.Path != badDir {
		t.Fatalf("CloseError = {%q, %q}, want {%q, %q}", closeErr.TenantID, closeErr.Path, "bad", badDir)
	}
	if _, err := good.Get([]byte("k")); err == nil {
		t.Fatal("good tenant still open after CloseAll")
	}
	if err := reg.CloseAll(context.Background()); err != nil {
		t.Fatalf("second CloseAll: got %v, want nil", err)
	}
}
