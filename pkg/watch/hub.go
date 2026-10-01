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

// Package watch implements real-time change notifications for the MVCC
// key-value store. It provides a high-performance pub/sub mechanism for
// key change events with minimal overhead on the write path.
//
// Key features:
//   - Sharded watch registry for low lock contention (256 shards)
//   - Support for exact key and prefix watches
//   - Zero-cost overhead when no watches are active (atomic check)
//   - Async event delivery via buffered channels
//   - Revision-ordered event delivery guaranteed by state machine integration
//
// Performance characteristics:
//   - No watches: single atomic load (~1-2ns)
//   - Active watches: O(1) hash lookup + channel send (~150-300ns)
//   - Memory: ~200 bytes per watcher
package watch

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
)

// shardCount is the number of shards in the watch registry. Must be a
// power of two for efficient modular arithmetic.
const shardCount = 256

// Hub coordinates all active watchers and dispatches events from the
// state machine. It uses a sharded registry to minimize lock contention,
// with each shard managing watchers whose keys hash to that shard.
//
// Thread safety: Hub is safe for concurrent use. NotifyPut and
// NotifyDelete are called from the state machine's Update method
// (single writer), while Watch/WatchPrefix/Close may be called from
// any goroutine.
type Hub struct {
	shards     [shardCount]*shard
	idSeq      atomic.Uint64
	watchCount atomic.Uint64
	closed     atomic.Bool

	// history replays committed changes for start-revision catch-up. Attached
	// via SetHistory at construction (nil on a non-MVCC hub, which then serves
	// live-only watches). Stored atomically so a late attach never races a Watch.
	history atomic.Pointer[HistoryReplayer]

	// afterBoundaryCapture is a test-only synchronization seam, nil in
	// production. A test sets it on its own Hub before opening a watch; the
	// replay goroutine reads it after spawn, so the go statement orders the
	// write before the read. See its use in startReplay for what it fires on.
	afterBoundaryCapture func()
}

// SetHistory attaches (or clears, with nil) the replayer that serves
// start-revision catch-up. Call it once during construction, before the hub
// serves watches.
func (h *Hub) SetHistory(r HistoryReplayer) {
	if r == nil {
		h.history.Store(nil)
		return
	}
	h.history.Store(&r)
}

// historyReplayer returns the attached replayer, or nil.
func (h *Hub) historyReplayer() HistoryReplayer {
	if p := h.history.Load(); p != nil {
		return *p
	}
	return nil
}

// shard holds a subset of watchers to reduce lock contention. Each
// shard manages watchers whose keys hash to that shard index.
type shard struct {
	exactWatches  map[string][]*watcher // exact key → watchers
	prefixWatches []*watcher            // prefix watchers (requires iteration)
	mu            sync.RWMutex
}

// NewHub creates a new Hub with pre-initialized shards.
func NewHub() *Hub {
	h := &Hub{}
	for i := range h.shards {
		h.shards[i] = &shard{
			exactWatches:  make(map[string][]*watcher),
			prefixWatches: make([]*watcher, 0),
		}
	}
	return h
}

// Watch creates a watcher that delivers events for the specified key.
// Returns an error if the hub is closed.
func (h *Hub) Watch(ctx context.Context, key string, opts ...WatchOption) (Watcher, error) {
	if h.closed.Load() {
		return nil, &HubClosedError{}
	}

	wo := ApplyWatchOptions(opts)

	// Validate a start revision BEFORE creating any state, so a compacted or
	// unsupported request fails cleanly with nothing registered.
	replay, err := h.prepareReplay(wo.StartRevision)
	if err != nil {
		return nil, err
	}

	id := h.idSeq.Add(1)
	w := newWatcher(id, []byte(key), nil, wo, ctx)
	if replay {
		w.beginReplay()
	}
	// Arm the deregistration signal before the watcher is reachable from any
	// other goroutine: from here on this hub owns its teardown, so Close waits
	// for cleanupOnCancel rather than returning while the watcher is still
	// registered.
	w.beginCleanup()

	sh := h.getShard([]byte(key))
	sh.mu.Lock()
	keyStr := key
	sh.exactWatches[keyStr] = append(sh.exactWatches[keyStr], w)
	boundary := h.captureReplayBoundary(replay)
	sh.mu.Unlock()

	h.watchCount.Add(1)

	// Start cleanup goroutine to remove watcher when cancelled.
	go h.cleanupOnCancel(w)

	// Registration is complete: replay historical changes and hand off to live.
	if replay {
		h.startReplay(w, wo, boundary)
	}

	return w, nil
}

