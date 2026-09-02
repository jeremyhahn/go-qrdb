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

package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// fakeHistory: a minimal watch.HistoryReplayer driving a real *watch.Hub, so
// these tests exercise the actual (already-tested) replay-then-live-handoff
// core through the real MCP wire, rather than re-mocking its semantics.
// ---------------------------------------------------------------------------

type fakeChange struct {
	rev   int64
	key   string
	value string
}

type fakeHistory struct {
	mu        sync.Mutex
	current   int64
	compacted int64
	changes   []fakeChange
}

func (f *fakeHistory) CurrentRevision() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current
}

func (f *fakeHistory) CompactedRevision() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.compacted
}

func (f *fakeHistory) Replay(startRev, endRev int64, keyMatch func([]byte) bool, _ bool, fn func(*watch.Event) bool) error {
	f.mu.Lock()
	changes := append([]fakeChange(nil), f.changes...)
	f.mu.Unlock()

	for _, c := range changes {
		if c.rev < startRev || c.rev > endRev {
			continue
		}
		if !keyMatch([]byte(c.key)) {
			continue
		}
		ev := &watch.Event{
			Type: watch.EventPut,
			Kv:   &kv.KeyValue{Key: []byte(c.key), Value: []byte(c.value), ModRevision: c.rev},
		}
		if !fn(ev) {
			return nil
		}
	}
	return nil
}

// push commits a new change and returns its revision.
func (f *fakeHistory) push(key, value string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current++
	f.changes = append(f.changes, fakeChange{rev: f.current, key: key, value: value})
	return f.current
}

// compact advances the compacted floor to rev.
func (f *fakeHistory) compact(rev int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compacted = rev
}

// dynamicMVCCSvc serves CurrentRevision from a live fakeHistory instead of a
// fixed value, so the SDK client's bootstrap-cursor CurrentRevision RPC
// reflects the same "now" the hub itself would report.
type dynamicMVCCSvc struct {
	*mockMVCCSvc
	fh *fakeHistory
}

func (d *dynamicMVCCSvc) CurrentRevision(_ context.Context) (int64, error) {
	return d.fh.CurrentRevision(), nil
}

// connectWatchHistoryInProcess wires a real *watch.Hub (backed by fh) into an
// in-process MCP server/client pair via service.NewWatchService, plus an
// MVCC service so the client's CurrentRevision bootstrap RPC works.
func connectWatchHistoryInProcess(t *testing.T, fh *fakeHistory) (*Transport, func()) {
	t.Helper()

	hub := watch.NewHub()
	hub.SetHistory(fh)
	watchSvc := service.NewWatchService(hub)
	mvccSvc := &dynamicMVCCSvc{mockMVCCSvc: newMockMVCCSvc(), fh: fh}

	srv := mcpapi.NewServer(
		newMockKVService(),
		newMockAdminService(),
		":0",
		mcpapi.WithWatch(watchSvc),
		mcpapi.WithMVCC(mvccSvc),
	)
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if connErr := tr.Connect(context.Background()); connErr != nil {
		t.Fatalf("Connect: %v", connErr)
	}
	return tr, func() { _ = tr.Close() }
}

// withFastWatchPolling shrinks the package-level poll interval/timeout for
// the duration of a test, so a multi-cycle polling test completes in
// milliseconds instead of seconds. Restored via t.Cleanup.
func withFastWatchPolling(t *testing.T) {
	t.Helper()
	origInterval := watchPollInterval
	origTimeout := watchPollTimeoutSeconds
	watchPollInterval = 15 * time.Millisecond
	watchPollTimeoutSeconds = 0.03
	t.Cleanup(func() {
		watchPollInterval = origInterval
		watchPollTimeoutSeconds = origTimeout
	})
}

// stepClock is a watchClock whose After never fires on its own: each call
// hands the poll loop a fresh channel and publishes it on pauses, so the test
// decides when — and whether — that delay elapses. This is what makes a
// polling test deterministic. The loop parked in After has provably closed its
// server-side watcher and not yet opened the next one, so a change pushed at
// that moment lands in a REAL inter-poll gap by construction, rather than by
// the statistical argument the old duty-cycle-based helper relied on.
//
// The send is non-blocking: if a test stops consuming pauses (teardown, or an
// early t.Fatal), After still returns promptly and the loop falls through to
// its ctx.Done() case, so cancellation can never deadlock against an
// unconsumed pause.
type stepClock struct {
	pauses chan chan time.Time
}

