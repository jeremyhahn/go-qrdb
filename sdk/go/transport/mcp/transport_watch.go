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

// defaultClientWatchTimeout is the default timeout for collecting watch
// events before returning a batch. MCP is request/response, so watches
// are polled in bounded intervals.
const defaultClientWatchTimeout = 5 * time.Second

// pollInterval is the delay between successive watch tool calls when
// polling for new events in the background goroutine.
const pollInterval = 500 * time.Millisecond

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
// Since MCP is a request/response protocol without native streaming,
// the transport polls the qrdb_watch tool at regular intervals and
// delivers results to the returned channel. The caller must cancel the
// context to stop watching.
func (t *Transport) Watch(ctx context.Context, key string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	return t.watchPoll(ctx, key, false, opts)
}

// WatchPrefix creates a watcher that delivers events for all keys
// matching the given prefix. See Watch for details on the polling
// mechanism.
func (t *Transport) WatchPrefix(ctx context.Context, prefix string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	return t.watchPoll(ctx, prefix, true, opts)
}

// watchPoll starts a background goroutine that repeatedly calls the
// qrdb_watch MCP tool, collecting events in bounded intervals and
// delivering them on the returned channel. The goroutine terminates
// and closes the channel when the context is canceled.
func (t *Transport) watchPoll(ctx context.Context, key string, prefix bool, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	ch := make(chan transport.WatchResponse, 16)

	go func() {
		defer close(ch)

		// Track the revision to replay from.
		nextRevision := opts.StartRevision

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			args := map[string]any{
				"key": key,
			}
			if prefix {
				args["prefix"] = true
			}
			if nextRevision > 0 {
				args["start_revision"] = nextRevision
			}
			if opts.PrevKV {
				args["prev_kv"] = true
			}
			// Use a short timeout per poll to keep latency low.
			args["timeout_seconds"] = 1.0

			text, err := t.callTool(ctx, "qrdb_watch", args)
			if err != nil {
				// If the context is canceled, exit gracefully.
				if ctx.Err() != nil {
					return
				}
				// Detect watch canceled from server side.
				if isWatchCanceledError(err) {
					select {
					case ch <- transport.WatchResponse{
						Canceled:     true,
						CancelReason: err.Error(),
					}:
					case <-ctx.Done():
					}
					return
				}
				// Transient error; wait before retrying.
				select {
				case <-ctx.Done():
					return
				case <-time.After(pollInterval):
					continue
				}
			}

			var result serverWatchResult
			if jsonErr := json.Unmarshal([]byte(text), &result); jsonErr != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(pollInterval):
					continue
				}
			}

			// Advance the revision for the next poll so events are
			// not replayed.
			if result.Revision > 0 {
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
			case <-time.After(pollInterval):
			}
		}
	}()

	return ch, nil
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
