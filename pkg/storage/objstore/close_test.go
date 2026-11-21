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

package objstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objmemory "github.com/jeremyhahn/go-objstore/pkg/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// closingStore is a memory provider that records how often it is released
// and whether it was used after its release. A Get of any key but the format
// marker can be held open, so a test can keep a call in flight.
type closingStore struct {
	objcommon.Storage

	mu            sync.Mutex
	closes        int
	usedAfterFree bool
	hold          chan struct{}
	entered       chan struct{}
}

func newClosingStore() *closingStore {
	return &closingStore{Storage: objmemory.New()}
}

func (s *closingStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++
	return nil
}

func (s *closingStore) Get(key string) (io.ReadCloser, error) {
	s.mu.Lock()
	if s.closes > 0 {
		s.usedAfterFree = true
	}
	hold, entered := s.hold, s.entered
	s.mu.Unlock()
	if hold != nil && key != formatMarkerName {
		entered <- struct{}{}
		<-hold
	}
	return s.Storage.Get(key)
}

func (s *closingStore) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}

func (s *closingStore) freedUse() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usedAfterFree
}

func newClosingEngine(t *testing.T, store, journal *closingStore) *Engine {
	t.Helper()
	require.NoError(t, store.Put(formatMarkerName, strings.NewReader(formatVersion)))
	return &Engine{store: store, journal: journal, provider: "test"}
}

func TestClose_WaitsForAReadInFlightBeforeReleasingTheProvider(t *testing.T) {
	s := newClosingStore()
	e := newClosingEngine(t, s, s)
	s.hold = make(chan struct{})
	s.entered = make(chan struct{}, 1)

	go func() { _, _ = e.Get([]byte("k")) }()
	select {
	case <-s.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the read never reached the provider")
	}

	done := make(chan error, 1)
	go func() { done <- e.Close() }()
	select {
	case <-done:
		t.Fatal("Close returned while a read was in flight")
	default:
	}
	assert.Equal(t, 0, s.closeCount(), "provider released while a read was in flight")

	close(s.hold)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the read finished")
	}
	assert.Equal(t, 1, s.closeCount())
	assert.False(t, s.freedUse(), "provider used after its release")
}

func TestClose_RefusesEveryCallAfterClose(t *testing.T) {
	s := newClosingStore()
	e := newClosingEngine(t, s, s)
	require.NoError(t, e.Close())

	closed := func(name string, err error) {
		t.Helper()
		assert.Truef(t, errors.Is(err, storage.ErrStorageClosed), "%s: got %v", name, err)
	}
	_, err := e.Get([]byte("k"))
	closed("Get", err)
	closed("Set", e.Set([]byte("k"), []byte("v")))
	_, err = e.CompareAndSwap(context.Background(), []byte("k"), nil, []byte("v"))
	closed("CompareAndSwap", err)
	closed("Delete", e.Delete([]byte("k")))
	closed("DeleteRange", e.DeleteRange([]byte("a"), []byte("b")))
	_, err = e.List(nil)
	closed("List", err)
	_, err = e.Exists([]byte("k"))
	closed("Exists", err)
	_, err = e.Snapshot()
	closed("Snapshot", err)
	b := e.Batch()
	_ = b.Set([]byte("k"), []byte("v"))
	closed("Batch.Commit", b.Commit())
	closed("NewIterator", e.NewIterator(nil).Error())
	assert.False(t, s.freedUse(), "provider used after its release")
}

func TestClose_ReleasesEachProviderOnceAndIsIdempotent(t *testing.T) {
	t.Run("shared store and journal", func(t *testing.T) {
		s := newClosingStore()
		e := newClosingEngine(t, s, s)
		require.NoError(t, e.Close())
		assert.Equal(t, 1, s.closeCount())
		require.NoError(t, e.Close())
		assert.Equal(t, 1, s.closeCount())
	})
	t.Run("distinct store and journal", func(t *testing.T) {
		s, j := newClosingStore(), newClosingStore()
		e := newClosingEngine(t, s, j)
		require.NoError(t, e.Close())
		assert.Equal(t, 1, s.closeCount())
		assert.Equal(t, 1, j.closeCount())
		require.NoError(t, e.Close())
		assert.Equal(t, 1, s.closeCount())
		assert.Equal(t, 1, j.closeCount())
	})
}

var badMarkerRuns atomic.Int64

func TestNew_ReleasesTheProvidersOfAFailedOpen(t *testing.T) {
	s := newClosingStore()
	require.NoError(t, s.Put(formatMarkerName, strings.NewReader("not-this-layout")))
	// Registrations are process-wide, so each run (-count) takes a new name.
	name := fmt.Sprintf("close-test-bad-marker-%d", badMarkerRuns.Add(1))
	require.NoError(t, RegisterProvider(name, ProviderCredentials{},
		func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
			return s, nil
		}))

	_, err := New(name, nil)
	var mismatch *FormatVersionMismatchError
	require.True(t, errors.As(err, &mismatch), "got %v", err)
	assert.Equal(t, 1, s.closeCount())
}
