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
	"os"
	"strings"
	"testing"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// dropEngineWrite is one engine's write through a resolved DatabaseServices
// handle; i makes every write's key, ID, index name and timestamp distinct.
type dropEngineWrite struct {
	name  string
	write func(ctx context.Context, s *DatabaseServices, i int) error
	// gone fails t when write i is readable through s.
	gone func(ctx context.Context, t *testing.T, s *DatabaseServices, i int)
}

var dropTSAt = time.Unix(1_700_000_000, 0)

func dropEngineWrites(withTS, withVec bool) []dropEngineWrite {
	key := func(p string, i int) string { return p + "-" + string(rune('a'+i)) }
	ws := []dropEngineWrite{
		{
			name: "kv",
			write: func(ctx context.Context, s *DatabaseServices, i int) error {
				return s.KV.Put(ctx, key("kv", i), []byte("v"))
			},
			gone: func(ctx context.Context, t *testing.T, s *DatabaseServices, i int) {
				if got, err := s.KV.Get(ctx, key("kv", i)); err == nil && got != nil {
					t.Errorf("recreated database holds KV %s = %q", key("kv", i), got)
				}
			},
		},
		{
			name: "mvcc",
			write: func(ctx context.Context, s *DatabaseServices, i int) error {
				return s.MVCC.Put(ctx, key("mvcc", i), []byte("v"))
			},
			gone: func(ctx context.Context, t *testing.T, s *DatabaseServices, i int) {
				if got, err := s.MVCC.Get(ctx, key("mvcc", i)); err == nil && got != nil {
					t.Errorf("recreated database holds MVCC %s = %q", key("mvcc", i), got)
				}
			},
		},
		{
			name: "collection",
			write: func(ctx context.Context, s *DatabaseServices, i int) error {
				_, err := s.Collection.CreateCollection(ctx, &service.CreateCollectionRequest{Name: key("coll", i), Database: "orders"})
				return err
			},
			gone: func(ctx context.Context, t *testing.T, s *DatabaseServices, i int) {
				if _, err := s.Collection.GetCollection(ctx, &service.GetCollectionRequest{Name: key("coll", i), Database: "orders"}); err == nil {
					t.Errorf("recreated database holds collection %s", key("coll", i))
				}
			},
		},
		{
			name: "index",
			write: func(ctx context.Context, s *DatabaseServices, i int) error {
				return s.Index.CreateIndex(ctx, key("idx", i), "f", index.IndexConfig{})
			},
			gone: func(ctx context.Context, t *testing.T, s *DatabaseServices, i int) {
				if _, err := s.Index.Query(ctx, key("idx", i), []byte("v")); err == nil {
					t.Errorf("recreated database holds index %s", key("idx", i))
				}
			},
		},
	}
	if withTS {
		ws = append(ws, dropEngineWrite{
			name: "timeseries",
			write: func(ctx context.Context, s *DatabaseServices, i int) error {
				return s.TimeSeries.Write(ctx, "drop.metric", float64(i), dropTSAt.Add(time.Duration(i)*time.Second), nil)
			},
			gone: func(ctx context.Context, t *testing.T, s *DatabaseServices, i int) {
				at := dropTSAt.Add(time.Duration(i) * time.Second)
				if pts, err := s.TimeSeries.Query(ctx, "drop.metric", at, at.Add(time.Millisecond)); err == nil && len(pts) != 0 {
					t.Errorf("recreated database holds time-series point %d: %v", i, pts)
				}
			},
		})
	}
	if withVec {
		ws = append(ws, dropEngineWrite{
			name: "vector",
			write: func(ctx context.Context, s *DatabaseServices, i int) error {
				_, err := s.Vector.UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: uint64(i + 1), Vector: []float32{1, float32(i + 1)}})
				return err
			},
			gone: func(ctx context.Context, t *testing.T, s *DatabaseServices, i int) {
				if resp, err := s.Vector.GetVector(ctx, &service.GetVectorRequest{Namespace: "ns", ID: uint64(i + 1)}); err == nil && resp != nil {
					t.Errorf("recreated database holds vector %d: %+v", i+1, resp)
				}
			},
		})
	}
	return ws
}

