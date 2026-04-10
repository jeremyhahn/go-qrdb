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

package watch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManagerWatch_ExactKey verifies watching exact keys.
func TestManagerWatch_ExactKey(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start watching
	eventCh, err := mgr.Watch(ctx, "test-key")
	require.NoError(t, err)
	require.NotNil(t, eventCh)

	// Trigger event in background
	go func() {
		time.Sleep(10 * time.Millisecond)
		mgr.NotifyPut([]byte("test-key"), nil, []byte("test-value"), 1, 1)
	}()

	// Receive event
	select {
	case event := <-eventCh:
		assert.Equal(t, ManagerEventTypePut, event.Type)
		assert.Equal(t, []byte("test-key"), event.Key)
		assert.Equal(t, []byte("test-value"), event.Value)
	case <-ctx.Done():
		t.Fatal("Timeout waiting for event")
	}
}

// TestManagerWatch_WithPrefix verifies prefix watching.
func TestManagerWatch_WithPrefix(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start watching with prefix
	eventCh, err := mgr.Watch(ctx, "/config/", WithPrefix())
	require.NoError(t, err)
	require.NotNil(t, eventCh)

	// Trigger event in background
	go func() {
		time.Sleep(10 * time.Millisecond)
		mgr.NotifyPut([]byte("/config/server"), nil, []byte("localhost"), 1, 1)
	}()

	// Receive event
	select {
	case event := <-eventCh:
		assert.Equal(t, ManagerEventTypePut, event.Type)
		assert.Equal(t, []byte("/config/server"), event.Key)
		assert.Equal(t, []byte("localhost"), event.Value)
	case <-ctx.Done():
		t.Fatal("Timeout waiting for event")
	}
}

// TestManagerWatch_DeleteEvent verifies DELETE event conversion.
func TestManagerWatch_DeleteEvent(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start watching
	eventCh, err := mgr.Watch(ctx, "test-key")
	require.NoError(t, err)

	// Trigger DELETE event
	go func() {
		time.Sleep(10 * time.Millisecond)
		mgr.NotifyDelete([]byte("test-key"), []byte("old-value"), 2, 2)
	}()

	// Receive event
	select {
	case event := <-eventCh:
		assert.Equal(t, ManagerEventTypeDelete, event.Type)
		assert.Equal(t, []byte("test-key"), event.Key)
		assert.Equal(t, []byte("old-value"), event.Value) // Previous value
	case <-ctx.Done():
		t.Fatal("Timeout waiting for event")
	}
}

// TestManagerWatch_ContextCancellation verifies cleanup on cancel.
func TestManagerWatch_ContextCancellation(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())

	// Start watching
	eventCh, err := mgr.Watch(ctx, "test-key")
	require.NoError(t, err)

	// Cancel context
	cancel()

	// Channel should close
	timeout := time.After(1 * time.Second)
	select {
	case _, ok := <-eventCh:
		if ok {
			t.Error("Channel should be closed")
		}
		// Expected - channel closed
	case <-timeout:
		t.Fatal("Timeout waiting for channel close")
	}
}

// TestManagerWatch_MultipleEvents verifies handling multiple events.
func TestManagerWatch_MultipleEvents(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start watching
	eventCh, err := mgr.Watch(ctx, "/data/", WithPrefix())
	require.NoError(t, err)

	// Send multiple events
	go func() {
		time.Sleep(10 * time.Millisecond)
		mgr.NotifyPut([]byte("/data/key1"), nil, []byte("value1"), 1, 1)
		mgr.NotifyPut([]byte("/data/key2"), nil, []byte("value2"), 2, 2)
		mgr.NotifyDelete([]byte("/data/key1"), []byte("value1"), 3, 3)
	}()

	// Receive events
	receivedEvents := []ManagerEvent{}
	timeout := time.After(500 * time.Millisecond)

	for i := 0; i < 3; i++ {
		select {
		case event := <-eventCh:
			receivedEvents = append(receivedEvents, event)
		case <-timeout:
			t.Fatalf("Timeout waiting for event %d", i+1)
		}
	}

	require.Len(t, receivedEvents, 3)

	// Verify events
	assert.Equal(t, ManagerEventTypePut, receivedEvents[0].Type)
	assert.Equal(t, []byte("/data/key1"), receivedEvents[0].Key)

	assert.Equal(t, ManagerEventTypePut, receivedEvents[1].Type)
	assert.Equal(t, []byte("/data/key2"), receivedEvents[1].Key)

	assert.Equal(t, ManagerEventTypeDelete, receivedEvents[2].Type)
	assert.Equal(t, []byte("/data/key1"), receivedEvents[2].Key)
}

