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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Mock Watcher
// ---------------------------------------------------------------------------

// mockWatcher implements the Watcher interface for testing.
type mockWatcher struct {
	eventsCh chan watch.WatchResponse
	watchID  int64
	closeErr error
	err      error
}

// Events returns the channel on which watch events are delivered.
func (w *mockWatcher) Events() <-chan watch.WatchResponse {
	return w.eventsCh
}

// WatchID returns the watch identifier.
func (w *mockWatcher) WatchID() int64 {
	return w.watchID
}

// Close cancels the watcher and releases its resources.
func (w *mockWatcher) Close() error {
	return w.closeErr
}

// Err returns the first non-nil error encountered by the watcher.
func (w *mockWatcher) Err() error {
	return w.err
}

// ---------------------------------------------------------------------------
// Mock WatchHub
// ---------------------------------------------------------------------------

// mockWatchHubCall records the arguments passed to a Watch or WatchPrefix call.
type mockWatchHubCall struct {
	ctx    context.Context
	key    string
	prefix string
	opts   []watch.WatchOption
}

// mockWatchHub implements the WatchHub interface for testing.
type mockWatchHub struct {
	watchFn       func(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error)
	watchPrefixFn func(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error)
	calls         []mockWatchHubCall
}

// Watch creates a watcher for the specified key.
func (h *mockWatchHub) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	h.calls = append(h.calls, mockWatchHubCall{ctx: ctx, key: key, opts: opts})
	if h.watchFn != nil {
		return h.watchFn(ctx, key, opts...)
	}
	return &mockWatcher{eventsCh: make(chan watch.WatchResponse)}, nil
}

// WatchPrefix creates a watcher for all keys matching the prefix.
func (h *mockWatchHub) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	h.calls = append(h.calls, mockWatchHubCall{ctx: ctx, prefix: prefix, opts: opts})
	if h.watchPrefixFn != nil {
		return h.watchPrefixFn(ctx, prefix, opts...)
	}
	return &mockWatcher{eventsCh: make(chan watch.WatchResponse)}, nil
}

// ---------------------------------------------------------------------------
// NewWatchService
// ---------------------------------------------------------------------------

func TestWatchService_NewWatchService_ReturnsNonNil(t *testing.T) {
	hub := &mockWatchHub{}
	svc := NewWatchService(hub)
	if svc == nil {
		t.Fatal("expected non-nil WatchService")
	}
}

func TestWatchService_NewWatchService_ImplementsInterface(t *testing.T) {
	hub := &mockWatchHub{}
	var _ WatchServicer = NewWatchService(hub) //nolint:staticcheck // QF1011: explicit interface assertion
}

// ---------------------------------------------------------------------------
// Watch - delegation
// ---------------------------------------------------------------------------

func TestWatchService_Watch_DelegatesToHubWatch(t *testing.T) {
	expectedWatcher := &mockWatcher{eventsCh: make(chan watch.WatchResponse)}
	hub := &mockWatchHub{
		watchFn: func(_ context.Context, key string, _ ...watch.WatchOption) (watch.Watcher, error) {
			if key != "test-key" {
				t.Fatalf("expected key 'test-key', got %q", key)
			}
			return expectedWatcher, nil
		},
	}

	svc := NewWatchService(hub)
	w, err := svc.Watch(context.Background(), "test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.WatchID() != expectedWatcher.WatchID() {
		t.Fatal("expected returned watcher to match the one from hub")
	}
}

