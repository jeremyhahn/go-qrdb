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

package lifecycle_test

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// dropRaceMarker tags every value a racing writer writes, so a file left
// under the node data dir that holds one is found by content.
const dropRaceMarker = "drop-race-writer-marker"

// vectorBuilt creates namespace ns (dimension 2) on mb's vector service and
// reports false when the vector engine is not compiled in (qrdb_vector).
func vectorBuilt(ctx context.Context, t *testing.T, mb *lifecycle.MaterializedBinding) bool {
	t.Helper()
	if mb.Vector() == nil {
		t.Fatal("binding has no vector service")
	}
	_, err := mb.Vector().CreateNamespace(ctx, &service.CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "COSINE"})
	if err != nil && strings.Contains(err.Error(), "qrdb_vector") {
		t.Logf("vector not built (%v): its cell runs under make test", err)
		return false
	}
	if err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	return true
}

// vectorGone fails t when vector id is still readable in namespace ns.
func vectorGone(ctx context.Context, t *testing.T, mb *lifecycle.MaterializedBinding, id uint64) {
	t.Helper()
	resp, err := mb.Vector().GetVector(ctx, &service.GetVectorRequest{Namespace: "ns", ID: id, IncludeVector: true})
	if err == nil && resp != nil {
		t.Fatalf("recreated database holds the dropped vector %d: %+v", id, resp)
	}
}

// assertNoDropMarkerUnder fails t when any file under dir holds a seal or
// drop-race marker. Unlike assertNoMarkerUnder it accepts an empty dir: a
// drop may leave nothing at all.
func assertNoDropMarkerUnder(t *testing.T, dir string) {
	t.Helper()
	scoped, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot %s: %v", dir, err)
	}
	defer func() { _ = scoped.Close() }()
	rootFS := scoped.FS()
	markers := append([]string{dropRaceMarker}, sealMarkers...)
	err = fs.WalkDir(rootFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(rootFS, path)
		if err != nil {
			return err
		}
		for _, m := range markers {
			if bytes.Contains(data, []byte(m)) {
				t.Errorf("%s holds the dropped marker %q", path, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir %s: %v", dir, err)
	}
}

// assertRootGone fails t when root, or any path under it, survives.
func assertRootGone(t *testing.T, root string) {
	t.Helper()
	if root == "" {
		return
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("binding root %s survives the drop (%v)", root, err)
	}
}

// TestLocalBindingFactory_DropRemovesEveryEngine_EveryBackend is the
// engineering.md "Drop under a binding" test over the real factory: a
// materialized database written on every engine, vectors included, is
// dropped; its binding root is gone, no file under the node data dir holds
// a value it wrote, and the name created again is empty on every engine.
func TestLocalBindingFactory_DropRemovesEveryEngine_EveryBackend(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), bindingTenant)
	for _, b := range []database.BackendType{database.BackendMemory, database.BackendFile, database.BackendPebble, database.BackendObjstore} {
		t.Run(string(b), func(t *testing.T) {
			dir := t.TempDir()
			mgr := newBindingManager(t, dir, nil)
			defer func() { _ = mgr.Close() }()
			cfg := database.DatabaseConfig{TenantID: bindingTenant, Name: "gone", Backend: b}
			db, err := mgr.Create(cfg)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			mb := materialized(t, db)
			root := mb.DataRoot()
			if b != database.BackendMemory && root == "" {
				t.Fatalf("%s binding has no data root", b)
			}
			live := newSealEngines(mb.Host(), mb.ShardID())
			docID, withTS := live.writeEveryEngine(t, "gone")
			assertControl(t, "open store", live.readEveryEngine("gone", docID, withTS))
			withVec := vectorBuilt(ctx, t, mb)
			if withVec {
				if _, err := mb.Vector().UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: 3, Vector: []float32{0, 1}}); err != nil {
					t.Fatalf("UpsertVector: %v", err)
				}
				if resp, err := mb.Vector().GetVector(ctx, &service.GetVectorRequest{Namespace: "ns", ID: 3}); err != nil || resp == nil {
					t.Fatalf("control: GetVector before the drop = %v, %v", resp, err)
				}
			}

			if err := mgr.DropForTenant(ctx, "gone"); err != nil {
				t.Fatalf("DropForTenant: %v", err)
			}
			assertRootGone(t, root)
			assertNoDropMarkerUnder(t, dir)

			db, err = mgr.Create(cfg)
			if err != nil {
				t.Fatalf("re-Create: %v", err)
			}
			again := materialized(t, db)
			fresh := newSealEngines(again.Host(), again.ShardID())
			for _, r := range fresh.readEveryEngine("gone", docID, withTS) {
				if r.err == nil && strings.Contains(r.got, r.marker) {
					t.Errorf("recreated database: %s still reads the dropped %q", r.engine, r.got)
				}
				if r.engine == "index" && r.err == nil && r.got != "0" {
					t.Errorf("recreated database: index returns %s hits for the dropped document", r.got)
				}
			}
			if withVec {
				vectorGone(ctx, t, again, 3)
			}
		})
	}
}

