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

package watch

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
)

// fakeChange is one historical change in a fakeHistory.
type fakeChange struct {
	rev      int64
	key      string
	isDelete bool
	value    string
}

// fakeHistory is a scripted HistoryReplayer. When gate is non-nil, Replay
// blocks on it after emitting firstN events, letting a test inject live events
// at a deterministic point in the handoff.
type fakeHistory struct {
	current   int64
	compacted int64
	changes   []fakeChange
	gate      chan struct{}
	firstN    int
}

func (f *fakeHistory) CurrentRevision() int64   { return f.current }
func (f *fakeHistory) CompactedRevision() int64 { return f.compacted }

func (f *fakeHistory) Replay(startRev, endRev int64, keyMatch func([]byte) bool, _ bool, fn func(*Event) bool) error {
	emitted := 0
	for _, c := range f.changes {
		if c.rev < startRev || c.rev > endRev {
			continue
		}
		if !keyMatch([]byte(c.key)) {
			continue
		}
		ev := &Event{}
		if c.isDelete {
			ev.Type = EventDelete
			ev.Kv = &kv.KeyValue{Key: []byte(c.key), ModRevision: c.rev}
		} else {
			ev.Type = EventPut
			ev.Kv = &kv.KeyValue{Key: []byte(c.key), Value: []byte(c.value), ModRevision: c.rev}
		}
		if !fn(ev) {
			return nil
		}
		emitted++
		if f.gate != nil && emitted == f.firstN {
			<-f.gate // block mid-replay until the test unblocks
		}
	}
	return nil
}

// collectN reads exactly n responses from w or fails after a timeout.
func collectN(t *testing.T, w Watcher, n int) []WatchResponse {
	t.Helper()
	var got []WatchResponse
	deadline := time.After(3 * time.Second)
	for len(got) < n {
		select {
		case resp, ok := <-w.Events():
			if !ok {
				t.Fatalf("events channel closed after %d/%d responses: %+v", len(got), n, got)
			}
			got = append(got, resp)
		case <-deadline:
			t.Fatalf("timed out after %d/%d responses: %+v", len(got), n, got)
		}
	}
	return got
}

func TestWatch_StartRevision_NoHistory_Unsupported(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := h.Watch(ctx, "k", WithStartRevision(1)); !errors.Is(err, ErrStartRevisionUnsupported) {
		t.Fatalf("Watch with start revision and no history = %v, want ErrStartRevisionUnsupported", err)
	}
	if h.WatchCount() != 0 {
		t.Errorf("a rejected watch left %d watchers registered", h.WatchCount())
	}
}

func TestWatch_StartRevision_Compacted(t *testing.T) {
	h := NewHub()
	h.SetHistory(&fakeHistory{current: 10, compacted: 5})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := h.Watch(ctx, "k", WithStartRevision(5)); !errors.Is(err, ErrCompacted) {
		t.Fatalf("Watch at compacted revision = %v, want ErrCompacted", err)
	}
	if _, err := h.Watch(ctx, "k", WithStartRevision(6)); err != nil {
		t.Fatalf("Watch just above the compaction floor = %v, want nil", err)
	}
}

