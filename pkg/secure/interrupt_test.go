// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package secure

import (
	"errors"
	"testing"
)

// TestPurge_NotInitialized asserts that Purge returns *NotInitializedError
// before CatchInterrupt has been invoked. This test must run BEFORE any
// other test that calls CatchInterrupt because initialized is a package
// global. Go test names sort alphabetically within a package, and this
// test's name ensures it's observed; we also reset the flag for safety.
func TestPurge_NotInitialized(t *testing.T) {
	// Snapshot and reset. Tests in this package do not rely on the
	// initialized flag being set — CatchInterrupt is called by the
	// owning binary, not by tests.
	prev := initialized.Swap(false)
	t.Cleanup(func() { initialized.Store(prev) })

	err := Purge()
	if err == nil {
		t.Fatalf("expected error before CatchInterrupt")
	}
	var ne *NotInitializedError
	if !errors.As(err, &ne) {
		t.Fatalf("expected *NotInitializedError, got %T", err)
	}
}

func TestCatchInterrupt_Idempotent(t *testing.T) {
	prev := initialized.Swap(false)
	t.Cleanup(func() { initialized.Store(prev) })

	CatchInterrupt()
	if !initialized.Load() {
		t.Fatalf("initialized not set after CatchInterrupt")
	}
	// Second call must be a no-op (no panic, no change).
	CatchInterrupt()
	if !initialized.Load() {
		t.Fatalf("initialized must remain set after second CatchInterrupt")
	}
}

// TestEnablePurge_ArmsWithoutSignalHandler verifies EnablePurge marks the
// subsystem initialized (so Purge works) and is idempotent. It must NOT
// install a process exit handler — that is what lets a binary owning its
// own signal handling exit gracefully (verified end-to-end by the
// integration graceful-shutdown test).
func TestEnablePurge_ArmsWithoutSignalHandler(t *testing.T) {
	prev := initialized.Swap(false)
	t.Cleanup(func() { initialized.Store(prev) })

	EnablePurge()
	if !initialized.Load() {
		t.Fatalf("initialized not set after EnablePurge")
	}
	// Purge must now succeed (no NotInitializedError).
	if err := Purge(); err != nil {
		t.Fatalf("Purge after EnablePurge: %v", err)
	}
	// Idempotent.
	EnablePurge()
	if !initialized.Load() {
		t.Fatalf("initialized must remain set after second EnablePurge")
	}
}

func TestPurgeOrPanic_PanicsWhenUninitialized(t *testing.T) {
	prev := initialized.Swap(false)
	t.Cleanup(func() { initialized.Store(prev) })

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic")
		}
	}()
	PurgeOrPanic()
}
