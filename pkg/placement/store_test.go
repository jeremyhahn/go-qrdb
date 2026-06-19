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

package placement

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
)

// fakeKV is an in-memory KV harness modeled on the membership-store test fake:
// it implements the placement KV substrate and (optionally) the Watcher seam.
type fakeKV struct {
	mu      sync.Mutex
	data    map[string][]byte
	watchCh chan WatchEvent
}

func newFakeKV() *fakeKV { return &fakeKV{data: make(map[string][]byte)} }

func (f *fakeKV) Put(_ context.Context, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	f.data[key] = cp
	return nil
}

func (f *fakeKV) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[key]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (f *fakeKV) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data, key)
	return nil
}

func (f *fakeKV) Scan(_ context.Context, prefix string, fn func(string, []byte) error) error {
	f.mu.Lock()
	keys := make([]string, 0, len(f.data))
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	snapshot := make(map[string][]byte, len(keys))
	for _, k := range keys {
		snapshot[k] = f.data[k]
	}
	f.mu.Unlock()
	sort.Strings(keys)
	for _, k := range keys {
		if err := fn(k, snapshot[k]); err != nil {
			return err
		}
	}
	return nil
}

// watchKV embeds fakeKV and adds the Watcher capability.
type watchKV struct {
	*fakeKV
	ch chan WatchEvent
}

func newWatchKV() *watchKV {
	return &watchKV{fakeKV: newFakeKV(), ch: make(chan WatchEvent, 8)}
}

func (w *watchKV) Watch(ctx context.Context, _ string) (<-chan WatchEvent, error) {
	return w.ch, nil
}

func samplePlacement(tenant, db string, shardID uint64) *Placement {
	return &Placement{
		TenantID:        tenant,
		Database:        db,
		ShardID:         shardID,
		RF:              2,
		LeaderReplicaID: 1,
		Replicas: []Replica{
			{ReplicaID: 1, NodeID: "n1", RaftAddr: "n1:1", DataRoot: "/d/n1"},
			{ReplicaID: 2, NodeID: "n2", RaftAddr: "n2:1", DataRoot: "/d/n2"},
		},
	}
}

func TestNewStore_NilKV(t *testing.T) {
	if _, err := NewStore(nil); !errors.Is(err, ErrNilKV) {
		t.Fatalf("NewStore(nil) error = %v, want ErrNilKV", err)
	}
}

// TestStore_RoundTrip exercises Put/Get/Delete against the in-memory KV harness.
func TestStore_RoundTrip(t *testing.T) {
	s, err := NewStore(newFakeKV())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	want := samplePlacement("acme", "orders", 1000)
	if err := s.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get(ctx, "acme", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ShardID != want.ShardID || got.RF != want.RF || len(got.Replicas) != 2 {
		t.Fatalf("round-trip mismatch: got %+v", got)
	}
	if got.Replicas[1].DataRoot != "/d/n2" {
		t.Errorf("replica data root = %q, want /d/n2", got.Replicas[1].DataRoot)
	}

	if err := s.Delete(ctx, "acme", "orders"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "acme", "orders"); !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("Get after delete error = %v, want ErrPlacementNotFound", err)
	}
}

// TestStore_GetNotFound verifies a missing entry is a typed not-found error.
func TestStore_GetNotFound(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	_, err := s.Get(context.Background(), "nope", "db")
	if !errors.Is(err, ErrPlacementNotFound) {
		t.Fatalf("error = %v, want ErrPlacementNotFound", err)
	}
}

// TestStore_List returns all placements and ListForTenant filters by tenant.
func TestStore_List(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	ctx := context.Background()
	for _, p := range []*Placement{
		samplePlacement("acme", "orders", 1000),
		samplePlacement("acme", "users", 1001),
		samplePlacement("beta", "logs", 1002),
	} {
		if err := s.Put(ctx, p); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	all, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("List len = %d, want 3", len(all))
	}

	acme, err := s.ListForTenant(ctx, "acme")
	if err != nil {
		t.Fatalf("ListForTenant: %v", err)
	}
	if len(acme) != 2 {
		t.Fatalf("ListForTenant(acme) len = %d, want 2", len(acme))
	}
	for _, p := range acme {
		if p.TenantID != "acme" {
			t.Errorf("ListForTenant returned foreign tenant %q", p.TenantID)
		}
	}
}

// TestStore_PutValidation rejects placements missing identity.
func TestStore_PutValidation(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	ctx := context.Background()
	if err := s.Put(ctx, &Placement{Database: "db"}); !errors.Is(err, ErrEmptyTenant) {
		t.Errorf("Put without tenant error = %v, want ErrEmptyTenant", err)
	}
	if err := s.Put(ctx, &Placement{TenantID: "t"}); !errors.Is(err, ErrEmptyDatabase) {
		t.Errorf("Put without database error = %v, want ErrEmptyDatabase", err)
	}
}

// TestStore_WatchUnsupported returns ErrWatchUnsupported for a KV without the
// Watcher capability.
func TestStore_WatchUnsupported(t *testing.T) {
	s, _ := NewStore(newFakeKV())
	if _, err := s.Watch(context.Background()); !errors.Is(err, ErrWatchUnsupported) {
		t.Fatalf("Watch error = %v, want ErrWatchUnsupported", err)
	}
}

// TestStore_Watch translates backend events into typed placement events.
func TestStore_Watch(t *testing.T) {
	kv := newWatchKV()
	s, _ := NewStore(kv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := s.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	p := samplePlacement("acme", "orders", 1000)
	val, _ := s.kvMarshal(p)
	kv.ch <- WatchEvent{Key: placementKey("acme", "orders"), Value: val}

	ev := <-events
	if ev.Type != EventPut || ev.TenantID != "acme" || ev.Database != "orders" {
		t.Fatalf("put event = %+v, want EventPut acme/orders", ev)
	}
	if ev.Placement == nil || ev.Placement.ShardID != 1000 {
		t.Fatalf("put event placement = %+v, want shard 1000", ev.Placement)
	}

	// A delete carrying the prior value recovers the identity.
	kv.ch <- WatchEvent{Deleted: true, Key: placementKey("acme", "orders"), PrevValue: val}
	ev = <-events
	if ev.Type != EventDelete || ev.TenantID != "acme" || ev.Database != "orders" {
		t.Fatalf("delete event = %+v, want EventDelete acme/orders", ev)
	}
}

// kvMarshal is a tiny test helper exposing the JSON encoding the store uses.
func (s *Store) kvMarshal(p *Placement) ([]byte, error) {
	kv := newFakeKV()
	tmp, _ := NewStore(kv)
	if err := tmp.Put(context.Background(), p); err != nil {
		return nil, err
	}
	return kv.Get(context.Background(), placementKey(p.TenantID, p.Database))
}
