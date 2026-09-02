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
	"encoding/json"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.WatchClient = (*Transport)(nil)

// MCP is request/response, not streaming, so Watch/WatchPrefix are built on
// top of the qrdb_watch tool by polling it in a loop (watchPoll). Each poll
// is a bounded (watchPollTimeoutSeconds) qrdb_watch call that creates a
// fresh server-side watcher, collects for the bound, and returns.
//
// An earlier version of this loop polled with no cursor: every call either
// omitted start_revision entirely, or only carried one forward once an event
// had already been observed. Any mutation landing in the gap between one
// poll's watcher closing and the next poll's watcher being created — which,
// with a ~500ms pollInterval plus the MCP round trip, is a real,
// non-negligible window — was silently and permanently lost, because a
// plain live-only watch (no start_revision) never replays anything that
// happened before its own creation.
//
// This version is cursor-based: it tracks the highest revision observed
// (nextRevision, primed to a real anchor before the very first poll — see
// below) and requests start_revision = nextRevision on every poll,
// unconditionally. The server-side qrdb_watch tool then always takes the
// hub's replay-then-live-handoff path (pkg/watch.Hub.startReplay), which
// guarantees every change since nextRevision is delivered exactly once
// before the tool call returns — including the empty "Created" response
// that arrives even when nothing changed, which is what lets the cursor
// advance (and the gap stay closed) during idle periods. This makes the
// polling loop as a whole gap-free for as long as it runs, independent of
// how often it actually observes an event.
//
// Bootstrap: nextRevision must be a real revision before the first poll, not
// merely "the next revision the caller happens to see" — otherwise the very
// first poll (and every poll before the first event) would still omit
// start_revision and reopen the same gap. If the caller supplied an
// explicit opts.StartRevision, that is used as-is (inclusive, matching
// pkg/watch.WithStartRevision semantics). Otherwise nextRevision is primed
// with a CurrentRevision RPC before watchPoll's loop starts, so the window
// between the caller invoking Watch/WatchPrefix and the first poll actually
// running is covered by that first poll's replay rather than silently
// dropped. If CurrentRevision is unsupported (a non-MVCC backend) or fails
// for any other reason, the loop degrades to the old best-effort live-only
// behavior for its first poll only; the cursor engages from the first
// reported revision onward.
var (
	// watchPollInterval is the delay between successive watch tool calls.
	// A package variable (not a const) so tests can shrink it.
	watchPollInterval = 500 * time.Millisecond

	// watchPollTimeoutSeconds is the collection window (timeout_seconds)
	// requested on each qrdb_watch tool call. A package variable (not a
	// const) so tests can shrink it.
	watchPollTimeoutSeconds = 1.0

	// watchPollClock supplies every delay the poll loop waits on. Production
	// uses realWatchClock, a direct time.After. Tests substitute a clock whose
	// After returns a channel they fire by hand, which turns "when does the
	// next poll happen" from a wall-clock race into a schedule the test owns:
	// a polling test can then push a change while the loop is provably parked
	// between polls and assert no-loss against that schedule rather than
	// against elapsed time. See issue #171 — the previous wall-clock-timed
	// test could not distinguish a genuinely lost update from a busy host, and
	// failed three times on unrelated changes because of it.
	//
	// A package variable, matching the two above, and deliberately a single
	// After method rather than a bespoke ticker abstraction: issue #172's
	// repo-wide clock seam can supply this package's clock by satisfying the
	// same method, so absorbing this costs an assignment rather than an undo.
	watchPollClock watchClock = realWatchClock{}
)

// watchClock is the minimal time seam the poll loop needs: a channel that
// fires after a delay. Kept to one method so any general-purpose clock
// satisfies it without adaptation.
type watchClock interface {
	After(d time.Duration) <-chan time.Time
}

// realWatchClock is the production clock, delegating to the runtime timer.
type realWatchClock struct{}

