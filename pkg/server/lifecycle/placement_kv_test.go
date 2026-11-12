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

package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// newPlacementStoreOverHub is the placement table a booted node builds: the
// KV over a real engine, its changes from a real watch hub -- the hub the
// state machine notifies as it applies.
func newPlacementStoreOverHub(t *testing.T) (*placement.Store, *watch.Hub) {
	t.Helper()
	stg := memory.NewStorage()
	t.Cleanup(func() { _ = stg.Close() })
	host := backend.NewLocalBackend(engine.NewKVEngine(stg))
	hub := watch.NewHub()
	store, err := placement.NewStore(newPlacementKV(service.NewKVService(host, 0), service.NewIndexService(host, 0), service.NewWatchService(hub)))
	if err != nil {
		t.Fatal(err)
	}
	return store, hub
}

func nextPlacementEvent(t *testing.T, ch <-chan placement.Event) placement.Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("the placement feed closed")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no placement event")
	}
	return placement.Event{}
}

// TestPlacementKV_ChangesReachTheStoresWatch: a placement written or deleted
// as the node applies it arrives as a typed placement event; other keys do
// not.
func TestPlacementKV_ChangesReachTheStoresWatch(t *testing.T) {
	store, hub := newPlacementStoreOverHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := store.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch = %v; want the node's change feed", err)
	}

	p := placement.Placement{TenantID: "acme", DatabaseID: "orders", Database: "orders", ShardID: 7}
	raw, _ := json.Marshal(p)
	key := []byte(placement.PlacementPrefix + "acme/orders")
	hub.NotifyPut([]byte("__sys:other/key"), &kv.KeyValue{Key: []byte("__sys:other/key"), Value: []byte("x")}, nil)
	hub.NotifyPut(key, &kv.KeyValue{Key: key, Value: raw}, nil)
	if ev := nextPlacementEvent(t, events); ev.Type != placement.EventPut || ev.TenantID != "acme" || ev.Database != "orders" || ev.Placement == nil || ev.Placement.ShardID != 7 {
		t.Fatalf("put event = %+v; want the placement of acme/orders", ev)
	}
	hub.NotifyDelete(key, &kv.KeyValue{Key: key}, &kv.KeyValue{Key: key, Value: raw})
	if ev := nextPlacementEvent(t, events); ev.Type != placement.EventDelete || ev.TenantID != "acme" || ev.Database != "orders" {
		t.Fatalf("delete event = %+v; want acme/orders removed", ev)
	}

	cancel()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("an event arrived after the watch ended")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the feed did not close with its context")
	}
}

// TestPlacementKV_WithoutAWatchServiceHasNoFeed: the store reports the
// feed unsupported rather than failing later.
func TestPlacementKV_WithoutAWatchServiceHasNoFeed(t *testing.T) {
	store, err := placement.NewStore(newPlacementKV(nil, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Watch(context.Background()); !errors.Is(err, placement.ErrWatchUnsupported) {
		t.Fatalf("Watch = %v; want ErrWatchUnsupported", err)
	}
}

func TestPlacementWatchEvent_SkipsEventsWithoutAKey(t *testing.T) {
	for _, ev := range []*watch.Event{nil, {Type: watch.EventPut}} {
		if _, ok := placementWatchEvent(ev); ok {
			t.Errorf("event %+v translated", ev)
		}
	}
}
