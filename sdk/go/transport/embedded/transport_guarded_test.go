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

package embedded

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The embedded transport implements the GuardedKVClient capability (go-qrdb#119)
// by delegating straight to the KV service's own guarded verbs. These tests
// drive the transport against a service that ENFORCES the store-once / guarded
// -delete contract (mockKVService is map-backed and applies the guard, not a
// fake that accepts what production refuses), and assert the transport surfaces
// the service's decision rather than emulating one client-side.

// The embedded transport must advertise the capability so a consumer's
// `client.(transport.GuardedKVClient)` assertion succeeds and go-quicbaas
// promotes its guarded surface over the embedded plane.
func TestEmbedded_ImplementsGuardedKVClient(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := interface{}(tr).(transport.GuardedKVClient); !ok {
		t.Fatal("embedded transport does not implement transport.GuardedKVClient")
	}
}

func TestEmbedded_PutIfAbsent_StoresOnce(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	stored, err := tr.PutIfAbsent(ctx, "claim", []byte("acme"))
	if err != nil {
		t.Fatalf("first PutIfAbsent: %v", err)
	}
	if !stored {
		t.Fatal("first PutIfAbsent should store when the key is absent")
	}

	// A second acquirer must be told it lost, through the return value, and must
	// not have overwritten the holder.
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

func TestEmbedded_CompareAndDelete_Guards(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if _, err := tr.PutIfAbsent(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("seed PutIfAbsent: %v", err)
	}

	// Wrong expected: the guard refuses, the key survives.
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

	// Matching expected: the guard permits, the key is removed.
	deleted, err = tr.CompareAndDelete(ctx, "k", []byte("v1"))
	if err != nil {
		t.Fatalf("CompareAndDelete(match): %v", err)
	}
	if !deleted {
		t.Error("CompareAndDelete with a matching expected must delete")
	}
	exists, err := tr.Exists(ctx, "k")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("key should be absent after a successful CompareAndDelete")
	}
}

func TestEmbedded_GuardedVerbs_ClosedTransport(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := context.Background()

	if _, err := tr.PutIfAbsent(ctx, "k", []byte("v")); err == nil {
		t.Error("PutIfAbsent on a closed transport should error")
	}
	if _, err := tr.CompareAndDelete(ctx, "k", []byte("v")); err == nil {
		t.Error("CompareAndDelete on a closed transport should error")
	}
}

// CompareAndSwap is the edit half of the guarded set. go-qrdb#119 gave the
// capability interface only the create and release halves, so a consumer that
// held it still had to read-then-write to EDIT a guarded value — reopening
// exactly the window the capability exists to close. go-qrdb#121 completes the
// set, and these tests drive the transport against a service that applies the
// guard for real.
func TestEmbedded_CompareAndSwap_Guards(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if _, err := tr.PutIfAbsent(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("seed PutIfAbsent: %v", err)
	}

	// A stale expectation must lose, and must leave the winner's value intact.
	// Without this half the test would pass against a swap that ignored its
	// precondition entirely.
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

// A swap is not a create. This is the property that keeps the three verbs
// distinct — PutIfAbsent creates, CompareAndSwap edits — and a transport that
// forwarded a swap to an unguarded Put would satisfy every other assertion
// here while silently creating the key.
func TestEmbedded_CompareAndSwap_DoesNotCreateAnAbsentKey(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
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