func newStepClock() *stepClock {
	return &stepClock{pauses: make(chan chan time.Time, 64)}
}

// After records a pause and returns a channel only the test can fire.
func (s *stepClock) After(time.Duration) <-chan time.Time {
	c := make(chan time.Time, 1)
	select {
	case s.pauses <- c:
	default:
	}
	return c
}

// awaitPause blocks until the poll loop reaches its next inter-poll pause and
// returns the channel that releases it. Reaching a pause means the poll before
// it ran to completion and any response it produced is already queued on the
// watch channel, so a caller may drain deliveries immediately afterwards.
//
// The bound is a failure deadline, not a timing assumption: a correct loop
// reaches its pause as fast as the machine allows, so a slow host makes this
// wait longer without making it flaky, and only a loop that never polls again
// trips it.
func (s *stepClock) awaitPause(t *testing.T) chan time.Time {
	t.Helper()
	select {
	case c := <-s.pauses:
		return c
	case <-time.After(30 * time.Second):
		t.Fatal("poll loop did not reach its next inter-poll pause")
		return nil
	}
}

// step releases exactly one poll and returns once that poll has completed and
// the loop is parked again, ready for the next push.
func (s *stepClock) step(t *testing.T, pause chan time.Time) chan time.Time {
	t.Helper()
	pause <- time.Time{}
	return s.awaitPause(t)
}

// withSteppedWatchPolling installs a stepClock for the duration of a test and
// returns it. The poll interval becomes irrelevant (the stepClock ignores the
// duration); the server-side collection window stays a real timer because it
// lives in the MCP tool handler, not in this package, so it is set generously
// wide relative to the work each poll actually does — delivering at most a
// handful of in-memory events. 200ms is 200x the 1ms window the previous
// duty-cycle helper used, under which the handler could be descheduled past
// its own deadline mid-replay (see the note in TestWatchPoll_NoLossAcrossPollGaps).
func withSteppedWatchPolling(t *testing.T) *stepClock {
	t.Helper()
	origInterval := watchPollInterval
	origTimeout := watchPollTimeoutSeconds
	origClock := watchPollClock
	clk := newStepClock()
	watchPollInterval = time.Hour // never waited on; the stepClock gates instead.
	watchPollTimeoutSeconds = 0.2
	watchPollClock = clk
	t.Cleanup(func() {
		watchPollInterval = origInterval
		watchPollTimeoutSeconds = origTimeout
		watchPollClock = origClock
	})
	return clk
}

// drainWatch counts every event currently queued on ch into seen, as
// seen[key][revision] = number of deliveries. It does not block: the caller
// invokes it at a point where the poll loop is parked, so everything the last
// poll produced is already queued. Counting per key rather than filtering to
// one is what lets a caller assert both "this key arrived" and "that key never
// did" from a single drain.
func drainWatch(ch <-chan transport.WatchResponse, seen map[string]map[int64]int) {
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				return
			}
			for _, ev := range resp.Events {
				if ev.Kv == nil {
					continue
				}
				key := string(ev.Kv.Key)
				if seen[key] == nil {
					seen[key] = make(map[int64]int)
				}
				seen[key][ev.Kv.ModRevision]++
			}
		default:
			return
		}
	}
}

// drainWatchUntilClosed consumes ch until the poll goroutine observes
// cancellation and closes it, so a test's t.Cleanup cannot race the
// still-running goroutine's reads of the package-level poll vars.
func drainWatchUntilClosed(ch <-chan transport.WatchResponse) {
	for range ch {
	}
}

// ---------------------------------------------------------------------------
// Cursor bootstrap
// ---------------------------------------------------------------------------

