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
	"context"
	"sync"
	"sync/atomic"
)

// defaultEventBufferSize is the default channel buffer for watch events.
// A large buffer provides reliability by reducing the chance of blocking
// the write path when consumers are temporarily slow.
const defaultEventBufferSize = 4096

// watcher implements Watcher. It receives events from the Hub
// via an internal channel and delivers them to the consumer. The watcher
// is safe for concurrent use.
type watcher struct {
	id       uint64
	key      []byte
	rangeEnd []byte // non-nil for prefix watches

	events chan WatchResponse
	ctx    context.Context
	cancel context.CancelFunc

	prevKV        bool
	startRevision int64
	filters       []WatchFilter

	// Replay handoff state. A watcher created with a start revision begins
	// UNSYNCED: live events from notify() are held in staging while the replay
	// goroutine streams the historical range, then flushed (deduped against
	// deliverFloor) and unsynced is cleared so subsequent live events go straight
	// to the channel. replayDone is closed once the replay goroutine has stopped
	// touching the channel, so cleanup never closes events out from under it. A
	// live-only watcher starts synced with replayDone pre-closed. unsynced is
	// false (the zero value) for a live-only watcher, so a freshly-constructed
	// watcher delivers directly; beginReplay sets it true and finishReplay clears
	// it.
	unsynced   atomic.Bool
	stagingMu  sync.Mutex
	staging    []WatchResponse
	replayDone chan struct{}

	// deregistered is closed by the hub's cleanup goroutine once the watcher is
	// out of the shard registry and no longer counted by WatchCount. Close blocks
	// on it, which is what makes "after Close returns, the watcher is gone" a
	// contract rather than a race the caller has to sleep around (#309). A
	// watcher that no hub owns keeps the pre-closed channel newWatcher installs,
	// so its Close returns immediately; the hub re-arms it (beginCleanup) before
	// it publishes the watcher to any other goroutine.
	deregistered chan struct{}

	// didReplay marks a watcher that performed a start-revision replay, so the
	// dedup guard applies. deliverFloor is the highest revision already accounted
	// for: the replay goroutine advances it to the last revision it actually
	// delivered, and both the staging flush and the post-sync live path drop any
	// event at or below it. It starts at startRevision-1 so live events below the
	// requested start are never leaked, and because durable revisions are
	// atomic-per-batch and monotonic, every revision <= deliverFloor was genuinely
	// replayed (dedup-safe) while every revision > deliverFloor must be delivered
	// (loss-safe) — this is what closes the window where the in-memory boundary
	// named a revision that was neither durable-for-replay nor yet delivered live.
	didReplay    bool
	deliverFloor atomic.Int64

	err    atomic.Pointer[error]
	closed atomic.Bool
}

// newWatcher creates a new watcher with the given configuration. The
// caller must register the watcher with a Hub shard before events
// will be delivered.
func newWatcher(id uint64, key, rangeEnd []byte, opts WatchOptions, parentCtx context.Context) *watcher {
	ctx, cancel := context.WithCancel(parentCtx)
	w := &watcher{
		id:            id,
		key:           key,
		rangeEnd:      rangeEnd,
		events:        make(chan WatchResponse, defaultEventBufferSize),
		ctx:           ctx,
		cancel:        cancel,
		prevKV:        opts.PrevKV,
		startRevision: opts.StartRevision,
		filters:       opts.Filters,
		replayDone:    make(chan struct{}),
		deregistered:  make(chan struct{}),
	}
	// Default: live-only (unsynced == false). A replaying watcher is switched to
	// unsynced by the hub before it is registered (beginReplay); here replayDone
	// is pre-closed so cleanup for a live-only watcher never blocks.
	close(w.replayDone)
	// Default: no hub owns this watcher's teardown, so Close has nothing to wait
	// for and returns immediately. The hub calls beginCleanup to re-arm the
	// signal before it registers the watcher.
	close(w.deregistered)
	return w
}

// beginCleanup re-arms the deregistration signal so Close blocks until the
// hub's cleanup goroutine has removed the watcher from the registry. The hub
// calls it before the watcher is published to any other goroutine — before
// registration and before cleanupOnCancel starts — so the field write needs no
// synchronisation of its own.
func (w *watcher) beginCleanup() {
	w.deregistered = make(chan struct{})
}

// markDeregistered releases Close: the watcher is out of the shard registry and
// WatchCount no longer includes it.
func (w *watcher) markDeregistered() { close(w.deregistered) }

// beginReplay marks the watcher as unsynced and re-arms replayDone so live
// events are staged until the replay goroutine finishes. Called by the hub
// before the watcher is registered, so no live event can be missed. The
// deliver floor starts one below the requested start revision so nothing below
// it is ever delivered; the replay goroutine raises it as it streams history.
func (w *watcher) beginReplay() {
	w.replayDone = make(chan struct{})
	w.didReplay = true
	w.deliverFloor.Store(w.startRevision - 1)
	w.unsynced.Store(true)
}

// advanceDeliverFloor raises the deliver floor to rev (the replay goroutine
// calls this after delivering each historical event; revisions are ascending).
func (w *watcher) advanceDeliverFloor(rev int64) {
	if rev > w.deliverFloor.Load() {
		w.deliverFloor.Store(rev)
	}
}

// dedupDrop reports whether a replaying watcher must drop resp because its
// revision was already accounted for by the replay (at or below the floor).
// Live-only watchers never drop (their events may legitimately carry revision 0).
func (w *watcher) dedupDrop(resp WatchResponse) bool {
	return w.didReplay && resp.Revision <= w.deliverFloor.Load()
}

