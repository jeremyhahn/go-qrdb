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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// pausingHost is the Raft host a materialized database's vector service
// proposes through, with one hook: once armed, the next SyncPropose -- the
// write's OpVector* proposal, made after the view's drop-fence check passed
// -- signals paused and blocks until release is closed.
type pausingHost struct {
	service.Host
	armed   atomic.Bool
	paused  chan struct{}
	release chan struct{}
}

func newPausingHost(inner service.Host) *pausingHost {
	return &pausingHost{Host: inner, paused: make(chan struct{}), release: make(chan struct{})}
}

func (h *pausingHost) SyncPropose(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
	if h.armed.CompareAndSwap(true, false) {
		close(h.paused)
		<-h.release
	}
	return h.Host.SyncPropose(ctx, shardID, cmd)
}

// applyHostMaterializingFactory materializes acme's databases as
// fakeMaterializingFactory does, but gives each one the vector service a
// lifecycle binding builds: no storage of its own, every write an OpVector*
// proposal into the database's own host (service.WithVectorApplyHost) --
// here through a pausingHost.
type applyHostMaterializingFactory struct {
	fakeMaterializingFactory
	host *pausingHost
}

func (f *applyHostMaterializingFactory) Create(cfg database.DatabaseConfig, dataDir string) (database.Backend, error) {
	b, err := f.fakeMaterializingFactory.Create(cfg, dataDir)
	if err != nil {
		return nil, err
	}
	fm, ok := b.(*fakeMaterialized)
	if !ok {
		return b, nil
	}
	log, err := logger.New(logger.LogConfig{Level: "error", Format: "json", Output: "stderr"})
	if err != nil {
		return nil, err
	}
	f.host = newPausingHost(fm.host)
	vec, err := service.NewVectorService(nil, logger.NewAdapter(log), service.WithVectorApplyHost(f.host, shard.ShardKV))
	if err != nil {
		return nil, err
	}
	fm.vector = vec
	return fm, nil
}

// materializedVectorFenceFixture is a materialized database "acme/orders"
// with a vector namespace "ns", resolved through the registry before any
// drop, and the hook that runs a write inside Manager.Drop's fence step.
type materializedVectorFenceFixture struct {
	ctx     context.Context
	dbMgr   *database.Manager
	factory *applyHostMaterializingFactory
	reclaim *writeAfterFence
	view    service.VectorServicer
}

const vectorFenceTenant, vectorFenceDB = "acme", "orders"

func newMaterializedVectorFenceFixture(t *testing.T) *materializedVectorFenceFixture {
	t.Helper()
	factory := &applyHostMaterializingFactory{}
	dbMgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(factory))
	t.Cleanup(func() { _ = dbMgr.Close() })
	reclaim := &writeAfterFence{KeyspaceReclaimService: service.NewKeyspaceReclaimService(service.ReclaimTarget{KV: newMockKVService()})}
	reclaim.SetStoreResolver(&storeResolver{databases: dbMgr, systemTenant: tenantid.System})
	dbMgr.SetKeyspaceReclaimer(reclaim)
	if _, err := dbMgr.Create(database.DatabaseConfig{TenantID: vectorFenceTenant, Name: vectorFenceDB, Backend: database.BackendMemory}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if factory.host == nil {
		t.Fatal("the database was not materialized")
	}
	r := NewRESTServiceRegistry(newFilledRegistry(), nil, tenantid.System, withDatabaseManager(dbMgr)).(*restServiceRegistry)
	svcs, err := r.resolve(vectorFenceTenant, vectorFenceDB)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if svcs.Vector == nil {
		t.Fatal("materialized bundle has no vector service")
	}
	ctx := tenant.WithTenantID(context.Background(), vectorFenceTenant)
	if _, err := svcs.Vector.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "COSINE"}); err != nil {
		if strings.Contains(err.Error(), "qrdb_vector") {
			t.Skipf("vector not built: %v", err)
		}
		t.Fatalf("CreateNamespace: %v", err)
	}
	return &materializedVectorFenceFixture{ctx: ctx, dbMgr: dbMgr, factory: factory, reclaim: reclaim, view: svcs.Vector}
}

// writeAcrossDrop starts write, waits until it is paused at its proposal --
// past the view's drop-fence check -- then drops the database and, inside
// Manager.Drop's fence step (after the fence marker is written, before the
// store is released), lets the write's proposal go and returns its error.
func (f *materializedVectorFenceFixture) writeAcrossDrop(t *testing.T, write func() error, after func()) error {
	t.Helper()
	host := f.factory.host
	host.armed.Store(true)
	done := make(chan error, 1)
	go func() { done <- write() }()
	<-host.paused

	var writeErr error
	f.reclaim.write = func() error {
		close(host.release)
		writeErr = <-done
		after()
		return nil
	}
	if err := f.dbMgr.DropForTenant(f.ctx, vectorFenceDB); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	return writeErr
}

// TestRESTServiceRegistry_MaterializedVectorUpsertDelayedAcrossDropIsRefused
// pins Codex round 4 P1 2 on #1673: an upsert through a materialized
// database's vector view that passed the drop-fence check before DROP
// DATABASE began, and proposed only after the drop fenced the database, is
// refused by the state machine -- its proposal carries the database's write
// guard -- and its vector never lands.
func TestRESTServiceRegistry_MaterializedVectorUpsertDelayedAcrossDropIsRefused(t *testing.T) {
	f := newMaterializedVectorFenceFixture(t)
	var got *service.GetVectorResponse
	var readErr error
	err := f.writeAcrossDrop(t, func() error {
		_, err := f.view.UpsertVector(f.ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: 7, Vector: []float32{1, 0}})
		return err
	}, func() {
		// Read through the same pre-drop handle while the store is still
		// open: the refused upsert left nothing behind.
		got, readErr = f.view.GetVector(f.ctx, &service.GetVectorRequest{Namespace: "ns", ID: 7})
	})
	assertDroppingError(t, err, vectorFenceDB)
	var notFound *service.VectorNotFoundError
	if !errors.As(readErr, &notFound) && (readErr != nil || got == nil || got.Found) {
		t.Fatalf("vector 7 after its refused upsert = %+v, %v; want not found", got, readErr)
	}
}

// TestRESTServiceRegistry_MaterializedVectorDeleteDelayedAcrossDropIsRefused
// is the delete twin: a vector delete delayed across the drop is refused at
// apply time, and the vector it named is not removed by it.
func TestRESTServiceRegistry_MaterializedVectorDeleteDelayedAcrossDropIsRefused(t *testing.T) {
	f := newMaterializedVectorFenceFixture(t)
	if _, err := f.view.UpsertVector(f.ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: 9, Vector: []float32{0, 1}}); err != nil {
		t.Fatalf("UpsertVector before the drop: %v", err)
	}

	var got *service.GetVectorResponse
	var readErr error
	err := f.writeAcrossDrop(t, func() error {
		_, err := f.view.DeleteVector(f.ctx, &service.DeleteVectorRequest{Namespace: "ns", ID: 9})
		return err
	}, func() {
		got, readErr = f.view.GetVector(f.ctx, &service.GetVectorRequest{Namespace: "ns", ID: 9})
	})
	assertDroppingError(t, err, vectorFenceDB)
	if readErr != nil || got == nil || !got.Found {
		t.Fatalf("vector 9 after its refused delete = %+v, %v; want it still stored", got, readErr)
	}
}
