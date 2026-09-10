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

import "github.com/jeremyhahn/go-qrdb/pkg/kv"

// ---------------------------------------------------------------------------
// Event types
// ---------------------------------------------------------------------------

// EventType identifies the type of a watch event.
type EventType uint8

const (
	// EventPut indicates a key was created or updated.
	EventPut EventType = iota

	// EventDelete indicates a key was deleted.
	EventDelete
)

// Event represents a single change to a key observed by a watcher.
type Event struct {
	// Type is the kind of event (put or delete).
	Type EventType

	// Kv holds the current key-value state after the event.
	Kv *kv.KeyValue

	// PrevKv holds the key-value state before the event. Only populated
	// when the watcher is created with WithPrevKV.
	PrevKv *kv.KeyValue
}

// ---------------------------------------------------------------------------
// Watch filter
// ---------------------------------------------------------------------------

// WatchFilter specifies which event types a watcher should skip.
type WatchFilter uint8

const (
	// WatchFilterNoPut filters out put events so the watcher only
	// receives delete events.
	WatchFilterNoPut WatchFilter = iota + 1

	// WatchFilterNoDelete filters out delete events so the watcher
	// only receives put events.
	WatchFilterNoDelete
)

// ---------------------------------------------------------------------------
// Watch response
// ---------------------------------------------------------------------------

// WatchResponse is the envelope delivered to watchers for each event batch.
// It mirrors the etcd WatchResponse structure for protocol compatibility.
type WatchResponse struct {
	// WatchID is the identifier of the watcher that generated this response.
	WatchID int64

	// Events contains the change events in this response.
	Events []*Event

	// Revision is the MVCC store revision at the time of the events.
	Revision int64

	// CompactRevision is set when the watcher falls behind a compaction.
	// A non-zero value indicates the watcher needs to be recreated.
	CompactRevision int64

	// Canceled is true when the watcher has been canceled by the server.
	Canceled bool

	// CancelReason describes why the watcher was canceled, if applicable.
	CancelReason string

	// Created is true for the single synthetic response emitted when a watch
	// with a start revision is established, before any change events. Its
	// Revision is the replay's intended upper bound, captured before a single
	// historical event has been delivered — NOT a delivery watermark. A
	// consumer that resumes from Created.Revision on a collection window that
	// expires mid-replay skips every event the replay never got to (#210).
	// Use Progress for a value that is actually safe to resume from.
	Created bool

	// Progress is true for the synthetic response startReplay emits after a
	// start-revision replay has FULLY delivered every matching event through
	// Revision (opt-in via WithProgressNotify), including the flush of any
	// live event staged during the replay. Unlike Created, a consumer that
	// advances its cursor to Progress.Revision has not skipped anything: every
	// revision at or below it was either replayed or handed off to live
	// delivery before this response was sent. Never emitted when the replay
	// fails or is truncated (finishReplay is only reached on success), so its
	// absence during a bounded collection window is itself the truncation
	// signal a caller acts on.
	Progress bool
}

// ---------------------------------------------------------------------------
// History replay
// ---------------------------------------------------------------------------

// HistoryReplayer replays historical key changes so a watch created with a
// start revision receives every change since that revision before it switches
// to live delivery. It is implemented by an adapter over the MVCC store and
// attached to a Hub with SetHistory; a Hub without one serves live-only watches
// and rejects a start revision with ErrCompacted semantics via the hub.
type HistoryReplayer interface {
	// CurrentRevision returns the store's latest committed revision.
	CurrentRevision() int64

	// CompactedRevision returns the highest revision that has been compacted
	// away; a start revision at or below it can no longer be replayed.
	CompactedRevision() int64

	// Replay invokes fn for each change with startRev <= revision <= endRev, in
	// ascending revision order (and ascending key order within a revision),
	// whose key satisfies keyMatch. When wantPrev is true, delete/update events
	// carry the previous key-value state. fn returns false to stop early.
	Replay(startRev, endRev int64, keyMatch func(key []byte) bool, wantPrev bool, fn func(*Event) bool) error
}

// ---------------------------------------------------------------------------
// Watcher interface
// ---------------------------------------------------------------------------

// Watcher delivers a stream of events for watched keys. Implementations
// must be safe for concurrent use.
type Watcher interface {
	// Events returns the channel on which watch responses are delivered.
	// The channel is closed when the watcher is canceled or an error
	// occurs.
	Events() <-chan WatchResponse

	// WatchID returns the unique identifier for this watcher.
	WatchID() int64

	// Close cancels the watcher and releases its resources. After Close
	// returns, the Events channel will be drained and closed.
	Close() error

	// Err returns the first non-nil error encountered by the watcher.
	// Returns nil if the watcher was closed normally via Close.
	Err() error
}

// ---------------------------------------------------------------------------
// Watch options
// ---------------------------------------------------------------------------

// WatchOptions holds the resolved configuration for a watch request.
type WatchOptions struct {
	StartRevision  int64
	PrevKV         bool
	ProgressNotify bool
	Filters        []WatchFilter
}

// WatchOption configures a watch request.
type WatchOption func(*WatchOptions)

// WithStartRevision begins watching from the given revision, delivering
// all events since that revision.
func WithStartRevision(rev int64) WatchOption {
	return func(o *WatchOptions) {
		o.StartRevision = rev
	}
}

// WithPrevKV requests that events include the previous key-value state
// before the modification.
func WithPrevKV() WatchOption {
	return func(o *WatchOptions) {
		o.PrevKV = true
	}
}

// WithProgressNotify enables periodic progress notification events
// even when no changes occur.
func WithProgressNotify() WatchOption {
	return func(o *WatchOptions) {
		o.ProgressNotify = true
	}
}

// WithFilters adds event type filters to the watch request. Filtered
// event types are silently dropped before delivery.
func WithFilters(filters ...WatchFilter) WatchOption {
	return func(o *WatchOptions) {
		o.Filters = append(o.Filters, filters...)
	}
}

// ApplyWatchOptions resolves variadic WatchOption values into a
// WatchOptions struct.
func ApplyWatchOptions(opts []WatchOption) WatchOptions {
	var wo WatchOptions
	for _, opt := range opts {
		opt(&wo)
	}
	return wo
}
