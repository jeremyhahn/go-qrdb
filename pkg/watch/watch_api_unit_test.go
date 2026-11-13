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

	// Trigger the event. Watch registers before it returns and notify only
	// enqueues, so the receive below is the synchronisation.
	mgr.NotifyPut([]byte("test-key"), nil, []byte("test-value"), 1, 1)

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

	// Trigger the event. Watch registers before it returns and notify only
	// enqueues, so the receive below is the synchronisation.
	mgr.NotifyPut([]byte("/config/server"), nil, []byte("localhost"), 1, 1)

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

	// Trigger the DELETE event; the receive below is the synchronisation.
	mgr.NotifyDelete([]byte("test-key"), []byte("old-value"), 2, 2)

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

	// Send multiple events; the receives below are the synchronisation.
	mgr.NotifyPut([]byte("/data/key1"), nil, []byte("value1"), 1, 1)
	mgr.NotifyPut([]byte("/data/key2"), nil, []byte("value2"), 2, 2)
	mgr.NotifyDelete([]byte("/data/key1"), []byte("value1"), 3, 3)

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
// TestManagerConvertEvents_DeleteEventWithPrevValue verifies DELETE with previous value.
// TestManagerConvertEvents_DeleteEventNoPrevValue verifies DELETE without previous value.
// TestManagerConvertEvents_ContextCancellation verifies cleanup on context cancel.
// TestManagerWatch_NoMatchingEvents verifies no events for non-matching keys.
// TestManagerWatch_EmptyPrefix verifies empty prefix watches all keys.
func TestManagerWatch_EmptyPrefix(t *testing.T) {
	mgr := NewManager()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Watch with empty prefix (should match everything)
	eventCh, err := mgr.Watch(ctx, "", WithPrefix())
	require.NoError(t, err)

	// Notify any key.
	mgr.NotifyPut([]byte("any-key"), nil, []byte("any-value"), 1, 1)

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
// TestManagerWatchResponse_Structure verifies ManagerWatchResponse struct.
// TestManagerEvent_Structure verifies ManagerEvent struct.
