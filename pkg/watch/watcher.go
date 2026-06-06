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

	prevKV         bool
	startRevision  int64
	progressNotify bool
	filters        []WatchFilter

	err    atomic.Pointer[error]
	closed atomic.Bool
}

// newWatcher creates a new watcher with the given configuration. The
// caller must register the watcher with a Hub shard before events
// will be delivered.
func newWatcher(id uint64, key, rangeEnd []byte, opts WatchOptions, parentCtx context.Context) *watcher {
	ctx, cancel := context.WithCancel(parentCtx)
	return &watcher{
		id:             id,
		key:            key,
		rangeEnd:       rangeEnd,
		events:         make(chan WatchResponse, defaultEventBufferSize),
		ctx:            ctx,
		cancel:         cancel,
		prevKV:         opts.PrevKV,
		startRevision:  opts.StartRevision,
		progressNotify: opts.ProgressNotify,
		filters:        opts.Filters,
	}
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

// Close cancels the watcher and releases its resources. After Close
// returns, the Events channel will be closed. Close is idempotent.
func (w *watcher) Close() error {
	if !w.closed.CompareAndSwap(false, true) {
		return nil
	}
	w.cancel()
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
	select {
	case w.events <- resp:
	case <-w.ctx.Done():
	default:
		w.setErr(ErrWatcherOverflow)
		w.cancel()
	}
}

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