// WatchPrefix creates a watcher that delivers events for all keys
// matching the given prefix. Returns an error if the hub is closed.
func (h *Hub) WatchPrefix(ctx context.Context, prefix string, opts ...WatchOption) (Watcher, error) {
	if h.closed.Load() {
		return nil, &HubClosedError{}
	}

	wo := ApplyWatchOptions(opts)

	replay, err := h.prepareReplay(wo.StartRevision)
	if err != nil {
		return nil, err
	}

	id := h.idSeq.Add(1)
	rangeEnd := prefixEnd([]byte(prefix))
	w := newWatcher(id, []byte(prefix), rangeEnd, wo, ctx)
	if replay {
		w.beginReplay()
	}
	w.beginCleanup()

	// Prefix watchers are stored in the shard for the prefix key itself.
	sh := h.getShard([]byte(prefix))
	sh.mu.Lock()
	sh.prefixWatches = append(sh.prefixWatches, w)
	boundary := h.captureReplayBoundary(replay)
	sh.mu.Unlock()

	h.watchCount.Add(1)

	go h.cleanupOnCancel(w)

	if replay {
		h.startReplay(w, wo, boundary)
	}

	return w, nil
}

// prepareReplay validates a requested start revision and reports whether a
// historical replay is required. A zero/negative start revision is a live-only
// watch (false, nil). A positive start revision needs a history replayer and a
// revision above the compaction floor, else it fails closed.
func (h *Hub) prepareReplay(startRev int64) (bool, error) {
	if startRev <= 0 {
		return false, nil
	}
	hr := h.historyReplayer()
	if hr == nil {
		return false, ErrStartRevisionUnsupported
	}
	if startRev <= hr.CompactedRevision() {
		return false, ErrCompacted
	}
	return true, nil
}

// captureReplayBoundary reads the replay boundary -- the upper bound for a
// historical scan, and the revision below which a live event needs no replay
// to be delivered safely -- and MUST be called by Watch/WatchPrefix while
// still holding the shard write lock that just made the watcher visible to
// notify(), in the same critical section as the registration write. Returns 0
// when replay is false (a live-only watch, which never uses the value) or
// when the history replayer has raced to nil since prepareReplay observed it
// (startReplay's own re-check handles that case; a stale 0 here is harmless
// because it is never read on that path).
//
// Reading hr.CurrentRevision() at any LATER point -- after this shard's lock
// has been released -- lets an arbitrary amount of wall-clock time separate
// "the watcher started receiving live events" from "the boundary used to
// decide whether a historical replay is even needed". A write to this
// watcher's own key racing ahead in that window is staged correctly (the
// watcher is already registered and unsynced), but a boundary read AFTER it
// would also count it, flipping a start revision that needed no replay at
// all into one the code believes needs a historical scan -- exposing an
// already-fully-satisfied watch to that scan's own compaction-race check,
// and cancelling it for a compaction race that invalidated nothing it
// actually needed (go-qrdb#514).
//
// Capturing it here cannot observe such a write: that write's own notify()
// call for THIS key needs this same shard lock, so it cannot have completed
// -- and therefore cannot yet be staged -- before this read returns. Every
// live event this watcher can ever see is therefore either already reflected
// in the returned boundary or is still strictly ahead of it, with nothing in
// between the two ever having reached (or bypassed) this watcher unseen.
func (h *Hub) captureReplayBoundary(replay bool) int64 {
	if !replay {
		return 0
	}
	hr := h.historyReplayer()
	if hr == nil {
		return 0
	}
	return hr.CurrentRevision()
}

