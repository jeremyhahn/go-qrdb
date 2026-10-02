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
	"sync"
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

	// currentGate, when non-nil, blocks the FIRST call to CurrentRevision
	// until it is closed. CurrentRevision is called synchronously inside
	// Hub.captureReplayBoundary, under the shard write lock in
	// Watch/WatchPrefix -- before Watch/WatchPrefix returns and before the
	// replay goroutine is even spawned -- so gating it lets a test pause
	// Watch mid-open, mutate history state (e.g. advance compacted) with no
	// other goroutine yet running, and then release it with a deterministic
	// happens-before edge (the channel close) rather than a sleep.
	currentGate chan struct{}

	// compactedRead, when non-nil, receives the value AFTER the first call
	// to CompactedRevision returns it. Hub.Watch calls CompactedRevision
	// once itself, synchronously, in prepareReplay -- before CurrentRevision
	// is ever reached -- so a test that wants to mutate `compacted` between
	// that call and a LATER one (e.g. the one startReplay's goroutine makes
	// in its fail-check) must wait for this signal first. Waiting on it
	// establishes a happens-before edge for the mutation via the channel
	// send/receive, rather than racing it.
	compactedRead     chan int64
	compactedReadOnce sync.Once
}

func (f *fakeHistory) CurrentRevision() int64 {
	if f.currentGate != nil {
		<-f.currentGate
	}
	return f.current
}

func (f *fakeHistory) CompactedRevision(_, _ []byte) int64 {
	v := f.compacted
	f.compactedReadOnce.Do(func() {
		if f.compactedRead != nil {
			f.compactedRead <- v
		}
	})
	return v
}

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

func TestWatch_StartRevision_AboveCurrent_AcceptsAndDeliversFromThatRevision(t *testing.T) {
	// #514: docs/watch used to claim a start revision above the current
	// revision is refused at open with an invalid-argument error. Nothing on
	// this path validates a start revision against CurrentRevision() (only
	// against the compaction floor, in prepareReplay), and the owner ruling
	// settled the contradiction as: accept it. A watch from a future revision
	// is a live watch that has not missed anything — it opens like any other
	// watch, replays nothing (there is nothing yet to replay), and starts
	// delivering once a live event reaches the requested revision, exactly as
	// etcd's Watch does for a start revision beyond the current one.
	h := NewHub()
	h.SetHistory(&fakeHistory{current: 3})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := h.Watch(ctx, "k", WithStartRevision(10))
	if err != nil {
		t.Fatalf("Watch with a start revision above current = %v, want nil (accepted)", err)
	}

	// An event below the requested start revision must not be delivered...
	h.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), Value: []byte("v9"), ModRevision: 9}, nil)
	// ...but the event AT the requested start revision must be.
	h.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), Value: []byte("v10"), ModRevision: 10}, nil)

	got := collectN(t, w, 2) // Created (boundary=3) + the event at revision 10
	if !got[0].Created || got[0].Revision != 3 {
		t.Fatalf("first response = %+v, want Created with revision 3 (the boundary at open)", got[0])
	}
	if len(got[1].Events) != 1 || got[1].Revision != 10 {
		t.Fatalf("second response = %+v, want the event at revision 10", got[1])
	}

	select {
	case extra := <-w.Events():
		t.Fatalf("unexpected extra delivery (revision 9 should have been dropped): %+v", extra)
	case <-time.After(200 * time.Millisecond):
		// good: nothing below the requested start revision leaked through.
	}
}

func TestWatch_StartRevision_AboveCurrent_CompactionRaceDoesNotCancel(t *testing.T) {
	// Review finding on the fix above: the guarantee does not hold under a
	// compaction race. Scenario -- register a watch at revision 10 while
	// current is 3; unrelated writes advance and compaction passes 10
	// BEFORE startReplay's goroutine reaches its fail-check. That check
	// read hr.CompactedRevision() unconditionally, so it set ErrCompacted
	// and cancelled the watcher even though no replay was ever attempted
	// (wo.StartRevision(10) > boundary(3) skips Replay entirely) and the
	// watcher was registered before every relevant event. The fail-check
	// exists to catch compaction invalidating an ATTEMPTED replay; a
	// compaction racing past a start revision that needed no replay
	// invalidates nothing.
	//
	// currentGate pins the race deterministically: CurrentRevision (the
	// boundary capture) runs synchronously inside Hub.Watch, before Watch
	// returns and before the replay goroutine is spawned, so blocking it
	// lets this test advance compacted with no other goroutine yet running,
	// then release the block with a channel-close happens-before edge
	// instead of a sleep. compactedRead pins the OTHER read: prepareReplay
	// calls CompactedRevision itself, before CurrentRevision is ever
	// reached, and that read must observe 0 (so registration succeeds) and
	// complete before this test's mutation -- waiting for the signal, rather
	// than sleeping, is what makes that ordering a happens-before edge
	// instead of a race the detector would (correctly) flag.
	currentGate := make(chan struct{})
	compactedRead := make(chan int64, 1)
	fh := &fakeHistory{current: 3, currentGate: currentGate, compactedRead: compactedRead}
	h := NewHub()
	h.SetHistory(fh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type openResult struct {
		w   Watcher
		err error
	}
	opened := make(chan openResult, 1)
	go func() {
		w, err := h.Watch(ctx, "k", WithStartRevision(10))
		opened <- openResult{w, err}
	}()

	// Wait for prepareReplay's own CompactedRevision call to complete (it
	// must see the original 0, or a future start revision would be refused
	// as already compacted before registration even happens). Only once that
	// has happened is it safe to advance compaction.
	select {
	case v := <-compactedRead:
		if v != 0 {
			t.Fatalf("prepareReplay observed compacted=%d, want 0", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prepareReplay never read CompactedRevision")
	}

	// Watch has now registered the watcher and is blocked inside
	// CurrentRevision (the boundary capture). While it is blocked, advance
	// compaction past the requested start revision -- unrelated writes and
	// compaction racing ahead of startReplay's goroutine, exactly as in the
	// field report.
	fh.compacted = 15
	close(currentGate)

	var res openResult
	select {
	case res = <-opened:
	case <-time.After(3 * time.Second):
		t.Fatal("Watch did not return after the boundary gate was released")
	}
	if res.err != nil {
		t.Fatalf("Watch with a start revision above current = %v, want nil (accepted)", res.err)
	}
	w := res.w

	// The event at the requested start revision must still be delivered:
	// registration happened before compaction raced ahead, and no replay was
	// ever attempted for compaction to have invalidated.
	h.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), Value: []byte("v10"), ModRevision: 10}, nil)

	got := collectN(t, w, 2) // Created (boundary=3) + the event at revision 10
	if !got[0].Created || got[0].Revision != 3 {
		t.Fatalf("first response = %+v, want Created with revision 3", got[0])
	}
	if len(got[1].Events) != 1 || got[1].Revision != 10 {
		t.Fatalf("second response = %+v, want the event at revision 10", got[1])
	}
	if err := w.Err(); err != nil {
		t.Fatalf("watcher.Err() = %v, want nil -- a compaction race past a future start "+
			"revision that needed no replay must not fail the watch", err)
	}
}

