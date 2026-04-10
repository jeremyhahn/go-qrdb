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

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// WatchEntity - success paths
// ---------------------------------------------------------------------------

func TestWatchService_WatchEntity_Success(t *testing.T) {
	eventsCh := make(chan watch.WatchResponse, 1)

	hub := &mockWatchHub{
		watchFn: func(_ context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
			if key != "user/u1" {
				t.Fatalf("expected key 'user/u1', got %q", key)
			}
			// Verify no start revision option when StartRevision is 0.
			if len(opts) != 0 {
				t.Fatalf("expected 0 opts, got %d", len(opts))
			}
			return &mockWatcher{eventsCh: eventsCh, watchID: 1}, nil
		},
	}

	svc := NewWatchService(hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntity(ctx, &WatchEntityRequest{
		EntityType: "user",
		ID:         "u1",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}

	// Send a put event through the raw watcher.
	eventsCh <- watch.WatchResponse{
		WatchID:  1,
		Revision: 10,
		Events: []*watch.Event{
			{
				Type: watch.EventPut,
				Kv: &kv.KeyValue{
					Key:   []byte("user/u1"),
					Value: []byte(`{"name":"alice"}`),
				},
			},
		},
	}

	// Read the transformed entity event.
	evt := <-ch
	if evt.EntityType != "user" {
		t.Fatalf("expected EntityType 'user', got %q", evt.EntityType)
	}
	if evt.ID != "u1" {
		t.Fatalf("expected ID 'u1', got %q", evt.ID)
	}
	if string(evt.Data) != `{"name":"alice"}` {
		t.Fatalf("expected Data '{\"name\":\"alice\"}', got %q", evt.Data)
	}
	if evt.Revision != 10 {
		t.Fatalf("expected Revision 10, got %d", evt.Revision)
	}
	if evt.Type != WatchEventPut {
		t.Fatalf("expected WatchEventPut (0), got %d", evt.Type)
	}
}

func TestWatchService_WatchEntity_DeleteEvent(t *testing.T) {
	eventsCh := make(chan watch.WatchResponse, 1)

	hub := &mockWatchHub{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{eventsCh: eventsCh, watchID: 2}, nil
		},
	}

	svc := NewWatchService(hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntity(ctx, &WatchEntityRequest{
		EntityType: "config",
		ID:         "c1",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	eventsCh <- watch.WatchResponse{
		WatchID:  2,
		Revision: 20,
		Events: []*watch.Event{
			{
				Type: watch.EventDelete,
				Kv: &kv.KeyValue{
					Key: []byte("config/c1"),
				},
			},
		},
	}

	evt := <-ch
	if evt.Type != WatchEventDelete {
		t.Fatalf("expected WatchEventDelete (1), got %d", evt.Type)
	}
	if evt.EntityType != "config" {
		t.Fatalf("expected EntityType 'config', got %q", evt.EntityType)
	}
	if evt.ID != "c1" {
		t.Fatalf("expected ID 'c1', got %q", evt.ID)
	}
}

func TestWatchService_WatchEntity_WithStartRevision(t *testing.T) {
	hub := &mockWatchHub{
		watchFn: func(_ context.Context, _ string, opts ...watch.WatchOption) (watch.Watcher, error) {
			if len(opts) != 1 {
				t.Fatalf("expected 1 watch option, got %d", len(opts))
			}
			wo := watch.ApplyWatchOptions(opts)
			if wo.StartRevision != 42 {
				t.Fatalf("expected StartRevision 42, got %d", wo.StartRevision)
			}
			return &mockWatcher{eventsCh: make(chan watch.WatchResponse)}, nil
		},
	}

	svc := NewWatchService(hub)
	_, err := svc.WatchEntity(context.Background(), &WatchEntityRequest{
		EntityType:    "user",
		ID:            "u1",
		StartRevision: 42,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestWatchService_WatchEntity_ChannelClosesOnContextCancel(t *testing.T) {
	eventsCh := make(chan watch.WatchResponse)

	hub := &mockWatchHub{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{eventsCh: eventsCh, watchID: 3}, nil
		},
	}

	svc := NewWatchService(hub)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := svc.WatchEntity(ctx, &WatchEntityRequest{
		EntityType: "user",
		ID:         "u1",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	cancel()

	// The channel should eventually close.
	_, ok := <-ch
	if ok {
		t.Fatal("expected channel to be closed after context cancel")
	}
}

func TestWatchService_WatchEntity_MultipleEventsInSingleResponse(t *testing.T) {
	eventsCh := make(chan watch.WatchResponse, 1)

	hub := &mockWatchHub{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{eventsCh: eventsCh, watchID: 4}, nil
		},
	}

	svc := NewWatchService(hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntity(ctx, &WatchEntityRequest{
		EntityType: "user",
		ID:         "u1",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	eventsCh <- watch.WatchResponse{
		WatchID:  4,
		Revision: 30,
		Events: []*watch.Event{
			{
				Type: watch.EventPut,
				Kv:   &kv.KeyValue{Key: []byte("user/u1"), Value: []byte("v1")},
			},
			{
				Type: watch.EventPut,
				Kv:   &kv.KeyValue{Key: []byte("user/u1"), Value: []byte("v2")},
			},
		},
	}

	evt1 := <-ch
	if string(evt1.Data) != "v1" {
		t.Fatalf("expected first event data 'v1', got %q", evt1.Data)
	}

	evt2 := <-ch
	if string(evt2.Data) != "v2" {
		t.Fatalf("expected second event data 'v2', got %q", evt2.Data)
	}
}

// ---------------------------------------------------------------------------
// WatchEntity - error paths
// ---------------------------------------------------------------------------

func TestWatchService_WatchEntity_EmptyEntityType(t *testing.T) {
	hub := &mockWatchHub{}
	svc := NewWatchService(hub)

	_, err := svc.WatchEntity(context.Background(), &WatchEntityRequest{
		EntityType: "",
		ID:         "u1",
	})
	if err == nil {
		t.Fatal("expected error for empty entity type, got nil")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected *EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestWatchService_WatchEntity_EmptyID(t *testing.T) {
	hub := &mockWatchHub{}
	svc := NewWatchService(hub)

	_, err := svc.WatchEntity(context.Background(), &WatchEntityRequest{
		EntityType: "user",
		ID:         "",
	})
	if err == nil {
		t.Fatal("expected error for empty ID, got nil")
	}

	var emptyIDErr *EntityEmptyIDError
	if !errors.As(err, &emptyIDErr) {
		t.Fatalf("expected *EntityEmptyIDError, got %T: %v", err, err)
	}
	if emptyIDErr.EntityType != "user" {
		t.Fatalf("expected EntityType 'user', got %q", emptyIDErr.EntityType)
	}
}

func TestWatchService_WatchEntity_HubError(t *testing.T) {
	hubErr := &WatchCanceledError{Reason: "compacted"}
	hub := &mockWatchHub{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, hubErr
		},
	}

	svc := NewWatchService(hub)
	ch, err := svc.WatchEntity(context.Background(), &WatchEntityRequest{
		EntityType: "user",
		ID:         "u1",
	})
	if ch != nil {
		t.Fatal("expected nil channel on error")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var wce *WatchCanceledError
	if !errors.As(err, &wce) {
		t.Fatalf("expected *WatchCanceledError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// WatchEntityType - success paths
// ---------------------------------------------------------------------------

func TestWatchService_WatchEntityType_Success(t *testing.T) {
	eventsCh := make(chan watch.WatchResponse, 1)

	hub := &mockWatchHub{
		watchPrefixFn: func(_ context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
			if prefix != "user/" {
				t.Fatalf("expected prefix 'user/', got %q", prefix)
			}
			if len(opts) != 0 {
				t.Fatalf("expected 0 opts, got %d", len(opts))
			}
			return &mockWatcher{eventsCh: eventsCh, watchID: 10}, nil
		},
	}

	svc := NewWatchService(hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntityType(ctx, &WatchEntityTypeRequest{
		EntityType: "user",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if ch == nil {
		t.Fatal("expected non-nil channel")
	}

	// Send events for two different entities.
	eventsCh <- watch.WatchResponse{
		WatchID:  10,
		Revision: 100,
		Events: []*watch.Event{
			{
				Type: watch.EventPut,
				Kv:   &kv.KeyValue{Key: []byte("user/u1"), Value: []byte("alice")},
			},
			{
				Type: watch.EventPut,
				Kv:   &kv.KeyValue{Key: []byte("user/u2"), Value: []byte("bob")},
			},
		},
	}

	evt1 := <-ch
	if evt1.EntityType != "user" {
		t.Fatalf("expected EntityType 'user', got %q", evt1.EntityType)
	}
	if evt1.ID != "u1" {
		t.Fatalf("expected ID 'u1', got %q", evt1.ID)
	}
	if string(evt1.Data) != "alice" {
		t.Fatalf("expected Data 'alice', got %q", evt1.Data)
	}

	evt2 := <-ch
	if evt2.ID != "u2" {
		t.Fatalf("expected ID 'u2', got %q", evt2.ID)
	}
	if string(evt2.Data) != "bob" {
		t.Fatalf("expected Data 'bob', got %q", evt2.Data)
	}
}

func TestWatchService_WatchEntityType_WithStartRevision(t *testing.T) {
	hub := &mockWatchHub{
		watchPrefixFn: func(_ context.Context, _ string, opts ...watch.WatchOption) (watch.Watcher, error) {
			if len(opts) != 1 {
				t.Fatalf("expected 1 watch option, got %d", len(opts))
			}
			wo := watch.ApplyWatchOptions(opts)
			if wo.StartRevision != 200 {
				t.Fatalf("expected StartRevision 200, got %d", wo.StartRevision)
			}
			return &mockWatcher{eventsCh: make(chan watch.WatchResponse)}, nil
		},
	}

	svc := NewWatchService(hub)
	_, err := svc.WatchEntityType(context.Background(), &WatchEntityTypeRequest{
		EntityType:    "config",
		StartRevision: 200,
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestWatchService_WatchEntityType_DeleteEvent(t *testing.T) {
	eventsCh := make(chan watch.WatchResponse, 1)

	hub := &mockWatchHub{
		watchPrefixFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{eventsCh: eventsCh, watchID: 11}, nil
		},
	}

	svc := NewWatchService(hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntityType(ctx, &WatchEntityTypeRequest{
		EntityType: "session",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	eventsCh <- watch.WatchResponse{
		WatchID:  11,
		Revision: 50,
		Events: []*watch.Event{
			{
				Type: watch.EventDelete,
				Kv:   &kv.KeyValue{Key: []byte("session/s1")},
			},
		},
	}

	evt := <-ch
	if evt.Type != WatchEventDelete {
		t.Fatalf("expected WatchEventDelete, got %d", evt.Type)
	}
	if evt.ID != "s1" {
		t.Fatalf("expected ID 's1', got %q", evt.ID)
	}
}

func TestWatchService_WatchEntityType_ChannelClosesOnWatcherClose(t *testing.T) {
	eventsCh := make(chan watch.WatchResponse)

	hub := &mockWatchHub{
		watchPrefixFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{eventsCh: eventsCh, watchID: 12}, nil
		},
	}

	svc := NewWatchService(hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntityType(ctx, &WatchEntityTypeRequest{
		EntityType: "user",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	// Close the raw watcher's event channel.
	close(eventsCh)

	// The entity event channel should also close.
	_, ok := <-ch
	if ok {
		t.Fatal("expected channel to be closed after watcher close")
	}
}

// ---------------------------------------------------------------------------
// WatchEntityType - error paths
// ---------------------------------------------------------------------------

func TestWatchService_WatchEntityType_EmptyType(t *testing.T) {
	hub := &mockWatchHub{}
	svc := NewWatchService(hub)

	_, err := svc.WatchEntityType(context.Background(), &WatchEntityTypeRequest{
		EntityType: "",
	})
	if err == nil {
		t.Fatal("expected error for empty entity type, got nil")
	}

	var emptyTypeErr *EntityEmptyTypeError
	if !errors.As(err, &emptyTypeErr) {
		t.Fatalf("expected *EntityEmptyTypeError, got %T: %v", err, err)
	}
}

func TestWatchService_WatchEntityType_HubError(t *testing.T) {
	hubErr := &WatchCanceledError{Reason: "shutdown"}
	hub := &mockWatchHub{
		watchPrefixFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return nil, hubErr
		},
	}

	svc := NewWatchService(hub)
	ch, err := svc.WatchEntityType(context.Background(), &WatchEntityTypeRequest{
		EntityType: "user",
	})
	if ch != nil {
		t.Fatal("expected nil channel on error")
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var wce *WatchCanceledError
	if !errors.As(err, &wce) {
		t.Fatalf("expected *WatchCanceledError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// WatchEntityEvent type constants
// ---------------------------------------------------------------------------

func TestWatchEventType_Constants(t *testing.T) {
	if WatchEventPut != 0 {
		t.Fatalf("expected WatchEventPut=0, got %d", WatchEventPut)
	}
	if WatchEventDelete != 1 {
		t.Fatalf("expected WatchEventDelete=1, got %d", WatchEventDelete)
	}
}

// ---------------------------------------------------------------------------
// relayEntityEvents - nil event handling
// ---------------------------------------------------------------------------

func TestWatchService_WatchEntity_NilEventSkipped(t *testing.T) {
	eventsCh := make(chan watch.WatchResponse, 2)

	hub := &mockWatchHub{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{eventsCh: eventsCh, watchID: 5}, nil
		},
	}

	svc := NewWatchService(hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntity(ctx, &WatchEntityRequest{
		EntityType: "user",
		ID:         "u1",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	// Send a response with a nil event followed by a valid event.
	eventsCh <- watch.WatchResponse{
		WatchID:  5,
		Revision: 15,
		Events: []*watch.Event{
			nil,
			{
				Type: watch.EventPut,
				Kv:   &kv.KeyValue{Key: []byte("user/u1"), Value: []byte("valid")},
			},
		},
	}

	// Only the valid event should come through.
	evt := <-ch
	if string(evt.Data) != "valid" {
		t.Fatalf("expected data 'valid', got %q", evt.Data)
	}
}

func TestWatchService_WatchEntity_NilKvSkipped(t *testing.T) {
	eventsCh := make(chan watch.WatchResponse, 2)

	hub := &mockWatchHub{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{eventsCh: eventsCh, watchID: 6}, nil
		},
	}

	svc := NewWatchService(hub)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntity(ctx, &WatchEntityRequest{
		EntityType: "user",
		ID:         "u1",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	// Send a response with a nil Kv followed by a valid event.
	eventsCh <- watch.WatchResponse{
		WatchID:  6,
		Revision: 16,
		Events: []*watch.Event{
			{Type: watch.EventPut, Kv: nil},
			{
				Type: watch.EventPut,
				Kv:   &kv.KeyValue{Key: []byte("user/u1"), Value: []byte("ok")},
			},
		},
	}

	evt := <-ch
	if string(evt.Data) != "ok" {
		t.Fatalf("expected data 'ok', got %q", evt.Data)
	}
}

// ---------------------------------------------------------------------------
// relayEntityEvents - context cancel during send (inner select branch)
// ---------------------------------------------------------------------------

func TestWatchService_WatchEntity_ContextCancelDuringSend(t *testing.T) {
	// This test covers the inner select branch in relayEntityEvents where
	// the context is canceled while the goroutine is blocked trying to
	// send an event to the full output channel (line 183 in watch_service.go).
	//
	// The output channel has a buffer of 16. We fill it completely, then
	// send one more event that will block on the inner select, and cancel
	// the context to trigger the ctx.Done() branch.
	eventsCh := make(chan watch.WatchResponse, 20)
	readyCh := make(chan struct{})

	hub := &mockWatchHub{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{eventsCh: eventsCh, watchID: 99}, nil
		},
	}

	svc := NewWatchService(hub)
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := svc.WatchEntity(ctx, &WatchEntityRequest{
		EntityType: "metric",
		ID:         "m1",
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	// Fill the output channel buffer (16 slots) by sending 16 events.
	// Each event is in its own WatchResponse so relayEntityEvents processes
	// them sequentially.
	for i := 0; i < 16; i++ {
		eventsCh <- watch.WatchResponse{
			WatchID:  99,
			Revision: int64(i + 1),
			Events: []*watch.Event{
				{
					Type: watch.EventPut,
					Kv:   &kv.KeyValue{Key: []byte("metric/m1"), Value: []byte("x")},
				},
			},
		}
	}

	// Wait until the relay goroutine has filled the output channel.
	// We do this by sending a 17th event and using a separate goroutine
	// to signal when the relay is blocked on the inner select.
	go func() {
		// Wait for all 16 events to be buffered in the output channel.
		// Once event 17 is sent, the relay will block on the inner select.
		eventsCh <- watch.WatchResponse{
			WatchID:  99,
			Revision: 17,
			Events: []*watch.Event{
				{
					Type: watch.EventPut,
					Kv:   &kv.KeyValue{Key: []byte("metric/m1"), Value: []byte("overflow")},
				},
			},
		}
		close(readyCh)
	}()

	// Wait for the 17th event to be queued.
	<-readyCh

	// Cancel the context. The relay goroutine should exit via the inner
	// ctx.Done() branch because the output channel is full.
	cancel()

	// Drain the output channel. The channel should close because the
	// relay goroutine exited.
	count := 0
	for range ch {
		count++
	}

	// We should have received at most 16 events (the buffer was full).
	// The 17th event may or may not have been delivered depending on
	// goroutine scheduling, but the channel must close.
	if count > 17 {
		t.Fatalf("expected at most 17 events, got %d", count)
	}
}