func TestWatchService_Watch_PassesContext(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")

	hub := &mockWatchHub{
		watchFn: func(ctx context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			val, ok := ctx.Value(ctxKey{}).(string)
			if !ok || val != "marker" {
				t.Fatal("expected context value 'marker'")
			}
			return &mockWatcher{eventsCh: make(chan watch.WatchResponse)}, nil
		},
	}

	svc := NewWatchService(hub)
	_, err := svc.Watch(ctx, "key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Watch - error propagation
// ---------------------------------------------------------------------------

func TestWatchService_Watch_ReturnsHubError(t *testing.T) {
	hubErr := &WatchCanceledError{Reason: "compacted"}
	hub := &mockWatchHub{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, hubErr
		},
	}

	svc := NewWatchService(hub)
	w, err := svc.Watch(context.Background(), "key")
	if w != nil {
		t.Fatal("expected nil watcher on error")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var wce *WatchCanceledError
	if !errors.As(err, &wce) {
		t.Fatalf("expected *WatchCanceledError, got %T: %v", err, err)
	}
	if wce.Reason != "compacted" {
		t.Fatalf("expected reason 'compacted', got %q", wce.Reason)
	}
}

func TestWatchService_Watch_ReturnsNilErrorOnSuccess(t *testing.T) {
	hub := &mockWatchHub{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{eventsCh: make(chan watch.WatchResponse)}, nil
		},
	}

	svc := NewWatchService(hub)
	_, err := svc.Watch(context.Background(), "key")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Watch - watcher return
// ---------------------------------------------------------------------------

func TestWatchService_Watch_ReturnsHubWatcher(t *testing.T) {
	ch := make(chan watch.WatchResponse, 1)
	expectedWatcher := &mockWatcher{eventsCh: ch, watchID: 7}
	hub := &mockWatchHub{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return expectedWatcher, nil
		},
	}

	svc := NewWatchService(hub)
	w, err := svc.Watch(context.Background(), "key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the watcher identity via WatchID.
	if w.WatchID() != 7 {
		t.Fatalf("expected WatchID 7, got %d", w.WatchID())
	}
}

func TestWatchService_Watch_ReturnsNilWatcherOnError(t *testing.T) {
	hub := &mockWatchHub{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, &WatchCanceledError{Reason: "shutdown"}
		},
	}

	svc := NewWatchService(hub)
	w, err := svc.Watch(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if w != nil {
		t.Fatal("expected nil watcher on error")
	}
}

// ---------------------------------------------------------------------------
// Watch - options passthrough
// ---------------------------------------------------------------------------

func TestWatchService_Watch_PassesNoOptions(t *testing.T) {
	hub := &mockWatchHub{}

	svc := NewWatchService(hub)
	_, err := svc.Watch(context.Background(), "key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(hub.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hub.calls))
	}
	if len(hub.calls[0].opts) != 0 {
		t.Fatalf("expected 0 options, got %d", len(hub.calls[0].opts))
	}
}

func TestWatchService_Watch_PassesSingleOption(t *testing.T) {
	hub := &mockWatchHub{}

	svc := NewWatchService(hub)
	_, err := svc.Watch(context.Background(), "key", watch.WithStartRevision(42))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(hub.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hub.calls))
	}
	if len(hub.calls[0].opts) != 1 {
		t.Fatalf("expected 1 option, got %d", len(hub.calls[0].opts))
	}

	// Verify the option applies correctly.
	wo := watch.ApplyWatchOptions(hub.calls[0].opts)
	if wo.StartRevision != 42 {
		t.Fatalf("expected StartRevision 42, got %d", wo.StartRevision)
	}
}

func TestWatchService_Watch_PassesMultipleOptions(t *testing.T) {
	hub := &mockWatchHub{}

	svc := NewWatchService(hub)
	_, err := svc.Watch(context.Background(), "key",
		watch.WithStartRevision(100),
		watch.WithPrevKV(),
		watch.WithProgressNotify(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(hub.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hub.calls))
	}
	if len(hub.calls[0].opts) != 3 {
		t.Fatalf("expected 3 options, got %d", len(hub.calls[0].opts))
	}

	// Verify all options apply correctly.
	wo := watch.ApplyWatchOptions(hub.calls[0].opts)
	if wo.StartRevision != 100 {
		t.Fatalf("expected StartRevision 100, got %d", wo.StartRevision)
	}
	if !wo.PrevKV {
		t.Fatal("expected PrevKV true")
	}
	if !wo.ProgressNotify {
		t.Fatal("expected ProgressNotify true")
	}
}