// startReplay streams the historical change range to a freshly registered,
// unsynced watcher, then hands off to live delivery. boundary is the replay
// upper bound, the Created cursor origin, and (once replay completes) the
// Progress watermark; the caller captured it atomically with registration
// (captureReplayBoundary), so it is immune to the race that reading
// hr.CurrentRevision() again here, after the fact, would reintroduce
// (go-qrdb#514). Delivery dedup is driven by the watcher's deliver floor --
// the last revision the replay ACTUALLY delivered -- not boundary, so a
// revision the in-memory counter had already advanced to but that was not
// yet durable at scan time is delivered by the staged-live path rather than
// dropped.
func (h *Hub) startReplay(w *watcher, wo WatchOptions, boundary int64) {
	hr := h.historyReplayer()
	if hr == nil {
		// Raced a SetHistory(nil); nothing to replay from. Fail closed rather
		// than leave the watcher wedged unsynced.
		w.setErr(ErrStartRevisionUnsupported)
		go func() {
			defer w.markReplayDone()
			w.finishReplay()
			w.cancel()
		}()
		return
	}

	go func() {
		defer w.markReplayDone()

		// afterBoundaryCapture, when set, exists solely so a test can pin an
		// interleaving between "boundary is already fixed, atomically, as of
		// registration" and whatever the replay decision below does with it --
		// go-qrdb#514. boundary itself cannot change past this point (it is a
		// local copy of the caller's already-captured value), so this hook can
		// only ever observe or delay the DECISION, never the value it decides
		// from. Nil in production; costs one nil check per replay.
		if hook := h.afterBoundaryCapture; hook != nil {
			hook()
		}

		// Created response first: its Revision is the client's cursor origin.
		w.deliver(WatchResponse{WatchID: int64(w.id), Created: true, Revision: boundary})

		// attemptedReplay is also what gates the fail-closed check below: a
		// start revision beyond the boundary needs no historical scan, so
		// there is nothing for a LATER compaction to have invalidated (see
		// that check's comment).
		attemptedReplay := w.ctx.Err() == nil && wo.StartRevision <= boundary
		var replayErr error
		if attemptedReplay {
			replayErr = hr.Replay(wo.StartRevision, boundary, w.matches, w.prevKV, func(ev *Event) bool {
				if w.ctx.Err() != nil {
					return false
				}
				if ev.Type == EventPut && w.hasFilter(WatchFilterNoPut) {
					return true
				}
				if ev.Type == EventDelete && w.hasFilter(WatchFilterNoDelete) {
					return true
				}
				if !w.prevKV {
					ev.PrevKv = nil
				}
				var rev int64
				if ev.Kv != nil {
					rev = ev.Kv.ModRevision
				}
				w.deliver(WatchResponse{WatchID: int64(w.id), Events: []*Event{ev}, Revision: rev})
				w.advanceDeliverFloor(rev)
				return true
			})
		}

		// Fail closed rather than hand off a silently incomplete stream when a
		// replay we actually attempted could not be completed intact: either
		// compaction advanced past our start revision WHILE replaying, or the
		// scan hit a value that had been compacted out from under it (Replay
		// returns an error). finishReplay first so no live event is left
		// stranded in staging.
		//
		// Gated on attemptedReplay: a start revision beyond the boundary
		// captured above needed no replay, so compaction advancing past it
		// LATER invalidates nothing there was ever a scan of. Without this
		// guard, a watch registered at a future revision could be spuriously
		// cancelled by unrelated writes and compaction racing past that
		// revision between registration and this check, even though the
		// watcher was registered before every relevant event and is already
		// correctly withholding them via the deliver floor until one arrives
		// at or after wo.StartRevision (go-qrdb#514).
		if attemptedReplay && (replayErr != nil || hr.CompactedRevision() >= wo.StartRevision) {
			w.setErr(ErrCompacted)
			w.finishReplay()
			w.cancel()
			return
		}

		w.finishReplay()

		// Post-replay watermark (#210): finishReplay has just flushed every
		// staged live event through the deliver floor, so everything through
		// boundary has now actually reached the channel. Created's Revision
		// cannot serve this role — it is sent before replay starts, so a
		// collection window that expires mid-replay would hand a caller a
		// cursor covering events it never got. Opt-in because it is one more
		// response per replay and only a start-revision caller that resumes
		// across bounded collection windows (e.g. pkg/api/mcp/tools_watch.go)
		// needs it.
		if wo.ProgressNotify {
			w.deliver(WatchResponse{WatchID: int64(w.id), Revision: boundary, Progress: true})
		}
	}()
}

