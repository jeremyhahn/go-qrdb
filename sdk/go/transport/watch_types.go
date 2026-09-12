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

package transport

import "strings"

// EventType identifies the type of a watch event.
type EventType uint8

const (
	// EventPut indicates a key was created or updated.
	EventPut EventType = iota

	// EventDelete indicates a key was deleted.
	EventDelete
)

// WatchEvent represents a single change to a key.
type WatchEvent struct {
	Type   EventType     `json:"type"`
	Kv     *MVCCKeyValue `json:"kv"`
	PrevKv *MVCCKeyValue `json:"prev_kv,omitempty"`
}

// WatchOptions configures a watch request.
type WatchOptions struct {
	StartRevision  int64 `json:"start_revision,omitempty"`
	PrevKV         bool  `json:"prev_kv,omitempty"`
	ProgressNotify bool  `json:"progress_notify,omitempty"`
}

// WatchResponse delivers watch events from the server.
type WatchResponse struct {
	WatchID      int64         `json:"watch_id"`
	Events       []*WatchEvent `json:"events"`
	Canceled     bool          `json:"canceled,omitempty"`
	CancelReason string        `json:"cancel_reason,omitempty"`
	Revision     int64         `json:"revision"`
}

// Err returns the typed error describing why the server canceled the watch,
// or nil if this response is not a terminal cancellation. It recognizes the
// watch-history conditions the server surfaces via CancelReason
// (WatchCompactedError, WatchStartRevisionUnsupportedError) and falls back
// to a generic WatchCanceledError for any other reason.
func (r WatchResponse) Err() error {
	if !r.Canceled {
		return nil
	}
	if typed := classifyWatchReason(r.CancelReason); typed != nil {
		return typed
	}
	return &WatchCanceledError{Reason: r.CancelReason}
}

// ---------------------------------------------------------------------------
// Watch error classification
// ---------------------------------------------------------------------------
//
// pkg/watch (server-side) surfaces two well-known conditions with stable
// sentinel error text: ErrCompacted ("required start revision has been
// compacted") when a requested start revision has already been GC'd from the
// change-history index, and ErrStartRevisionUnsupported ("start revision
// replay is not supported by this store") when the store backing the watch
// has no change history attached. The SDK does not import pkg/watch (client
// code must not depend on server-internal packages), so these two helpers
// classify by matching the stable message substring — however it arrived,
// whether as a request-time error (REST/gRPC/QUIC/unix) or as the
// CancelReason of a terminal WatchResponse delivered mid-stream (all six
// transports, embedded included since go-qrdb#405).

const (
	watchCompactedReasonSubstr          = "required start revision has been compacted"
	watchStartRevisionUnsupportedSubstr = "start revision replay is not supported by this store"
)

// classifyWatchReason returns the typed error for a known watch-history
// condition matching reason, or nil if reason does not match either
// condition.
func classifyWatchReason(reason string) error {
	switch {
	case strings.Contains(reason, watchCompactedReasonSubstr):
		return &WatchCompactedError{Reason: reason}
	case strings.Contains(reason, watchStartRevisionUnsupportedSubstr):
		return &WatchStartRevisionUnsupportedError{Reason: reason}
	default:
		return nil
	}
}

// ClassifyWatchError inspects err's message for a known watch-history
// condition surfaced by the server (see classifyWatchReason) and, if found,
// returns the corresponding typed error (WatchCompactedError or
// WatchStartRevisionUnsupportedError). It returns err unchanged if the
// message does not match a known condition, and nil if err is nil. Transport
// implementations call this when decoding a request-time error from the
// Watch/WatchPrefix call so every transport surfaces the same typed error
// for the same server-side condition.
func ClassifyWatchError(err error) error {
	if err == nil {
		return nil
	}
	if typed := classifyWatchReason(err.Error()); typed != nil {
		return typed
	}
	return err
}
