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

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// committedTestWait bounds every channel read in this file; the hub delivers
// in memory, so it is reached only when a response is genuinely missing.
const committedTestWait = 5 * time.Second

// TestMVCCHistory_BoundaryExcludesAnUncommittedBatch: the replay boundary is
// the store's committed revision, not its apply counter. While an apply batch
// is open the counter names revisions the batch has assigned but storage does
// not yet hold, so a replay bounded by it cannot find them.
func TestMVCCHistory_BoundaryExcludesAnUncommittedBatch(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("v0"), 0)})
	history := newMVCCHistoryAdapter(eng.Store())
	if got := history.CurrentRevision(); got != 1 {
		t.Fatalf("boundary after one committed put = %d, want 1", got)
	}

	b := stg.Batch()
	defer func() { _ = b.Close() }()
	if _, _, err := eng.UpdateEntries(b, []sm.Entry{mvccPutEntry(2, []byte("k"), []byte("v1"), 0)}, make([]sm.Result, 1)); err != nil {
		t.Fatalf("UpdateEntries: %v", err)
	}
	if got := eng.Store().CurrentRevision(); got != 2 {
		t.Fatalf("apply counter with the batch open = %d, want 2", got)
	}
	if got := history.CurrentRevision(); got != 1 {
		t.Fatalf("boundary with revision 2 uncommitted = %d, want 1", got)
	}

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(3, []byte("k"), []byte("v2"), 0)})
	if got, want := history.CurrentRevision(), eng.Store().CurrentRevision(); got != want {
		t.Fatalf("boundary after Update committed = %d, want the apply counter %d", got, want)
	}
}

// TestMVCCHistory_ProgressNeverCoversAnUncommittedRevision is the gap behind
// a lost MCP watch mutation: a start-revision watch opened while the batch
// holding its next revision is still open must not report a progress
// watermark covering that revision. The MCP watch tool resumes its next poll
// from that watermark; had it named the uncommitted revision, the event would
// arrive live only if the batch committed inside the same bounded collection
// window, and otherwise never.
func TestMVCCHistory_ProgressNeverCoversAnUncommittedRevision(t *testing.T) {
	hub := watch.NewHub()
	defer hub.Close()
	eng, stg := newMVCCEngine(t, WithMVCCWatchHub(hub))
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("v0"), 0)})

	// Revision 2 is assigned and staged, but its batch has not committed:
	// the slow-commit window a loaded host widens.
	b := stg.Batch()
	defer func() { _ = b.Close() }()
	notifs, _, err := eng.UpdateEntries(b, []sm.Entry{mvccPutEntry(2, []byte("k"), []byte("v1"), 0)}, make([]sm.Result, 1))
	if err != nil {
		t.Fatalf("UpdateEntries: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := hub.Watch(ctx, "k", watch.WithStartRevision(2), watch.WithProgressNotify())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Close() }()

	for progressed := false; !progressed; {
		select {
		case resp, ok := <-w.Events():
			if !ok {
				t.Fatalf("watch closed before its progress watermark: %v", w.Err())
			}
			if len(resp.Events) > 0 {
				t.Fatalf("replay delivered %d events before revision 2 committed", len(resp.Events))
			}
			if resp.Progress {
				if resp.Revision >= 2 {
					t.Fatalf("progress watermark = %d with revision 2 uncommitted: a cursor resumed from it skips revision 2", resp.Revision)
				}
				progressed = true
			}
		case <-time.After(committedTestWait):
			t.Fatal("no progress watermark")
		}
	}

	// Commit, mark and notify in the order the engine's update does.
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	eng.Store().MarkCommitted(2)
	eng.DeliverNotifications(notifs)

	select {
	case resp, ok := <-w.Events():
		if !ok {
			t.Fatalf("watch closed before revision 2: %v", w.Err())
		}
		if len(resp.Events) != 1 || resp.Events[0].Kv == nil || resp.Events[0].Kv.ModRevision != 2 || string(resp.Events[0].Kv.Value) != "v1" {
			t.Fatalf("live response = %+v, want the put of v1 at revision 2", resp)
		}
	case <-time.After(committedTestWait):
		t.Fatal("revision 2 was never delivered after its batch committed")
	}
}
