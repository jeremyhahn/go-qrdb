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

// withGapProneWatchPolling configures a poll cadence whose server-side live
// collection window (timeout_seconds) is a tiny fraction of the total cycle
// (interval + timeout), so a push landing at a random instant overwhelmingly
// likely falls in the *gap* between one poll's watcher closing and the
// next's opening rather than inside a live window. This makes
// TestWatchPoll_NoLossAcrossPollGaps a near-deterministic detector of the
// cursor regression it targets: without the cursor (start_revision sent on
// every poll, unconditionally), a push landing in the gap is only caught by
// the luck of overlapping a live window, which these odds make vanishingly
// unlikely across several independent pushes; with the cursor, the next
// poll's replay always covers it regardless of duty cycle.
func withGapProneWatchPolling(t *testing.T) {
	t.Helper()
	origInterval := watchPollInterval
	origTimeout := watchPollTimeoutSeconds
	watchPollInterval = 40 * time.Millisecond
	watchPollTimeoutSeconds = 0.001
	t.Cleanup(func() {
		watchPollInterval = origInterval
		watchPollTimeoutSeconds = origTimeout
	})
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
	withFastWatchPolling(t)

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

	// Let the bootstrap CurrentRevision call and at least one full poll
	// cycle complete before pushing the change under test, so the assertion
	// below isolates "was the cursor anchored at all" from "did this
	// specific push happen to race the first poll" (covered separately by
	// TestWatchPoll_NoLossAcrossPollGaps).
	time.Sleep(10 * watchPollInterval)

	rev := fh.push("k", "v1")

	found := false
	deadline := time.After(3 * time.Second)
loop:
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				break loop
			}
			for _, ev := range resp.Events {
				if ev.Kv == nil {
					continue
				}
				if string(ev.Kv.Key) == "other-key" {
					t.Errorf("pre-existing history must not be replayed on a live watch, got key %q", ev.Kv.Key)
				}
				if string(ev.Kv.Key) == "k" {
					if ev.Kv.ModRevision != rev {
						t.Errorf("revision = %d, want %d", ev.Kv.ModRevision, rev)
					}
					found = true
					break loop
				}
			}
		case <-deadline:
			break loop
		}
	}

	cancel()
	for range ch {
		// Drain until the poll goroutine observes cancellation and closes
		// the channel, so withFastWatchPolling's t.Cleanup cannot race the
		// still-running goroutine's reads of the package-level poll vars.
	}

	if !found {
		t.Fatal("timed out waiting for the pushed change")
	}
}

// ---------------------------------------------------------------------------
// No loss across poll gaps (the core regression this fix targets)
// ---------------------------------------------------------------------------

// TestWatchPoll_NoLossAcrossPollGaps pushes several changes spaced across
// multiple poll cycles — including gaps between polls where no watcher is
// server-side registered — and verifies every change is delivered exactly
// once, in revision order. The old tear-down/recreate-with-no-cursor design
// would silently drop a change landing in such a gap.
func TestWatchPoll_NoLossAcrossPollGaps(t *testing.T) {
	withGapProneWatchPolling(t)

	fh := &fakeHistory{}
	tr, cleanup := connectWatchHistoryInProcess(t, fh)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())

	ch, err := tr.Watch(ctx, "k", transport.WatchOptions{})
	if err != nil {
		cancel()
		t.Fatalf("Watch: %v", err)
	}

	// Let the bootstrap-anchored first poll cycle complete before pushing
	// anything, so every push below is guaranteed to land at or after the
	// cursor's starting point (see TestWatchPoll_BootstrapsCursorViaCurrentRevision
	// for the bootstrap behavior itself).
	time.Sleep(10 * watchPollInterval)

	const numChanges = 5
	wantRevs := make([]int64, 0, numChanges)
	for i := 0; i < numChanges; i++ {
		wantRevs = append(wantRevs, fh.push("k", "v"))
		// Sleep longer than a full poll cycle (interval+timeout) so each
		// push has a real chance of landing in the gap between one poll's
		// watcher closing and the next one opening — the exact window the
		// old tear-down/recreate-with-no-cursor design silently dropped.
		time.Sleep(4 * watchPollInterval)
	}

	seen := make(map[int64]int)
	deadline := time.After(6 * time.Second)
collect:
	for len(seen) < numChanges {
		select {
		case resp, ok := <-ch:
			if !ok {
				break collect
			}
			for _, ev := range resp.Events {
				if ev.Kv != nil && string(ev.Kv.Key) == "k" {
					seen[ev.Kv.ModRevision]++
				}
			}
		case <-deadline:
			break collect
		}
	}

	cancel()
	for range ch {
		// Drain until the poll goroutine observes cancellation and closes
		// the channel, so withFastWatchPolling's t.Cleanup cannot race the
		// still-running goroutine's reads of the package-level poll vars.
	}

	if len(seen) != numChanges {
		t.Fatalf("observed %d distinct revisions, want %d (pushed %v): %v", len(seen), numChanges, wantRevs, seen)
	}
	for _, rev := range wantRevs {
		if seen[rev] != 1 {
			t.Errorf("revision %d delivered %d times, want exactly 1", rev, seen[rev])
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
	withFastWatchPolling(t)

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

	time.Sleep(6 * watchPollInterval)
	cancel()
	for range ch {
		// Drain until the poll goroutine observes cancellation and closes
		// the channel (see the other tests in this file for why this
		// matters with shared package-level poll vars).
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sawStartRevision) < 2 {
		t.Fatalf("expected at least 2 polls in %v, got %d", 6*watchPollInterval, len(sawStartRevision))
	}
	for i, has := range sawStartRevision {
		if has {
			t.Errorf("poll %d unexpectedly carried start_revision; a zero-event/zero-revision live-only response must never establish a cursor", i)
		}
	}
}
