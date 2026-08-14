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

package rest

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Watch SSE response types
// ---------------------------------------------------------------------------

// watchEventJSON is the JSON representation of a single watch event sent
// as an SSE data payload.
type watchEventJSON struct {
	WatchID         int64        `json:"watch_id"`
	Events          []*eventJSON `json:"events"`
	Revision        int64        `json:"revision"`
	CompactRevision int64        `json:"compact_revision,omitempty"`
	Canceled        bool         `json:"canceled,omitempty"`
	CancelReason    string       `json:"cancel_reason,omitempty"`
}

// eventJSON is the JSON representation of a single key change event.
type eventJSON struct {
	Type   string      `json:"type"`
	Kv     *kvResponse `json:"kv,omitempty"`
	PrevKv *kvResponse `json:"prev_kv,omitempty"`
}

// ---------------------------------------------------------------------------
// Watch handler (Server-Sent Events)
// ---------------------------------------------------------------------------

// handleWatch handles GET /watch -- streams key change events using
// Server-Sent Events (SSE). The connection remains open until the client
// disconnects or the watcher is canceled.
//
// Query parameters:
//   - key: the key or prefix to watch (required)
//   - prefix: when "true", watch all keys with the given prefix
//   - start-revision: replay events from this revision
//   - prev-kv: when "true", include previous key-value in events
func (s *Server) handleWatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	watchSvc := s.watchSvcFor(w, ctx)
	if watchSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "watch"})
		}
		return
	}

	q := r.URL.Query()

	key := q.Get("key")
	if key == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "key"})
		return
	}

	isPrefix := q.Get("prefix") == "true"

	var opts []watch.WatchOption

	if startRevStr := q.Get("start-revision"); startRevStr != "" {
		rev, err := strconv.ParseInt(startRevStr, 10, 64)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "start-revision", Err: err})
			return
		}
		opts = append(opts, watch.WithStartRevision(rev))
	}

	if q.Get("prev-kv") == "true" {
		opts = append(opts, watch.WithPrevKV())
	}

	var watcher watch.Watcher
	var err error
	if isPrefix {
		watcher, err = watchSvc.WatchPrefix(ctx, key, opts...)
	} else {
		watcher, err = watchSvc.Watch(ctx, key, opts...)
	}
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	defer func() {
		_ = watcher.Close()
	}()

	// Verify the ResponseWriter supports flushing for SSE.
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErrorJSON(w, http.StatusInternalServerError, &SSENotSupportedError{})
		return
	}

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	enc := json.NewEncoder(w)

	for {
		select {
		case <-ctx.Done():
			return
		case resp, open := <-watcher.Events():
			if !open {
				// The hub closes Events() without a final Canceled response
				// when it evicts a watcher mid-stream (e.g. compaction races
				// past an in-progress start-revision replay — see
				// hub.startReplay). Synthesize one here so the SSE client
				// always observes a terminal frame with a reason, the same
				// contract as the normal in-band Canceled path below.
				if wErr := watcher.Err(); wErr != nil {
					if !writeWatchSSEFrame(w, enc, watchEventJSON{
						WatchID:      watcher.WatchID(),
						Canceled:     true,
						CancelReason: wErr.Error(),
					}) {
						return
					}
					flusher.Flush()
				}
				return
			}

			events := make([]*eventJSON, len(resp.Events))
			for i, evt := range resp.Events {
				events[i] = &eventJSON{
					Type:   eventTypeToString(evt.Type),
					Kv:     kvToResponse(evt.Kv),
					PrevKv: kvToResponse(evt.PrevKv),
				}
			}

			payload := watchEventJSON{
				WatchID:         resp.WatchID,
				Events:          events,
				Revision:        resp.Revision,
				CompactRevision: resp.CompactRevision,
				Canceled:        resp.Canceled,
				CancelReason:    resp.CancelReason,
			}

			if !writeWatchSSEFrame(w, enc, payload) {
				return
			}
			flusher.Flush()

			// If the watcher was canceled, terminate the stream.
			if resp.Canceled {
				return
			}
		}
	}
}

// writeWatchSSEFrame writes a single SSE "data: {json}\n\n" frame carrying
// payload. Returns false (and logs) on any write/encode failure, signaling
// the caller to give up on the connection.
func writeWatchSSEFrame(w http.ResponseWriter, enc *json.Encoder, payload watchEventJSON) bool {
	if _, writeErr := w.Write([]byte("data: ")); writeErr != nil {
		slog.Debug("rest: SSE write error", "error", writeErr)
		return false
	}
	if encErr := enc.Encode(payload); encErr != nil {
		slog.Debug("rest: SSE encode error", "error", encErr)
		return false
	}
	if _, writeErr := w.Write([]byte("\n")); writeErr != nil {
		slog.Debug("rest: SSE write error", "error", writeErr)
		return false
	}
	return true
}

// eventTypeToString converts a watch.EventType to its string representation.
func eventTypeToString(t watch.EventType) string {
	if t == watch.EventDelete {
		return "DELETE"
	}
	return "PUT"
}
