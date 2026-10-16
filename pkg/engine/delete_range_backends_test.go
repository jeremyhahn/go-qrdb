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

package engine

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// The range delete contract (docs/databases/engineering.md, "Range delete
// command") run unmodified against both engines over every storage backend
// (vectorTestBackends): a range delete leaves what the same single deletes
// leave, and tells a watcher one DELETE per key, in key order, at one
// revision.

// rangeEngineKind builds one engine kind over a storage with a watch hub, and
// encodes that kind's single delete.
type rangeEngineKind struct {
	name   string
	new    func(t *testing.T, stg storage.Storage, hub *watch.Hub) Engine
	delete func(idx uint64, key string) sm.Entry
}

func rangeEngineKinds() []rangeEngineKind {
	return []rangeEngineKind{
		{
			name: "kv",
			new: func(_ *testing.T, stg storage.Storage, hub *watch.Hub) Engine {
				return NewKVEngine(stg, WithKVWatchHub(hub))
			},
			delete: kvDeleteEntry,
		},
		{
			name: "mvcc",
			new: func(t *testing.T, stg storage.Storage, hub *watch.Hub) Engine {
				t.Helper()
				eng, err := NewMVCCEngine(stg, WithMVCCWatchHub(hub))
				if err != nil {
					t.Fatalf("NewMVCCEngine: %v", err)
				}
				return eng
			},
			delete: func(idx uint64, key string) sm.Entry { return mvccDeleteEntry(idx, []byte(key)) },
		},
	}
}

// rangeEngine is one engine under test and the hub it notifies.
type rangeEngine struct {
	Engine
	hub  *watch.Hub
	next uint64
}

// forEachRangeEngine runs fn against every (backend, engine kind) pair, each
// over its own storage, engine and hub.
func forEachRangeEngine(t *testing.T, fn func(t *testing.T, kind rangeEngineKind, newEngine func() *rangeEngine)) {
	t.Helper()
	for _, b := range vectorTestBackends() {
		t.Run(b.name, func(t *testing.T) {
			for _, k := range rangeEngineKinds() {
				t.Run(k.name, func(t *testing.T) {
					fn(t, k, func() *rangeEngine {
						stg := b.new(t)
						t.Cleanup(func() { _ = stg.Close() })
						hub := watch.NewHub()
						t.Cleanup(hub.Close)
						eng := k.new(t, stg, hub)
						t.Cleanup(func() { _ = eng.Close(context.Background()) })
						return &rangeEngine{Engine: eng, hub: hub}
					})
				})
			}
		})
	}
}

// apply commits entries, renumbering them after every entry already applied.
func (r *rangeEngine) apply(t *testing.T, entries ...sm.Entry) []sm.Result {
	t.Helper()
	for i := range entries {
		r.next++
		entries[i].Index = r.next
	}
	return applyKVEntries(t, r.Engine, entries)
}

func (r *rangeEngine) seed(t *testing.T, keys ...string) {
	t.Helper()
	entries := make([]sm.Entry, len(keys))
	for i, k := range keys {
		entries[i] = kvPutEntry(0, k, "v-"+k)
	}
	r.apply(t, entries...)
}

// live returns which of keys can be read back.
func (r *rangeEngine) live(t *testing.T, keys ...string) []string {
	t.Helper()
	var out []string
	for _, k := range keys {
		val, err := r.Lookup(context.Background(), k)
		if b, _ := val.([]byte); err == nil && len(b) > 0 {
			out = append(out, k)
		}
	}
	return out
}

// deleteEvents collects the next n events delivered to w, failing if they do
// not arrive or one is not a DELETE.
func deleteEvents(t *testing.T, w watch.Watcher, n int) (keys []string, revisions []int64) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for len(keys) < n {
		select {
		case resp := <-w.Events():
			for _, ev := range resp.Events {
				if ev.Type != watch.EventDelete {
					t.Fatalf("event on %q is %v, want a DELETE", ev.Kv.Key, ev.Type)
				}
				keys = append(keys, string(ev.Kv.Key))
				revisions = append(revisions, resp.Revision)
			}
		case <-timeout:
			t.Fatalf("got %d of %d delete events: %q", len(keys), n, keys)
		}
	}
	return keys, revisions
}

