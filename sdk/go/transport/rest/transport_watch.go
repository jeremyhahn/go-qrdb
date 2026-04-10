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
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.WatchClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Wire types for watch SSE communication
// ---------------------------------------------------------------------------

// watchWireEvent matches the JSON shape of a single event within an SSE
// data frame from the REST server's watch handler.
type watchWireEvent struct {
	Type   string              `json:"type"`
	Kv     *mvccKVWireResponse `json:"kv,omitempty"`
	PrevKv *mvccKVWireResponse `json:"prev_kv,omitempty"`
}

// watchWireResponse matches the JSON shape of an SSE data frame from the
// REST server's watch handler.
type watchWireResponse struct {
	WatchID         int64             `json:"watch_id"`
	Events          []*watchWireEvent `json:"events"`
	Revision        int64             `json:"revision"`
	CompactRevision int64             `json:"compact_revision,omitempty"`
	Canceled        bool              `json:"canceled,omitempty"`
	CancelReason    string            `json:"cancel_reason,omitempty"`
}

// watchEventTypeStringMap converts the REST server's string event types
// to transport EventType values.
var watchEventTypeStringMap = map[string]transport.EventType{
	"PUT":    transport.EventPut,
	"DELETE": transport.EventDelete,
}

// ---------------------------------------------------------------------------
// WatchClient implementation
// ---------------------------------------------------------------------------

// Watch creates a watcher that delivers events for the specified key.
// Maps to GET /watch?key=X with SSE streaming. Returns a channel of
// WatchResponse that is closed when the watch ends. The caller must
// cancel the context to stop watching.
func (t *Transport) Watch(ctx context.Context, key string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	return t.watchSSE(ctx, key, false, opts)
}

// WatchPrefix creates a watcher that delivers events for all keys
// matching the given prefix. Maps to GET /watch?key=X&prefix=true
// with SSE streaming.
func (t *Transport) WatchPrefix(ctx context.Context, prefix string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	return t.watchSSE(ctx, prefix, true, opts)
}

// watchSSE opens an HTTP GET connection to the watch endpoint and spawns
// a goroutine that reads SSE data frames, decodes them, and sends
// transport.WatchResponse values to the returned channel. The channel
// is closed when the context is canceled, the server closes the
// connection, or the watcher is canceled.
func (t *Transport) watchSSE(ctx context.Context, key string, isPrefix bool, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	params.Set("key", key)
	if isPrefix {
		params.Set("prefix", "true")
	}
	if opts.StartRevision > 0 {
		params.Set("start-revision", strconv.FormatInt(opts.StartRevision, 10))
	}
	if opts.PrevKV {
		params.Set("prev-kv", "true")
	}

	reqURL := t.baseURL + "/watch?" + params.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "Watch", Err: err}
	}
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "Watch", Err: err}
	}

	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, t.decodeErrorResponse("Watch", resp)
	}

	ch := make(chan transport.WatchResponse, 16)
	go readSSEWatchResponses(ctx, resp, ch)

	return ch, nil
}

// readSSEWatchResponses reads SSE data frames from the HTTP response
// body in a loop, decodes them as JSON watchWireResponse, and sends
// the converted transport.WatchResponse to the channel. The loop
// terminates when the context is canceled, the response body is
// closed, or a read error occurs. The channel is always closed on exit.
func readSSEWatchResponses(ctx context.Context, resp *http.Response, ch chan<- transport.WatchResponse) {
	defer close(ch)
	defer func() { _ = resp.Body.Close() }()

	scanner := bufio.NewScanner(resp.Body)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !scanner.Scan() {
			return
		}

		line := scanner.Text()

		// SSE format: "data: {json}\n"
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		jsonData := strings.TrimPrefix(line, "data: ")

		var wireResp watchWireResponse
		if err := json.Unmarshal([]byte(jsonData), &wireResp); err != nil {
			ch <- transport.WatchResponse{
				Canceled:     true,
				CancelReason: (&jsonDecodeError{Err: err}).Error(),
			}
			return
		}

		events := make([]*transport.WatchEvent, len(wireResp.Events))
		for i, we := range wireResp.Events {
			events[i] = &transport.WatchEvent{
				Type:   watchEventTypeStringMap[we.Type],
				Kv:     wireKVToTransport(we.Kv),
				PrevKv: wireKVToTransport(we.PrevKv),
			}
		}

		watchResp := transport.WatchResponse{
			WatchID:      wireResp.WatchID,
			Events:       events,
			Canceled:     wireResp.Canceled,
			CancelReason: wireResp.CancelReason,
			Revision:     wireResp.Revision,
		}

		select {
		case <-ctx.Done():
			return
		case ch <- watchResp:
		}

		if wireResp.Canceled {
			return
		}
	}
}