// Events returns the channel on which watch responses are delivered. The
// channel is closed when the watcher is canceled or an error occurs.
func (w *watcher) Events() <-chan WatchResponse {
	return w.events
}

// WatchID returns the unique identifier for this watcher.
func (w *watcher) WatchID() int64 {
	return int64(w.id)
}

// Close cancels the watcher and blocks until it has been removed from the Hub
// registry. After Close returns the watcher is gone: no further event can be
// dispatched to it and Hub.WatchCount no longer counts it, so a caller may
// assert on the count immediately. Before #309 Close only cancelled the
// context and deregistration happened later on the cleanup goroutine, which
// left every caller — the tests included — synchronising on a sleep that a
// loaded host outran.
//
// Close deliberately does NOT wait for the Events channel to close. That step
// additionally waits for an in-flight historical replay to stop touching the
// channel, and a replay only observes cancellation between the events it
// matches, so waiting here would let an arbitrarily long history scan block
// Close — and pkg/api/etcd's cancel path calls Close while holding the mutex
// that serialises its whole watch stream. Deregistration costs one shard write
// lock and nothing else. A caller that needs the channel drained ranges over
// Events(); it is still closed exactly once, immediately after the replay
// releases it.
//
// Close is idempotent, and every caller returns only after deregistration —
// not just the one that won the race to cancel.
func (w *watcher) Close() error {
	if w.closed.CompareAndSwap(false, true) {
		w.cancel()
	}
	<-w.deregistered
	return nil
}

// Err returns the first non-nil error encountered by the watcher.
// Returns nil if the watcher was closed normally via Close.
func (w *watcher) Err() error {
	if ep := w.err.Load(); ep != nil {
		return *ep
	}
	return nil
}

// setErr stores the error for later retrieval via Err.
func (w *watcher) setErr(err error) {
	w.err.CompareAndSwap(nil, &err)
}

// send delivers a watch response to the watcher. It MUST NOT block: it is
// called from the state-machine apply path (under the hub shard read lock),
// so blocking here would stall the entire Raft apply loop and make the node
// appear Unavailable to every client. Three non-blocking outcomes:
//   - buffer has room: enqueue the response;
//   - context already cancelled: drop (the watcher is going away);
//   - buffer full (slow/abandoned consumer): evict the watcher — record
//     ErrWatcherOverflow and cancel it.
//
// cancel() only flips the context; the cleanup goroutine's
// removeWatcher/close(events) is serialized behind the shard WRITE lock, so
// it cannot run until notify releases the READ lock it holds across this
// call — there is no send-on-closed-channel race.
func (w *watcher) send(resp WatchResponse) {
	// Fast path: a synced (or live-only) watcher delivers directly. This is the
	// common case and adds only one atomic load over the pre-replay design. A
	// replaying watcher drops a late notify for a revision the replay already
	// delivered (dedupDrop), so a delayed live delivery of a replayed revision
	// cannot duplicate it.
	if !w.unsynced.Load() {
		if w.dedupDrop(resp) {
			return
		}
		w.deliver(resp)
		return
	}
	// Unsynced: hold the live event in staging until replay completes. Re-check
	// UNDER the lock so an event cannot slip between the fast-path miss and
	// finishReplay clearing the flag — either this append is drained by
	// finishReplay, or the re-check sees synced and delivers directly.
	w.stagingMu.Lock()
	if !w.unsynced.Load() {
		w.stagingMu.Unlock()
		if w.dedupDrop(resp) {
			return
		}
		w.deliver(resp)
		return
	}
	w.staging = append(w.staging, resp)
	w.stagingMu.Unlock()
}

// deliver performs the actual non-blocking channel send with the same three
// outcomes send() documents (enqueue / drop-on-cancel / evict-on-overflow). It
// is called both from notify() (under the shard read lock) and from the replay
// goroutine (guarded by replayDone so cleanup cannot close events beneath it).
func (w *watcher) deliver(resp WatchResponse) {
	select {
	case w.events <- resp:
	case <-w.ctx.Done():
	default:
		w.setErr(ErrWatcherOverflow)
		w.cancel()
	}
}

// finishReplay flushes staged live events with a revision strictly greater than
// the deliver floor (events at or below it were already delivered by the
// historical replay, so dropping them dedupes the handoff), then flips the
// watcher to synced so later live events bypass staging. Ordering is preserved:
// notify is a single writer of monotonically increasing revisions, so staged
// events are already in revision order. The floor is the last revision the
// replay actually delivered — NOT the in-memory boundary — so a staged event
// for a revision the replay could not see (not yet durable at scan time) is
// above the floor and is delivered here rather than silently dropped.
func (w *watcher) finishReplay() {
	floor := w.deliverFloor.Load()
	w.stagingMu.Lock()
	for _, resp := range w.staging {
		if resp.Revision > floor {
			w.deliver(resp)
		}
	}
	w.staging = nil
	w.unsynced.Store(false)
	w.stagingMu.Unlock()
}

// markReplayDone signals that the replay goroutine has stopped touching the
// event channel, releasing cleanup to close it.
func (w *watcher) markReplayDone() { close(w.replayDone) }

// hasFilter returns true if the watcher has the specified filter.
func (w *watcher) hasFilter(f WatchFilter) bool {
	for _, wf := range w.filters {
		if wf == f {
			return true
		}
	}
	return false
}

// Compile-time assertion.
var _ Watcher = (*watcher)(nil)
