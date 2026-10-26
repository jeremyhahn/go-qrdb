// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package wiring

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// postureStore is an in-memory kvstore.AtomicKVStore whose compare-and-set
// operations decide under one lock, as a replicated store's state machine
// does. beforeWrite runs ahead of each PutIfAbsent or CompareAndSwap, so a
// test can land a concurrent node's write in the window after the read.
type postureStore struct {
	*kvstore.MemoryKVStore
	mu          sync.Mutex
	beforeWrite func()
	getErr      error
	putErr      error
	swapErr     error
	writes      int
}

var _ kvstore.AtomicKVStore = (*postureStore)(nil)

func newPostureStore() *postureStore {
	return &postureStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
}

func (s *postureStore) Get(ctx context.Context, key string) ([]byte, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	return s.MemoryKVStore.Get(ctx, key)
}

func (s *postureStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if s.beforeWrite != nil {
		s.beforeWrite()
	}
	if s.putErr != nil {
		return false, s.putErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.MemoryKVStore.Get(ctx, key)
	if err != nil {
		return false, err
	}
	if cur != nil {
		return false, nil
	}
	s.writes++
	return true, s.MemoryKVStore.Put(ctx, key, value)
}

func (s *postureStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.MemoryKVStore.Get(ctx, key)
	if err != nil || cur == nil || !bytes.Equal(cur, expected) {
		return false, err
	}
	return true, s.MemoryKVStore.Delete(ctx, key)
}

func (s *postureStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if s.beforeWrite != nil {
		s.beforeWrite()
	}
	if s.swapErr != nil {
		return false, s.swapErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.MemoryKVStore.Get(ctx, key)
	if err != nil || cur == nil || !bytes.Equal(cur, expected) {
		return false, err
	}
	s.writes++
	return true, s.MemoryKVStore.Put(ctx, key, next)
}

// recorded reads the posture record straight from the backing map.
func (s *postureStore) recorded(t *testing.T) string {
	t.Helper()
	v, err := s.MemoryKVStore.Get(context.Background(), SeparationPostureKey)
	require.NoError(t, err)
	return string(v)
}

// postureSink is an audit.AuditLogger double that keeps every event it is
// handed.
type postureSink struct {
	mu     sync.Mutex
	events []audit.AuditEvent
}

var _ audit.AuditLogger = (*postureSink)(nil)

func (s *postureSink) Emit(_ context.Context, ev audit.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}

func (s *postureSink) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}

func (s *postureSink) Close() error { return nil }

func (s *postureSink) all() []audit.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]audit.AuditEvent(nil), s.events...)
}

func requirePostureEvent(t *testing.T, ev audit.AuditEvent, previous, posture string, fipsForced bool) {
	t.Helper()
	assert.Equal(t, SeparationPostureAuditResource, ev.Resource)
	assert.Equal(t, ActionSeparationPostureChange, ev.Action)
	assert.Equal(t, "success", ev.Outcome)
	assert.Equal(t, separationPostureAuditActor, ev.Actor)
	assert.Equal(t, map[string]any{
		"previous":    previous,
		"posture":     posture,
		"fips_forced": fipsForced,
	}, ev.Attributes)
}

func TestRecordSeparationPosture_FirstBootRecordsAndEmits(t *testing.T) {
	ctx := context.Background()
	store, sink := newPostureStore(), &postureSink{}

	changed, err := recordSeparationPosture(ctx, store, sink, false, true)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, PostureStrict, store.recorded(t))
	events := sink.all()
	require.Len(t, events, 1)
	requirePostureEvent(t, events[0], PostureNone, PostureStrict, false)
}

func TestRecordSeparationPosture_UnchangedEmitsNothing(t *testing.T) {
	ctx := context.Background()
	for name, configured := range map[string]bool{"strict": true, "standard": false} {
		t.Run(name, func(t *testing.T) {
			store, sink := newPostureStore(), &postureSink{}
			_, err := recordSeparationPosture(ctx, store, nil, false, configured)
			require.NoError(t, err)
			writes := store.writes

			changed, err := recordSeparationPosture(ctx, store, sink, false, configured)
			require.NoError(t, err)
			assert.False(t, changed)
			assert.Empty(t, sink.all(), "an unchanged posture must emit nothing")
			assert.Equal(t, writes, store.writes, "an unchanged posture must write nothing")
		})
	}
}

