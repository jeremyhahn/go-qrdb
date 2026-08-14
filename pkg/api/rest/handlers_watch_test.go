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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// start-revision threading
// ---------------------------------------------------------------------------

// TestHandleWatch_StartRevisionThreaded verifies the ?start-revision= query
// parameter reaches the service layer as watch.WithStartRevision, for both
// the exact-key and prefix paths.
func TestHandleWatch_StartRevisionThreaded(t *testing.T) {
	var gotKey watch.WatchOptions
	watchSvc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, opts ...watch.WatchOption) (watch.Watcher, error) {
			gotKey = watch.ApplyWatchOptions(opts)
			ch := make(chan watch.WatchResponse)
			close(ch)
			return &mockWatcher{eventsCh: ch, id: 1}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch?key=mykey&start-revision=42", nil)
	rec := newFlushableRecorder()
	srv.handleWatch(rec, req)

	if gotKey.StartRevision != 42 {
		t.Fatalf("StartRevision = %d, want 42", gotKey.StartRevision)
	}
}

// TestHandleWatch_StartRevisionThreaded_Prefix mirrors the above for the
// prefix-watch path.
func TestHandleWatch_StartRevisionThreaded_Prefix(t *testing.T) {
	var gotOpts watch.WatchOptions
	watchSvc := &mockWatchService{
		watchPrefixFn: func(_ context.Context, _ string, opts ...watch.WatchOption) (watch.Watcher, error) {
			gotOpts = watch.ApplyWatchOptions(opts)
			ch := make(chan watch.WatchResponse)
			close(ch)
			return &mockWatcher{eventsCh: ch, id: 1}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch?key=prefix/&prefix=true&start-revision=7", nil)
	rec := newFlushableRecorder()
	srv.handleWatch(rec, req)

	if gotOpts.StartRevision != 7 {
		t.Fatalf("StartRevision = %d, want 7", gotOpts.StartRevision)
	}
}

// TestHandleWatch_NoStartRevision verifies omitting the query parameter
// leaves StartRevision at its zero value (live-only watch).
func TestHandleWatch_NoStartRevision(t *testing.T) {
	var gotOpts watch.WatchOptions
	watchSvc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, opts ...watch.WatchOption) (watch.Watcher, error) {
			gotOpts = watch.ApplyWatchOptions(opts)
			ch := make(chan watch.WatchResponse)
			close(ch)
			return &mockWatcher{eventsCh: ch, id: 1}, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch?key=mykey", nil)
	rec := newFlushableRecorder()
	srv.handleWatch(rec, req)

	if gotOpts.StartRevision != 0 {
		t.Fatalf("StartRevision = %d, want 0", gotOpts.StartRevision)
	}
}

// ---------------------------------------------------------------------------
// watch.ErrCompacted / watch.ErrStartRevisionUnsupported -> HTTP status
// ---------------------------------------------------------------------------

// TestHandleWatch_ErrCompacted verifies a synchronous ErrCompacted from the
// service layer (a start revision at or below the compacted floor) maps to
// 416 Requested Range Not Satisfiable, the same status family as
// service.RevisionCompactedError.
func TestHandleWatch_ErrCompacted(t *testing.T) {
	watchSvc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, watch.ErrCompacted
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch?key=mykey&start-revision=1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusRequestedRangeNotSatisfiable)
}

// TestHandleWatch_ErrStartRevisionUnsupported verifies a synchronous
// ErrStartRevisionUnsupported maps to 501 Not Implemented.
func TestHandleWatch_ErrStartRevisionUnsupported(t *testing.T) {
	watchSvc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, watch.ErrStartRevisionUnsupported
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch?key=mykey&start-revision=1", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotImplemented)
}

// ---------------------------------------------------------------------------
// Mid-stream cancellation (Events() closes with a non-nil watcher.Err())
// ---------------------------------------------------------------------------

// TestHandleWatch_SSEMidStreamCompacted exercises the case where the hub
// evicts a replaying watcher mid-stream because compaction raced past the
// requested start revision (hub.startReplay's fail-closed path): Events()
// closes with no in-band Canceled response, but watcher.Err() carries
// watch.ErrCompacted. The handler must synthesize a terminal SSE frame
// carrying Canceled=true and the reason text rather than silently dropping
// the connection with no explanation.
func TestHandleWatch_SSEMidStreamCompacted(t *testing.T) {
	ch := make(chan watch.WatchResponse)
	close(ch) // Events() already closed, as the hub leaves it post-eviction.
	watcher := &mockWatcher{eventsCh: ch, id: 9, watchErr: watch.ErrCompacted}

	watchSvc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch?key=mykey&start-revision=1", nil)
	rec := newFlushableRecorder()
	srv.handleWatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (SSE headers already committed)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"canceled":true`) {
		t.Fatalf("body should carry a terminal canceled frame, got: %s", body)
	}
	if !strings.Contains(body, "compacted") {
		t.Fatalf("body should carry the compaction reason, got: %s", body)
	}
	if rec.flushed < 1 {
		t.Fatal("expected the synthesized frame to be flushed")
	}
}

// TestHandleWatch_SSEMidStreamNoError verifies a plain, error-free channel
// close (the normal end-of-watch case, e.g. hub.Close during shutdown with
// no watcher-level error recorded) still ends the stream cleanly with no
// synthesized frame.
func TestHandleWatch_SSEMidStreamNoError(t *testing.T) {
	ch := make(chan watch.WatchResponse)
	close(ch)
	watcher := &mockWatcher{eventsCh: ch, id: 1}

	watchSvc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", WithWatch(watchSvc))

	req := httptest.NewRequest(http.MethodGet, "/watch?key=mykey", nil)
	rec := newFlushableRecorder()
	srv.handleWatch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "canceled") {
		t.Fatalf("body should not carry a canceled frame, got: %s", rec.Body.String())
	}
}