// TestManagerConvertEvents_PutEvent verifies PUT event conversion.
func TestManagerConvertEvents_PutEvent(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create watch manually to test convertEvents
	stream := &managerNoopStream{ctx: ctx}
	watch, err := mgr.addInternal([]byte("key"), nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	require.NoError(t, err)

	eventCh := make(chan ManagerEvent, 10)

	// Start convertEvents
	go mgr.convertEvents(watch, eventCh)

	// Send ManagerWatchEvent
	watchEvent := &ManagerWatchEvent{
		Type: PUT,
		Kv: &ManagerKeyValue{
			Key:   []byte("key"),
			Value: []byte("value"),
		},
		Revision: 1,
	}
	watch.Events <- watchEvent

	// Receive converted event
	timeout := time.After(500 * time.Millisecond)
	select {
	case event := <-eventCh:
		assert.Equal(t, ManagerEventTypePut, event.Type)
		assert.Equal(t, []byte("key"), event.Key)
		assert.Equal(t, []byte("value"), event.Value)
	case <-timeout:
		t.Fatal("Timeout waiting for converted event")
	}
}

// TestManagerConvertEvents_DeleteEventWithPrevValue verifies DELETE with previous value.
func TestManagerConvertEvents_DeleteEventWithPrevValue(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := &managerNoopStream{ctx: ctx}
	watch, err := mgr.addInternal([]byte("key"), nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	require.NoError(t, err)

	eventCh := make(chan ManagerEvent, 10)
	go mgr.convertEvents(watch, eventCh)

	// Send DELETE with PrevKv
	watchEvent := &ManagerWatchEvent{
		Type: DELETE,
		Kv: &ManagerKeyValue{
			Key:   []byte("key"),
			Value: nil,
		},
		PrevKv: &ManagerKeyValue{
			Key:   []byte("key"),
			Value: []byte("old-value"),
		},
		Revision: 2,
	}
	watch.Events <- watchEvent

	timeout := time.After(500 * time.Millisecond)
	select {
	case event := <-eventCh:
		assert.Equal(t, ManagerEventTypeDelete, event.Type)
		assert.Equal(t, []byte("key"), event.Key)
		assert.Equal(t, []byte("old-value"), event.Value)
	case <-timeout:
		t.Fatal("Timeout waiting for converted event")
	}
}

// TestManagerConvertEvents_DeleteEventNoPrevValue verifies DELETE without previous value.
func TestManagerConvertEvents_DeleteEventNoPrevValue(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := &managerNoopStream{ctx: ctx}
	watch, err := mgr.addInternal([]byte("key"), nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	require.NoError(t, err)

	eventCh := make(chan ManagerEvent, 10)
	go mgr.convertEvents(watch, eventCh)

	// Send DELETE without PrevKv
	watchEvent := &ManagerWatchEvent{
		Type: DELETE,
		Kv: &ManagerKeyValue{
			Key:   []byte("key"),
			Value: nil,
		},
		PrevKv:   nil,
		Revision: 2,
	}
	watch.Events <- watchEvent

	timeout := time.After(500 * time.Millisecond)
	select {
	case event := <-eventCh:
		assert.Equal(t, ManagerEventTypeDelete, event.Type)
		assert.Equal(t, []byte("key"), event.Key)
		assert.Nil(t, event.Value)
	case <-timeout:
		t.Fatal("Timeout waiting for converted event")
	}
}

// TestManagerConvertEvents_ContextCancellation verifies cleanup on context cancel.
func TestManagerConvertEvents_ContextCancellation(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithCancel(context.Background())

	stream := &managerNoopStream{ctx: ctx}
	watch, err := mgr.addInternal([]byte("key"), nil, 0, WatchFilters{}, stream, &managerWatchOptions{}, true)
	require.NoError(t, err)

	eventCh := make(chan ManagerEvent, 10)
	go mgr.convertEvents(watch, eventCh)

	// Cancel context
	cancel()

	// Channel should close
	timeout := time.After(500 * time.Millisecond)
	select {
	case _, ok := <-eventCh:
		if ok {
			t.Error("Channel should be closed")
		}
	case <-timeout:
		t.Fatal("Timeout waiting for channel close")
	}
}

// TestManagerWatch_NoMatchingEvents verifies no events for non-matching keys.
func TestManagerWatch_NoMatchingEvents(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// Watch specific key
	eventCh, err := mgr.Watch(ctx, "/config/server")
	require.NoError(t, err)

	// Notify different key
	go func() {
		time.Sleep(10 * time.Millisecond)
		mgr.NotifyPut([]byte("/config/client"), nil, []byte("value"), 1, 1)
	}()

	// Should not receive event
	select {
	case event := <-eventCh:
		t.Errorf("Should not receive event for non-matching key, got: %+v", event)
	case <-ctx.Done():
		// Expected - timeout
	}
}

// TestManagerWatch_EmptyPrefix verifies empty prefix watches all keys.
func TestManagerWatch_EmptyPrefix(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Watch with empty prefix (should match everything)
	eventCh, err := mgr.Watch(ctx, "", WithPrefix())
	require.NoError(t, err)

	// Notify any key
	go func() {
		time.Sleep(10 * time.Millisecond)
		mgr.NotifyPut([]byte("any-key"), nil, []byte("any-value"), 1, 1)
	}()

	// Should receive event
	select {
	case event := <-eventCh:
		assert.Equal(t, ManagerEventTypePut, event.Type)
		assert.Equal(t, []byte("any-key"), event.Key)
	case <-ctx.Done():
		t.Fatal("Timeout waiting for event")
	}
}

// TestManagerEventTypes verifies event type constants.
func TestManagerEventTypes(t *testing.T) {
	assert.Equal(t, "PUT", ManagerEventTypePut)
	assert.Equal(t, "DELETE", ManagerEventTypeDelete)
}

// TestManagerWatchResponse_Structure verifies ManagerWatchResponse struct.
func TestManagerWatchResponse_Structure(t *testing.T) {
	resp := &ManagerWatchResponse{
		WatchID:  123,
		Revision: 456,
		Events: []*ManagerWatchEvent{
			{
				Type: PUT,
				Kv: &ManagerKeyValue{
					Key:   []byte("key"),
					Value: []byte("value"),
				},
			},
		},
	}

	assert.Equal(t, int64(123), resp.WatchID)
	assert.Equal(t, int64(456), resp.Revision)
	require.Len(t, resp.Events, 1)
	assert.Equal(t, PUT, resp.Events[0].Type)
}

// TestManagerEvent_Structure verifies ManagerEvent struct.
func TestManagerEvent_Structure(t *testing.T) {
	event := ManagerEvent{
		Type:  ManagerEventTypePut,
		Key:   []byte("test-key"),
		Value: []byte("test-value"),
	}

	assert.Equal(t, ManagerEventTypePut, event.Type)
	assert.Equal(t, []byte("test-key"), event.Key)
	assert.Equal(t, []byte("test-value"), event.Value)
}
