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
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// collectWatch reads exactly n responses from a watcher or fails on timeout.
func collectWatch(t *testing.T, w watch.Watcher, n int) []watch.WatchResponse {
	t.Helper()
	var got []watch.WatchResponse
	deadline := time.After(3 * time.Second)
	for len(got) < n {
		select {
		case resp, ok := <-w.Events():
			if !ok {
				t.Fatalf("events closed after %d/%d: %+v", len(got), n, got)
			}
			got = append(got, resp)
		case <-deadline:
			t.Fatalf("timed out after %d/%d responses", len(got), n)
		}
	}
	return got
}

// TestMVCCEngine_WatchStartRevision_ReplaysCommittedHistory proves the full
// wiring: committed MVCC changes are recorded in the durable revision index,
// and a watch created with a start revision replays them through the engine's
// history adapter — filtered to the watched key — before going live.
func TestMVCCEngine_WatchStartRevision_ReplaysCommittedHistory(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng, err := NewMVCCEngine(stg, WithMVCCWatchHub(hub))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	// Commit three changes: k1@rev1, k2@rev2, k1@rev3.
	applyMVCCEntries(t, eng, []sm.Entry{
		baseKVPutEntry(1, "k1", "v1"),
		baseKVPutEntry(2, "k2", "v2"),
		baseKVPutEntry(3, "k1", "v1b"),
	})
	if got := eng.Store().CurrentRevision(); got != 3 {
		t.Fatalf("current revision = %d, want 3", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Exact watch on k1 from revision 1 must replay ONLY k1's changes (rev 1
	// and rev 3), in order, after a Created response carrying the head revision.
	w, err := hub.Watch(ctx, "k1", watch.WithStartRevision(1))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	got := collectWatch(t, w, 3) // Created + k1@1 + k1@3
	if !got[0].Created || got[0].Revision != 3 {
		t.Fatalf("first response = %+v, want Created at revision 3", got[0])
	}
	if got[1].Revision != 1 || string(got[1].Events[0].Kv.Key) != "k1" || string(got[1].Events[0].Kv.Value) != "v1" {
		t.Fatalf("replay[0] = %+v, want k1=v1 at revision 1", got[1])
	}
	if got[2].Revision != 3 || string(got[2].Events[0].Kv.Value) != "v1b" {
		t.Fatalf("replay[1] = %+v, want k1=v1b at revision 3", got[2])
	}
}

// TestMVCCEngine_WatchStartRevision_Compacted proves a start revision at or
// below the compaction floor fails closed with ErrCompacted.
func TestMVCCEngine_WatchStartRevision_Compacted(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng, err := NewMVCCEngine(stg, WithMVCCWatchHub(hub))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		baseKVPutEntry(1, "k", "v1"),
		baseKVPutEntry(2, "k", "v2"),
		baseKVPutEntry(3, "k", "v3"),
	})
	if err := eng.Store().Compact(2); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := hub.Watch(ctx, "k", watch.WithStartRevision(2)); err == nil {
		t.Fatal("Watch at compacted revision should fail with ErrCompacted")
	}
	// Above the floor still works.
	if _, err := hub.Watch(ctx, "k", watch.WithStartRevision(3)); err != nil {
		t.Fatalf("Watch above compaction floor = %v, want nil", err)
	}
}
