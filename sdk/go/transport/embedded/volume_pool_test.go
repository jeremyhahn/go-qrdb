// Copyright 2026 Jeremy Hahn
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

package embedded

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// memStateStore is a tiny in-memory objpool.StateStore for tests.
type memStateStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemStateStore() *memStateStore {
	return &memStateStore{m: make(map[string][]byte)}
}

func (s *memStateStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), v...), true, nil
}

func (s *memStateStore) Put(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = append([]byte(nil), value...)
	return nil
}

func newVolumePoolTransport(t *testing.T, paths []string) (*Transport, objpool.Manager) {
	t.Helper()
	state := newMemStateStore()
	mgr := objpool.NewManager()

	if len(paths) > 0 {
		candidates := make([]interface {
			// satisfy compiler — we'll pass through the local.Local impls
			// directly via objpool.Strategy.
		}, 0)
		_ = candidates // unused — we build common.Storage below

		// build candidates
		var stored []objpool.Pool
		_ = stored

		// Use buildLocalCandidates from the package for parity.
		bs, err := buildLocalCandidates(paths)
		if err != nil {
			t.Fatalf("buildLocalCandidates: %v", err)
		}

		if err := mgr.Register("default", bs, &objpool.RoundRobinStrategy{State: state}); err != nil {
			t.Fatalf("Register: %v", err)
		}
		mgr = objpool.LocalOnly(mgr)
	}

	tr, err := New(newMockKVService(), &mockAdminService{},
		WithVolumePoolManager(mgr),
		WithVolumePoolStateStore(state),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr, mgr
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestVolumePoolClient_List_RegisteredPool(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	tr, _ := newVolumePoolTransport(t, []string{a, b})

	got, err := tr.VolumePoolClient().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 pool, got %d", len(got))
	}
	if got[0].Name != "default" {
		t.Errorf("expected pool name default, got %q", got[0].Name)
	}
	if got[0].BackendCount != 2 {
		t.Errorf("expected 2 backends, got %d", got[0].BackendCount)
	}
	if len(got[0].LocalPaths) != 2 {
		t.Errorf("expected 2 local paths, got %d", len(got[0].LocalPaths))
	}
}

func TestVolumePoolClient_List_MissingManager(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = tr.VolumePoolClient().List(context.Background())
	if err == nil {
		t.Fatal("expected MissingVolumePoolManagerError")
	}
	var miss *MissingVolumePoolManagerError
	if !errors.As(err, &miss) {
		t.Errorf("expected *MissingVolumePoolManagerError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// Pick
// ---------------------------------------------------------------------------

func TestVolumePoolClient_Pick_RoundRobin(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	tr, _ := newVolumePoolTransport(t, []string{a, b})

	pc := tr.VolumePoolClient()

	first, err := pc.Pick(context.Background(), "default", transport.VolumePoolHint{TenantID: "t1"})
	if err != nil {
		t.Fatalf("first Pick: %v", err)
	}
	second, err := pc.Pick(context.Background(), "default", transport.VolumePoolHint{TenantID: "t2"})
	if err != nil {
		t.Fatalf("second Pick: %v", err)
	}
	if first.LocalPath == second.LocalPath {
		t.Errorf("round-robin returned same path twice: %q", first.LocalPath)
	}
	if first.PoolName != "default" {
		t.Errorf("expected PoolName 'default', got %q", first.PoolName)
	}
}

func TestVolumePoolClient_Pick_PoolNotConfigured(t *testing.T) {
	tr, _ := newVolumePoolTransport(t, nil)
	pc := tr.VolumePoolClient()

	_, err := pc.Pick(context.Background(), "missing", transport.VolumePoolHint{})
	if err == nil {
		t.Fatal("expected PoolNotConfiguredError")
	}
	var nc *transport.PoolNotConfiguredError
	if !errors.As(err, &nc) {
		t.Errorf("expected *transport.PoolNotConfiguredError, got %T", err)
	}
}

func TestVolumePoolClient_Pick_EmptyPool(t *testing.T) {
	mgr := objpool.NewManager()
	state := newMemStateStore()
	if err := mgr.Register("empty", nil, &objpool.RoundRobinStrategy{State: state}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithVolumePoolManager(objpool.LocalOnly(mgr)),
		WithVolumePoolStateStore(state),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = tr.VolumePoolClient().Pick(context.Background(), "empty", transport.VolumePoolHint{})
	if err == nil {
		t.Fatal("expected PoolEmptyError")
	}
	var pe *transport.PoolEmptyError
	if !errors.As(err, &pe) {
		t.Errorf("expected *transport.PoolEmptyError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// RegisterPool
// ---------------------------------------------------------------------------

func TestVolumePoolClient_RegisterPool_Happy(t *testing.T) {
	dir := t.TempDir()
	tr, _ := newVolumePoolTransport(t, nil)

	// The transport from newVolumePoolTransport(nil paths) already has
	// an empty manager + state store wired. Use the registration flow.
	err := tr.VolumePoolClient().RegisterPool(context.Background(), transport.RegisterVolumePoolRequest{
		Name:     "secondary",
		Paths:    []string{filepath.Join(dir, "x"), filepath.Join(dir, "y")},
		Strategy: "round_robin",
	})
	if err != nil {
		t.Fatalf("RegisterPool: %v", err)
	}

	pools, err := tr.VolumePoolClient().List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(pools) != 1 || pools[0].Name != "secondary" {
		t.Errorf("expected 'secondary' pool, got %+v", pools)
	}
}

func TestVolumePoolClient_RegisterPool_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	tr, _ := newVolumePoolTransport(t, []string{a})

	err := tr.VolumePoolClient().RegisterPool(context.Background(), transport.RegisterVolumePoolRequest{
		Name:  "default",
		Paths: []string{filepath.Join(dir, "b")},
	})
	if err == nil {
		t.Fatal("expected duplicate pool error")
	}
	var dup *transport.PoolAlreadyRegisteredError
	if !errors.As(err, &dup) {
		t.Errorf("expected *transport.PoolAlreadyRegisteredError, got %T", err)
	}
}

func TestVolumePoolClient_RegisterPool_EmptyName(t *testing.T) {
	dir := t.TempDir()
	tr, _ := newVolumePoolTransport(t, nil)

	err := tr.VolumePoolClient().RegisterPool(context.Background(), transport.RegisterVolumePoolRequest{
		Name:  "",
		Paths: []string{filepath.Join(dir, "x")},
	})
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestVolumePoolClient_RegisterPool_InvalidStrategy(t *testing.T) {
	dir := t.TempDir()
	tr, _ := newVolumePoolTransport(t, nil)

	err := tr.VolumePoolClient().RegisterPool(context.Background(), transport.RegisterVolumePoolRequest{
		Name:     "x",
		Paths:    []string{filepath.Join(dir, "x")},
		Strategy: "weird",
	})
	if err == nil {
		t.Fatal("expected InvalidPoolStrategyError")
	}
	var ipe *transport.InvalidPoolStrategyError
	if !errors.As(err, &ipe) {
		t.Errorf("expected *transport.InvalidPoolStrategyError, got %T", err)
	}
}

func TestVolumePoolClient_RegisterPool_MissingStateStore(t *testing.T) {
	mgr := objpool.NewManager()
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithVolumePoolManager(mgr),
		// Note: no WithVolumePoolStateStore.
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = tr.VolumePoolClient().RegisterPool(context.Background(), transport.RegisterVolumePoolRequest{
		Name:  "x",
		Paths: []string{t.TempDir()},
	})
	if err == nil {
		t.Fatal("expected MissingVolumePoolStateStoreError")
	}
	var miss *MissingVolumePoolStateStoreError
	if !errors.As(err, &miss) {
		t.Errorf("expected *MissingVolumePoolStateStoreError, got %T", err)
	}
}

// confirm the local-storage helper actually wires up.
func TestVolumePool_BuildLocalCandidates_Direct(t *testing.T) {
	dir := t.TempDir()
	got, err := buildLocalCandidates([]string{filepath.Join(dir, "a")})
	if err != nil {
		t.Fatalf("buildLocalCandidates: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(got))
	}
	// Confirm it's a local backend.
	if _, ok := got[0].(*local.Local); !ok {
		t.Errorf("expected *local.Local, got %T", got[0])
	}
}

func TestVolumePool_BuildLocalCandidates_EmptyPathSkipped(t *testing.T) {
	got, err := buildLocalCandidates([]string{""})
	if err != nil {
		t.Fatalf("buildLocalCandidates: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 candidates for empty path, got %d", len(got))
	}
}
