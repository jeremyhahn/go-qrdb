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

package seal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBarrierRegistry_RequireInsideLocalPool_RefusesSymlinkOutsidePool
// proves invariant 2 holds even after a pool is open and its tenant
// directory has been replaced (by an operator, or a compromised writer)
// with a symlink pointing OUTSIDE the pool entirely. Before the fix, only
// the string-level path-traversal check ran; a symlink whose string form
// was a clean, one-component-deep child of the pool root sailed through.
func TestBarrierRegistry_RequireInsideLocalPool_RefusesSymlinkOutsidePool(t *testing.T) {
	poolRoot := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("not yours"), 0o600); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	// "acme"'s directory is a symlink pointing entirely outside the pool.
	if err := os.Symlink(outside, filepath.Join(poolRoot, "acme")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	allocator := newFakeAllocator()
	resolver := newFakeVolumeResolver()
	resolver.roots["fast"] = poolRoot
	if _, err := allocator.Reserve(context.Background(), "acme", "fast", "acme", 0); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	r, err := NewBarrierRegistry(testLogger(), newSystemBarrier(t),
		WithVolumeAllocator(allocator), WithVolumeResolver(resolver))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	_, err = r.RequireInsideLocalPool(context.Background(), "acme")
	var symErr *TenantPathIsSymlinkError
	if !errors.As(err, &symErr) {
		t.Fatalf("RequireInsideLocalPool(symlinked outside pool) = %v, want *TenantPathIsSymlinkError", err)
	}
	// The wire-facing message must never carry the resolved path.
	if msg := err.Error(); !errorMessageNamesOnlyTenant(msg, "acme") {
		t.Fatalf("Error() = %q, must name only the tenant, never a path", msg)
	}
}

// TestBarrierRegistry_RequireInsideLocalPool_SymlinkToSiblingCannotDeleteIt
// proves the sharper form of the same defect: "attacker"'s directory is a
// symlink to "victim"'s REAL directory, both valid tenants of the same
// pool. Before the fix, DestroyTenant("attacker") would resolve straight
// through the symlink and call RemoveDataRoot on victim's real directory,
// deleting the wrong tenant's data. Now RequireInsideLocalPool refuses
// before DestroyTenant ever reaches RemoveDataRoot, and victim's file
// survives.
func TestBarrierRegistry_RequireInsideLocalPool_SymlinkToSiblingCannotDeleteIt(t *testing.T) {
	poolRoot := t.TempDir()
	victimDir := filepath.Join(poolRoot, "victim")
	if err := os.MkdirAll(victimDir, 0o700); err != nil {
		t.Fatalf("mkdir victim: %v", err)
	}
	victimFile := filepath.Join(victimDir, "data.bin")
	if err := os.WriteFile(victimFile, []byte("victim's data"), 0o600); err != nil {
		t.Fatalf("seed victim file: %v", err)
	}
	// "attacker"'s directory is a symlink to victim's real directory.
	if err := os.Symlink(victimDir, filepath.Join(poolRoot, "attacker")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	allocator := newFakeAllocator()
	resolver := newFakeVolumeResolver()
	resolver.roots["fast"] = poolRoot
	if _, err := allocator.Reserve(context.Background(), "victim", "fast", "victim", 0); err != nil {
		t.Fatalf("Reserve(victim): %v", err)
	}
	if _, err := allocator.Reserve(context.Background(), "attacker", "fast", "attacker", 0); err != nil {
		t.Fatalf("Reserve(attacker): %v", err)
	}
	r, err := NewBarrierRegistry(testLogger(), newSystemBarrier(t),
		WithVolumeAllocator(allocator), WithVolumeResolver(resolver))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	destroyErr := r.DestroyTenant(context.Background(), "attacker")
	var symErr *TenantPathIsSymlinkError
	if !errors.As(destroyErr, &symErr) {
		t.Fatalf("DestroyTenant(attacker) = %v, want *TenantPathIsSymlinkError", destroyErr)
	}

	// The sibling's directory and file must be untouched.
	if _, statErr := os.Stat(victimDir); statErr != nil {
		t.Fatalf("victim directory was removed: %v", statErr)
	}
	got, readErr := os.ReadFile(victimFile)
	if readErr != nil {
		t.Fatalf("victim file was removed: %v", readErr)
	}
	if string(got) != "victim's data" {
		t.Fatalf("victim file content = %q, want unchanged", got)
	}

	// victim's own assignment is unaffected and still resolves normally.
	gotRoot, err := r.RequireInsideLocalPool(context.Background(), "victim")
	if err != nil {
		t.Fatalf("RequireInsideLocalPool(victim): %v", err)
	}
	if gotRoot != victimDir {
		t.Fatalf("RequireInsideLocalPool(victim) = %q, want %q", gotRoot, victimDir)
	}
}

// TestBarrierRegistry_RequireInsideLocalPool_RealDirectoryStillWorks proves
// the new symlink check does not regress the ordinary case: a tenant
// directory that is a REAL directory (not a symlink) resolves exactly as
// before, whether it already exists or does not exist yet (the first-create
// case, which has nothing to Lstat).
func TestBarrierRegistry_RequireInsideLocalPool_RealDirectoryStillWorks(t *testing.T) {
	poolRoot := t.TempDir()
	allocator := newFakeAllocator()
	resolver := newFakeVolumeResolver()
	resolver.roots["fast"] = poolRoot
	if _, err := allocator.Reserve(context.Background(), "acme", "fast", "acme", 0); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	r, err := NewBarrierRegistry(testLogger(), newSystemBarrier(t),
		WithVolumeAllocator(allocator), WithVolumeResolver(resolver))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	// Does not exist yet -- the first-create case.
	want := filepath.Join(poolRoot, "acme")
	got, err := r.RequireInsideLocalPool(context.Background(), "acme")
	if err != nil {
		t.Fatalf("RequireInsideLocalPool(not yet created): %v", err)
	}
	if got != want {
		t.Fatalf("RequireInsideLocalPool(not yet created) = %q, want %q", got, want)
	}

	// Now create it as a real directory and resolve again.
	if err := os.MkdirAll(want, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err = r.RequireInsideLocalPool(context.Background(), "acme")
	if err != nil {
		t.Fatalf("RequireInsideLocalPool(real directory): %v", err)
	}
	if got != want {
		t.Fatalf("RequireInsideLocalPool(real directory) = %q, want %q", got, want)
	}
}

// errorMessageNamesOnlyTenant checks msg contains tenantID and nothing that
// looks like a filesystem path, guarding against a future regression that
// re-introduces path detail into this error's wire-facing message.
func errorMessageNamesOnlyTenant(msg, tenantID string) bool {
	if !strings.Contains(msg, tenantID) {
		return false
	}
	return !strings.Contains(msg, "/tmp/") && !strings.Contains(msg, os.TempDir())
}