// TestRESTServiceRegistry_MaterializedDropRefusesRacingWritersOnEveryEngine
// is the engineering.md "Drop under a binding" test through the registry
// over the real lifecycle.LocalBindingFactory: every engine's handle
// resolved before the drop writes, the database is dropped, and every one
// of those handles is refused afterwards (fenced or closed); the binding
// root is gone and the name created again resolves to an empty database.
func TestRESTServiceRegistry_MaterializedDropRefusesRacingWritersOnEveryEngine(t *testing.T) {
	const tenantID, name = "acme", "orders"
	ctx := tenant.WithTenantID(context.Background(), tenantID)
	for _, b := range []database.BackendType{database.BackendMemory, database.BackendFile, database.BackendPebble, database.BackendObjstore} {
		t.Run(string(b), func(t *testing.T) {
			dir := t.TempDir()
			factory := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir}, nil, qrdbconfig.StorageConfig{},
				func(id string) (database.BindingTenant, error) {
					return database.BindingTenant{ID: id, Isolation: tenant.IsolationPrefix}, nil
				})
			dbMgr := databasetest.NewManager(nil, dir, database.WithBackendFactory(factory))
			t.Cleanup(func() { _ = dbMgr.Close() })
			cfg := database.DatabaseConfig{TenantID: tenantID, Name: name, Backend: b}
			db, err := dbMgr.Create(cfg)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			mb, ok := db.Backend().(*lifecycle.MaterializedBinding)
			if !ok {
				t.Fatalf("db.Backend() = %T, want *lifecycle.MaterializedBinding", db.Backend())
			}
			root := mb.DataRoot()
			r := NewRESTServiceRegistry(newFilledRegistry(), nil, tenantid.System, withDatabaseManager(dbMgr)).(*restServiceRegistry)
			stale, err := r.resolve(tenantID, name)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if stale.Placement != service.StorePlacementNode {
				t.Fatalf("Placement = %q, want %q", stale.Placement, service.StorePlacementNode)
			}

			err = stale.TimeSeries.Write(ctx, "drop.metric", 1, dropTSAt.Add(-time.Minute), nil)
			withTS := err == nil
			if err != nil && !strings.Contains(err.Error(), "not compiled") {
				t.Fatalf("TimeSeries Write: %v", err)
			}
			_, err = stale.Vector.CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "COSINE"})
			withVec := err == nil
			if err != nil && !strings.Contains(err.Error(), "qrdb_vector") {
				t.Fatalf("CreateNamespace: %v", err)
			}
			engines := dropEngineWrites(withTS, withVec)

			// Control: every engine's pre-drop handle writes, so the
			// refusals below are not vacuous.
			for _, e := range engines {
				if err := e.write(ctx, stale, 0); err != nil {
					t.Fatalf("control: %s write before the drop: %v", e.name, err)
				}
			}
			if err := dbMgr.DropForTenant(ctx, name); err != nil {
				t.Fatalf("DropForTenant: %v", err)
			}
			for _, e := range engines {
				if err := e.write(ctx, stale, 1); err == nil {
					t.Errorf("%s: a handle resolved before the drop accepted a write after it", e.name)
				}
			}
			if root != "" {
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("binding root %s survives the drop (%v)", root, err)
				}
			}

			if _, err := dbMgr.Create(cfg); err != nil {
				t.Fatalf("recreate: %v", err)
			}
			fresh, err := r.resolve(tenantID, name)
			if err != nil {
				t.Fatalf("resolve the recreated database: %v", err)
			}
			for _, e := range engines {
				for i := 0; i < 2; i++ {
					e.gone(ctx, t, fresh, i)
				}
				if err := e.write(ctx, stale, 2); err == nil {
					t.Errorf("%s: a handle from the dropped incarnation accepted a write after the name was recreated", e.name)
				}
			}
			if err := fresh.KV.Put(ctx, "fresh", []byte("v")); err != nil {
				t.Fatalf("Put on the recreated database: %v", err)
			}
		})
	}
}