// NotifyPut dispatches a put event to all watchers matching the key.
// Called from the state machine after a successful put operation. The
// kv parameter is the new key-value state; prevKv is the previous
// state (nil for new keys).
func (h *Hub) NotifyPut(key []byte, kvVal, prevKv *kv.KeyValue) {
	if h.watchCount.Load() == 0 {
		return
	}
	h.notify(key, EventPut, kvVal, prevKv)
}

// NotifyDelete dispatches a delete event to all watchers matching the
// key. Called from the state machine after a successful delete
// operation. The kv parameter contains the tombstone entry; prevKv is
// the value that was deleted.
func (h *Hub) NotifyDelete(key []byte, kvVal, prevKv *kv.KeyValue) {
	if h.watchCount.Load() == 0 {
		return
	}
	h.notify(key, EventDelete, kvVal, prevKv)
}

// WatchCount returns the number of active watchers.
func (h *Hub) WatchCount() uint64 {
	return h.watchCount.Load()
}

// Close cancels all active watchers and prevents new watchers from
// being created. After Close returns, all watcher event channels will
// be closed.
func (h *Hub) Close() {
	if !h.closed.CompareAndSwap(false, true) {
		return
	}

	for i := range h.shards {
		sh := h.shards[i]
		sh.mu.Lock()

		for _, watchers := range sh.exactWatches {
			for _, w := range watchers {
				w.cancel()
			}
		}
		sh.exactWatches = make(map[string][]*watcher)

		for _, w := range sh.prefixWatches {
			w.cancel()
		}
		sh.prefixWatches = sh.prefixWatches[:0]

		sh.mu.Unlock()
	}

	h.watchCount.Store(0)
}

// ---------------------------------------------------------------------------
// Internal methods
// ---------------------------------------------------------------------------

// notify dispatches an event to all matching watchers (exact + prefix).
func (h *Hub) notify(key []byte, eventType EventType, kvVal, prevKv *kv.KeyValue) {
	// Check exact watchers in the shard for this key.
	sh := h.getShard(key)
	sh.mu.RLock()
	if watchers, ok := sh.exactWatches[string(key)]; ok {
		for _, w := range watchers {
			h.sendEvent(w, eventType, kvVal, prevKv)
		}
	}
	sh.mu.RUnlock()

	// Check prefix watchers across all shards, since a prefix watcher
	// may be in a different shard than the event key.
	for i := range h.shards {
		s := h.shards[i]
		s.mu.RLock()
		for _, w := range s.prefixWatches {
			if matchesRange(key, w.key, w.rangeEnd) {
				h.sendEvent(w, eventType, kvVal, prevKv)
			}
		}
		s.mu.RUnlock()
	}
}

// sendEvent constructs an event and sends it to the watcher, applying
// any configured filters.
func (h *Hub) sendEvent(w *watcher, eventType EventType, kvVal, prevKv *kv.KeyValue) {
	// Apply filters: skip events matching a filter.
	if eventType == EventPut && w.hasFilter(WatchFilterNoPut) {
		return
	}
	if eventType == EventDelete && w.hasFilter(WatchFilterNoDelete) {
		return
	}

	event := &Event{
		Type: eventType,
		Kv:   kvVal,
	}

	if w.prevKV {
		event.PrevKv = prevKv
	}

	var rev int64
	if kvVal != nil {
		rev = kvVal.ModRevision
	}

	w.send(WatchResponse{
		WatchID:  int64(w.id),
		Events:   []*Event{event},
		Revision: rev,
	})
}