// raceEngine is one engine a writer races a drop on: write writes the i-th
// value through a handle obtained before the drop, and gone fails t when any
// written value is readable on the recreated database.
type raceEngine struct {
	name  string
	setup func(ctx context.Context, t *testing.T, mb *lifecycle.MaterializedBinding) bool
	write func(ctx context.Context, mb *lifecycle.MaterializedBinding, i int) (uint64, error)
	gone  func(ctx context.Context, t *testing.T, mb *lifecycle.MaterializedBinding, i int, id uint64)
}

var raceAt = time.Unix(1_700_000_000, 0)

func raceEngines() []raceEngine {
	value := func(i int) []byte { return []byte(fmt.Sprintf("%s-%d", dropRaceMarker, i)) }
	noSetup := func(context.Context, *testing.T, *lifecycle.MaterializedBinding) bool { return true }
	return []raceEngine{
		{
			name:  "kv",
			setup: noSetup,
			write: func(ctx context.Context, mb *lifecycle.MaterializedBinding, i int) (uint64, error) {
				return 0, service.NewKVService(mb.Host(), mb.ShardID()).Put(ctx, fmt.Sprintf("race-kv-%d", i), value(i))
			},
			gone: func(ctx context.Context, t *testing.T, mb *lifecycle.MaterializedBinding, i int, _ uint64) {
				if got, err := service.NewKVService(mb.Host(), mb.ShardID()).Get(ctx, fmt.Sprintf("race-kv-%d", i)); err == nil && got != nil {
					t.Errorf("recreated database holds the racing KV write %d: %q", i, got)
				}
			},
		},
		{
			name:  "mvcc",
			setup: noSetup,
			write: func(ctx context.Context, mb *lifecycle.MaterializedBinding, i int) (uint64, error) {
				return 0, service.NewMVCCKVService(mb.Host(), mb.ShardID()).Put(ctx, fmt.Sprintf("race-mvcc-%d", i), value(i))
			},
			gone: func(ctx context.Context, t *testing.T, mb *lifecycle.MaterializedBinding, i int, _ uint64) {
				if got, err := service.NewMVCCKVService(mb.Host(), mb.ShardID()).Get(ctx, fmt.Sprintf("race-mvcc-%d", i)); err == nil && got != nil {
					t.Errorf("recreated database holds the racing MVCC write %d: %q", i, got)
				}
			},
		},
		{
			name: "collection",
			setup: func(ctx context.Context, t *testing.T, mb *lifecycle.MaterializedBinding) bool {
				t.Helper()
				coll := newSealEngines(mb.Host(), mb.ShardID()).coll
				if _, err := coll.CreateCollection(ctx, &service.CreateCollectionRequest{Name: "race", Database: "racy"}); err != nil {
					t.Fatalf("CreateCollection: %v", err)
				}
				return true
			},
			write: func(ctx context.Context, mb *lifecycle.MaterializedBinding, i int) (uint64, error) {
				coll := newSealEngines(mb.Host(), mb.ShardID()).coll
				ins, err := coll.InsertDocument(ctx, &service.InsertDocumentRequest{
					Collection: "race", Database: "racy", Document: map[string]interface{}{"v": string(value(i))},
				})
				if err != nil {
					return 0, err
				}
				return ins.ID, nil
			},
			gone: func(ctx context.Context, t *testing.T, mb *lifecycle.MaterializedBinding, i int, id uint64) {
				coll := newSealEngines(mb.Host(), mb.ShardID()).coll
				doc, err := coll.GetDocument(ctx, &service.GetDocumentRequest{Collection: "race", Database: "racy", ID: id})
				if err == nil && doc != nil {
					t.Errorf("recreated database holds the racing document %d (id %d): %v", i, id, doc.Document)
				}
			},
		},
		{
			name:  "index",
			setup: noSetup,
			write: func(ctx context.Context, mb *lifecycle.MaterializedBinding, i int) (uint64, error) {
				return 0, service.NewIndexService(mb.Host(), mb.ShardID()).CreateIndex(ctx, fmt.Sprintf("race-idx-%d", i), "v", index.IndexConfig{})
			},
			gone: func(ctx context.Context, t *testing.T, mb *lifecycle.MaterializedBinding, i int, _ uint64) {
				// An index the recreated database does not hold answers not
				// found; any other answer means the racing create survived.
				_, err := service.NewIndexService(mb.Host(), mb.ShardID()).Query(ctx, fmt.Sprintf("race-idx-%d", i), value(i))
				if err == nil {
					t.Errorf("recreated database holds the racing index race-idx-%d", i)
				}
			},
		},
		{
			name: "timeseries",
			setup: func(ctx context.Context, t *testing.T, mb *lifecycle.MaterializedBinding) bool {
				t.Helper()
				err := service.NewTimeSeriesService(mb.Host(), mb.ShardID()).Write(ctx, "race.metric", 1, raceAt.Add(-time.Minute), nil)
				if err != nil && strings.Contains(err.Error(), "not compiled") {
					t.Skipf("time series not compiled in (%v): runs under make test", err)
				}
				if err != nil {
					t.Fatalf("TimeSeries Write: %v", err)
				}
				return true
			},
			write: func(ctx context.Context, mb *lifecycle.MaterializedBinding, i int) (uint64, error) {
				return 0, service.NewTimeSeriesService(mb.Host(), mb.ShardID()).Write(ctx, "race.metric", float64(i), raceAt.Add(time.Duration(i)*time.Second), map[string]string{"m": string(value(i))})
			},
			gone: func(ctx context.Context, t *testing.T, mb *lifecycle.MaterializedBinding, i int, _ uint64) {
				at := raceAt.Add(time.Duration(i) * time.Second)
				points, err := service.NewTimeSeriesService(mb.Host(), mb.ShardID()).Query(ctx, "race.metric", at, at.Add(time.Millisecond))
				if err == nil && len(points) != 0 {
					t.Errorf("recreated database holds the racing point %d: %v", i, points)
				}
			},
		},
		{
			name: "vector",
			setup: func(ctx context.Context, t *testing.T, mb *lifecycle.MaterializedBinding) bool {
				t.Helper()
				if !vectorBuilt(ctx, t, mb) {
					t.Skip("vector not compiled in: runs under make test")
				}
				return true
			},
			write: func(ctx context.Context, mb *lifecycle.MaterializedBinding, i int) (uint64, error) {
				_, err := mb.Vector().UpsertVector(ctx, &service.UpsertVectorRequest{Namespace: "ns", ID: uint64(i + 1), Vector: []float32{1, float32(i + 1)}})
				return uint64(i + 1), err
			},
			gone: func(ctx context.Context, t *testing.T, mb *lifecycle.MaterializedBinding, _ int, id uint64) {
				t.Helper()
				vectorGone(ctx, t, mb, id)
			},
		},
	}
}

