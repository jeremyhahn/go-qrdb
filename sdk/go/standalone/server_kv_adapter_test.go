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

package standalone

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mapKVSvc is a map-backed KVServicer covering the methods the server's KV
// adapter calls. Like the real service, Get on an absent key returns a
// *service.KeyNotFoundError; any other method panics on the nil embedded
// interface, so a new adapter dependency fails loudly here.
type mapKVSvc struct {
	service.KVServicer
	mu    sync.RWMutex
	store map[string][]byte
}

func newMapKVSvc() *mapKVSvc {
	return &mapKVSvc{store: make(map[string][]byte)}
}

func (m *mapKVSvc) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store[key] = append([]byte(nil), value...)
	return nil
}

func (m *mapKVSvc) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.store[key]
	if !ok {
		return nil, &service.KeyNotFoundError{Key: key}
	}
	return v, nil
}

func (m *mapKVSvc) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.store, key)
	return nil
}

func (m *mapKVSvc) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.store {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (m *mapKVSvc) Scan(_ context.Context, prefix string) (map[string][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string][]byte)
	for k, v := range m.store {
		if strings.HasPrefix(k, prefix) {
			result[k] = v
		}
	}
	return result, nil
}

func TestServerKVAdapter_BasicCRUD(t *testing.T) {
	a := newServerKVAdapter(newMapKVSvc())
	ctx := context.Background()

	// Put + Get
	if err := a.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := a.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("Get got %q", got)
	}

	// Get missing → nil, nil (per KVStore contract)
	got, err = a.Get(ctx, "missing")
	if err != nil {
		t.Fatalf("Get missing: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing key, got %q", got)
	}

	// Exists for existing key
	exists, err := a.Exists(ctx, "k")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected key to exist")
	}

	// Exists for missing key → (false, nil), per the same KVStore contract
	// Get honours two blocks up: absence is a VALUE on this interface, not an
	// error.
	//
	// This block used to assert the opposite -- "adapter Exists returns false
	// with that error" -- which described what the adapter did rather than
	// what the interface promises, and so pinned the defect in place. A test
	// that records the behaviour it found cannot report the behaviour being
	// wrong.
	exists, err = a.Exists(ctx, "missing")
	if err != nil {
		t.Fatalf("Exists on a missing key returned an error: %v", err)
	}
	if exists {
		t.Fatal("expected exists=false for missing key")
	}

	// List
	keys, err := a.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0] != "k" {
		t.Fatalf("List got %v", keys)
	}

	// Scan
	scanCount := 0
	if err := a.Scan(ctx, "", func(_ string, _ []byte) error {
		scanCount++
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scanCount != 1 {
		t.Fatalf("Scan count = %d", scanCount)
	}

	// Scan with error from fn
	scanErr := errors.New("scan boom")
	if err := a.Scan(ctx, "", func(_ string, _ []byte) error {
		return scanErr
	}); !errors.Is(err, scanErr) {
		t.Fatalf("Scan should propagate fn error, got: %v", err)
	}

	// Delete
	if err := a.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// TestServerKVAdapter_IndexesRefuse pins the typed refusal.
//
// This test previously asserted that every index method returned a nil error
// (and, for the queries, a nil result). It passed, and that is exactly how the
// defect survived: a green test declared "answers every index query with an
// empty result and no error" to be the intended contract, when that answer is
// indistinguishable from a maintained index with no matches. The adapter wraps
// a bare KVServicer with no index subsystem behind it, so the honest answer is
// kvstore.ErrIndexUnsupported — which lets pkg/dao plan a full scan instead of
// concluding the entity does not exist.
func TestServerKVAdapter_IndexesRefuse(t *testing.T) {
	a := newServerKVAdapter(newMapKVSvc())
	ctx := context.Background()

	if err := a.RegisterEntityIndexes("user", nil); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("RegisterEntityIndexes = %v; want kvstore.ErrIndexUnsupported", err)
	}

	keys, err := a.QueryIndex(ctx, "x", []byte("v"))
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("QueryIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if keys != nil {
		t.Errorf("QueryIndex returned %v alongside a refusal", keys)
	}

	entries, err := a.ScanIndex(ctx, "x", []byte("a"), []byte("z"))
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("ScanIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if entries != nil {
		t.Errorf("ScanIndex returned %v alongside a refusal", entries)
	}

	if _, err := a.QueryCompoundIndex(ctx, "x", [][]byte{[]byte("v")}); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("QueryCompoundIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if _, err := a.ScanCompoundIndex(ctx, "x", [][]byte{[]byte("p")}, []byte("a"), []byte("z")); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("ScanCompoundIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
}
