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

package unix

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The unix transport carries the guarded verbs over a domain socket
// (go-qrdb#125): the client method calls the server RPC, pkg/api/unix's server
// hands it to the same pkg/api/grpc service implementation the TCP transport
// uses, and that delegates to the KV service's own atomic primitive. The
// decision is made there and never emulated on the client.
//
// These tests drive the full client -> socket -> server -> service path against
// a mockKVService that ENFORCES the guard, so a store-once or refused-release
// result that survives the round trip is the service's decision rather than the
// transport's. The end-to-end delegation against a real node, on every engine
// and mode, is the go-qrdb#121 conformance matrix.

func TestUnixGuarded_ImplementsGuardedKVClient(t *testing.T) {
	tr, cleanup := startServer(t, newMockKVService())
	defer cleanup()
	if _, ok := interface{}(tr).(transport.GuardedKVClient); !ok {
		t.Fatal("unix transport does not implement transport.GuardedKVClient")
	}
}

// guardedBinaryValue is deliberately not valid UTF-8, so a transport that
// round-tripped values through a string somewhere would corrupt it and the
// guard would be comparing a different value than the caller passed.
var guardedBinaryValue = []byte{0x00, 0xff, 0xfe, 'a', 0x80, '\n', 0x7f}

func TestUnixGuarded_PutIfAbsent_StoresOnce(t *testing.T) {
	tr, cleanup := startServer(t, newMockKVService())
	defer cleanup()
	ctx := context.Background()

	stored, err := tr.PutIfAbsent(ctx, "claim", guardedBinaryValue)
	if err != nil {
		t.Fatalf("first PutIfAbsent: %v", err)
	}
	if !stored {
		t.Fatal("PutIfAbsent must store when the key is absent")
	}

	// The second call is what makes this a test of the GUARD rather than of
	// the wiring: an implementation that ignored its precondition would
	// satisfy the first assertion and still be useless, because "did I win the
	// claim?" is the entire question the caller is asking.
	stored, err = tr.PutIfAbsent(ctx, "claim", []byte("globex"))
	if err != nil {
		t.Fatalf("second PutIfAbsent: %v", err)
	}
	if stored {
		t.Error("PutIfAbsent must decline when the key is present")
	}
	got, err := tr.Get(ctx, "claim")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(guardedBinaryValue) {
		t.Errorf("the losing acquirer overwrote the winner: %v, want %v", got, guardedBinaryValue)
	}
}

func TestUnixGuarded_CompareAndDelete_Guards(t *testing.T) {
	tr, cleanup := startServer(t, newMockKVService())
	defer cleanup()
	ctx := context.Background()

	if _, err := tr.PutIfAbsent(ctx, "k", guardedBinaryValue); err != nil {
		t.Fatalf("seed PutIfAbsent: %v", err)
	}

	deleted, err := tr.CompareAndDelete(ctx, "k", []byte("WRONG"))
	if err != nil {
		t.Fatalf("CompareAndDelete(stale): %v", err)
	}
	if deleted {
		t.Error("CompareAndDelete must refuse a stale expected value")
	}
	if got, err := tr.Get(ctx, "k"); err != nil || string(got) != string(guardedBinaryValue) {
		t.Errorf("a refused CompareAndDelete removed or changed the key: %v err=%v", got, err)
	}

	deleted, err = tr.CompareAndDelete(ctx, "k", guardedBinaryValue)
	if err != nil {
		t.Fatalf("CompareAndDelete(match): %v", err)
	}
	if !deleted {
		t.Error("CompareAndDelete must delete when the expected value matches")
	}
	if exists, err := tr.Exists(ctx, "k"); err != nil || exists {
		t.Errorf("key still present after a successful CompareAndDelete (exists=%v err=%v)", exists, err)
	}
}

func TestUnixGuarded_CompareAndSwap_Guards(t *testing.T) {
	tr, cleanup := startServer(t, newMockKVService())
	defer cleanup()
	ctx := context.Background()

	if _, err := tr.PutIfAbsent(ctx, "k", guardedBinaryValue); err != nil {
		t.Fatalf("seed PutIfAbsent: %v", err)
	}

	next := []byte{0x01, 0x02, 0xc3, 0x28}

	swapped, err := tr.CompareAndSwap(ctx, "k", []byte("WRONG"), next)
	if err != nil {
		t.Fatalf("CompareAndSwap(stale): %v", err)
	}
	if swapped {
		t.Error("CompareAndSwap must refuse a stale expected value")
	}
	if got, err := tr.Get(ctx, "k"); err != nil || string(got) != string(guardedBinaryValue) {
		t.Errorf("a refused swap changed the value: %v err=%v", got, err)
	}

	// This also pins the field order. `expected` and `next` are two byte
	// fields of the same request, and sending them the wrong way round makes
	// the guard compare against the replacement and write the stale value —
	// which still returns a plausible boolean.
	swapped, err = tr.CompareAndSwap(ctx, "k", guardedBinaryValue, next)
	if err != nil {
		t.Fatalf("CompareAndSwap(match): %v", err)
	}
	if !swapped {
		t.Error("CompareAndSwap must swap when the expected value matches")
	}
	if got, err := tr.Get(ctx, "k"); err != nil || string(got) != string(next) {
		t.Errorf("after a successful swap the value is %v err=%v, want %v", got, err, next)
	}
}

// A swap is not a create. This is what keeps the three verbs distinct, and an
// implementation that forwarded a swap to an unguarded Put would satisfy every
// assertion above while silently creating keys.
func TestUnixGuarded_CompareAndSwap_DoesNotCreateAnAbsentKey(t *testing.T) {
	tr, cleanup := startServer(t, newMockKVService())
	defer cleanup()
	ctx := context.Background()

	swapped, err := tr.CompareAndSwap(ctx, "absent", nil, []byte("created"))
	if err != nil {
		t.Fatalf("CompareAndSwap(absent): %v", err)
	}
	if swapped {
		t.Error("CompareAndSwap reported a swap on an absent key")
	}
	if exists, err := tr.Exists(ctx, "absent"); err != nil || exists {
		t.Errorf("CompareAndSwap CREATED an absent key (exists=%v err=%v)", exists, err)
	}
}

// A guarded call on a closed transport must fail before touching the socket.
func TestUnixGuarded_ClosedTransport(t *testing.T) {
	tr, cleanup := startServer(t, newMockKVService())
	defer cleanup()
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx := context.Background()
	var closed *transport.ClosedError
	if _, err := tr.PutIfAbsent(ctx, "k", nil); !errors.As(err, &closed) {
		t.Errorf("PutIfAbsent error = %T (%v), want *transport.ClosedError", err, err)
	}
	if _, err := tr.CompareAndDelete(ctx, "k", nil); !errors.As(err, &closed) {
		t.Errorf("CompareAndDelete error = %T (%v), want *transport.ClosedError", err, err)
	}
	if _, err := tr.CompareAndSwap(ctx, "k", nil, nil); !errors.As(err, &closed) {
		t.Errorf("CompareAndSwap error = %T (%v), want *transport.ClosedError", err, err)
	}
}