func TestWatch_StartRevision_AboveCurrent_LiveEventsSurviveCompactionAfterRegistration(t *testing.T) {
	// Second-round review finding: the guarantee must hold across the full
	// sequence -- pause right after registration, advance current past
	// startRevision while EMITTING MATCHING EVENTS INTO STAGING (some below
	// startRevision, some at or above it), compact past startRevision, then
	// resume -- not just for the narrower single-field mutation the first
	// regression test above pins.
	//
	// afterBoundaryCapture fires once boundary is already fixed (a local
	// copy of what captureReplayBoundary read atomically with registration),
	// so it is the correct seam for "right after registration": everything
	// this test does next can change wo.StartRevision's relationship to
	// hr.CurrentRevision()/CompactedRevision() going forward, but cannot
	// retroactively change the value startReplay's goroutine already holds.
	// h.NotifyPut is used for the events (not a direct field write) so they
	// travel the real staging path a caller's writes would use.
	release := make(chan struct{})
	proceed := make(chan struct{})

	h := NewHub()
	h.afterBoundaryCapture = func() {
		close(proceed)
		<-release
	}
	fh := &fakeHistory{current: 3}
	h.SetHistory(fh)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opened := make(chan struct {
		w   Watcher
		err error
	}, 1)
	go func() {
		w, err := h.Watch(ctx, "k", WithStartRevision(10))
		opened <- struct {
			w   Watcher
			err error
		}{w, err}
	}()

	select {
	case <-proceed:
	case <-time.After(3 * time.Second):
		t.Fatal("startReplay's goroutine never reached afterBoundaryCapture")
	}

	// (b) advance current past startRevision, emitting matching events into
	// staging -- some below startRevision, some at or above it.
	fh.current = 15
	h.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), Value: []byte("v8"), ModRevision: 8}, nil)
	h.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), Value: []byte("v9"), ModRevision: 9}, nil)
	h.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), Value: []byte("v10"), ModRevision: 10}, nil)
	h.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), Value: []byte("v12"), ModRevision: 12}, nil)

	// (c) compact past startRevision.
	fh.compacted = 15

	// (d) resume.
	close(release)

	var res struct {
		w   Watcher
		err error
	}
	select {
	case res = <-opened:
	case <-time.After(3 * time.Second):
		t.Fatal("Watch did not return after resuming")
	}
	if res.err != nil {
		t.Fatalf("Watch with a start revision above current = %v, want nil (accepted)", res.err)
	}
	w := res.w

	// Events >= startRevision delivered exactly once and in order; events
	// below it dropped; no ErrCompacted.
	got := collectN(t, w, 3) // Created + rev10 + rev12
	if !got[0].Created {
		t.Fatalf("first response = %+v, want Created", got[0])
	}
	wantRevs := []int64{10, 12}
	for i, want := range wantRevs {
		r := got[i+1]
		if len(r.Events) != 1 || r.Revision != want {
			t.Fatalf("response[%d] = %+v, want exactly one event at revision %d", i+1, r, want)
		}
	}
	select {
	case extra := <-w.Events():
		t.Fatalf("unexpected extra delivery (revisions below startRevision, or a duplicate): %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}
	if err := w.Err(); err != nil {
		t.Fatalf("watcher.Err() = %v, want nil -- registration and its captured boundary "+
			"preceded every relevant event and the compaction race", err)
	}
}

func TestWatch_StartRevision_AtCurrent_StillOpens(t *testing.T) {
	// The negative alongside the above: a start revision exactly AT the
	// current revision is neither a future revision nor a compacted one, and
	// must open exactly like any other in-range start revision.
	h := NewHub()
	h.SetHistory(&fakeHistory{current: 3})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := h.Watch(ctx, "k", WithStartRevision(3))
	if err != nil {
		t.Fatalf("Watch with a start revision at current = %v, want nil", err)
	}

	h.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), Value: []byte("v3"), ModRevision: 3}, nil)

	got := collectN(t, w, 2) // Created + the event at revision 3
	if !got[0].Created || got[0].Revision != 3 {
		t.Fatalf("first response = %+v, want Created with revision 3", got[0])
	}
	if len(got[1].Events) != 1 || got[1].Revision != 3 {
		t.Fatalf("second response = %+v, want the event at revision 3", got[1])
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
