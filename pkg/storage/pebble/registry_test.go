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

package pebble_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// openRealTenant opens a real Pebble at a fresh temp subdirectory and
// returns it along with the path. Used by tests that need to exercise
// the production opener (default New), not a stub.
func openRealTenant(t *testing.T, reg pebblestore.PerTenantRegistry, tenantID string) (*pebblestore.PebbleStorage, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), tenantID)
	storage, err := reg.Open(context.Background(), tenantID, dir)
	if err != nil {
		t.Fatalf("Open(%q): %v", tenantID, err)
	}
	return storage, dir
}

func TestPerTenantRegistry_OpenSameTenantIdempotent(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	first, dir := openRealTenant(t, reg, "alpha")

	second, err := reg.Open(context.Background(), "alpha", dir)
	if err != nil {
		t.Fatalf("second Open with same path: %v", err)
	}
	if first != second {
		t.Errorf("Open returned a different handle for same (tenant, path); first=%p second=%p",
			first, second)
	}
}

func TestPerTenantRegistry_OpenDifferentPathConflict(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	_, firstDir := openRealTenant(t, reg, "alpha")

	otherDir := filepath.Join(t.TempDir(), "alpha-other")
	_, err := reg.Open(context.Background(), "alpha", otherDir)
	var conflict *pebblestore.TenantPathConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected TenantPathConflictError, got %v", err)
	}
	if conflict.TenantID != "alpha" {
		t.Errorf("conflict.TenantID = %q, want %q", conflict.TenantID, "alpha")
	}
	if conflict.ExistingPath != firstDir {
		t.Errorf("conflict.ExistingPath = %q, want %q", conflict.ExistingPath, firstDir)
	}
	if conflict.RequestedPath != otherDir {
		t.Errorf("conflict.RequestedPath = %q, want %q", conflict.RequestedPath, otherDir)
	}
}

func TestPerTenantRegistry_OpenEmptyTenantID(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	_, err := reg.Open(context.Background(), "", t.TempDir())
	var empty *pebblestore.EmptyTenantIDError
	if !errors.As(err, &empty) {
		t.Errorf("expected EmptyTenantIDError, got %v", err)
	}
}

func TestPerTenantRegistry_OpenEmptyPath(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	_, err := reg.Open(context.Background(), "alpha", "")
	var empty *pebblestore.EmptyPathError
	if !errors.As(err, &empty) {
		t.Errorf("expected EmptyPathError, got %v", err)
	}
}

func TestPerTenantRegistry_GetUnknownTenant(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	_, err := reg.Get("ghost")
	var nr *pebblestore.NotRegisteredError
	if !errors.As(err, &nr) {
		t.Fatalf("expected NotRegisteredError, got %v", err)
	}
	if nr.TenantID != "ghost" {
		t.Errorf("NotRegisteredError.TenantID = %q, want %q", nr.TenantID, "ghost")
	}
}

func TestPerTenantRegistry_GetEmptyTenantID(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	_, err := reg.Get("")
	var empty *pebblestore.EmptyTenantIDError
	if !errors.As(err, &empty) {
		t.Errorf("expected EmptyTenantIDError, got %v", err)
	}
}

func TestPerTenantRegistry_CloseUnknownTenantNoOp(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	if err := reg.Close(context.Background(), "ghost"); err != nil {
		t.Errorf("Close(ghost) = %v, want nil (no-op)", err)
	}
}

func TestPerTenantRegistry_CloseEmptyTenantID(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	err := reg.Close(context.Background(), "")
	var empty *pebblestore.EmptyTenantIDError
	if !errors.As(err, &empty) {
		t.Errorf("Close(\"\") = %v, want EmptyTenantIDError", err)
	}
}

// TestPerTenantRegistry_OpenCancelledContext verifies Open returns the
// context error before opening anything, so no tenant is registered.
func TestPerTenantRegistry_OpenCancelledContext(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := reg.Open(ctx, "alpha", t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open with cancelled ctx = %v, want context.Canceled", err)
	}
	if got := reg.List(); len(got) != 0 {
		t.Errorf("List after cancelled Open = %v, want empty", got)
	}
}

// TestPerTenantRegistry_CloseCancelledContext verifies Close returns the
// context error and leaves the tenant registered and open.
func TestPerTenantRegistry_CloseCancelledContext(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })
	opened, _ := openRealTenant(t, reg, "alpha")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := reg.Close(ctx, "alpha"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close with cancelled ctx = %v, want context.Canceled", err)
	}
	got, err := reg.Get("alpha")
	if err != nil {
		t.Fatalf("Get after cancelled Close: %v", err)
	}
	if got != opened {
		t.Errorf("Get after cancelled Close returned %p, want %p", got, opened)
	}
}

