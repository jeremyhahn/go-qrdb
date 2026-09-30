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
	cfg := database.DatabaseConfig{Name: name, Engine: database.EngineKV, Backend: database.BackendMemory}
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
