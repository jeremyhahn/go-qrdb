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

package seal

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// osReadDir is a thin alias for os.ReadDir kept here so test helpers
// reference a single seam if we ever swap it out for a fake fs.
var osReadDir = os.ReadDir

// poolTestLogger returns a discard logger to avoid noisy test output.
func poolTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newPoolSoftwareStrategy returns a SoftwareStrategy with a deterministic
// passphrase. Used by every pooled-tenant test that needs to register a
// barrier against a fresh per-tenant Pebble.
func newPoolSoftwareStrategy(t *testing.T) *SoftwareStrategy {
	t.Helper()
	s, err := NewSoftwareStrategy(context.Background(), []byte("pool-test-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	return s
}

// newPoolSystemBarrier builds a StorageBarrier suitable as the registry's
// system barrier in pool tests.
func newPoolSystemBarrier(t *testing.T) *StorageBarrier {
	t.Helper()
	store := NewMemoryBackend()
	b, err := NewStorageBarrier(poolTestLogger(), store, BarrierConfig{
		RootKeyPath: "system/root-key",
	}, newPoolSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	return b
}

// newPooledRegistry builds a BarrierRegistry over a single real local
// volume (one pool, named "default", backed by newTestVolumeFixture's real
// "dir" provider and Catalog) and a real per-tenant Pebble registry. It
// returns the registry, the single-element slice of pool root paths (kept
// as a slice for call-site compatibility with the old multi-path-pool
// signature; decision D1 -- one mount per pool per node -- means there is
// only ever one root per pool now), and the per-tenant Pebble registry.
//
// The returned registry's volumeAllocator (unexported field, same
// package) is the fixture's *volume.Catalog: tests that need to Reserve a
// tenant before RegisterVolumeTenant/ReattachVolumeTenant (which require a
// pre-existing Assignment -- see barrier_registry.go's doc comments) use
// reg.volumeAllocator.Reserve directly.
func newPooledRegistry(t *testing.T) (*BarrierRegistry, []string, pebblestore.PerTenantRegistry) {
	t.Helper()
	catalog, open, root := newTestVolumeFixture(t)

	system := newPoolSystemBarrier(t)
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	reg, err := NewBarrierRegistry(
		poolTestLogger(),
		system,
		WithVolumeAllocator(catalog),
		WithVolumeResolver(open),
		WithPerTenantRegistry(perTenantReg),
	)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return reg, []string{root}, perTenantReg
}

// newPooledRegistryUnsealed is newPooledRegistry with its system barrier
// already initialized (and so unsealed): RegisterVolumeTenant/ReattachVolumeTenant
// refuse with ErrSealed otherwise, and most volume-tenant tests are not
// about the system barrier's own seal state. Kept separate from
// newPooledRegistry itself because at least one caller (system-managed
// barrier tests) initializes the returned registry's system barrier ITSELF,
// with specific credentials; pre-initializing there too would make that
// Initialize call fail with ErrAlreadyInitialized.
func newPooledRegistryUnsealed(t *testing.T) (*BarrierRegistry, []string, pebblestore.PerTenantRegistry) {
	t.Helper()
	reg, roots, perTenantReg := newPooledRegistry(t)
	if err := reg.SystemBarrier().Initialize(context.Background(), MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	return reg, roots, perTenantReg
}

// newShareableComponents builds the primitive components (system barrier,
// attach options over a shared catalog/open-volumes/per-tenant registry)
// needed to create multiple BarrierRegistry instances that share the same
// underlying persistent state. This allows tests to simulate a "restart"
// or a "follower node": a fresh BarrierRegistry is created with the same
// system barrier and the same catalog, but with empty in-memory maps
// (tenants).
//
// The returned attachOptions slice is pre-built from the shared catalog and
// per-tenant registry; pass it to NewBarrierRegistry via the variadic opts
// argument. The roots slice (length 1; decision D1) contains the pool's
// root path so callers can assert DataRoot placement, and the returned
// *volume.Catalog lets a caller Reserve a tenant's Assignment before
// registering it.
func newShareableComponents(t *testing.T) (
	system *StorageBarrier,
	attachOptions []Option,
	roots []string,
) {
	t.Helper()

	catalog, open, root := newTestVolumeFixture(t)

	system = newPoolSystemBarrier(t)
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	attachOptions = []Option{
		WithVolumeAllocator(catalog),
		WithVolumeResolver(open),
		WithPerTenantRegistry(perTenantReg),
	}
	roots = []string{root}
	return
}

// assertPebbleAtPath verifies a Pebble database directory has been
// created at the supplied path. We can't assume the database has any
// keys yet — only that pebble.Open succeeded and produced its on-disk
// structure (CURRENT, OPTIONS-*, etc.).
func assertPebbleAtPath(t *testing.T, path string) {
	t.Helper()
	exists, err := pebbleExists(path)
	if err != nil {
		t.Fatalf("pebbleExists(%q): %v", path, err)
	}
	if !exists {
		t.Errorf("expected Pebble at %q, none found", path)
	}
}

// assertNoPebbleAtPath verifies no Pebble database directory exists at the
// supplied path.
func assertNoPebbleAtPath(t *testing.T, path string) {
	t.Helper()
	exists, err := pebbleExists(path)
	if err != nil {
		t.Fatalf("pebbleExists(%q): %v", path, err)
	}
	if exists {
		t.Errorf("expected no Pebble at %q, one was found", path)
	}
}

// pebbleExists reports whether a directory looks like a Pebble store
// (has at least one CURRENT or OPTIONS-* file).
func pebbleExists(path string) (bool, error) {
	entries, err := readDir(path)
	if err != nil {
		return false, err
	}
	for _, name := range entries {
		if name == "CURRENT" || strings.HasPrefix(name, "OPTIONS-") {
			return true, nil
		}
	}
	return false, nil
}

// readDir lists entries in a directory by name, returning an empty slice
// when the directory does not exist.
func readDir(path string) ([]string, error) {
	entries, err := osReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out, nil
}