// TestWatchPoll_BootstrapsCursorViaCurrentRevision verifies that when the
// caller does not supply an explicit start revision, the poll loop's cursor
// is anchored by a CurrentRevision call rather than left at zero — the fix
// for the permanent gap the old code left open on every poll until the
// first event happened to be observed live. It pushes an unrelated change
// before Watch (proving the watch does NOT replay pre-existing history —
// live-watch semantics are preserved), waits for the bootstrap-anchored
// first poll cycle to complete, then pushes the change under test and
// confirms it is delivered without needing a second live event to
// "accidentally" establish the cursor first.
func TestWatchPoll_BootstrapsCursorViaCurrentRevision(t *testing.T) {
	clk := withSteppedWatchPolling(t)

	fh := &fakeHistory{}
	fh.push("other-key", "v0") // pre-existing history; must NOT be replayed.

	tr, cleanup := connectWatchHistoryInProcess(t, fh)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())

	ch, err := tr.Watch(ctx, "k", transport.WatchOptions{})
	if err != nil {
		cancel()
		t.Fatalf("Watch: %v", err)
	}

	// Reaching the first pause means the bootstrap CurrentRevision call and a
	// full poll cycle are done, so the assertion below isolates "was the
	// cursor anchored at all" from "did this push race the first poll"
	// (covered by TestWatchPoll_NoLossAcrossPollGaps).
	pause := clk.awaitPause(t)

	rev := fh.push("k", "v1")
	clk.step(t, pause)

	seen := make(map[string]map[int64]int)
	drainWatch(ch, seen)

	cancel()
	drainWatchUntilClosed(ch)

	if len(seen["other-key"]) != 0 {
		t.Errorf("pre-existing history must not be replayed on a live watch, got %v", seen["other-key"])
	}
	if seen["k"][rev] != 1 {
		t.Fatalf("revision %d delivered %d times, want exactly 1: %v", rev, seen["k"][rev], seen)
	}
}

// ---------------------------------------------------------------------------
// No loss across poll gaps (the core regression this fix targets)
// ---------------------------------------------------------------------------

// TestWatchPoll_NoLossAcrossPollGaps pushes a change into every gap between
// two polls — the window where no server-side watcher exists at all — and
// verifies each one is delivered exactly once. The old tear-down/recreate
// design with no cursor dropped exactly these.
//
// The schedule is driven, not timed. The loop is parked in stepClock.After
// when each push happens, so "this change landed in a gap" is a fact about the
// schedule rather than a probability, and the assertion below is about what
// the poller delivered across a known number of polls rather than about what
// arrived before a wall-clock deadline.
//
// Why this shape (issue #171): the previous version pushed on a timer and
// collected until a 6s deadline, so a host busy enough to stretch the run 6x
// produced the same failure text as a poller that genuinely lost an update —
// it failed three times on unrelated changes (PR #168, verifying #196, and a
// two-line go.mod bump on PR #202) and could not have caught a real regression
// distinguishably. A test that only passes on a quiet machine is not a gate.
//
// Residual note for whoever touches this next: the qrdb_watch tool's
// collection window is a server-side timer this package cannot inject, and a
// window narrow enough to expire mid-replay makes the tool report a cursor
// covering events it never delivered. That is a real defect in the tool
// handler, not in this loop, and it is tracked separately; it is why
// withSteppedWatchPolling sets a generously wide window instead of the 1ms
// the old helper used. Do not narrow it to "make the gaps tighter" — the gaps
// here are already exact.
func TestWatchPoll_NoLossAcrossPollGaps(t *testing.T) {
	clk := withSteppedWatchPolling(t)

	fh := &fakeHistory{}
	tr, cleanup := connectWatchHistoryInProcess(t, fh)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())

	ch, err := tr.Watch(ctx, "k", transport.WatchOptions{})
	if err != nil {
		cancel()
		t.Fatalf("Watch: %v", err)
	}

	// The loop reaching its first pause means the bootstrap CurrentRevision
	// call and the first poll have both completed, so every push below is at
	// or after the cursor's anchor (the bootstrap itself is covered by
	// TestWatchPoll_BootstrapsCursorViaCurrentRevision).
	pause := clk.awaitPause(t)

	const numChanges = 5
	wantRevs := make([]int64, 0, numChanges)
	seen := make(map[string]map[int64]int)

	for i := 0; i < numChanges; i++ {
		// Parked between polls: no watcher is registered server-side, so this
		// change can only reach the caller through the next poll's replay.
		wantRevs = append(wantRevs, fh.push("k", "v"))
		pause = clk.step(t, pause)
		drainWatch(ch, seen)
	}

	cancel()
	drainWatchUntilClosed(ch)

	got := seen["k"]
	if len(got) != numChanges {
		t.Fatalf("observed %d distinct revisions across %d polled gaps, want %d (pushed %v): %v",
			len(got), numChanges, numChanges, wantRevs, got)
	}
	for _, rev := range wantRevs {
		if got[rev] != 1 {
			t.Errorf("revision %d delivered %d times, want exactly 1", rev, got[rev])
		}
	}
}

// ---------------------------------------------------------------------------
// watch.ErrCompacted -> terminal Canceled response
// ---------------------------------------------------------------------------