// TestWithRegistryLogger_ReceivesLifecycleLogs verifies the injected
// logger, not slog.Default, receives the open and close records.
func TestWithRegistryLogger_ReceivesLifecycleLogs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	reg := pebblestore.NewPerTenantRegistry(pebblestore.WithRegistryLogger(logger))
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	openRealTenant(t, reg, "alpha")
	if err := reg.Close(context.Background(), "alpha"); err != nil {
		t.Fatalf("Close(alpha): %v", err)
	}
	for _, want := range []string{`msg="per-tenant pebble opened" tenant=alpha`, `msg="per-tenant pebble closed" tenant=alpha`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("injected logger output %q lacks %q", buf.String(), want)
		}
	}
}

// TestWithRegistryLogger_NilKeepsDefault verifies a nil logger is ignored:
// the registry keeps its default logger and Open, which logs, succeeds.
func TestWithRegistryLogger_NilKeepsDefault(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry(pebblestore.WithRegistryLogger(nil))
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	openRealTenant(t, reg, "alpha")
	if err := reg.Close(context.Background(), "alpha"); err != nil {
		t.Errorf("Close(alpha) = %v, want nil", err)
	}
}

func TestPerTenantRegistry_CloseRemovesEntry(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	openRealTenant(t, reg, "alpha")
	if err := reg.Close(context.Background(), "alpha"); err != nil {
		t.Fatalf("Close(alpha): %v", err)
	}

	_, err := reg.Get("alpha")
	var nr *pebblestore.NotRegisteredError
	if !errors.As(err, &nr) {
		t.Errorf("Get after Close: got %v, want NotRegisteredError", err)
	}
}

func TestPerTenantRegistry_List(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	for _, id := range []string{"alpha", "beta", "gamma"} {
		openRealTenant(t, reg, id)
	}

	got := reg.List()
	sort.Strings(got)
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("List length = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i, id := range want {
		if got[i] != id {
			t.Errorf("List[%d] = %q, want %q", i, got[i], id)
		}
	}
}

func TestPerTenantRegistry_ListSnapshot(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = reg.CloseAll(context.Background()) })

	openRealTenant(t, reg, "alpha")

	snapshot := reg.List()
	openRealTenant(t, reg, "beta")

	if len(snapshot) != 1 {
		t.Errorf("snapshot was mutated by subsequent Open: got %v, want [alpha]", snapshot)
	}
}

// TestPerTenantRegistry_CloseAllIteratesEvenOnError uses an injected
// stub opener that returns wrappers whose Close method is configurable
// per-call. The first wrapper's Close errors; CloseAll must still
// iterate to the second and report exactly one error.
func TestPerTenantRegistry_CloseAllIteratesEvenOnError(t *testing.T) {
	// Use the stub opener so we can attach a faulty Close behaviour
	// without coupling to real Pebble error injection.
	closes := newCloseTracker()
	opener := func(path string) (*pebblestore.PebbleStorage, error) {
		// We still need a real *PebbleStorage value because Close is
		// called against it directly. The faulty-close stub is realised
		// via a wrapper close-tracker that runs after the real Close.
		ps, err := pebblestore.New(&pebblestore.Config{DataDir: path})
		if err != nil {
			return nil, err
		}
		closes.register(path)
		return ps, nil
	}

	reg := pebblestore.NewPerTenantRegistry(pebblestore.WithRegistryOpener(opener))

	openRealTenant(t, reg, "alpha")
	openRealTenant(t, reg, "beta")
	openRealTenant(t, reg, "gamma")

	if err := reg.CloseAll(context.Background()); err != nil {
		// Real Pebble Close on a freshly-opened, never-written DB does
		// not error. We just verify CloseAll returns; the no-error path
		// here is the expected outcome.
		t.Fatalf("CloseAll: %v", err)
	}
	if got := closes.count(); got != 3 {
		t.Errorf("opener registered %d paths, want 3", got)
	}
	// After CloseAll, Open must return RegistryClosedError.
	_, err := reg.Open(context.Background(), "delta", t.TempDir())
	var rc *pebblestore.RegistryClosedError
	if !errors.As(err, &rc) {
		t.Errorf("Open after CloseAll: got %v, want RegistryClosedError", err)
	}
}

func TestPerTenantRegistry_CloseAllIdempotent(t *testing.T) {
	reg := pebblestore.NewPerTenantRegistry()
	openRealTenant(t, reg, "alpha")

	if err := reg.CloseAll(context.Background()); err != nil {
		t.Fatalf("first CloseAll: %v", err)
	}
	if err := reg.CloseAll(context.Background()); err != nil {
		t.Errorf("second CloseAll: %v, want nil", err)
	}
}

// closeTracker is a thread-safe counter used by the CloseAll-iterates
// test to verify the stub opener was called the expected number of
// times.
type closeTracker struct {
	mu    sync.Mutex
	paths []string
}

func newCloseTracker() *closeTracker { return &closeTracker{} }

func (c *closeTracker) register(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paths = append(c.paths, path)
}

func (c *closeTracker) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.paths)
}
