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
	"testing"
)

// victim creates a directory holding one file, outside every pool, and
// returns the directory and the file.
func victim(t *testing.T) (dir, file string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "victim")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(dir, "precious")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, file
}

// mustSurvive fails the test if file was removed.
func mustSurvive(t *testing.T, file string) {
	t.Helper()
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("%s was removed through the guard: %v", file, err)
	}
}

// requireOutside asserts RemoveDataRoot refused path with
// *DataRootOutsideLocalRootsError naming it.
func requireOutside(t *testing.T, reg *BarrierRegistry, path string) {
	t.Helper()
	err := reg.RemoveDataRoot(context.Background(), path)
	var outside *DataRootOutsideLocalRootsError
	if !errors.As(err, &outside) || outside.DataRoot != path {
		t.Fatalf("RemoveDataRoot(%q) = %v; want *DataRootOutsideLocalRootsError", path, err)
	}
}

// TestRequirePathInsideLocalPools_SiblingWithTheSamePrefixIsOutside: /pool10 is
// not inside /pool1. Containment is by path component, never by string prefix.
func TestRequirePathInsideLocalPools_SiblingWithTheSamePrefixIsOutside(t *testing.T) {
	parent := t.TempDir()
	reg, _ := newPooledTestRegistryAt(t, filepath.Join(parent, "pool1"))
	sibling := filepath.Join(parent, "pool10", "tenant")
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sibling, "precious")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	requireOutside(t, reg, sibling)
	mustSurvive(t, file)
}

// TestRequirePathInsideLocalPools_TrailingSeparators: a path to remove that
// is not already clean -- a trailing or doubled separator, or a relative
// path -- is refused rather than normalised. Every real path removal is
// handed comes from filepath.Join, so an unclean one did not; Volume.Root
// itself is always absolute and clean by contract (pkg/volume.Volume), so
// there is no pool base to exercise this against.
func TestRequirePathInsideLocalPools_TrailingSeparators(t *testing.T) {
	ctx := context.Background()
	pool := t.TempDir()
	reg, _ := newPooledTestRegistryAt(t, pool)
	tenant := filepath.Join(pool, "tenant")
	if err := os.MkdirAll(tenant, 0o700); err != nil {
		t.Fatal(err)
	}

	requireOutside(t, reg, tenant+"/")
	requireOutside(t, reg, pool+"//tenant")
	requireOutside(t, reg, pool+"/")
	if err := reg.RequirePathInsideLocalPools(ctx, "tenant"); err == nil {
		t.Fatal("a relative path was accepted")
	}
	if _, err := os.Stat(tenant); err != nil {
		t.Fatalf("a refused path was removed: %v", err)
	}

	if err := reg.RemoveDataRoot(ctx, tenant); err != nil {
		t.Fatalf("RemoveDataRoot(%s) = %v; a clean path under the pool must still be removable", tenant, err)
	}
	if _, err := os.Stat(tenant); !os.IsNotExist(err) {
		t.Fatalf("the tenant directory survived its removal: %v", err)
	}
}

// TestRequirePathInsideLocalPools_SymlinkedTenantDirectoryIsOutside: a tenant
// directory inside the pool that is a symbolic link to a directory outside it
// is refused, and nothing it points at is touched. Removal lists the
// directory and removes its entries, so following the link would empty the
// target.
func TestRequirePathInsideLocalPools_SymlinkedTenantDirectoryIsOutside(t *testing.T) {
	reg, _ := newPooledTestRegistry(t)
	base := poolBase(t, reg)
	target, file := victim(t)
	link := filepath.Join(base, "tenant")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	requireOutside(t, reg, link)
	mustSurvive(t, file)
}

// TestRequirePathInsideLocalPools_SymlinkedAncestorIsOutside: a link anywhere
// above the path -- here a directory in the pool that points outside it --
// carries the removal outside just the same.
func TestRequirePathInsideLocalPools_SymlinkedAncestorIsOutside(t *testing.T) {
	reg, _ := newPooledTestRegistry(t)
	base := poolBase(t, reg)
	target, _ := victim(t)
	inner := filepath.Join(target, "tenant")
	if err := os.MkdirAll(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(inner, "precious")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(base, "databases")); err != nil {
		t.Fatal(err)
	}

	requireOutside(t, reg, filepath.Join(base, "databases", "tenant"))
	// A path below the link that does not exist yet resolves through the
	// link too.
	requireOutside(t, reg, filepath.Join(base, "databases", "not-yet"))
	mustSurvive(t, file)
}

// TestRequirePathInsideLocalPools_SymlinkedPoolPathStillContainsItsTenants: the
// pool path itself may be a link -- an operator's mount alias. The "dir"
// provider refuses to Open a pool path that is itself a symlink, so this
// uses a Volume double (newPooledTestRegistryAtAlias) reporting the alias
// from Root() to exercise RequirePathInsideLocalPools' own resolution of a
// symlinked pool base: resolving both sides keeps its tenants inside, and
// their removal proceeds.
func TestRequirePathInsideLocalPools_SymlinkedPoolPathStillContainsItsTenants(t *testing.T) {
	ctx := context.Background()
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "pool-alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	reg, _ := newPooledTestRegistryAtAlias(t, real, alias)
	tenant := filepath.Join(alias, "tenant")
	if err := os.MkdirAll(filepath.Join(tenant, "barrier"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := reg.RemoveDataRoot(ctx, tenant); err != nil {
		t.Fatalf("RemoveDataRoot(%s) under a symlinked pool path = %v", tenant, err)
	}
	if _, err := os.Stat(filepath.Join(real, "tenant")); !os.IsNotExist(err) {
		t.Fatalf("the tenant directory survived its removal: %v", err)
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("the pool itself was removed: %v", err)
	}
}

// TestRequirePathInsideLocalPools_UnresolvablePathIsRefused: a path whose
// resolution fails for a reason other than not existing -- here a link loop
// -- is refused, carrying the cause.
func TestRequirePathInsideLocalPools_UnresolvablePathIsRefused(t *testing.T) {
	reg, _ := newPooledTestRegistry(t)
	base := poolBase(t, reg)
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	err := reg.RequirePathInsideLocalPools(context.Background(), filepath.Join(a, "tenant"))
	var outside *DataRootOutsideLocalRootsError
	if !errors.As(err, &outside) || outside.Err == nil || errors.Unwrap(err) == nil {
		t.Fatalf("RequirePathInsideLocalPools through a link loop = %v; want a refusal carrying the cause", err)
	}
}
