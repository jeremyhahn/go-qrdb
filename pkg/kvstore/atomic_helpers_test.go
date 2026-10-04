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

package kvstore

import (
	"context"
	"errors"
	"testing"
)

func TestCompareAndSwapWithCurrent_SuccessReportsNoCurrent(t *testing.T) {
	store := NewMemoryKVStore()
	ctx := context.Background()
	if err := store.Put(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	swapped, current, err := CompareAndSwapWithCurrent(ctx, store, "k", []byte("v1"), []byte("v2"))
	if err != nil {
		t.Fatalf("CompareAndSwapWithCurrent: %v", err)
	}
	if !swapped {
		t.Fatal("swapped = false, want true")
	}
	if current != nil {
		t.Errorf("current = %q, want nil on success", current)
	}

	got, err := store.Get(ctx, "k")
	if err != nil || string(got) != "v2" {
		t.Fatalf("Get after swap = (%q, %v), want (v2, nil)", got, err)
	}
}

// TestCompareAndSwapWithCurrent_MismatchReturnsCurrent is this helper's
// whole point: a caller retrying a contended counter (a PIN-lockout attempt
// count) learns the value it should have compared against in the same call
// that told it the compare failed.
func TestCompareAndSwapWithCurrent_MismatchReturnsCurrent(t *testing.T) {
	store := NewMemoryKVStore()
	ctx := context.Background()
	if err := store.Put(ctx, "k", []byte("actual")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	swapped, current, err := CompareAndSwapWithCurrent(ctx, store, "k", []byte("stale-expected"), []byte("next"))
	if err != nil {
		t.Fatalf("CompareAndSwapWithCurrent: %v", err)
	}
	if swapped {
		t.Fatal("swapped = true, want false (expected did not match)")
	}
	if string(current) != "actual" {
		t.Fatalf("current = %q, want %q", current, "actual")
	}

	// The store is unchanged: a failed compare never writes.
	got, err := store.Get(ctx, "k")
	if err != nil || string(got) != "actual" {
		t.Fatalf("Get after failed swap = (%q, %v), want (actual, nil)", got, err)
	}
}

// TestCompareAndSwapWithCurrent_AbsentKeyReportsNilCurrent verifies the
// absent case matches KVStore.Get's own "(nil, nil) means absent" contract
// rather than reporting an empty, non-nil slice.
func TestCompareAndSwapWithCurrent_AbsentKeyReportsNilCurrent(t *testing.T) {
	store := NewMemoryKVStore()
	ctx := context.Background()

	swapped, current, err := CompareAndSwapWithCurrent(ctx, store, "missing", []byte("expected"), []byte("next"))
	if err != nil {
		t.Fatalf("CompareAndSwapWithCurrent: %v", err)
	}
	if swapped {
		t.Fatal("swapped = true, want false (key is absent, not a swap target)")
	}
	if current != nil {
		t.Errorf("current = %q, want nil for an absent key", current)
	}
}

// fakeAtomicStore lets the failure paths be forced deterministically rather
// than relying on a real store ever actually failing one of these calls.
type fakeAtomicStore struct {
	AtomicKVStore
	compareAndSwapFn func(ctx context.Context, key string, expected, next []byte) (bool, error)
	getFn            func(ctx context.Context, key string) ([]byte, error)
}

func (f *fakeAtomicStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return f.compareAndSwapFn(ctx, key, expected, next)
}

func (f *fakeAtomicStore) Get(ctx context.Context, key string) ([]byte, error) {
	return f.getFn(ctx, key)
}

func TestCompareAndSwapWithCurrent_CompareAndSwapErrorPropagates(t *testing.T) {
	sentinel := errors.New("compare refused")
	store := &fakeAtomicStore{
		compareAndSwapFn: func(context.Context, string, []byte, []byte) (bool, error) { return false, sentinel },
		getFn: func(context.Context, string) ([]byte, error) {
			t.Fatal("Get must not be called when CompareAndSwap itself errored")
			return nil, nil
		},
	}

	swapped, current, err := CompareAndSwapWithCurrent(context.Background(), store, "k", []byte("a"), []byte("b"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	if swapped || current != nil {
		t.Fatalf("swapped=%v current=%q, want (false, nil) when CompareAndSwap errors", swapped, current)
	}
}

// TestCompareAndSwapWithCurrent_FollowUpGetErrorPropagates verifies a
// failure reading the current value back is returned rather than swallowed
// into a misleading (false, nil, nil).
func TestCompareAndSwapWithCurrent_FollowUpGetErrorPropagates(t *testing.T) {
	sentinel := errors.New("get refused")
	store := &fakeAtomicStore{
		compareAndSwapFn: func(context.Context, string, []byte, []byte) (bool, error) { return false, nil },
		getFn:            func(context.Context, string) ([]byte, error) { return nil, sentinel },
	}

	swapped, current, err := CompareAndSwapWithCurrent(context.Background(), store, "k", []byte("a"), []byte("b"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	if swapped || current != nil {
		t.Fatalf("swapped=%v current=%q, want (false, nil) when the follow-up Get errors", swapped, current)
	}
}