// ---------------------------------------------------------------------------
// Watch - key passthrough
// ---------------------------------------------------------------------------

func TestWatchService_Watch_PassesCorrectKey(t *testing.T) {
	hub := &mockWatchHub{}

	svc := NewWatchService(hub)
	_, err := svc.Watch(context.Background(), "my/special/key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(hub.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hub.calls))
	}
	if hub.calls[0].key != "my/special/key" {
		t.Fatalf("expected key 'my/special/key', got %q", hub.calls[0].key)
	}
}

func TestWatchService_Watch_PassesEmptyKey(t *testing.T) {
	hub := &mockWatchHub{}

	svc := NewWatchService(hub)
	_, err := svc.Watch(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(hub.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hub.calls))
	}
	if hub.calls[0].key != "" {
		t.Fatalf("expected empty key, got %q", hub.calls[0].key)
	}
}

// ---------------------------------------------------------------------------
// WatchPrefix - delegation
// ---------------------------------------------------------------------------

func TestWatchService_WatchPrefix_DelegatesToHubWatchPrefix(t *testing.T) {
	expectedWatcher := &mockWatcher{eventsCh: make(chan watch.WatchResponse), watchID: 10}
	hub := &mockWatchHub{
		watchPrefixFn: func(_ context.Context, prefix string, _ ...watch.WatchOption) (watch.Watcher, error) {
			if prefix != "/app/config/" {
				t.Fatalf("expected prefix '/app/config/', got %q", prefix)
			}
			return expectedWatcher, nil
		},
	}

	svc := NewWatchService(hub)
	w, err := svc.WatchPrefix(context.Background(), "/app/config/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.WatchID() != 10 {
		t.Fatalf("expected WatchID 10, got %d", w.WatchID())
	}
}

func TestWatchService_WatchPrefix_PassesContext(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "prefix-marker")

	hub := &mockWatchHub{
		watchPrefixFn: func(ctx context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			val, ok := ctx.Value(ctxKey{}).(string)
			if !ok || val != "prefix-marker" {
				t.Fatal("expected context value 'prefix-marker'")
			}
			return &mockWatcher{eventsCh: make(chan watch.WatchResponse)}, nil
		},
	}

	svc := NewWatchService(hub)
	_, err := svc.WatchPrefix(ctx, "prefix/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// WatchPrefix - error propagation
// ---------------------------------------------------------------------------

func TestWatchService_WatchPrefix_ReturnsHubError(t *testing.T) {
	hubErr := &WatchCanceledError{Reason: "revision compacted"}
	hub := &mockWatchHub{
		watchPrefixFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, hubErr
		},
	}

	svc := NewWatchService(hub)
	w, err := svc.WatchPrefix(context.Background(), "prefix/")
	if w != nil {
		t.Fatal("expected nil watcher on error")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var wce *WatchCanceledError
	if !errors.As(err, &wce) {
		t.Fatalf("expected *WatchCanceledError, got %T: %v", err, err)
	}
	if wce.Reason != "revision compacted" {
		t.Fatalf("expected reason 'revision compacted', got %q", wce.Reason)
	}
}

func TestWatchService_WatchPrefix_ReturnsNilErrorOnSuccess(t *testing.T) {
	hub := &mockWatchHub{
		watchPrefixFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{eventsCh: make(chan watch.WatchResponse)}, nil
		},
	}

	svc := NewWatchService(hub)
	_, err := svc.WatchPrefix(context.Background(), "prefix/")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// WatchPrefix - watcher return
// ---------------------------------------------------------------------------

func TestWatchService_WatchPrefix_ReturnsHubWatcher(t *testing.T) {
	ch := make(chan watch.WatchResponse, 1)
	expectedWatcher := &mockWatcher{eventsCh: ch, watchID: 12}
	hub := &mockWatchHub{
		watchPrefixFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return expectedWatcher, nil
		},
	}

	svc := NewWatchService(hub)
	w, err := svc.WatchPrefix(context.Background(), "prefix/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the watcher identity via WatchID.
	if w.WatchID() != 12 {
		t.Fatalf("expected WatchID 12, got %d", w.WatchID())
	}
}

func TestWatchService_WatchPrefix_ReturnsNilWatcherOnError(t *testing.T) {
	hub := &mockWatchHub{
		watchPrefixFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, &WatchCanceledError{Reason: "shutdown"}
		},
	}

	svc := NewWatchService(hub)
	w, err := svc.WatchPrefix(context.Background(), "prefix/")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if w != nil {
		t.Fatal("expected nil watcher on error")
	}
}

