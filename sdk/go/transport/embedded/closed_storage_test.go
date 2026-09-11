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

package embedded_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	pebblestg "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// newTransportOverClosedStore builds the in-process stack a composer wires for
// an embedded node — PebbleDB storage, MVCC engine, LocalBackend, KV service,
// embedded transport — and then closes the storage under it, which is what a
// daemon shutdown does to a goroutine still persisting state (go-qrdb#371).
func newTransportOverClosedStore(t *testing.T) *embedded.Transport {
	t.Helper()

	stg, err := pebblestg.New(&pebblestg.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("pebble.New: %v", err)
	}
	eng, err := engine.NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(eng)
	tr, err := embedded.New(
		service.NewKVService(host, 1),
		service.NewAdminService(host),
	)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}

	if err := tr.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put before close: %v", err)
	}
	if err := stg.Close(); err != nil {
		t.Fatalf("storage Close: %v", err)
	}
	return tr
}

// callOutcome runs fn and reports a panic as an error, so a stack that panics
// fails the assertion instead of taking the test binary down with it.
func callOutcome(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = &panicEscapedError{Err: e}
				return
			}
			err = &panicEscapedError{Err: errors.New("non-error panic")}
		}
	}()
	return fn()
}

// panicEscapedError reports a panic that reached the SDK boundary.
type panicEscapedError struct{ Err error }

// Error returns a human-readable description of the escaped panic.
func (e *panicEscapedError) Error() string {
	return "a panic escaped the SDK transport: " + e.Err.Error()
}

// Unwrap returns the recovered value for errors.Is and errors.As support.
func (e *panicEscapedError) Unwrap() error { return e.Err }

// TestEmbeddedKVOnClosedStoreIsMatchable is the consumer-facing assertion of
// go-qrdb#371: a KV operation issued after the node closed its store returns
// an error that errors.Is matches against the exported sentinel, with no panic
// to recover and no message to compare.
func TestEmbeddedKVOnClosedStoreIsMatchable(t *testing.T) {
	tr := newTransportOverClosedStore(t)
	ctx := context.Background()

	operations := map[string]func() error{
		"Put":    func() error { return tr.Put(ctx, "k", []byte("v")) },
		"Delete": func() error { return tr.Delete(ctx, "k") },
		"PutIfAbsent": func() error {
			_, err := tr.PutIfAbsent(ctx, "k", []byte("v"))
			return err
		},
		"CompareAndDelete": func() error {
			_, err := tr.CompareAndDelete(ctx, "k", []byte("v"))
			return err
		},
	}
	for name, call := range operations {
		t.Run(name, func(t *testing.T) {
			err := callOutcome(call)
			if err == nil {
				t.Fatalf("%s on a closed store returned nil, want the closed-store error", name)
			}
			var escaped *panicEscapedError
			if errors.As(err, &escaped) {
				t.Fatalf("%s on a closed store: %v", name, err)
			}
			if !errors.Is(err, sdk.ErrStorageClosed) {
				t.Fatalf("%s: errors.Is(%v, sdk.ErrStorageClosed) = false, want true", name, err)
			}
			if !sdk.IsStorageClosed(err) {
				t.Fatalf("%s: sdk.IsStorageClosed(%v) = false, want true", name, err)
			}
		})
	}
}

// TestEmbeddedKVOnClosedStoreCarriesTheStorageType asserts the concrete type
// survives the embedded seam too, so a caller that prefers errors.As over the
// sentinel is served as well.
func TestEmbeddedKVOnClosedStoreCarriesTheStorageType(t *testing.T) {
	tr := newTransportOverClosedStore(t)

	err := callOutcome(func() error { return tr.Put(context.Background(), "k", []byte("v")) })
	var closed *sdk.StorageClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("errors.As(%v, **sdk.StorageClosedError) = false, want true", err)
	}
}
