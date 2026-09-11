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
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// defaultWatchTimeout is the maximum duration to collect watch events
// before returning a response. MCP is request/response, so watches are
// bounded to this collection period.
//
// The qrdb_watch tool itself is stateless request/response: each call
// creates a fresh watcher, collects for up to timeout_seconds, closes the
// watcher, and returns a snapshot carrying the highest revision actually
// DELIVERED (Revision in watchResult). The SDK client (sdk/go/transport/mcp)
// is what makes repeated calls to this tool loss-free: it tracks that
// revision as a cursor and always requests start_revision = cursor+1 on the
// next call, so pkg/watch's replay-then-live-handoff redelivers anything
// committed in the gap between one call ending and the next beginning. A
// single call to this tool in isolation (start_revision omitted) is a plain
// live-only snapshot with no such guarantee — the loss-free contract lives
// in the client's polling loop, not in this handler.
//
// Revision is computed only from responses that carry proof of delivery: an
// event batch's own Revision, or the post-replay Progress watermark (see
// pkg/watch.Hub.startReplay, requested here via WithProgressNotify). The
// zero-event "Created" response a start-revision watch gets on creation is
// NOT used — its Revision is the replay's intended upper bound, captured
// before a single historical event has been delivered, so a collection
// window that expires mid-replay would otherwise hand the caller a cursor
// covering events the replay never got to: silent, permanent loss on the
// caller's next poll (#210).
const defaultWatchTimeout = 5 * time.Second

// watchTools returns the MCP tool registrations for watch operations.
func watchTools(watchSvc service.WatchServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolWatch(),
			Perm:    permission.WatchWatch,
			Handler: handleWatch(watchSvc),
		},
	}
}

// toolWatch defines the qrdb_watch tool schema.
func toolWatch() mcp.Tool {
	return mcp.NewTool("qrdb_watch",
		mcp.WithDescription("Watch for changes to a key or key prefix. Since MCP is request/response, collects events for a bounded duration and returns them as a snapshot."),
		mcp.WithString("key",
			mcp.Required(),
			mcp.Description("The key to watch. When prefix is true, watches all keys with this prefix."),
		),
		mcp.WithBoolean("prefix",
			mcp.Description("When true, watch all keys matching the key as a prefix. Defaults to false."),
		),
		mcp.WithNumber("start_revision",
			mcp.Description("Begin watching from this revision, replaying all events since. 0 for current."),
		),
		mcp.WithBoolean("prev_kv",
			mcp.Description("Include the previous key-value state before modification in events."),
		),
		mcp.WithNumber("timeout_seconds",
			mcp.Description("Maximum seconds to collect events before returning. Defaults to 5."),
		),
	)
}

// watchResult is the JSON envelope for watch tool results.
type watchResult struct {
	Events   []watchEventJSON `json:"events"`
	Revision int64            `json:"revision"`
	WatchID  int64            `json:"watch_id"`
}

// watchEventJSON is the JSON representation of a single watch event.
type watchEventJSON struct {
	Type   string          `json:"type"`
	Kv     json.RawMessage `json:"kv,omitempty"`
	PrevKv json.RawMessage `json:"prev_kv,omitempty"`
}

// handleWatch returns a tool handler that creates a bounded watcher,
// collects events for up to the configured timeout, then returns them.
func handleWatch(watchSvc service.WatchServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		watchSvc, rerr := resolveWatch(ctx, watchSvc)
		if rerr != nil {
			return nil, rerr
		}
		key := mcp.ParseString(req, "key", "")
		if key == "" {
			return mcp.NewToolResultError("key is required"), nil
		}

		isPrefix := mcp.ParseBoolean(req, "prefix", false)
		startRevision := mcp.ParseInt64(req, "start_revision", 0)
		prevKV := mcp.ParseBoolean(req, "prev_kv", false)
		timeoutSec := mcp.ParseFloat64(req, "timeout_seconds", 0)

		timeout := defaultWatchTimeout
		if timeoutSec > 0 {
			timeout = time.Duration(timeoutSec * float64(time.Second))
		}

		// Build watch options. WithProgressNotify only matters alongside a
		// start revision — it is what lets startReplay report a Revision this
		// handler can trust even when the replay delivered zero events (see
		// the package doc comment above and pkg/watch.Hub.startReplay).
		var opts []watch.WatchOption
		if startRevision > 0 {
			opts = append(opts, watch.WithStartRevision(startRevision), watch.WithProgressNotify())
		}
		if prevKV {
			opts = append(opts, watch.WithPrevKV())
		}

		// Create the watcher.
		var w watch.Watcher
		var err error
		if isPrefix {
			w, err = watchSvc.WatchPrefix(ctx, key, opts...)
		} else {
			w, err = watchSvc.Watch(ctx, key, opts...)
		}
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_watch", Err: err}
		}
		defer func() {
			_ = w.Close()
		}()

		// Collect events for the bounded duration.
		collectCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		var events []watchEventJSON
		var lastRevision int64

	collectLoop:
		for {
			select {
			case <-collectCtx.Done():
				break collectLoop
			case resp, ok := <-w.Events():
				if !ok {
					// The hub closes Events() without a final response when it
					// evicts a watcher mid-collection (e.g. compaction races
					// past an in-progress start-revision replay — see
					// hub.startReplay). Surface that as a tool failure rather
					// than silently returning a truncated snapshot as if
					// nothing happened; the SDK client (transport_watch.go)
					// classifies this the same way it classifies the
					// synchronous watchSvc.Watch/WatchPrefix failure below.
					if wErr := w.Err(); wErr != nil {
						return nil, &ToolError{Tool: "qrdb_watch", Err: wErr}
					}
					break collectLoop
				}
				// Only credit Revision from a response that proves delivery: an
				// event batch's own Revision, or the post-replay Progress
				// watermark. Created's Revision is the replay's intended upper
				// bound, not a delivery watermark (#210) — crediting it here is
				// exactly the defect this handler exists to not have.
				if resp.Progress || len(resp.Events) > 0 {
					if resp.Revision > lastRevision {
						lastRevision = resp.Revision
					}
				}
				for _, evt := range resp.Events {
					if evt == nil {
						continue
					}
					we := watchEventJSON{
						Type: watchEventTypeName(evt.Type),
					}
					if evt.Kv != nil {
						kvData, _ := json.Marshal(evt.Kv)
						we.Kv = kvData
					}
					if evt.PrevKv != nil {
						prevData, _ := json.Marshal(evt.PrevKv)
						we.PrevKv = prevData
					}
					events = append(events, we)
				}
			}
		}

		if events == nil {
			events = []watchEventJSON{}
		}

		result := watchResult{
			Events:   events,
			Revision: lastRevision,
			WatchID:  w.WatchID(),
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_watch", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// watchEventTypeNames maps EventType to string names for O(1) dispatch.
var watchEventTypeNames = map[watch.EventType]string{
	watch.EventPut:    "PUT",
	watch.EventDelete: "DELETE",
}

// watchEventTypeName returns the string name for a watch event type.
func watchEventTypeName(t watch.EventType) string {
	if name, ok := watchEventTypeNames[t]; ok {
		return name
	}
	return "UNKNOWN"
}
