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

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// newBarrierServicesTestDatabase builds a *database.Database backed by an
// in-memory standalone Backend, standing in for a dedicated-store database's
// own encrypting Backend() without needing a real DatabaseBarrierManager --
// buildDedicatedStoreServices only cares about db.Backend()/db.Dropping(),
// not about barrier state.
func newBarrierServicesTestDatabase(t *testing.T, name string) *database.Database {
	t.Helper()
	backend := database.NewStandaloneBackend(memory.NewStorage())
	cfg := database.DatabaseConfig{Name: name, Backend: database.BackendMemory}
	return database.NewDatabase(cfg, backend, t.TempDir())
}

// ---------------------------------------------------------------------------
// go-qrdb#482 round-2 finding (1), the dedicated-store-barrier path: a
// caller that resolved buildDedicatedStoreServices' KV bundle before a
// concurrent Manager.Drop set the fence on the SAME *database.Database must
// still be refused when it actually writes, not only when it resolves.
// ---------------------------------------------------------------------------

// TestBuildDedicatedStoreServices_RefusesWriteOnHandleResolvedBeforeTheFence
// is the failing-first regression: the KV handle is resolved first --
// succeeding, because nothing is fenced yet -- and the fence is set
// afterward, directly on the same *database.Database the handle was built
// over, standing in for a Drop that fences the database while a caller
// holds a handle obtained a moment earlier.
func TestBuildDedicatedStoreServices_RefusesWriteOnHandleResolvedBeforeTheFence(t *testing.T) {
	db := newBarrierServicesTestDatabase(t, "orders")
	svcs := buildDedicatedStoreServices(db)

	if err := svcs.KV.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put() before the fence = %v, want success", err)
	}

	db.SetDropping(true)

	err := svcs.KV.Put(context.Background(), "k2", []byte("v2"))
	var dropping *database.DroppingError
	if !errors.As(err, &dropping) {
		t.Fatalf("Put() on a handle resolved before the fence = %v (%T), want *database.DroppingError", err, err)
	}
}

// TestBuildDedicatedStoreServices_AllowsWriteBeforeTheFence is the control:
// an ordinary write on a freshly built, non-dropping bundle must still
// succeed.
func TestBuildDedicatedStoreServices_AllowsWriteBeforeTheFence(t *testing.T) {
	db := newBarrierServicesTestDatabase(t, "orders")
	svcs := buildDedicatedStoreServices(db)

	if err := svcs.KV.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put() error = %v, want success", err)
	}
	got, err := svcs.KV.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if string(got) != "v" {
		t.Errorf("Get() = %q, want %q", got, "v")
	}
}

// closeRecordingVector is a bundle's vector service that records its Close.
type closeRecordingVector struct {
	service.VectorServicer
	closed bool
}

func (v *closeRecordingVector) Close() error {
	v.closed = true
	return nil
}

func newEvictTestDatabase(t *testing.T, tenantID, name string) *database.Database {
	t.Helper()
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	return database.NewDatabase(
		database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: database.BackendMemory},
		database.NewStandaloneBackend(store),
		t.TempDir(),
	)
}

// TestMaterializedServiceCache_DropEvictsTheEntry proves a dropped
// database's bundle leaves the cache and its vector service (the HNSW
// indexes) is closed, while another tenant's same-named entry stays.
func TestMaterializedServiceCache_DropEvictsTheEntry(t *testing.T) {
	c := newMaterializedServiceCache()
	db := newEvictTestDatabase(t, "acme", "orders")
	other := newEvictTestDatabase(t, "globex", "orders")
	vec := &closeRecordingVector{}
	otherVec := &closeRecordingVector{}
	c.getOrBuild(db, func() *DatabaseServices { return &DatabaseServices{Name: "orders", Vector: vec} })
	c.getOrBuild(other, func() *DatabaseServices { return &DatabaseServices{Name: "orders", Vector: otherVec} })

	c.evict("acme", "orders")

	c.mu.RLock()
	_, kept := c.entries["acme\x00orders"]
	_, otherKept := c.entries["globex\x00orders"]
	c.mu.RUnlock()
	if kept {
		t.Error("the dropped database's entry is still cached")
	}
	if !vec.closed {
		t.Error("the dropped database's vector service was not closed")
	}
	if !otherKept || otherVec.closed {
		t.Errorf("another tenant's entry: kept %v, closed %v; want kept, open", otherKept, otherVec.closed)
	}
}

// TestDedicatedStoreServiceCache_DropEvictsTheEntry proves a dropped
// database's dedicated-store bundle leaves the cache and is closed.
func TestDedicatedStoreServiceCache_DropEvictsTheEntry(t *testing.T) {
	c := newDedicatedStoreServiceCache()
	db := newEvictTestDatabase(t, "acme", "orders")
	c.getOrBuild(db)
	vec := &closeRecordingVector{}
	c.mu.Lock()
	c.entries["orders"].svcs.Vector = vec
	c.mu.Unlock()

	c.evict("globex", "orders")
	c.mu.RLock()
	_, kept := c.entries["orders"]
	c.mu.RUnlock()
	if !kept || vec.closed {
		t.Fatalf("another tenant's drop evicted the entry: kept %v, closed %v", kept, vec.closed)
	}

	c.evict("acme", "orders")
	c.mu.RLock()
	_, kept = c.entries["orders"]
	c.mu.RUnlock()
	if kept {
		t.Error("the dropped database's entry is still cached")
	}
	if !vec.closed {
		t.Error("the dropped database's vector service was not closed")
	}
}
