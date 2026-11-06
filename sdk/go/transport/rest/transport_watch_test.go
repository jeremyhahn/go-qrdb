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
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// start-revision threading
// ---------------------------------------------------------------------------

// TestTransport_Watch_StartRevisionQueryParam verifies WatchOptions.StartRevision
// is threaded onto the ?start-revision= query parameter of the /watch request.
func TestTransport_Watch_StartRevisionQueryParam(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("recorder must support flushing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		// No frames: the client reads until the connection closes.
	}))
	defer srv.Close()

	rt := newWiredTransport(srv)
	defer func() { _ = rt.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := rt.Watch(ctx, "mykey", transport.WatchOptions{StartRevision: 42})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	// Drain until the goroutine notices the closed body and exits.
	for range ch {
	}

	if gotQuery == "" {
		t.Fatal("server never received a request")
	}
	values, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("ParseQuery(%q): %v", gotQuery, err)
	}
	if got := values.Get("start-revision"); got != "42" {
		t.Fatalf("start-revision = %q, want %q (query: %s)", got, "42", gotQuery)
	}
	if got := values.Get("key"); got != "mykey" {
		t.Fatalf("key = %q, want %q", got, "mykey")
	}
}

// ---------------------------------------------------------------------------
// watch.ErrCompacted classification
// ---------------------------------------------------------------------------

// TestTransport_Watch_ErrCompactedClassified verifies a synchronous 416
// response (mapHTTPError's mapping for watch.ErrCompacted) whose JSON body
// carries the server-side sentinel text is classified into a typed
// transport.WatchCompactedError, not left as an opaque RequestError.
func TestTransport_Watch_ErrCompactedClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		_, _ = w.Write([]byte(`{"error":"qrdb/watch: required start revision has been compacted"}`))
	}))
	defer srv.Close()

	rt := newWiredTransport(srv)
	defer func() { _ = rt.Close() }()

	_, err := rt.Watch(context.Background(), "mykey", transport.WatchOptions{StartRevision: 1})
	if err == nil {
		t.Fatal("expected an error")
	}

	var wce *transport.WatchCompactedError
	if !errors.As(err, &wce) {
		t.Fatalf("Watch() error = %T (%v), want *transport.WatchCompactedError", err, err)
	}
}

// TestTransport_Watch_ErrStartRevisionUnsupportedClassified mirrors the
// above for the 501 / ErrStartRevisionUnsupported condition.
func TestTransport_Watch_ErrStartRevisionUnsupportedClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"error":"qrdb/watch: start revision replay is not supported by this store"}`))
	}))
	defer srv.Close()

	rt := newWiredTransport(srv)
	defer func() { _ = rt.Close() }()

	_, err := rt.WatchPrefix(context.Background(), "prefix/", transport.WatchOptions{StartRevision: 1})
	if err == nil {
		t.Fatal("expected an error")
	}

	var wsue *transport.WatchStartRevisionUnsupportedError
	if !errors.As(err, &wsue) {
		t.Fatalf("WatchPrefix() error = %T (%v), want *transport.WatchStartRevisionUnsupportedError", err, err)
	}
}

// ---------------------------------------------------------------------------
// Mid-stream cancellation propagation (server already emits a Canceled frame
// — see handlers_watch.go's mid-stream fix; the client only needs to relay
// Canceled/CancelReason verbatim, which readSSEWatchResponses already does).
// ---------------------------------------------------------------------------

// TestTransport_Watch_MidStreamCancelPropagated verifies a terminal SSE
// frame with canceled:true/cancel_reason set (as the server now emits when
// the hub evicts a watcher mid-replay) reaches the channel with Err()
// classifying to the typed compacted error.
func TestTransport_Watch_MidStreamCancelPropagated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"watch_id":1,"events":[],"revision":0,"canceled":true,"cancel_reason":"qrdb/watch: required start revision has been compacted"}` + "\n"))
		flusher.Flush()
	}))
	defer srv.Close()

	rt := newWiredTransport(srv)
	defer func() { _ = rt.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := rt.Watch(ctx, "mykey", transport.WatchOptions{StartRevision: 1})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	resp, ok := <-ch
	if !ok {
		t.Fatal("channel closed without a response")
	}
	if !resp.Canceled {
		t.Fatalf("resp.Canceled = false, want true: %+v", resp)
	}

	var wce *transport.WatchCompactedError
	if !errors.As(resp.Err(), &wce) {
		t.Fatalf("resp.Err() = %T (%v), want *transport.WatchCompactedError", resp.Err(), resp.Err())
	}
}

func TestRESTWatch_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(context.Context, *Transport) error{
		"Watch": func(ctx context.Context, rt *Transport) error {
			_, err := rt.Watch(ctx, "k", transport.WatchOptions{})
			return err
		},
		"WatchPrefix": func(ctx context.Context, rt *Transport) error {
			_, err := rt.WatchPrefix(ctx, "p", transport.WatchOptions{})
			return err
		},
	})
}