// TestWatchPoll_CompactedSurfacesTypedError verifies a start revision at or
// below the compacted floor ends the poll loop with a terminal Canceled
// response classifying to transport.WatchCompactedError, rather than
// retrying forever against an unrecoverable condition.
func TestWatchPoll_CompactedSurfacesTypedError(t *testing.T) {
	withFastWatchPolling(t)

	fh := &fakeHistory{}
	fh.push("k", "v1") // current=1
	fh.compact(1)      // compacted floor at 1: start_revision<=1 is compacted.

	tr, cleanup := connectWatchHistoryInProcess(t, fh)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := tr.Watch(ctx, "k", transport.WatchOptions{StartRevision: 1})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	resp, ok := <-ch
	if !ok {
		t.Fatal("channel closed without a terminal response")
	}
	if !resp.Canceled {
		t.Fatalf("resp.Canceled = false, want true: %+v", resp)
	}
	var wce *transport.WatchCompactedError
	if !errors.As(resp.Err(), &wce) {
		t.Fatalf("resp.Err() = %T (%v), want *transport.WatchCompactedError", resp.Err(), resp.Err())
	}

	if _, ok := <-ch; ok {
		t.Fatal("expected the channel to close after the terminal response")
	}
}

// ---------------------------------------------------------------------------
// Degraded fallback: CurrentRevision unavailable
// ---------------------------------------------------------------------------

// TestWatchPoll_LiveOnlyZeroEventDoesNotAdvanceCursor targets a regression
// found while building the cursor: when CurrentRevision is unavailable (no
// qrdb_mvcc_current_revision tool registered — the degraded fallback
// documented on bootstrapWatchCursor), the first poll omits start_revision
// entirely (a plain live-only watch, per pkg/watch.Hub semantics). Such a
// poll never gets the hub's "Created" response, so a zero-event result
// always reports revision:0 as the Go zero value, NOT as a confirmed
// revision. A naive "always advance the cursor from result.Revision" rule
// would misread that zero as "confirmed current revision 0" and start
// sending start_revision=1 on every following poll — silently reintroducing
// exactly the gap this feature fixes, and, on a real non-empty store, very
// likely triggering a spurious ErrCompacted since revision 1 is almost
// certainly already gone. The cursor must instead stay at 0 (no
// start_revision sent) until a real event or a genuine Created response
// establishes it.
func TestWatchPoll_LiveOnlyZeroEventDoesNotAdvanceCursor(t *testing.T) {
	clk := withSteppedWatchPolling(t)

	var mu sync.Mutex
	var sawStartRevision []bool

	srv := mcpserver.NewMCPServer("qrdb-stub-guard", "1.0.0", mcpserver.WithToolCapabilities(false))
	srv.AddTool(mcp.NewTool("qrdb_watch"), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, hasStartRevision := req.GetArguments()["start_revision"]
		mu.Lock()
		sawStartRevision = append(sawStartRevision, hasStartRevision)
		mu.Unlock()
		// The exact shape handleWatch returns for a live-only poll (no
		// start_revision, hence no replay, hence no Created response) that
		// collected zero events: Revision is Go's zero value, not a
		// confirmed "current revision 0".
		return mcp.NewToolResultText(`{"events":[],"revision":0,"watch_id":1}`), nil
	})
	// Deliberately no qrdb_mvcc_current_revision tool: CurrentRevision must
	// fail, exercising bootstrapWatchCursor's degraded fallback to 0.

	tr, err := NewInProcess(srv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	defer func() { _ = tr.Close() }()
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	ch, err := tr.Watch(ctx, "k", transport.WatchOptions{})
	if err != nil {
		cancel()
		t.Fatalf("Watch: %v", err)
	}

	// Step a fixed number of polls rather than sleeping for a duration and
	// hoping enough happened: the count below is then exact on any machine.
	const wantPolls = 4
	pause := clk.awaitPause(t)
	for i := 1; i < wantPolls; i++ {
		pause = clk.step(t, pause)
	}

	cancel()
	drainWatchUntilClosed(ch)

	mu.Lock()
	defer mu.Unlock()
	if len(sawStartRevision) < wantPolls {
		t.Fatalf("expected at least %d polls, got %d", wantPolls, len(sawStartRevision))
	}
	for i, has := range sawStartRevision {
		if has {
			t.Errorf("poll %d unexpectedly carried start_revision; a zero-event/zero-revision live-only response must never establish a cursor", i)
		}
	}
}
