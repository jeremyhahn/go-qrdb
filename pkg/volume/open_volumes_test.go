// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"errors"
	"testing"
)

func openTestVolume(t *testing.T, pool string) Volume {
	t.Helper()
	v, err := NewDirProvider().Open(context.Background(), Spec{Pool: pool, Path: t.TempDir()})
	if err != nil {
		t.Fatalf("Open(%s): %v", pool, err)
	}
	return v
}

func TestOpenVolumes_AddGetRoot(t *testing.T) {
	ov := NewOpenVolumes("node-1")
	v := openTestVolume(t, "fast")
	if err := ov.Add("fast", v); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, ok := ov.Get("fast")
	if !ok || got != v {
		t.Fatalf("Get(fast) = (%v, %v), want (%v, true)", got, ok, v)
	}
	root, err := ov.Root(context.Background(), "fast")
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if root != v.Root() {
		t.Fatalf("Root() = %q, want %q", root, v.Root())
	}
}

func TestOpenVolumes_AddInvalid(t *testing.T) {
	ov := NewOpenVolumes("node-1")
	if err := ov.Add("", openTestVolume(t, "fast")); err == nil {
		t.Fatal("Add(empty pool) should fail")
	}
	if err := ov.Add("fast", nil); err == nil {
		t.Fatal("Add(nil volume) should fail")
	}
}

func TestOpenVolumes_AddDuplicateRefused(t *testing.T) {
	ov := NewOpenVolumes("node-1")
	if err := ov.Add("fast", openTestVolume(t, "fast")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	err := ov.Add("fast", openTestVolume(t, "fast"))
	var target *VolumeAlreadyOpenError
	if !errors.As(err, &target) {
		t.Fatalf("Add(duplicate) error = %v, want *VolumeAlreadyOpenError", err)
	}
}

func TestOpenVolumes_Remove(t *testing.T) {
	ov := NewOpenVolumes("node-1")
	v := openTestVolume(t, "fast")
	if err := ov.Add("fast", v); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, ok := ov.Remove("fast")
	if !ok || got != v {
		t.Fatalf("Remove(fast) = (%v, %v), want (%v, true)", got, ok, v)
	}
	if _, ok := ov.Get("fast"); ok {
		t.Fatal("Get(fast) after Remove should report absent")
	}
	// Removing again is a no-op.
	if _, ok := ov.Remove("fast"); ok {
		t.Fatal("Remove(fast) a second time should report absent")
	}
}

func TestOpenVolumes_RootUnavailable(t *testing.T) {
	ov := NewOpenVolumes("node-1")
	_, err := ov.Root(context.Background(), "ghost")
	var target *PoolUnavailableError
	if !errors.As(err, &target) {
		t.Fatalf("Root(unopened) error = %v, want *PoolUnavailableError", err)
	}
	if target.NodeID != "node-1" {
		t.Fatalf("PoolUnavailableError.NodeID = %q, want node-1", target.NodeID)
	}
}

// fakeHealthVolume is a minimal Volume double whose Health is settable, so
// a test can simulate a Lockable volume that is open (tracked by
// OpenVolumes) but not currently readable: locked, or failed.
type fakeHealthVolume struct {
	root   string
	health Health
}

func (f *fakeHealthVolume) Root() string                         { return f.root }
func (f *fakeHealthVolume) Usage(context.Context) (Usage, error) { return Usage{}, nil }
func (f *fakeHealthVolume) Health(context.Context) Health        { return f.health }
func (f *fakeHealthVolume) Close(context.Context) error          { return nil }

// TestOpenVolumes_Root_RefusesWhenLockedOrUnavailable proves Root never
// falls through to the bare mount point for a volume whose Health is not
// StateReady: a Lockable volume that has not been unlocked yet, that
// failed to unlock, or that was locked again by a re-seal all report
// *PoolUnavailableError instead of a readable (but actually inaccessible)
// path.
func TestOpenVolumes_Root_RefusesWhenLockedOrUnavailable(t *testing.T) {
	for _, state := range []State{StateLocked, StateUnavailable} {
		t.Run(state.String(), func(t *testing.T) {
			ov := NewOpenVolumes("node-1")
			v := &fakeHealthVolume{root: "/pools/fast", health: Health{State: state}}
			if err := ov.Add("fast", v); err != nil {
				t.Fatalf("Add: %v", err)
			}
			_, err := ov.Root(context.Background(), "fast")
			var target *PoolUnavailableError
			if !errors.As(err, &target) {
				t.Fatalf("Root(state=%s) error = %v, want *PoolUnavailableError", state, err)
			}
		})
	}
}

// TestOpenVolumes_Root_ReadyServesThePath is the positive complement: a
// volume reporting StateReady serves its path normally.
func TestOpenVolumes_Root_ReadyServesThePath(t *testing.T) {
	ov := NewOpenVolumes("node-1")
	v := &fakeHealthVolume{root: "/pools/fast", health: Health{State: StateReady}}
	if err := ov.Add("fast", v); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := ov.Root(context.Background(), "fast")
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got != "/pools/fast" {
		t.Fatalf("Root() = %q, want %q", got, "/pools/fast")
	}
}

func TestOpenVolumes_PoolsSorted(t *testing.T) {
	ov := NewOpenVolumes("node-1")
	for _, name := range []string{"fast", "bulk", "vault"} {
		if err := ov.Add(name, openTestVolume(t, name)); err != nil {
			t.Fatalf("Add(%s): %v", name, err)
		}
	}
	got := ov.Pools()
	want := []string{"bulk", "fast", "vault"}
	if len(got) != len(want) {
		t.Fatalf("Pools() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Pools() = %v, want %v", got, want)
		}
	}
}
