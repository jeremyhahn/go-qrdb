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

package pool_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
)

// newTempStateStore opens a real Pebble at a temp directory and wraps
// it in a PebbleStateStore. The returned closer must be invoked at
// test end via t.Cleanup.
func newTempStateStore(t *testing.T) (*qrdbpool.PebbleStateStore, *pebblestore.PebbleStorage, string) {
	t.Helper()
	dir := t.TempDir()
	storage, err := pebblestore.New(&pebblestore.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("open Pebble: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	store, err := qrdbpool.NewPebbleStateStore(storage)
	if err != nil {
		t.Fatalf("NewPebbleStateStore: %v", err)
	}
	return store, storage, dir
}

func TestPebbleStateStore_New_NilStorage(t *testing.T) {
	_, err := qrdbpool.NewPebbleStateStore(nil)
	var ns *qrdbpool.NilStorageError
	if !errors.As(err, &ns) {
		t.Errorf("expected NilStorageError, got %v", err)
	}
}

func TestPebbleStateStore_GetMissingKey(t *testing.T) {
	store, _, _ := newTempStateStore(t)

	value, found, err := store.Get(context.Background(), "pool/default/round_robin/cursor")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Error("expected found=false on missing key")
	}
	if value != nil {
		t.Errorf("expected nil value on missing key, got %v", value)
	}
}

func TestPebbleStateStore_PutGetRoundTrip(t *testing.T) {
	store, _, _ := newTempStateStore(t)
	ctx := context.Background()
	key := "pool/default/round_robin/cursor"
	want := []byte{0, 0, 0, 0, 0, 0, 0, 42}

	if err := store.Put(ctx, key, want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, found, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("expected found=true after Put")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Get returned %v, want %v", got, want)
	}
}

func TestPebbleStateStore_PutOverwrites(t *testing.T) {
	store, _, _ := newTempStateStore(t)
	ctx := context.Background()
	key := "pool/default/round_robin/cursor"

	if err := store.Put(ctx, key, []byte{1}); err != nil {
		t.Fatalf("first Put: %v", err)
	}
	want := []byte{2, 3, 4}
	if err := store.Put(ctx, key, want); err != nil {
		t.Fatalf("second Put: %v", err)
	}

	got, found, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found {
		t.Fatal("expected found=true")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Get returned %v, want %v", got, want)
	}
}

// TestPebbleStateStore_PersistsAcrossRestart simulates a daemon
// restart by closing the underlying Pebble and re-opening it at the
// same path. The cursor written before close must survive.
func TestPebbleStateStore_PersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	key := "pool/default/round_robin/cursor"
	want := []byte{9, 8, 7, 6, 5, 4, 3, 2}

	// Boot 1: open, write, close.
	storage1, err := pebblestore.New(&pebblestore.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	store1, err := qrdbpool.NewPebbleStateStore(storage1)
	if err != nil {
		t.Fatalf("NewPebbleStateStore (first): %v", err)
	}
	if err := store1.Put(ctx, key, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := storage1.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}

	// Boot 2: re-open at same path, read.
	storage2, err := pebblestore.New(&pebblestore.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	t.Cleanup(func() { _ = storage2.Close() })
	store2, err := qrdbpool.NewPebbleStateStore(storage2)
	if err != nil {
		t.Fatalf("NewPebbleStateStore (second): %v", err)
	}
	got, found, err := store2.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if !found {
		t.Fatal("cursor lost across restart")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Get after restart = %v, want %v", got, want)
	}
}

// TestPebbleStateStore_KeyIsNamespaced verifies the qrdb pool prefix
// is applied at the storage layer. Reading the namespaced key
// directly via the underlying *PebbleStorage must succeed; reading
// the bare pool key must miss.
func TestPebbleStateStore_KeyIsNamespaced(t *testing.T) {
	store, raw, _ := newTempStateStore(t)
	ctx := context.Background()
	logicalKey := "pool/default/round_robin/cursor"
	value := []byte{1, 2, 3}

	if err := store.Put(ctx, logicalKey, value); err != nil {
		t.Fatalf("Put: %v", err)
	}

	bare, err := raw.Get([]byte(logicalKey))
	if err != nil {
		t.Fatalf("raw Get bare key: %v", err)
	}
	if bare != nil {
		t.Errorf("bare logical key resolved unexpectedly: %v", bare)
	}

	namespaced, err := raw.Get([]byte("__sys:volumepool/" + logicalKey))
	if err != nil {
		t.Fatalf("raw Get namespaced key: %v", err)
	}
	if !bytes.Equal(namespaced, value) {
		t.Errorf("namespaced key = %v, want %v", namespaced, value)
	}
}

func TestPebbleStateStore_GetEmptyKey(t *testing.T) {
	store, _, _ := newTempStateStore(t)

	_, _, err := store.Get(context.Background(), "")
	var ek *qrdbpool.EmptyKeyError
	if !errors.As(err, &ek) {
		t.Errorf("expected EmptyKeyError, got %v", err)
	}
}

func TestPebbleStateStore_PutEmptyKey(t *testing.T) {
	store, _, _ := newTempStateStore(t)

	err := store.Put(context.Background(), "", []byte{1})
	var ek *qrdbpool.EmptyKeyError
	if !errors.As(err, &ek) {
		t.Errorf("expected EmptyKeyError, got %v", err)
	}
}

func TestPebbleStateStore_GetCancelledContext(t *testing.T) {
	store, _, _ := newTempStateStore(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := store.Get(ctx, "pool/default/round_robin/cursor")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Get on cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestPebbleStateStore_PutCancelledContext(t *testing.T) {
	store, _, _ := newTempStateStore(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := store.Put(ctx, "pool/default/round_robin/cursor", []byte{1})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Put on cancelled ctx = %v, want context.Canceled", err)
	}
}

// TestPebbleStateStore_GetStorageError verifies that a Pebble-level
// failure during Get is wrapped in a GetError and propagated.
func TestPebbleStateStore_GetStorageError(t *testing.T) {
	store, raw, _ := newTempStateStore(t)

	// Close the underlying storage so that the next Pebble Get returns
	// a storage.ClosedError, which is the reliable way to inject a
	// storage-level failure without modifying production code.
	if err := raw.Close(); err != nil {
		t.Fatalf("Close storage: %v", err)
	}

	_, _, err := store.Get(context.Background(), "pool/default/round_robin/cursor")
	var ge *qrdbpool.GetError
	if !errors.As(err, &ge) {
		t.Fatalf("expected GetError, got %T: %v", err, err)
	}
	if ge.Key != "pool/default/round_robin/cursor" {
		t.Errorf("GetError.Key = %q, want %q", ge.Key, "pool/default/round_robin/cursor")
	}
	if ge.Unwrap() == nil {
		t.Error("GetError.Unwrap() must not be nil")
	}
}

// TestPebbleStateStore_PutStorageError verifies that a Pebble-level
// failure during Put is wrapped in a PutError and propagated.
func TestPebbleStateStore_PutStorageError(t *testing.T) {
	store, raw, _ := newTempStateStore(t)

	if err := raw.Close(); err != nil {
		t.Fatalf("Close storage: %v", err)
	}

	err := store.Put(context.Background(), "pool/default/round_robin/cursor", []byte{1})
	var pe *qrdbpool.PutError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PutError, got %T: %v", err, err)
	}
	if pe.Key != "pool/default/round_robin/cursor" {
		t.Errorf("PutError.Key = %q, want %q", pe.Key, "pool/default/round_robin/cursor")
	}
	if pe.Unwrap() == nil {
		t.Error("PutError.Unwrap() must not be nil")
	}
}

// TestErrorMessages verifies that all error types produce non-empty,
// well-formed strings and that Unwrap returns the wrapped cause.
func TestErrorMessages(t *testing.T) {
	t.Run("NilStorageError", func(t *testing.T) {
		e := &qrdbpool.NilStorageError{}
		msg := e.Error()
		if msg == "" {
			t.Error("NilStorageError.Error() must not be empty")
		}
	})

	t.Run("EmptyKeyError", func(t *testing.T) {
		e := &qrdbpool.EmptyKeyError{}
		msg := e.Error()
		if msg == "" {
			t.Error("EmptyKeyError.Error() must not be empty")
		}
	})

	t.Run("GetError", func(t *testing.T) {
		cause := errors.New("pebble: closed")
		e := &qrdbpool.GetError{Key: "somekey", Err: cause}
		msg := e.Error()
		if msg == "" {
			t.Error("GetError.Error() must not be empty")
		}
		if !errors.Is(e, cause) {
			t.Error("errors.Is(GetError, cause) must be true via Unwrap")
		}
	})

	t.Run("PutError", func(t *testing.T) {
		cause := errors.New("pebble: closed")
		e := &qrdbpool.PutError{Key: "somekey", Err: cause}
		msg := e.Error()
		if msg == "" {
			t.Error("PutError.Error() must not be empty")
		}
		if !errors.Is(e, cause) {
			t.Error("errors.Is(PutError, cause) must be true via Unwrap")
		}
	})
}

// TestPebbleStateStore_ConcurrentPutGet exercises concurrent Put/Get
// calls to catch races under the -race detector.
func TestPebbleStateStore_ConcurrentPutGet(t *testing.T) {
	store, _, _ := newTempStateStore(t)
	ctx := context.Background()
	const workers = 8
	done := make(chan struct{})
	for i := range workers {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			key := "pool/worker/cursor"
			val := []byte{byte(id)}
			if err := store.Put(ctx, key, val); err != nil {
				t.Errorf("worker %d Put: %v", id, err)
				return
			}
			_, _, err := store.Get(ctx, key)
			if err != nil {
				t.Errorf("worker %d Get: %v", id, err)
			}
		}(i)
	}
	for range workers {
		<-done
	}
}