// After returns a channel that receives after d has elapsed.
func (realWatchClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// ---------------------------------------------------------------------------
// Server-side JSON decode types for watch responses
// ---------------------------------------------------------------------------

// serverWatchResult mirrors the watchResult envelope returned by the
// qrdb_watch tool.
type serverWatchResult struct {
	Events   []serverWatchEvent `json:"events"`
	Revision int64              `json:"revision"`
	WatchID  int64              `json:"watch_id"`
}

// serverWatchEvent mirrors the watchEventJSON returned by the
// qrdb_watch tool.
type serverWatchEvent struct {
	Type   string          `json:"type"`
	Kv     json.RawMessage `json:"kv,omitempty"`
	PrevKv json.RawMessage `json:"prev_kv,omitempty"`
}

// watchEventTypeFromString maps server event type strings to SDK
// EventType constants for O(1) dispatch.
var watchEventTypeFromString = map[string]transport.EventType{
	"PUT":    transport.EventPut,
	"DELETE": transport.EventDelete,
}

// ---------------------------------------------------------------------------
// WatchClient implementation
// ---------------------------------------------------------------------------

// Watch creates a watcher that delivers events for the specified key.
// Since MCP is a request/response protocol without native streaming, the
// transport polls the qrdb_watch tool in a cursor-based loop (see the
// package-level doc comment above) and delivers results to the returned
// channel. The caller must cancel the context to stop watching.
func (t *Transport) Watch(ctx context.Context, key string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	return t.watchPoll(ctx, key, false, opts)
}

// WatchPrefix creates a watcher that delivers events for all keys
// matching the given prefix. See Watch for details on the polling
// mechanism.
func (t *Transport) WatchPrefix(ctx context.Context, prefix string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	return t.watchPoll(ctx, prefix, true, opts)
}

// watchPoll starts a background goroutine that polls the qrdb_watch MCP
// tool in a cursor-based loop, delivering results to the returned channel.
// The goroutine terminates and closes the channel when the context is
// canceled or the watch is terminally canceled by the server (a compacted
// or unsupported start revision — see transport.ClassifyWatchError).
func (t *Transport) watchPoll(ctx context.Context, key string, prefix bool, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ch := make(chan transport.WatchResponse, 16)

	// Read the clock here, on the caller's goroutine, so the poll goroutine
	// never races a test restoring watchPollClock during teardown.
	clock := watchPollClock

	go func() {
		defer close(ch)

		nextRevision := t.bootstrapWatchCursor(ctx, opts.StartRevision)

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			// sentStartRevision records whether THIS poll's request carries
			// start_revision, captured before nextRevision is potentially
			// updated below. It gates the cursor-advancement step: only a
			// poll that requested replay is guaranteed a "Created" response
			// (with a trustworthy Revision, even 0 for an empty store) when
			// no events occurred. A plain live-only poll (no start_revision)
			// never gets a Created response, so its zero-value Revision
			// field when no events occurred carries no information — it
			// must not be mistaken for "confirmed revision 0" and used to
			// spuriously pin the cursor.
			sentStartRevision := nextRevision > 0

			args := map[string]any{
				"key":             key,
				"timeout_seconds": watchPollTimeoutSeconds,
			}
			if prefix {
				args["prefix"] = true
			}
			if sentStartRevision {
				args["start_revision"] = nextRevision
			}
			if opts.PrevKV {
				args["prev_kv"] = true
			}

			text, err := t.callTool(ctx, "qrdb_watch", args)
			if err != nil {
				// If the context is canceled, exit gracefully.
				if ctx.Err() != nil {
					return
				}
				// A terminal, server-reported watch condition (compacted or
				// unsupported start revision, or an explicit watch-canceled
				// failure) ends the watch rather than retrying forever
				// against the same unrecoverable error.
				if reason, terminal := classifyWatchPollError(err); terminal {
					select {
					case ch <- transport.WatchResponse{Canceled: true, CancelReason: reason}:
					case <-ctx.Done():
					}
					return
				}
				// Transient error; wait before retrying.
				select {
				case <-ctx.Done():
					return
				case <-clock.After(watchPollInterval):
					continue
				}
			}

			var result serverWatchResult
			if jsonErr := json.Unmarshal([]byte(text), &result); jsonErr != nil {
				select {
				case <-ctx.Done():
					return
				case <-clock.After(watchPollInterval):
					continue
				}
			}

			// Advance the cursor past every revision the server reported.
			// A replay poll (sentStartRevision) always gets a Created
			// response even with zero events, so its Revision anchors the
			// next poll's start_revision even when nothing changed — what
			// keeps the loop gap-free while idle. A live-only poll's
			// Revision is only trustworthy when it actually carried events
			// (see sentStartRevision's comment above).
			if (sentStartRevision || len(result.Events) > 0) && result.Revision+1 > nextRevision {
				nextRevision = result.Revision + 1
			}

			// Convert server events to transport events.
			if len(result.Events) > 0 {
				events := make([]*transport.WatchEvent, 0, len(result.Events))
				for _, sev := range result.Events {
					we := &transport.WatchEvent{
						Type: watchEventTypeFromString[strings.ToUpper(sev.Type)],
					}
					if len(sev.Kv) > 0 {
						var kv serverKeyValue
						if jsonErr := json.Unmarshal(sev.Kv, &kv); jsonErr == nil {
							we.Kv = serverKeyValueToTransport(&kv)
						}
					}
					if len(sev.PrevKv) > 0 {
						var pkv serverKeyValue
						if jsonErr := json.Unmarshal(sev.PrevKv, &pkv); jsonErr == nil {
							we.PrevKv = serverKeyValueToTransport(&pkv)
						}
					}
					events = append(events, we)
				}

				resp := transport.WatchResponse{
					WatchID:  result.WatchID,
					Events:   events,
					Revision: result.Revision,
				}

				select {
				case <-ctx.Done():
					return
				case ch <- resp:
				}
			}

			// Brief pause between polls to avoid busy-looping when
			// no events are available.
			select {
			case <-ctx.Done():
				return
			case <-clock.After(watchPollInterval):
			}
		}
	}()

	return ch, nil
}