func TestEngines_DeleteRangeOnEveryBackend(t *testing.T) {
	all := []string{"a", "b", "bz", "c0", "d"}

	forEachRangeEngine(t, func(t *testing.T, _ rangeEngineKind, newEngine func() *rangeEngine) {
		eng := newEngine()
		eng.seed(t, all...)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		w, err := eng.hub.WatchPrefix(ctx, "")
		if err != nil {
			t.Fatalf("WatchPrefix: %v", err)
		}

		res := eng.apply(t, deleteRangeEntry(t, 0, "b", "d"))[0]
		if _, refused := protocol.DecodeAppError(res.Data); refused {
			t.Fatalf("range delete refused: %+v", res)
		}
		if got := eng.live(t, all...); !slices.Equal(got, []string{"a", "d"}) {
			t.Fatalf("live keys = %q, want [a d]", got)
		}
		keys, revisions := deleteEvents(t, w, 3)
		if !slices.Equal(keys, []string{"b", "bz", "c0"}) {
			t.Fatalf("DELETE events = %q, want [b bz c0] in key order", keys)
		}
		if revisions[0] != revisions[1] || revisions[1] != revisions[2] {
			t.Fatalf("event revisions = %v, want one revision for the whole range", revisions)
		}
	})
}

func TestEngines_DeleteRangeUnboundedOnEveryBackend(t *testing.T) {
	all := []string{"a", "m", "mm", "z"}

	forEachRangeEngine(t, func(t *testing.T, _ rangeEngineKind, newEngine func() *rangeEngine) {
		eng := newEngine()
		eng.seed(t, all...)

		eng.apply(t, deleteRangeEntry(t, 0, "m", ""))
		if got := eng.live(t, all...); !slices.Equal(got, []string{"a"}) {
			t.Fatalf("live keys = %q, want [a]", got)
		}
	})
}

func TestEngines_DeleteRangeEqualsSingleDeletesOnEveryBackend(t *testing.T) {
	all := []string{"p/1", "p/2", "p/20", "p0", "q"}
	inRange := []string{"p/1", "p/2", "p/20"}

	forEachRangeEngine(t, func(t *testing.T, kind rangeEngineKind, newEngine func() *rangeEngine) {
		ranged, single := newEngine(), newEngine()
		ranged.seed(t, all...)
		single.seed(t, all...)

		ranged.apply(t, deleteRangeEntry(t, 0, "p/", "p0"))
		for _, k := range inRange {
			single.apply(t, kind.delete(0, k))
		}
		got, want := ranged.live(t, all...), single.live(t, all...)
		if !slices.Equal(got, want) {
			t.Fatalf("after a range delete live = %q; after single deletes live = %q", got, want)
		}
	})
}

// TestEngines_DeleteRangeInterleavedInOneApplyOnEveryBackend applies puts and
// range deletes as one batch of entries, so each entry reads through the
// batch's pending writes: a range delete hides the committed keys and the keys
// written ahead of it in the batch, and a key written after it survives.
func TestEngines_DeleteRangeInterleavedInOneApplyOnEveryBackend(t *testing.T) {
	all := []string{"a", "b", "bb", "c", "d", "e", "e2", "f"}

	forEachRangeEngine(t, func(t *testing.T, _ rangeEngineKind, newEngine func() *rangeEngine) {
		eng := newEngine()
		eng.seed(t, "a", "b", "c", "d", "e")

		eng.apply(t,
			kvPutEntry(0, "bb", "pending"),
			deleteRangeEntry(t, 0, "b", "d"),
			kvPutEntry(0, "c", "after"),
			kvPutEntry(0, "e2", "pending"),
			deleteRangeEntry(t, 0, "e", ""),
			kvPutEntry(0, "e", "after"),
			kvPutEntry(0, "f", "after"),
		)
		if got := eng.live(t, all...); !slices.Equal(got, []string{"a", "c", "d", "e", "f"}) {
			t.Fatalf("live keys = %q, want [a c d e f]", got)
		}
		for key, want := range map[string]string{"a": "v-a", "c": "after", "d": "v-d", "e": "after"} {
			val, err := eng.Lookup(context.Background(), key)
			if got, _ := val.([]byte); err != nil || string(got) != want {
				t.Fatalf("Lookup(%q) = %q, %v; want %q", key, got, err, want)
			}
		}
	})
}
