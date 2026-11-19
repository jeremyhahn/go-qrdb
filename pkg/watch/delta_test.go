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
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/messaging"
)

// TestUser is a test entity for delta computation
type TestUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Age   int    `json:"age"`
}

// TestWithDeltaMode verifies the WithDeltaMode option.
func TestWithDeltaMode(t *testing.T) {
	t.Parallel()

	opts := &managerWatchOptions{}
	assert.False(t, opts.deltaMode)

	WithDeltaMode()(opts)
	assert.True(t, opts.deltaMode)
}

// TestWithIncludeOldValues verifies the WithIncludeOldValues option.
func TestWithIncludeOldValues(t *testing.T) {
	t.Parallel()

	opts := &managerWatchOptions{}
	assert.False(t, opts.includeOldValue)

	WithIncludeOldValues()(opts)
	assert.True(t, opts.includeOldValue)
}

// TestWithEntityType verifies the WithEntityType option.
func TestWithEntityType(t *testing.T) {
	t.Parallel()

	opts := &managerWatchOptions{}
	assert.Nil(t, opts.entityType)

	entityType := &TestUser{}
	WithEntityType(entityType)(opts)
	assert.Equal(t, entityType, opts.entityType)
}

// TestNewManagerWithDelta verifies the delta-enabled manager constructor.
// TestDeltaWatch_ComputesDelta verifies that delta mode computes deltas.
func TestDeltaWatch_ComputesDelta(t *testing.T) {
	t.Parallel()

	log, err := logger.New(logger.LogConfig{
		Level:  "debug",
		Format: "console",
		Output: "stdout",
	})
	require.NoError(t, err)

	deltaComputer := messaging.NewDeltaComputer(log)
	mgr := NewManagerWithDelta(deltaComputer, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := newMockManagerWatchStreamWithContext(ctx)

	// Create watch with delta mode enabled
	opts := &managerWatchOptions{
		deltaMode:  true,
		entityType: &TestUser{},
	}
	watch, err := mgr.addInternal([]byte("users/alice"), nil, 0, WatchFilters{}, stream, opts, true)
	require.NoError(t, err)
	require.NotNil(t, watch)
	assert.True(t, watch.deltaMode)
	assert.NotNil(t, watch.entityType)

	// Create old and new user data
	oldUser := TestUser{ID: "alice", Name: "Alice", Email: "alice@example.com", Age: 25}
	newUser := TestUser{ID: "alice", Name: "Alice Smith", Email: "alice@example.com", Age: 26}

	oldData, err := json.Marshal(oldUser)
	require.NoError(t, err)
	newData, err := json.Marshal(newUser)
	require.NoError(t, err)

	// Notify with both old and new values
	mgr.NotifyPut([]byte("users/alice"), oldData, newData, 1, 1)

	// Wait for event to be delivered
	select {
	case event := <-watch.Events:
		require.NotNil(t, event)
		assert.Equal(t, PUT, event.Type)

		// Delta mode should populate Delta field
		require.NotNil(t, event.Delta, "Delta should be populated in delta mode")
		assert.Nil(t, event.Kv, "Kv should be nil in delta mode")

		// Verify changes
		assert.Len(t, event.Delta.Changes, 2, "Should have 2 changes (name and age)")

		// Find name and age changes
		var nameChange, ageChange *messaging.FieldChange
		for i := range event.Delta.Changes {
			switch event.Delta.Changes[i].Path {
			case "name":
				nameChange = &event.Delta.Changes[i]
			case "age":
				ageChange = &event.Delta.Changes[i]
			}
		}

		require.NotNil(t, nameChange, "Should have name change")
		assert.Equal(t, messaging.OpSet, nameChange.Operation)
		assert.Equal(t, "Alice Smith", nameChange.Value)

		require.NotNil(t, ageChange, "Should have age change")
		assert.Equal(t, messaging.OpSet, ageChange.Operation)
		// JSON numbers can be float64 or int depending on unmarshaling
		switch v := ageChange.Value.(type) {
		case float64:
			assert.Equal(t, float64(26), v)
		case int:
			assert.Equal(t, 26, v)
		default:
			t.Errorf("unexpected age value type: %T", ageChange.Value)
		}

	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for delta event")
	}

	cancel()
}

// TestDeltaWatch_IncludesOldValues verifies old values are included when requested.
func TestDeltaWatch_IncludesOldValues(t *testing.T) {
	t.Parallel()

	log, err := logger.New(logger.LogConfig{
		Level:  "debug",
		Format: "console",
		Output: "stdout",
	})
	require.NoError(t, err)

	deltaComputer := messaging.NewDeltaComputer(log)
	mgr := NewManagerWithDelta(deltaComputer, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := newMockManagerWatchStreamWithContext(ctx)

	// Create watch with delta mode AND includeOldValue enabled
	opts := &managerWatchOptions{
		deltaMode:       true,
		includeOldValue: true,
		entityType:      &TestUser{},
	}
	watch, err := mgr.addInternal([]byte("users/bob"), nil, 0, WatchFilters{}, stream, opts, true)
	require.NoError(t, err)
	assert.True(t, watch.includeOldValue)

	// Create old and new user data
	oldUser := TestUser{ID: "bob", Name: "Bob", Email: "bob@example.com", Age: 30}
	newUser := TestUser{ID: "bob", Name: "Bob Jones", Email: "bob@example.com", Age: 30}

	oldData, err := json.Marshal(oldUser)
	require.NoError(t, err)
	newData, err := json.Marshal(newUser)
	require.NoError(t, err)

	mgr.NotifyPut([]byte("users/bob"), oldData, newData, 1, 1)

	select {
	case event := <-watch.Events:
		require.NotNil(t, event.Delta)

		// Find the name change
		var nameChange *messaging.FieldChange
		for i := range event.Delta.Changes {
			if event.Delta.Changes[i].Path == "name" {
				nameChange = &event.Delta.Changes[i]
				break
			}
		}

		require.NotNil(t, nameChange)
		assert.Equal(t, "Bob Jones", nameChange.Value)
		assert.Equal(t, "Bob", nameChange.OldValue, "OldValue should be preserved when includeOldValue is true")

	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for event")
	}

	cancel()
}

// TestDeltaWatch_WithoutOldValues verifies old values are stripped when not requested.
func TestDeltaWatch_WithoutOldValues(t *testing.T) {
	t.Parallel()

	log, err := logger.New(logger.LogConfig{
		Level:  "debug",
		Format: "console",
		Output: "stdout",
	})
	require.NoError(t, err)

	deltaComputer := messaging.NewDeltaComputer(log)
	mgr := NewManagerWithDelta(deltaComputer, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := newMockManagerWatchStreamWithContext(ctx)

	// Create watch with delta mode but WITHOUT includeOldValue
	opts := &managerWatchOptions{
		deltaMode:       true,
		includeOldValue: false,
		entityType:      &TestUser{},
	}
	watch, err := mgr.addInternal([]byte("users/charlie"), nil, 0, WatchFilters{}, stream, opts, true)
	require.NoError(t, err)
	assert.False(t, watch.includeOldValue)

	oldUser := TestUser{ID: "charlie", Name: "Charlie", Email: "charlie@example.com", Age: 40}
	newUser := TestUser{ID: "charlie", Name: "Charlie Brown", Email: "charlie@example.com", Age: 40}

	oldData, err := json.Marshal(oldUser)
	require.NoError(t, err)
	newData, err := json.Marshal(newUser)
	require.NoError(t, err)

	mgr.NotifyPut([]byte("users/charlie"), oldData, newData, 1, 1)

	select {
	case event := <-watch.Events:
		require.NotNil(t, event.Delta)

		// Find the name change
		var nameChange *messaging.FieldChange
		for i := range event.Delta.Changes {
			if event.Delta.Changes[i].Path == "name" {
				nameChange = &event.Delta.Changes[i]
				break
			}
		}

		require.NotNil(t, nameChange)
		assert.Equal(t, "Charlie Brown", nameChange.Value)
		assert.Nil(t, nameChange.OldValue, "OldValue should be nil when includeOldValue is false")

	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for event")
	}

	cancel()
}

// TestDeltaWatch_FallsBackToFullValue verifies fallback when no entity type is provided.
func TestDeltaWatch_FallsBackToFullValue(t *testing.T) {
	t.Parallel()

	log, err := logger.New(logger.LogConfig{
		Level:  "debug",
		Format: "console",
		Output: "stdout",
	})
	require.NoError(t, err)

	deltaComputer := messaging.NewDeltaComputer(log)
	mgr := NewManagerWithDelta(deltaComputer, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := newMockManagerWatchStreamWithContext(ctx)

	// Create watch with delta mode but NO entity type
	opts := &managerWatchOptions{
		deltaMode:  true,
		entityType: nil, // No entity type - should fallback to full value
	}
	watch, err := mgr.addInternal([]byte("users/dave"), nil, 0, WatchFilters{}, stream, opts, true)
	require.NoError(t, err)
	assert.True(t, watch.deltaMode)
	assert.Nil(t, watch.entityType)

	newUser := TestUser{ID: "dave", Name: "Dave", Email: "dave@example.com", Age: 50}
	newData, err := json.Marshal(newUser)
	require.NoError(t, err)

	mgr.NotifyPut([]byte("users/dave"), nil, newData, 1, 1)

	select {
	case event := <-watch.Events:
		// Without entity type, should fallback to full value
		assert.Nil(t, event.Delta, "Delta should be nil without entity type")
		require.NotNil(t, event.Kv, "Kv should be populated as fallback")
		assert.Equal(t, newData, event.Kv.Value)

	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for event")
	}

	cancel()
}

// TestDeltaWatch_WithoutDeltaComputer verifies non-delta mode works normally.
func TestDeltaWatch_WithoutDeltaComputer(t *testing.T) {
	t.Parallel()

	// Use regular manager without delta computer
	mgr := NewManager()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := newMockManagerWatchStreamWithContext(ctx)

	// Create watch with delta mode (but manager has no delta computer)
	opts := &managerWatchOptions{
		deltaMode:  true,
		entityType: &TestUser{},
	}
	watch, err := mgr.addInternal([]byte("users/eve"), nil, 0, WatchFilters{}, stream, opts, true)
	require.NoError(t, err)

	newUser := TestUser{ID: "eve", Name: "Eve", Email: "eve@example.com", Age: 60}
	newData, err := json.Marshal(newUser)
	require.NoError(t, err)

	mgr.NotifyPut([]byte("users/eve"), nil, newData, 1, 1)

	select {
	case event := <-watch.Events:
		// Without delta computer, should use full value
		assert.Nil(t, event.Delta)
		require.NotNil(t, event.Kv)
		assert.Equal(t, newData, event.Kv.Value)

	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for event")
	}

	cancel()
}

// TestDeltaWatch_NewEntity verifies delta computation for new entities (nil old value).
func TestDeltaWatch_NewEntity(t *testing.T) {
	t.Parallel()

	log, err := logger.New(logger.LogConfig{
		Level:  "debug",
		Format: "console",
		Output: "stdout",
	})
	require.NoError(t, err)

	deltaComputer := messaging.NewDeltaComputer(log)
	mgr := NewManagerWithDelta(deltaComputer, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := newMockManagerWatchStreamWithContext(ctx)

	opts := &managerWatchOptions{
		deltaMode:  true,
		entityType: &TestUser{},
	}
	watch, err := mgr.addInternal([]byte("users/frank"), nil, 0, WatchFilters{}, stream, opts, true)
	require.NoError(t, err)

	// New entity - no previous value
	newUser := TestUser{ID: "frank", Name: "Frank", Email: "frank@example.com", Age: 70}
	newData, err := json.Marshal(newUser)
	require.NoError(t, err)

	mgr.NotifyPut([]byte("users/frank"), nil, newData, 1, 1) // nil prevValue for new entity

	select {
	case event := <-watch.Events:
		require.NotNil(t, event.Delta, "Delta should be computed for new entity")

		// For new entity, all fields should be "set" operations
		assert.Greater(t, len(event.Delta.Changes), 0, "Should have changes for new entity")

		// Verify all operations are OpSet
		for _, change := range event.Delta.Changes {
			assert.Equal(t, messaging.OpSet, change.Operation, "All changes should be OpSet for new entity")
		}

	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for event")
	}

	cancel()
}

// TestComputeDeltaEvent_NilEntityType tests computeDeltaEvent with nil entityType.
// TestComputeDeltaEvent_EmptyData tests computeDeltaEvent with empty data.
func TestComputeDeltaEvent_EmptyData(t *testing.T) {
	t.Parallel()

	log, err := logger.New(logger.LogConfig{
		Level:  "debug",
		Format: "console",
		Output: "stdout",
	})
	require.NoError(t, err)

	deltaComputer := messaging.NewDeltaComputer(log)
	mgr := NewManagerWithDelta(deltaComputer, log)

	watch := &ManagerWatch{
		ID:         1,
		deltaMode:  true,
		entityType: &TestUser{},
	}

	event := &ManagerWatchEvent{
		Type: PUT,
		Kv: &ManagerKeyValue{
			Key:   []byte("test-key"),
			Value: nil, // Empty value
		},
		PrevKv: nil, // No previous value
	}

	result := mgr.computeDeltaEvent(watch, event)
	assert.Nil(t, result, "Should return nil when both old and new data are empty")
}

// TestComputeDeltaEvent_InvalidJSON tests computeDeltaEvent with invalid JSON.
func TestComputeDeltaEvent_InvalidJSON(t *testing.T) {
	t.Parallel()

	log, err := logger.New(logger.LogConfig{
		Level:  "debug",
		Format: "console",
		Output: "stdout",
	})
	require.NoError(t, err)

	deltaComputer := messaging.NewDeltaComputer(log)
	mgr := NewManagerWithDelta(deltaComputer, log)

	watch := &ManagerWatch{
		ID:         1,
		deltaMode:  true,
		entityType: &TestUser{},
	}

	event := &ManagerWatchEvent{
		Type: PUT,
		Kv: &ManagerKeyValue{
			Key:   []byte("test-key"),
			Value: []byte("not valid json"),
		},
	}

	result := mgr.computeDeltaEvent(watch, event)
	assert.Nil(t, result, "Should return nil when JSON is invalid")
}

// TestDeltaWatch_PrefixWatch tests delta mode with prefix watches.
func TestDeltaWatch_PrefixWatch(t *testing.T) {
	t.Parallel()

	log, err := logger.New(logger.LogConfig{
		Level:  "debug",
		Format: "console",
		Output: "stdout",
	})
	require.NoError(t, err)

	deltaComputer := messaging.NewDeltaComputer(log)
	mgr := NewManagerWithDelta(deltaComputer, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := newMockManagerWatchStreamWithContext(ctx)

	// Create prefix watch with delta mode
	prefix := []byte("/users/")
	rangeEnd := managerGetPrefix(prefix)

	opts := &managerWatchOptions{
		deltaMode:  true,
		entityType: &TestUser{},
	}
	watch, err := mgr.addInternal(prefix, rangeEnd, 0, WatchFilters{}, stream, opts, true)
	require.NoError(t, err)

	// Notify for a key matching the prefix
	oldUser := TestUser{ID: "grace", Name: "Grace", Email: "grace@example.com", Age: 80}
	newUser := TestUser{ID: "grace", Name: "Grace Hopper", Email: "grace@example.com", Age: 80}

	oldData, err := json.Marshal(oldUser)
	require.NoError(t, err)
	newData, err := json.Marshal(newUser)
	require.NoError(t, err)

	mgr.NotifyPut([]byte("/users/grace"), oldData, newData, 1, 1)

	select {
	case event := <-watch.Events:
		require.NotNil(t, event.Delta, "Delta should be computed for prefix watch")

		// Find the name change
		var nameChange *messaging.FieldChange
		for i := range event.Delta.Changes {
			if event.Delta.Changes[i].Path == "name" {
				nameChange = &event.Delta.Changes[i]
				break
			}
		}

		require.NotNil(t, nameChange)
		assert.Equal(t, "Grace Hopper", nameChange.Value)

	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for event")
	}

	cancel()
}

// TestWatch_DeltaAndNonDeltaMixed tests that delta and non-delta watches can coexist.
func TestWatch_DeltaAndNonDeltaMixed(t *testing.T) {
	t.Parallel()

	log, err := logger.New(logger.LogConfig{
		Level:  "debug",
		Format: "console",
		Output: "stdout",
	})
	require.NoError(t, err)

	deltaComputer := messaging.NewDeltaComputer(log)
	mgr := NewManagerWithDelta(deltaComputer, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream1 := newMockManagerWatchStreamWithContext(ctx)
	stream2 := newMockManagerWatchStreamWithContext(ctx)

	// Create delta watch
	deltaOpts := &managerWatchOptions{
		deltaMode:  true,
		entityType: &TestUser{},
	}
	deltaWatch, err := mgr.addInternal([]byte("users/henry"), nil, 0, WatchFilters{}, stream1, deltaOpts, true)
	require.NoError(t, err)

	// Create non-delta watch on same key
	normalOpts := &managerWatchOptions{
		deltaMode: false,
	}
	normalWatch, err := mgr.addInternal([]byte("users/henry"), nil, 0, WatchFilters{}, stream2, normalOpts, true)
	require.NoError(t, err)

	// Send notification
	oldUser := TestUser{ID: "henry", Name: "Henry", Email: "henry@example.com", Age: 90}
	newUser := TestUser{ID: "henry", Name: "Henry VIII", Email: "henry@example.com", Age: 90}

	oldData, err := json.Marshal(oldUser)
	require.NoError(t, err)
	newData, err := json.Marshal(newUser)
	require.NoError(t, err)

	mgr.NotifyPut([]byte("users/henry"), oldData, newData, 1, 1)

	// Delta watch should receive delta
	select {
	case event := <-deltaWatch.Events:
		require.NotNil(t, event.Delta, "Delta watch should receive delta")
		assert.Nil(t, event.Kv, "Delta watch should not have Kv")
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for delta event")
	}

	// Normal watch should receive full value
	select {
	case event := <-normalWatch.Events:
		assert.Nil(t, event.Delta, "Normal watch should not receive delta")
		require.NotNil(t, event.Kv, "Normal watch should have Kv")
		assert.Equal(t, newData, event.Kv.Value)
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for normal event")
	}

	cancel()
}