func TestRecordSeparationPosture_ChangeEmitsOneEventWithBothValues(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		from, to          bool
		previous, posture string
	}{
		"standard to strict": {from: false, to: true, previous: PostureStandard, posture: PostureStrict},
		"strict to standard": {from: true, to: false, previous: PostureStrict, posture: PostureStandard},
	} {
		t.Run(name, func(t *testing.T) {
			store, sink := newPostureStore(), &postureSink{}
			_, err := recordSeparationPosture(ctx, store, nil, false, tc.from)
			require.NoError(t, err)

			changed, err := recordSeparationPosture(ctx, store, sink, false, tc.to)
			require.NoError(t, err)
			assert.True(t, changed)
			assert.Equal(t, tc.posture, store.recorded(t))

			// The next start under the new posture leaves the one event.
			changed, err = recordSeparationPosture(ctx, store, sink, false, tc.to)
			require.NoError(t, err)
			assert.False(t, changed)
			events := sink.all()
			require.Len(t, events, 1, "a change must leave exactly one event")
			requirePostureEvent(t, events[0], tc.previous, tc.posture, false)
		})
	}
}

func TestRecordSeparationPosture_FIPSForcesStrict(t *testing.T) {
	ctx := context.Background()
	store, sink := newPostureStore(), &postureSink{}
	_, err := recordSeparationPosture(ctx, store, nil, false, false)
	require.NoError(t, err)

	changed, err := recordSeparationPosture(ctx, store, sink, true, false)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, PostureStrict, store.recorded(t))
	events := sink.all()
	require.Len(t, events, 1)
	requirePostureEvent(t, events[0], PostureStandard, PostureStrict, true)
}

// TestRecordSeparationPosture_LostRaceEmitsNothing proves that when another
// node records the same change between this node's read and its write, only
// the winner emits.
func TestRecordSeparationPosture_LostRaceEmitsNothing(t *testing.T) {
	ctx := context.Background()
	for name, seed := range map[string]*bool{"first boot": nil, "change": new(bool)} {
		t.Run(name, func(t *testing.T) {
			store, sink := newPostureStore(), &postureSink{}
			if seed != nil {
				_, err := recordSeparationPosture(ctx, store, nil, false, *seed)
				require.NoError(t, err)
			}
			store.beforeWrite = func() {
				store.beforeWrite = nil
				_, err := recordSeparationPosture(ctx, store, nil, false, true)
				require.NoError(t, err)
			}

			changed, err := recordSeparationPosture(ctx, store, sink, false, true)
			require.NoError(t, err)
			assert.False(t, changed)
			assert.Empty(t, sink.all())
			assert.Equal(t, PostureStrict, store.recorded(t))
		})
	}
}

func TestRecordSeparationPosture_StoreErrorsAreTyped(t *testing.T) {
	ctx := context.Background()
	cause := errors.New("store down")
	for op, tc := range map[string]struct {
		arrange func(*postureStore)
	}{
		"read":   {arrange: func(s *postureStore) { s.getErr = cause }},
		"create": {arrange: func(s *postureStore) { s.putErr = cause }},
		"swap": {arrange: func(s *postureStore) {
			_, err := recordSeparationPosture(ctx, s, nil, false, false)
			require.NoError(t, err)
			s.swapErr = cause
		}},
	} {
		t.Run(op, func(t *testing.T) {
			store, sink := newPostureStore(), &postureSink{}
			tc.arrange(store)

			changed, err := recordSeparationPosture(ctx, store, sink, false, true)
			assert.False(t, changed)
			var postureErr *SeparationPostureError
			require.ErrorAs(t, err, &postureErr)
			assert.Equal(t, op, postureErr.Op)
			assert.ErrorIs(t, err, cause)
			assert.Contains(t, err.Error(), op)
			assert.Empty(t, sink.all())
		})
	}
}

// TestRecordSeparationPosture_ContendedIsTyped proves a write that loses
// every compare-and-set to a node recording a different posture fails with
// a typed error rather than looping or emitting.
func TestRecordSeparationPosture_ContendedIsTyped(t *testing.T) {
	ctx := context.Background()
	store, sink := newPostureStore(), &postureSink{}
	other := 0
	store.beforeWrite = func() {
		other++
		require.NoError(t, store.MemoryKVStore.Put(ctx, SeparationPostureKey, []byte(strconv.Itoa(other))))
	}

	changed, err := recordSeparationPosture(ctx, store, sink, true, true)
	assert.False(t, changed)
	var postureErr *SeparationPostureError
	require.ErrorAs(t, err, &postureErr)
	assert.Equal(t, "contended", postureErr.Op)
	assert.NoError(t, postureErr.Unwrap())
	assert.Equal(t, "auth/wiring: separation-of-duties posture record contended", err.Error())
	assert.Empty(t, sink.all())
}

func TestRecordSeparationPosture_NilSinkRecords(t *testing.T) {
	store := newPostureStore()
	changed, err := RecordSeparationPosture(context.Background(), store, nil, true)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, PostureStrict, store.recorded(t))
}

func TestRecordSeparationPosture_ResolvesLikeTheAuthorizer(t *testing.T) {
	store, sink := newPostureStore(), &postureSink{}
	changed, err := RecordSeparationPosture(context.Background(), store, sink, false)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, postureNames[ResolveStrictSeparation(false)], store.recorded(t))
	require.Len(t, sink.all(), 1)
}