// TestLocalBindingFactory_WriterRacingDropIsRefused is the engineering.md
// "Drop under a binding" race over the real factory: on every engine, a
// writer holding a handle obtained before the drop writes in a loop while
// DropForTenant runs. Once the drop returns the writer's next write is
// refused, and the name created again holds none of the writer's values,
// checked by exact key, ID, index name or timestamp.
func TestLocalBindingFactory_WriterRacingDropIsRefused(t *testing.T) {
	ctx := tenant.WithTenantID(context.Background(), bindingTenant)
	for _, eng := range raceEngines() {
		t.Run(eng.name, func(t *testing.T) {
			dir := t.TempDir()
			mgr := newBindingManager(t, dir, nil)
			defer func() { _ = mgr.Close() }()
			cfg := database.DatabaseConfig{TenantID: bindingTenant, Name: "racy", Backend: database.BackendPebble}
			db, err := mgr.Create(cfg)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			mb := materialized(t, db)
			root := mb.DataRoot()
			eng.setup(ctx, t, mb)

			var (
				mu       sync.Mutex
				attempts []uint64 // the ID each attempt i returned
			)
			started := make(chan struct{})
			dropped := make(chan struct{})
			final := make(chan error, 1)
			go func() {
				var once sync.Once
				for i := 0; ; i++ {
					select {
					case <-dropped:
						id, err := eng.write(ctx, mb, i)
						mu.Lock()
						attempts = append(attempts, id)
						mu.Unlock()
						final <- err
						return
					default:
					}
					id, err := eng.write(ctx, mb, i)
					mu.Lock()
					attempts = append(attempts, id)
					mu.Unlock()
					if err == nil {
						once.Do(func() { close(started) })
					} else if i == 0 {
						// The very first write, before any drop, must land:
						// without it the race below is vacuous.
						final <- fmt.Errorf("control: first write before the drop failed: %w", err)
						close(started)
						return
					}
				}
			}()
			<-started
			if err := mgr.DropForTenant(ctx, "racy"); err != nil {
				t.Fatalf("DropForTenant: %v", err)
			}
			close(dropped)
			err = <-final
			if err != nil && strings.HasPrefix(err.Error(), "control:") {
				t.Fatal(err)
			}
			if err == nil {
				t.Fatalf("%s: a write on a pre-drop handle succeeded after the drop returned", eng.name)
			}
			assertRootGone(t, root)
			assertNoDropMarkerUnder(t, dir)

			db, err = mgr.Create(cfg)
			if err != nil {
				t.Fatalf("re-Create: %v", err)
			}
			again := materialized(t, db)
			mu.Lock()
			defer mu.Unlock()
			for i, id := range attempts {
				eng.gone(ctx, t, again, i, id)
			}
		})
	}
}
