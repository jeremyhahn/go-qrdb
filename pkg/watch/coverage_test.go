// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package watch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStream is a ManagerWatchStream that records sent responses.
type mockStream struct {
	ctx     context.Context
	ch      chan *ManagerWatchResponse
	sendErr error
}

func newMockStream(ctx context.Context) *mockStream {
	return &mockStream{ctx: ctx, ch: make(chan *ManagerWatchResponse, 100)}
}

func (s *mockStream) Send(resp *ManagerWatchResponse) error {
	if s.sendErr != nil {
		return s.sendErr
	}
	s.ch <- resp
	return nil
}

func (s *mockStream) Context() context.Context {
	return s.ctx
}

// TestDeliverEvents_ReceivesEvent verifies that the deliverEvents goroutine
// (started by Add) sends events to the stream after a NotifyPut.
func TestDeliverEvents_ReceivesEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mgr := NewManager()
	stream := newMockStream(ctx)

	w, err := mgr.Add([]byte("deliver-key"), nil, 0, WatchFilters{}, stream)
	require.NoError(t, err)
	require.NotNil(t, w)

	// Trigger an event. Add registered the watch before it returned and notify
	// only enqueues, so this needs no goroutine and no delay; the receive below
	// is the synchronisation.
	mgr.NotifyPut([]byte("deliver-key"), nil, []byte("v1"), 1, 1)

	select {
	case resp := <-stream.ch:
		assert.Equal(t, int64(w.ID), resp.WatchID) //nolint:gosec
		assert.Len(t, resp.Events, 1)
		assert.Equal(t, PUT, resp.Events[0].Type)
	case <-ctx.Done():
		t.Fatal("timeout waiting for deliverEvents to send event")
	}
}

// TestDeliverEvents_StreamError verifies that deliverEvents cancels the watch
// and returns cleanly when Send returns an error, so that no further events
// are delivered on the stream channel.
func TestDeliverEvents_StreamError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mgr := NewManager()
	stream := newMockStream(ctx)
	stream.sendErr = &WatchCreateError{Cause: context.Canceled}

	w, err := mgr.Add([]byte("err-key"), nil, 0, WatchFilters{}, stream)
	require.NoError(t, err)

	// Trigger an event; deliverEvents should handle the Send error and cancel the watch.
	mgr.NotifyPut([]byte("err-key"), nil, []byte("v"), 1, 1)

	// The cancellation IS the thing under test, so it is also the signal: wait
	// for it rather than for a duration. The sleeps this replaced were also the
	// only reason the emptiness check below looked meaningful — this stream's
	// Send always fails, so nothing can ever reach its channel and that
	// assertion cannot fail on its own. The cancellation assertion can.
	select {
	case <-w.ctx.Done():
	case <-ctx.Done():
		t.Fatal("deliverEvents did not cancel the watch after Send returned an error")
	}

	// After the send error the watch is cancelled. A second notification must
	// not appear on the stream channel: the goroutine that is the only writer
	// to it has returned.
	mgr.NotifyPut([]byte("err-key"), nil, []byte("v2"), 2, 2)

	assert.Empty(t, stream.ch, "no events should be delivered after a send error cancels the watch")
}

// TestConvertEvents_DeleteEvent verifies that convertEvents produces a
// ManagerEventTypeDelete event when a DELETE watch event is received.
func TestConvertEvents_DeleteEvent(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	eventCh, err := mgr.Watch(ctx, "del-key")
	require.NoError(t, err)

	mgr.NotifyDelete([]byte("del-key"), []byte("old-value"), 2, 1)

	select {
	case event := <-eventCh:
		assert.Equal(t, ManagerEventTypeDelete, event.Type)
		assert.Equal(t, []byte("del-key"), event.Key)
		// Previous value should be available when PrevKv was set.
		assert.Equal(t, []byte("old-value"), event.Value)
	case <-ctx.Done():
		t.Fatal("timeout waiting for delete event")
	}
}

// TestSendEvent_FilterNoPut verifies that sendEvent discards PUT events when
// the NoPut filter is set.
func TestSendEvent_FilterNoPut(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	stream := newMockStream(ctx)
	_, err := mgr.Add([]byte("filter-put"), nil, 0, WatchFilters{NoPut: true}, stream)
	require.NoError(t, err)

	mgr.NotifyPut([]byte("filter-put"), nil, []byte("v"), 1, 1)

	// No event should arrive because NoPut filter discards it.
	select {
	case resp := <-stream.ch:
		t.Errorf("unexpected event received with NoPut filter: %v", resp)
	case <-ctx.Done():
		// Expected: timeout because no event was delivered.
	}
}

// TestSendEvent_FilterNoDelete verifies that sendEvent discards DELETE events
// when the NoDelete filter is set.
func TestSendEvent_FilterNoDelete(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	stream := newMockStream(ctx)
	_, err := mgr.Add([]byte("filter-del"), nil, 0, WatchFilters{NoDelete: true}, stream)
	require.NoError(t, err)

	mgr.NotifyDelete([]byte("filter-del"), nil, 1, 1)

	// No event should arrive because NoDelete filter discards it.
	select {
	case resp := <-stream.ch:
		t.Errorf("unexpected event received with NoDelete filter: %v", resp)
	case <-ctx.Done():
		// Expected: timeout because no event was delivered.
	}
}

// TestWatch_ContextCancellation verifies that Watch cleans up properly when
// the context is cancelled before any event is received.
func TestWatch_ContextCancellation(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())

	eventCh, err := mgr.Watch(ctx, "cancel-key")
	require.NoError(t, err)

	cancel()

	// Channel should be closed once the goroutine exits.
	select {
	case _, ok := <-eventCh:
		if ok {
			t.Error("expected channel to be closed after context cancellation")
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("timeout: channel was not closed after context cancellation")
	}
}

// TestWatch_DeleteEventNilPrevKv verifies that a delete event with no PrevKv
// produces a nil Value in the ManagerEvent.
func TestWatch_DeleteEventNilPrevKv(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	eventCh, err := mgr.Watch(ctx, "nil-prevkv")
	require.NoError(t, err)

	mgr.NotifyDelete([]byte("nil-prevkv"), nil, 3, 1)

	select {
	case event := <-eventCh:
		assert.Equal(t, ManagerEventTypeDelete, event.Type)
		assert.Nil(t, event.Value, "Value should be nil when no PrevKv available")
	case <-ctx.Done():
		t.Fatal("timeout waiting for delete event with nil PrevKv")
	}
}
