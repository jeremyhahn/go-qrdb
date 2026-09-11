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

package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebblestg "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// newClosedStorageEngine builds the engine for backendKind over a PebbleDB
// store, applies one write to prove the engine is live, then closes the store
// underneath it — which is what a node teardown does to a job goroutine that
// is still writing (go-qrdb#371).
func newClosedStorageEngine(t *testing.T, mvcc bool) *backend.LocalBackend {
	t.Helper()

	stg, err := pebblestg.New(&pebblestg.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("pebble.New: %v", err)
	}

	var eng engine.Engine
	if mvcc {
		eng, err = engine.NewMVCCEngine(stg)
		if err != nil {
			t.Fatalf("NewMVCCEngine: %v", err)
		}
	} else {
		eng = engine.NewKVEngine(stg)
	}

	lb := backend.NewLocalBackend(eng)
	cmd := encodePut(t, "k", "v")
	if _, err := lb.SyncPropose(context.Background(), 1, cmd); err != nil {
		t.Fatalf("propose before close: %v", err)
	}

	if err := stg.Close(); err != nil {
		t.Fatalf("storage Close: %v", err)
	}
	return lb
}

// encodePut builds the KV wire command a KVService.Put proposes.
func encodePut(t *testing.T, key, value string) []byte {
	t.Helper()
	cmd, err := kv.EncodePut([]byte(key), []byte(value))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	return cmd
}

// proposeOutcome runs a propose and converts a panic into an error, so an
// engine that panics fails the assertion instead of killing the test binary.
func proposeOutcome(lb *backend.LocalBackend, cmd []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = &escapedPanicError{Err: e}
				return
			}
			err = &escapedPanicError{Err: errors.New("non-error panic")}
		}
	}()
	_, err = lb.SyncPropose(context.Background(), 1, cmd)
	return err
}

// escapedPanicError reports a panic that reached the propose boundary.
type escapedPanicError struct{ Err error }

// Error returns a human-readable description of the escaped panic.
func (e *escapedPanicError) Error() string { return "a panic escaped the engine: " + e.Err.Error() }

// Unwrap returns the recovered value for errors.Is and errors.As support.
func (e *escapedPanicError) Unwrap() error { return e.Err }

// TestMVCCEngineProposeOnClosedStorageReturnsTypedError walks the exact path
// go-qrdb#371 reports for the pebble backend: KVService.Put encodes a KV
// command, LocalBackend proposes it, and MVCCEngine.Update opens a storage
// batch and reads through it. On dev/main the read panicked `pebble: closed`
// out of pebbleBatch.Get.
func TestMVCCEngineProposeOnClosedStorageReturnsTypedError(t *testing.T) {
	lb := newClosedStorageEngine(t, true)

	err := proposeOutcome(lb, encodePut(t, "k", "v"))
	if err == nil {
		t.Fatal("propose on a closed store returned nil, want ErrStorageClosed")
	}
	var escaped *escapedPanicError
	if errors.As(err, &escaped) {
		t.Fatalf("propose on a closed store: %v", err)
	}
	if !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("errors.Is(%v, storage.ErrStorageClosed) = false, want true", err)
	}
}

// TestKVEngineProposeOnClosedStorageReturnsTypedError is the same assertion
// for the plain KV engine, whose put commits a storage batch directly. On
// dev/main it panicked out of pebbleBatch.Commit — a different frame from the
// MVCC one, which is why a caller matching a single message text is fragile.
func TestKVEngineProposeOnClosedStorageReturnsTypedError(t *testing.T) {
	lb := newClosedStorageEngine(t, false)

	err := proposeOutcome(lb, encodePut(t, "k", "v"))
	if err == nil {
		t.Fatal("propose on a closed store returned nil, want ErrStorageClosed")
	}
	var escaped *escapedPanicError
	if errors.As(err, &escaped) {
		t.Fatalf("propose on a closed store: %v", err)
	}
	if !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("errors.Is(%v, storage.ErrStorageClosed) = false, want true", err)
	}
}

// TestClosedLocalBackendReportsTheSameCondition asserts the other half of the
// shutdown race: when the backend is closed first, its refusal matches the
// same sentinel as the storage engine's. A caller must not have to know which
// of the two lost the race.
func TestClosedLocalBackendReportsTheSameCondition(t *testing.T) {
	stg, err := pebblestg.New(&pebblestg.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("pebble.New: %v", err)
	}
	t.Cleanup(func() { _ = stg.Close() })

	lb := backend.NewLocalBackend(engine.NewKVEngine(stg))
	if err := lb.Close(); err != nil {
		t.Fatalf("backend Close: %v", err)
	}

	proposeErr := proposeOutcome(lb, encodePut(t, "k", "v"))
	if !errors.Is(proposeErr, storage.ErrStorageClosed) {
		t.Errorf("SyncPropose after backend Close = %v, want ErrStorageClosed", proposeErr)
	}

	_, readErr := lb.SyncRead(context.Background(), 1, []byte("k"))
	if !errors.Is(readErr, storage.ErrStorageClosed) {
		t.Errorf("SyncRead after backend Close = %v, want ErrStorageClosed", readErr)
	}

	var backendClosed *backend.BackendClosedError
	if !errors.As(proposeErr, &backendClosed) {
		t.Errorf("SyncPropose after backend Close = %v, want a *backend.BackendClosedError", proposeErr)
	}
}