// bootstrapWatchCursor resolves the start_revision to request on the very
// first poll. An explicit caller-supplied start revision is used as-is
// (inclusive, matching pkg/watch.WithStartRevision semantics). Otherwise it
// anchors the cursor to "now" via a CurrentRevision call so the window
// between this call and the first poll actually running is covered by that
// poll's replay rather than dropped. If CurrentRevision is unsupported or
// fails, it returns 0: the caller's first poll omits start_revision (the
// pre-cursor, best-effort live-only behavior), and the cursor engages from
// the first reported revision onward.
func (t *Transport) bootstrapWatchCursor(ctx context.Context, explicit int64) int64 {
	if explicit > 0 {
		return explicit
	}
	cur, err := t.CurrentRevision(ctx)
	if err != nil {
		return 0
	}
	return cur + 1
}

// classifyWatchPollError reports whether err represents a terminal,
// server-reported watch condition that should end the polling loop (rather
// than being retried as transient), and the reason text to surface on the
// resulting Canceled WatchResponse. Recognizes the watch-history conditions
// pkg/watch surfaces (compacted / unsupported start revision, via
// transport.ClassifyWatchError) plus the legacy "watch canceled" text some
// server-side paths still use.
func classifyWatchPollError(err error) (reason string, terminal bool) {
	if err == nil {
		return "", false
	}
	if typed := transport.ClassifyWatchError(err); typed != err {
		return typed.Error(), true
	}
	if isWatchCanceledError(err) {
		return err.Error(), true
	}
	return "", false
}

// isWatchCanceledError checks whether an error indicates that a watch
// was canceled by the server.
func isWatchCanceledError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "watch canceled") || strings.Contains(msg, "watch cancelled")
}
