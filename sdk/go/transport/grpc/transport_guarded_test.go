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

package grpc

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The gRPC transport carries the guarded verbs over the wire (go-qrdb#119/#123):
// the client method calls the server RPC, the server handler delegates to the
// KV service's own atomic primitive, and the store-once / guarded-delete
// decision is made there — never emulated on the client. These tests drive the
// full client→wire→server→service path against a service that ENFORCES the
// contract (mockKVService is map-backed and applies the guard), so a store-once
// result that survives the round trip is the service's decision, not the
// transport's.

func TestTransport_ImplementsGuardedKVClient(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()
	if _, ok := interface{}(tr).(transport.GuardedKVClient); !ok {
		t.Fatal("gRPC transport does not implement transport.GuardedKVClient")
	}
}

func TestTransport_PutIfAbsent_StoresOnce(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()
	ctx := context.Background()

	stored, err := tr.PutIfAbsent(ctx, "claim", []byte("acme"))
	if err != nil {
		t.Fatalf("first PutIfAbsent: %v", err)
	}
	if !stored {
		t.Fatal("first PutIfAbsent should store when the key is absent")
	}

	stored, err = tr.PutIfAbsent(ctx, "claim", []byte("globex"))
	if err != nil {
		t.Fatalf("second PutIfAbsent: %v", err)
	}
	if stored {
		t.Error("second PutIfAbsent should decline when the key is present")
	}
	got, err := tr.Get(ctx, "claim")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "acme" {
		t.Errorf("holder value = %q, want %q", got, "acme")
	}
}

func TestTransport_CompareAndDelete_Guards(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()
	ctx := context.Background()

	if _, err := tr.PutIfAbsent(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("seed PutIfAbsent: %v", err)
	}

	deleted, err := tr.CompareAndDelete(ctx, "k", []byte("WRONG"))
	if err != nil {
		t.Fatalf("CompareAndDelete(wrong): %v", err)
	}
	if deleted {
		t.Error("CompareAndDelete with a non-matching expected must not delete")
	}
	if got, err := tr.Get(ctx, "k"); err != nil || string(got) != "v1" {
		t.Errorf("after a refused CompareAndDelete, key = %q err=%v; want %q present", got, err, "v1")
	}

	deleted, err = tr.CompareAndDelete(ctx, "k", []byte("v1"))
	if err != nil {
		t.Fatalf("CompareAndDelete(match): %v", err)
	}
	if !deleted {
		t.Error("CompareAndDelete with a matching expected must delete")
	}
	if exists, err := tr.Exists(ctx, "k"); err != nil || exists {
		t.Errorf("key should be absent after a successful CompareAndDelete (exists=%v err=%v)", exists, err)
	}
}

// CompareAndSwap over the wire (go-qrdb#121): the client method calls the
// server RPC, the handler delegates to the KV service's guarded swap, and the
// comparison happens there. Emulating it client-side with a Get and a Put
// would reopen the window the verb closes, and would do so across a network
// round trip — so these assertions are about a decision made on the far side.
func TestTransport_CompareAndSwap_Guards(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
	defer cleanup()
	ctx := context.Background()

	if _, err := tr.PutIfAbsent(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("seed PutIfAbsent: %v", err)
	}

	swapped, err := tr.CompareAndSwap(ctx, "k", []byte("WRONG"), []byte("v2"))
	if err != nil {
		t.Fatalf("CompareAndSwap(wrong): %v", err)
	}
	if swapped {
		t.Error("CompareAndSwap with a non-matching expected must not swap")
	}
	if got, err := tr.Get(ctx, "k"); err != nil || string(got) != "v1" {
		t.Errorf("after a refused swap, key = %q err=%v; want %q untouched", got, err, "v1")
	}

	swapped, err = tr.CompareAndSwap(ctx, "k", []byte("v1"), []byte("v2"))
	if err != nil {
		t.Fatalf("CompareAndSwap(match): %v", err)
	}
	if !swapped {
		t.Error("CompareAndSwap with a matching expected must swap")
	}
	if got, err := tr.Get(ctx, "k"); err != nil || string(got) != "v2" {
		t.Errorf("after a successful swap, key = %q err=%v; want %q", got, err, "v2")
	}
}

// The two byte fields of CompareAndSwapRequest are `expected` and `next`, and
// nothing but a value assertion catches them being wired in the wrong order —
// swapping them makes the guard compare against the replacement and write the
// stale value, which still returns a plausible boolean.
func TestTransport_CompareAndSwap_DoesNotCreateAnAbsentKey(t *testing.T) {
	tr, cleanup := connectTransport(t, newMockKVService(), newMockAdminService())
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
		t.Errorf("CompareAndSwap CREATED an absent key (exists=%v err=%v); a swap must not create", exists, err)
	}
}
