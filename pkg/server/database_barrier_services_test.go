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
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// TestDedicatedStoreServiceCache_EvictsADroppedDatabase: a database the
// manager has dropped is never served again, so the cache must not keep it,
// and the backend and object store it carries, for the life of the process.
// The drop is the manager's own, over an objstore database.
func TestDedicatedStoreServiceCache_EvictsADroppedDatabase(t *testing.T) {
	open := func(string, string, string) (storage.Storage, database.ObjstoreReclaim, error) {
		e, err := objstore.New("memory", map[string]string{})
		if err != nil {
			return nil, nil, err
		}
		return e, e.Destroy, nil
	}
	mgr := databasetest.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir(),
		database.WithBackendFactory(database.NewEmbeddedBackendFactory(database.WithObjstoreOpener(open))))
	t.Cleanup(func() { _ = mgr.Close() })
	dropped, err := mgr.Create(database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore})
	if err != nil {
		t.Fatalf("Create(blobs): %v", err)
	}
	kept, err := mgr.Create(database.DatabaseConfig{Name: "kept", Backend: database.BackendObjstore})
	if err != nil {
		t.Fatalf("Create(kept): %v", err)
	}

	c := newDedicatedStoreServiceCache()
	evicted := make(chan dedicatedStoreKey, 1)
	c.onEvict = func(key dedicatedStoreKey) { evicted <- key }
	c.getOrBuild(dropped)
	keptSvcs := c.getOrBuild(kept)
	if err := mgr.Drop(context.Background(), "blobs"); err != nil {
		t.Fatalf("Drop(blobs): %v", err)
	}
	select {
	case key := <-evicted:
		if key.name != "blobs" {
			t.Fatalf("evicted %v, want the dropped database", key)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the drop did not evict the dropped database's entry")
	}
	if again := c.getOrBuild(kept); again != keptSvcs {
		t.Fatal("the drop of another database evicted a live database's bundle")
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	for key, e := range c.entries {
		if e.db == dropped {
			t.Fatalf("the dropped database %v is still cached", key)
		}
	}
	if len(c.entries) != 1 {
		t.Fatalf("cache holds %d entries, want 1 (the live database)", len(c.entries))
	}
}

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