func TestWatch_StartRevision_ReplaysHistoryInOrder(t *testing.T) {
	h := NewHub()
	h.SetHistory(&fakeHistory{
		current: 3,
		changes: []fakeChange{
			{1, "k", false, "v1"},
			{2, "k", false, "v2"},
			{3, "k", true, ""},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := h.Watch(ctx, "k", WithStartRevision(1))
	if err != nil {
		t.Fatal(err)
	}

	got := collectN(t, w, 4) // Created + 3 changes
	if !got[0].Created || got[0].Revision != 3 {
		t.Fatalf("first response = %+v, want Created with revision 3", got[0])
	}
	wantRev := []int64{1, 2, 3}
	for i, wr := range wantRev {
		r := got[i+1]
		if r.Created || len(r.Events) != 1 || r.Revision != wr {
			t.Fatalf("response[%d] = %+v, want event at revision %d", i+1, r, wr)
		}
	}
	if got[3].Events[0].Type != EventDelete {
		t.Errorf("revision 3 should be a delete, got %v", got[3].Events[0].Type)
	}
}

func TestWatch_StartRevision_HandsOffToLiveAfterHistory(t *testing.T) {
	gate := make(chan struct{})
	h := NewHub()
	h.SetHistory(&fakeHistory{
		current: 2,
		changes: []fakeChange{{1, "k", false, "v1"}, {2, "k", false, "v2"}},
		gate:    gate,
		firstN:  1, // block after emitting revision 1
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := h.Watch(ctx, "k", WithStartRevision(1))
	if err != nil {
		t.Fatal(err)
	}

	// While replay is blocked mid-history, a live event at revision 3 arrives.
	// It must be staged and delivered AFTER the historical events, exactly once.
	h.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), Value: []byte("v3"), ModRevision: 3}, nil)
	close(gate) // let replay finish

	got := collectN(t, w, 4) // Created, rev1, rev2, rev3
	if !got[0].Created {
		t.Fatalf("first response not Created: %+v", got[0])
	}
	gotRevs := []int64{got[1].Revision, got[2].Revision, got[3].Revision}
	for i, want := range []int64{1, 2, 3} {
		if gotRevs[i] != want {
			t.Fatalf("post-handoff revisions = %v, want [1 2 3]", gotRevs)
		}
	}
}

func TestWatch_StartRevision_DedupesLiveEventWithinReplayRange(t *testing.T) {
	gate := make(chan struct{})
	h := NewHub()
	h.SetHistory(&fakeHistory{
		current: 2,
		changes: []fakeChange{{1, "k", false, "v1"}, {2, "k", false, "v2"}},
		gate:    gate,
		firstN:  1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := h.Watch(ctx, "k", WithStartRevision(1))
	if err != nil {
		t.Fatal(err)
	}

	// A live notification for revision 2 (<= boundary) arrives during replay. It
	// is BOTH staged and part of the historical replay; the boundary dedup must
	// drop the staged copy so revision 2 is delivered exactly once.
	h.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), Value: []byte("v2"), ModRevision: 2}, nil)
	close(gate)

	// Then a genuinely new live event at revision 3.
	// (Delivered after sync; may race, so read what we expect and assert no dup.)
	got := collectN(t, w, 3) // Created, rev1, rev2  (rev2 exactly once)
	revs := []int64{got[1].Revision, got[2].Revision}
	if revs[0] != 1 || revs[1] != 2 {
		t.Fatalf("revisions = %v, want [1 2] with revision 2 exactly once", revs)
	}
	// No duplicate revision 2 should be queued.
	select {
	case extra := <-w.Events():
		if extra.Revision == 2 {
			t.Fatalf("revision 2 was delivered twice (duplicate handoff)")
		}
	case <-time.After(200 * time.Millisecond):
		// good: nothing extra
	}
}

func TestWatch_StartRevision_BoundaryRevisionNotYetDurableIsNotLost(t *testing.T) {
	// C1 regression. CurrentRevision() (the boundary) is advanced by the apply
	// path BEFORE the batch is durably committed and BEFORE the live notify
	// fires, so it can name a revision the replay scan cannot yet see. That
	// revision then arrives as a live notify during the unsynced window; it must
	// be delivered by the staged-live path (deliver floor = last-REPLAYED rev),
	// not dropped by a boundary-based dedup. Before the fix this was a silent
	// loss.
	gate := make(chan struct{})
	h := NewHub()
	h.SetHistory(&fakeHistory{
		current: 5, // boundary = 5
		// No change at revision 5: it is "committed" (current==5) but not yet in
		// the durable index the replay scans.
		changes: []fakeChange{
			{1, "k", false, "v1"}, {2, "k", false, "v2"},
			{3, "k", false, "v3"}, {4, "k", false, "v4"},
		},
		gate:   gate,
		firstN: 2, // block after emitting revisions 1 and 2
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := h.Watch(ctx, "k", WithStartRevision(1))
	if err != nil {
		t.Fatal(err)
	}

	// While replay is blocked mid-history, the boundary revision 5 commits and
	// its live notify fires — the exact interleaving that lost it before.
	h.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), Value: []byte("v5"), ModRevision: 5}, nil)
	close(gate)

	got := collectN(t, w, 6) // Created + rev1..4 + rev5

	seen := map[int64]int{}
	for _, r := range got {
		if len(r.Events) > 0 {
			seen[r.Revision]++
		}
	}
	if seen[5] == 0 {
		t.Fatalf("revision 5 (boundary, not durable at scan time) was LOST: %+v", got)
	}
	for rev, n := range seen {
		if n > 1 {
			t.Fatalf("revision %d delivered %d times (duplicate handoff)", rev, n)
		}
	}
}

func TestWatcher_DeliverFloor_DropsBelowStartRevision(t *testing.T) {
	// M2 regression. A watch from a start revision above the current revision
	// must not leak live events below the requested start. The deliver floor is
	// initialised to startRevision-1, so a staged event below it is dropped.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newWatcher(1, []byte("k"), nil, WatchOptions{StartRevision: 100}, ctx)
	w.beginReplay() // deliver floor = 99, unsynced

	// Live events arrive while unsynced (staged). Replay delivers nothing (a
	// future start), so the floor stays at 99.
	w.send(WatchResponse{WatchID: 1, Revision: 60, Events: []*Event{{Kv: &kv.KeyValue{ModRevision: 60}}}})
	w.send(WatchResponse{WatchID: 1, Revision: 100, Events: []*Event{{Kv: &kv.KeyValue{ModRevision: 100}}}})
	w.finishReplay()

	// Drain what is available; expect ONLY revision 100.
	var got []WatchResponse
	deadline := time.After(time.Second)
drain:
	for {
		select {
		case resp := <-w.Events():
			got = append(got, resp)
		case <-deadline:
			break drain
		case <-time.After(150 * time.Millisecond):
			break drain
		}
	}
	if len(got) != 1 || got[0].Revision != 100 {
		t.Fatalf("deliver floor leaked events below start revision: got %+v, want only revision 100", got)
	}
}

func TestWatch_StartRevision_PrefixReplayMatchesOnly(t *testing.T) {
	h := NewHub()
	h.SetHistory(&fakeHistory{
		current: 3,
		changes: []fakeChange{
			{1, "app/a", false, "1"},
			{2, "other", false, "2"},
			{3, "app/b", false, "3"},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := h.WatchPrefix(ctx, "app/", WithStartRevision(1))
	if err != nil {
		t.Fatal(err)
	}

	got := collectN(t, w, 3) // Created + app/a + app/b (not "other")
	if !got[0].Created {
		t.Fatalf("first not Created: %+v", got[0])
	}
	for _, r := range got[1:] {
		if !bytes.HasPrefix(r.Events[0].Kv.Key, []byte("app/")) {
			t.Fatalf("prefix replay delivered a non-matching key: %q", r.Events[0].Kv.Key)
		}
	}
}