// cleanupOnCancel waits for a watcher's context to be cancelled, then
// removes it from the shard registry and closes its event channel.
func (h *Hub) cleanupOnCancel(w *watcher) {
	<-w.ctx.Done()

	// Deregister FIRST, then release Close. removeWatcher takes the shard write
	// lock, so once it returns no new notify can reach this watcher and every
	// live send already in flight has completed under the read lock it waited
	// on — which is exactly the state Close publishes to its caller.
	h.removeWatcher(w)
	w.markDeregistered()

	// Closing the event channel additionally requires the replay goroutine to
	// have stopped touching it, so a replay in flight can never send on a closed
	// channel. For a live-only watcher replayDone is pre-closed and this is a
	// no-op. It sits AFTER markDeregistered on purpose: a replay observes
	// cancellation only between the events it matches, so a Close that waited
	// here would inherit the duration of a history scan it has no reason to
	// care about.
	<-w.replayDone
	close(w.events)
}

// matches reports whether key falls within this watcher's scope: exact-key
// equality for a key watch, range containment for a prefix watch.
func (w *watcher) matches(key []byte) bool {
	if w.rangeEnd == nil {
		return bytes.Equal(key, w.key)
	}
	return matchesRange(key, w.key, w.rangeEnd)
}

// removeWatcher removes a watcher from the shard registry and decrements the
// watch count if — and only if — it was still registered.
//
// The decrement is conditional because Hub.Close empties the registry and
// zeroes the count itself, and each cancelled watcher's cleanup goroutine then
// arrives here with nothing left to remove. Decrementing unconditionally wrapped
// the unsigned counter: measured on this tree before the fix, WatchCount()
// returned 18446744073709551615 after a Hub.Close whose cleanup goroutines had
// run. That was survivable only while Close was asynchronous, because the
// goroutines usually landed after the caller had already read the count; making
// Close wait for them (#309) puts the wrap on the path of every Hub.Close.
func (h *Hub) removeWatcher(w *watcher) {
	removed := false
	sh := h.getShard(w.key)

	if w.rangeEnd == nil {
		// Exact watcher.
		sh.mu.Lock()
		keyStr := string(w.key)
		watchers := sh.exactWatches[keyStr]
		for j, cw := range watchers {
			if cw.id == w.id {
				sh.exactWatches[keyStr] = append(watchers[:j], watchers[j+1:]...)
				if len(sh.exactWatches[keyStr]) == 0 {
					delete(sh.exactWatches, keyStr)
				}
				removed = true
				break
			}
		}
		sh.mu.Unlock()
	} else {
		// Prefix watcher.
		sh.mu.Lock()
		for j, cw := range sh.prefixWatches {
			if cw.id == w.id {
				sh.prefixWatches = append(sh.prefixWatches[:j], sh.prefixWatches[j+1:]...)
				removed = true
				break
			}
		}
		sh.mu.Unlock()
	}

	if !removed {
		return
	}
	h.watchCount.Add(^uint64(0)) // atomic decrement
}

// getShard returns the shard for a given key using FNV-1a hash.
func (h *Hub) getShard(key []byte) *shard {
	return h.shards[fnv1a(key)]
}

// fnv1a implements the FNV-1a hash function for shard selection.
// Returns a shard index in the range [0, 255].
func fnv1a(key []byte) uint8 {
	const (
		offset32 = uint32(2166136261)
		prime32  = uint32(16777619)
	)
	hash := offset32
	for _, b := range key {
		hash ^= uint32(b)
		hash *= prime32
	}
	return uint8(hash % shardCount)
}

// matchesRange checks if a key falls within a watch's range [start, end).
// An empty but non-nil end means "match all keys at or after start".
func matchesRange(key, start, end []byte) bool {
	if bytes.Compare(key, start) < 0 {
		return false
	}
	if len(end) > 0 && bytes.Compare(key, end) >= 0 {
		return false
	}
	return true
}

// prefixEnd returns the range end for a prefix watch. For example,
// "foo/" becomes "foo0" to match all keys starting with "foo/".
// An empty prefix returns an empty non-nil slice (match all keys).
func prefixEnd(prefix []byte) []byte {
	if len(prefix) == 0 {
		return []byte{}
	}

	end := make([]byte, len(prefix))
	copy(end, prefix)

	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}

	// All bytes are 0xff: match everything.
	return []byte{}
}
