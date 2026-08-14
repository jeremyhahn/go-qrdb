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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// start_revision threading
// ---------------------------------------------------------------------------

// TestHandleWatch_StartRevisionThreaded verifies the start_revision tool
// argument reaches the service layer as watch.WithStartRevision.
func TestHandleWatch_StartRevisionThreaded(t *testing.T) {
	var gotOpts watch.WatchOptions
	ch := make(chan watch.WatchResponse)
	close(ch)
	svc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, opts ...watch.WatchOption) (watch.Watcher, error) {
			gotOpts = watch.ApplyWatchOptions(opts)
			return &mockWatcher{eventsCh: ch, watchID: 1}, nil
		},
	}
	handler := handleWatch(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"key":             "k1",
		"start_revision":  float64(42),
		"timeout_seconds": float64(0.1),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts.StartRevision != 42 {
		t.Fatalf("StartRevision = %d, want 42", gotOpts.StartRevision)
	}
}

// TestHandleWatch_StartRevisionThreaded_Prefix mirrors the above for the
// prefix-watch path.
func TestHandleWatch_StartRevisionThreaded_Prefix(t *testing.T) {
	var gotOpts watch.WatchOptions
	ch := make(chan watch.WatchResponse)
	close(ch)
	svc := &mockWatchService{
		watchPrefixFn: func(_ context.Context, _ string, opts ...watch.WatchOption) (watch.Watcher, error) {
			gotOpts = watch.ApplyWatchOptions(opts)
			return &mockWatcher{eventsCh: ch, watchID: 1}, nil
		},
	}
	handler := handleWatch(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"key":             "prefix/",
		"prefix":          true,
		"start_revision":  float64(7),
		"timeout_seconds": float64(0.1),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotOpts.StartRevision != 7 {
		t.Fatalf("StartRevision = %d, want 7", gotOpts.StartRevision)
	}
}

// ---------------------------------------------------------------------------
// Mid-stream cancellation (Events() closes with a non-nil watcher.Err())
// ---------------------------------------------------------------------------

// TestHandleWatch_MidStreamCompacted exercises the case where the hub
// evicts a replaying watcher mid-collection because compaction raced past
// the requested start revision (hub.startReplay's fail-closed path):
// Events() closes with watcher.Err() carrying watch.ErrCompacted. The
// handler must surface this as a tool failure (so the SDK client's
// classification sees it), not silently return a truncated snapshot as if
// nothing happened.
func TestHandleWatch_MidStreamCompacted(t *testing.T) {
	ch := make(chan watch.WatchResponse)
	close(ch)
	watcher := &mockWatcher{eventsCh: ch, watchID: 9, watchErr: watch.ErrCompacted}
	svc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	handler := handleWatch(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"key":             "k1",
		"start_revision":  float64(1),
		"timeout_seconds": float64(1),
	}))
	if err == nil {
		t.Fatal("expected an error from a mid-stream watcher failure")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected *ToolError, got %T: %v", err, err)
	}
	if !errors.Is(toolErr, watch.ErrCompacted) {
		t.Fatalf("expected the ToolError to wrap watch.ErrCompacted, got: %v", toolErr)
	}
}

// TestHandleWatch_MidStreamNoError verifies a plain, error-free channel
// close (the ordinary end-of-collection-window case) still returns a normal
// (non-error) snapshot result.
func TestHandleWatch_MidStreamNoError(t *testing.T) {
	ch := make(chan watch.WatchResponse)
	close(ch)
	watcher := &mockWatcher{eventsCh: ch, watchID: 1}
	svc := &mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return watcher, nil
		},
	}
	handler := handleWatch(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":             "k1",
		"timeout_seconds": float64(0.1),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || result.IsError {
		t.Fatalf("expected a successful result, got: %+v", result)
	}
}
