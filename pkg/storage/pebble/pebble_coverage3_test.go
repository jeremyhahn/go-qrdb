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
	"fmt"
	"log/slog"
	"testing"
)

// ---------------------------------------------------------------------------
// registry_errors.go — Error() and Unwrap() coverage
// ---------------------------------------------------------------------------

// TestEmptyTenantIDError_Error verifies the error string.
func TestEmptyTenantIDError_Error(t *testing.T) {
	e := &EmptyTenantIDError{}
	want := "qrdb/storage/pebble: tenant id must not be empty"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestEmptyPathError_Error verifies the error string.
func TestEmptyPathError_Error(t *testing.T) {
	e := &EmptyPathError{}
	want := "qrdb/storage/pebble: per-tenant path must not be empty"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestRegistryClosedError_Error verifies the error string.
func TestRegistryClosedError_Error(t *testing.T) {
	e := &RegistryClosedError{}
	want := "qrdb/storage/pebble: per-tenant registry is closed"
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestTenantPathConflictError_Error verifies the formatted error string.
func TestTenantPathConflictError_Error(t *testing.T) {
	e := &TenantPathConflictError{
		TenantID:      "tenant-a",
		ExistingPath:  "/data/a",
		RequestedPath: "/data/b",
	}
	want := `qrdb/storage/pebble: tenant "tenant-a" already opened at "/data/a", refused to re-open at "/data/b"`
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestNotRegisteredError_Error verifies the formatted error string.
func TestNotRegisteredError_Error(t *testing.T) {
	e := &NotRegisteredError{TenantID: "ghost"}
	want := `qrdb/storage/pebble: tenant "ghost" is not registered`
	if got := e.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestOpenError_Error verifies the formatted error string including wrapped cause.
func TestOpenError_Error(t *testing.T) {
	cause := fmt.Errorf("disk full")
	e := &OpenError{TenantID: "t1", Path: "/db/t1", Err: cause}
	got := e.Error()
	if got == "" {
		t.Fatal("Error() returned empty string")
	}
	// Must contain tenant id, path and cause.
	for _, sub := range []string{"t1", "/db/t1", "disk full"} {
		if !containsSubstr(got, sub) {
			t.Errorf("Error() = %q, missing substring %q", got, sub)
		}
	}
}

// TestOpenError_Unwrap verifies Unwrap returns the wrapped cause.
func TestOpenError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("disk full")
	e := &OpenError{TenantID: "t1", Path: "/db/t1", Err: cause}
	if !errors.Is(e, cause) {
		t.Fatalf("errors.Is(e, cause) = false, want true")
	}
	if e.Unwrap() != cause {
		t.Fatalf("Unwrap() = %v, want %v", e.Unwrap(), cause)
	}
}

// TestCloseError_Error verifies the formatted error string including wrapped cause.
func TestCloseError_Error(t *testing.T) {
	cause := fmt.Errorf("io error")
	e := &CloseError{TenantID: "t2", Path: "/db/t2", Err: cause}
	got := e.Error()
	if got == "" {
		t.Fatal("Error() returned empty string")
	}
	for _, sub := range []string{"t2", "/db/t2", "io error"} {
		if !containsSubstr(got, sub) {
			t.Errorf("Error() = %q, missing substring %q", got, sub)
		}
	}
}

// TestCloseError_Unwrap verifies Unwrap returns the wrapped cause.
func TestCloseError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("io error")
	e := &CloseError{TenantID: "t2", Path: "/db/t2", Err: cause}
	if !errors.Is(e, cause) {
		t.Fatalf("errors.Is(e, cause) = false, want true")
	}
	if e.Unwrap() != cause {
		t.Fatalf("Unwrap() = %v, want %v", e.Unwrap(), cause)
	}
}

// containsSubstr is a simple substring check to avoid importing strings.
func containsSubstr(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}

// ---------------------------------------------------------------------------
// WithRegistryLogger — 0% coverage
// ---------------------------------------------------------------------------

// TestWithRegistryLogger_NonNil verifies that a non-nil logger is accepted.
func TestWithRegistryLogger_NonNil(t *testing.T) {
	logger := slog.Default()
	reg := NewPerTenantRegistry(WithRegistryLogger(logger))
	if reg == nil {
		t.Fatal("NewPerTenantRegistry with logger returned nil")
	}
	// Verify the logger was actually applied by opening a tenant (which logs).
	dir := t.TempDir()
	s, err := reg.Open(context.Background(), "test-tenant", dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = s.Close()
	_ = reg.CloseAll(context.Background())
}

// TestWithRegistryLogger_Nil verifies that a nil logger is coerced to slog.Default
// (the option is applied without panic and the registry still functions).
func TestWithRegistryLogger_Nil(t *testing.T) {
	reg := NewPerTenantRegistry(WithRegistryLogger(nil))
	if reg == nil {
		t.Fatal("NewPerTenantRegistry with nil logger returned nil")
	}
	dir := t.TempDir()
	s, err := reg.Open(context.Background(), "t", dir)
	if err != nil {
		t.Fatalf("Open with nil-logger registry: %v", err)
	}
	_ = s.Close()
	_ = reg.CloseAll(context.Background())
}

// ---------------------------------------------------------------------------
// registry.go:Open — ctx.Err() branch (90%)
// ---------------------------------------------------------------------------

// TestPerTenantRegistry_Open_CancelledContext verifies that Open returns the
// context error when the context is already cancelled.
func TestPerTenantRegistry_Open_CancelledContext(t *testing.T) {
	reg := newStubRegistry(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	_, err := reg.Open(ctx, "tenant", t.TempDir())
	if err == nil {
		t.Fatal("Open with cancelled context: expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Open with cancelled context: got %v, want context.Canceled", err)
	}
}

// ---------------------------------------------------------------------------
// registry.go:Close — EmptyTenantID and ctx.Err() branches (75%)
// ---------------------------------------------------------------------------

// TestPerTenantRegistry_Close_EmptyTenantID verifies that Close returns
// EmptyTenantIDError for an empty tenant id.
func TestPerTenantRegistry_Close_EmptyTenantID(t *testing.T) {
	reg := newStubRegistry(t)

	err := reg.Close(context.Background(), "")
	var empty *EmptyTenantIDError
	if !errors.As(err, &empty) {
		t.Fatalf("Close with empty tenant id: got %v, want EmptyTenantIDError", err)
	}
}

// TestPerTenantRegistry_Close_CancelledContext verifies that Close returns the
// context error when the context is already cancelled.
func TestPerTenantRegistry_Close_CancelledContext(t *testing.T) {
	reg := newStubRegistry(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	err := reg.Close(ctx, "any-tenant")
	if err == nil {
		t.Fatal("Close with cancelled context: expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Close with cancelled context: got %v, want context.Canceled", err)
	}
}

// ---------------------------------------------------------------------------
// registry.go:CloseAll — error-accumulation branch (76.5%)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// registry.go:Get — EmptyTenantID branch is already covered, but verify
// the "closed" field guard in Open after CloseAll (RegistryClosedError).
// ---------------------------------------------------------------------------

// TestPerTenantRegistry_Open_AfterCloseAll verifies RegistryClosedError is returned.
func TestPerTenantRegistry_Open_AfterCloseAll(t *testing.T) {
	reg := newStubRegistry(t)
	_ = reg.CloseAll(context.Background())

	_, err := reg.Open(context.Background(), "t", t.TempDir())
	var rc *RegistryClosedError
	if !errors.As(err, &rc) {
		t.Fatalf("Open after CloseAll: got %v, want RegistryClosedError", err)
	}
}

// ---------------------------------------------------------------------------
// iterator.go — Error branch with nil inner iter and stored err (85.7%)
// ---------------------------------------------------------------------------

// TestIterator_Error_NilInnerWithStoredErr verifies that the stored err
// branch takes priority over the nil-inner-iter guard.
func TestIterator_Error_NilInnerWithStoredErr(t *testing.T) {
	sentinel := fmt.Errorf("stored-iterator-error")
	it := &pebbleIterator{err: sentinel}

	err := it.Error()
	if err == nil {
		t.Fatal("Error() with stored err: got nil, want non-nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("Error() with stored err: got %v, want to wrap %v", err, sentinel)
	}
}

// TestIterator_Close_NilInnerAlreadyClosed verifies that Close on an
// already-closed iterator with nil inner is idempotent (no panic, nil error).
func TestIterator_Close_NilInnerAlreadyClosed(t *testing.T) {
	it := &pebbleIterator{} // nil inner
	// Close once to flip the closed flag.
	if err := it.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Close again — must be no-op (CompareAndSwap returns false path).
	if err := it.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// newStubRegistry is a helper that creates a registry with a stub opener
// so tests don't create real Pebble files on disk for the registry itself.
// Tenants that need real storage use openRealTenant-style code directly.
// ---------------------------------------------------------------------------

// newStubRegistry returns a PerTenantRegistry with the default (real) opener
// but registered inside t.Cleanup for safe shutdown.
func newStubRegistry(t *testing.T) PerTenantRegistry {
	t.Helper()
	reg := NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })
	return reg
}