// ---------------------------------------------------------------------------
// WatchPrefix - options passthrough
// ---------------------------------------------------------------------------

func TestWatchService_WatchPrefix_PassesNoOptions(t *testing.T) {
	hub := &mockWatchHub{}

	svc := NewWatchService(hub)
	_, err := svc.WatchPrefix(context.Background(), "prefix/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(hub.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hub.calls))
	}
	if len(hub.calls[0].opts) != 0 {
		t.Fatalf("expected 0 options, got %d", len(hub.calls[0].opts))
	}
}

func TestWatchService_WatchPrefix_PassesSingleOption(t *testing.T) {
	hub := &mockWatchHub{}

	svc := NewWatchService(hub)
	_, err := svc.WatchPrefix(context.Background(), "prefix/", watch.WithPrevKV())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(hub.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hub.calls))
	}
	if len(hub.calls[0].opts) != 1 {
		t.Fatalf("expected 1 option, got %d", len(hub.calls[0].opts))
	}

	// Verify the option applies correctly.
	wo := watch.ApplyWatchOptions(hub.calls[0].opts)
	if !wo.PrevKV {
		t.Fatal("expected PrevKV true")
	}
}

func TestWatchService_WatchPrefix_PassesMultipleOptions(t *testing.T) {
	hub := &mockWatchHub{}

	svc := NewWatchService(hub)
	_, err := svc.WatchPrefix(context.Background(), "prefix/",
		watch.WithStartRevision(200),
		watch.WithPrevKV(),
		watch.WithProgressNotify(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(hub.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hub.calls))
	}
	if len(hub.calls[0].opts) != 3 {
		t.Fatalf("expected 3 options, got %d", len(hub.calls[0].opts))
	}

	// Verify all options apply correctly.
	wo := watch.ApplyWatchOptions(hub.calls[0].opts)
	if wo.StartRevision != 200 {
		t.Fatalf("expected StartRevision 200, got %d", wo.StartRevision)
	}
	if !wo.PrevKV {
		t.Fatal("expected PrevKV true")
	}
	if !wo.ProgressNotify {
		t.Fatal("expected ProgressNotify true")
	}
}

// ---------------------------------------------------------------------------
// WatchPrefix - prefix passthrough
// ---------------------------------------------------------------------------

func TestWatchService_WatchPrefix_PassesCorrectPrefix(t *testing.T) {
	hub := &mockWatchHub{}

	svc := NewWatchService(hub)
	_, err := svc.WatchPrefix(context.Background(), "/data/users/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(hub.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hub.calls))
	}
	if hub.calls[0].prefix != "/data/users/" {
		t.Fatalf("expected prefix '/data/users/', got %q", hub.calls[0].prefix)
	}
}

func TestWatchService_WatchPrefix_PassesEmptyPrefix(t *testing.T) {
	hub := &mockWatchHub{}

	svc := NewWatchService(hub)
	_, err := svc.WatchPrefix(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(hub.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(hub.calls))
	}
	if hub.calls[0].prefix != "" {
		t.Fatalf("expected empty prefix, got %q", hub.calls[0].prefix)
	}
}
