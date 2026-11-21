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
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objmemory "github.com/jeremyhahn/go-objstore/pkg/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// closingStore is a provider that holds a resource: it counts its Close
// calls and records any Get made after one, the use of a released provider.
// When held is set, the next Get signals entered and waits for release
// before it reads.
type closingStore struct {
	objcommon.Storage
	closes        atomic.Int32
	usedAfterFree atomic.Bool
	held          atomic.Bool
	entered       chan struct{}
	release       chan struct{}
}

func newClosingStore() *closingStore {
	return &closingStore{
		Storage: objmemory.New(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *closingStore) Get(name string) (io.ReadCloser, error) {
	if c.held.CompareAndSwap(true, false) {
		close(c.entered)
		<-c.release
	}
	if c.closes.Load() > 0 {
		c.usedAfterFree.Store(true)
	}
	return c.Storage.Get(name)
}

func (c *closingStore) Close() error {
	c.closes.Add(1)
	return nil
}

// newClosingEngine builds an engine over one closingStore, as New builds a
// "memory" engine, holding the key "k".
func newClosingEngine(t *testing.T) (*Engine, *closingStore) {
	t.Helper()
	cs := newClosingStore()
	e := &Engine{store: cs, journal: cs, provider: "memory"}
	require.NoError(t, e.ensureFormatMarker())
	require.NoError(t, e.Set([]byte("k"), []byte("v")))
	return e, cs
}

func TestClose_WaitsForAReadInFlightBeforeReleasingTheProvider(t *testing.T) {
	e, cs := newClosingEngine(t)
	cs.held.Store(true)

	type result struct {
		value []byte
		err   error
	}
	read := make(chan result, 1)
	go func() {
		v, err := e.Get([]byte("k"))
		read <- result{v, err}
	}()
	<-cs.entered // the read is inside the provider

	closed := make(chan error, 1)
	go func() { closed <- e.Close() }()
	for !e.closed.Load() {
		runtime.Gosched()
	}

	// Close has begun: a new call is refused, and the provider the read is
	// using is not released under it.
	_, err := e.Get([]byte("k"))
	var closedErr *storage.ClosedError
	require.ErrorAs(t, err, &closedErr)
	assert.Zero(t, cs.closes.Load(), "Close released the provider under a read in flight")

	close(cs.release)
	r := <-read
	require.NoError(t, r.err)
	assert.Equal(t, []byte("v"), r.value)
	require.NoError(t, <-closed)
	assert.Equal(t, int32(1), cs.closes.Load(), "Close releases the provider once the read is done")
	assert.False(t, cs.usedAfterFree.Load(), "no call used the released provider")

	require.NoError(t, e.Close())
	assert.Equal(t, int32(1), cs.closes.Load(), "a second Close releases nothing again")
}

func TestClose_RefusesACallThatPassedTheClosedCheckBeforeClose(t *testing.T) {
	e, cs := newClosingEngine(t)
	gap := make(chan struct{})
	resume := make(chan struct{})
	e.beforeEnter = func() {
		close(gap)
		<-resume
	}

	read := make(chan error, 1)
	go func() {
		_, err := e.Get([]byte("k"))
		read <- err
	}()
	<-gap // the read passed the first closed check and has not entered

	require.NoError(t, e.Close())
	assert.Equal(t, int32(1), cs.closes.Load())

	close(resume)
	var closedErr *storage.ClosedError
	require.ErrorAs(t, <-read, &closedErr, "the recheck refuses the read")
	assert.False(t, cs.usedAfterFree.Load(), "the read never reached the released provider")
}

func TestClose_EveryCallWaitsAndIsRefusedAfter(t *testing.T) {
	e, cs := newClosingEngine(t)
	require.NoError(t, e.Close())
	var closedErr *storage.ClosedError
	_, err := e.Get([]byte("k"))
	assert.ErrorAs(t, err, &closedErr)
	_, err = e.Exists([]byte("k"))
	assert.ErrorAs(t, err, &closedErr)
	_, err = e.List(nil)
	assert.ErrorAs(t, err, &closedErr)
	_, err = e.Snapshot()
	assert.ErrorAs(t, err, &closedErr)
	assert.ErrorAs(t, e.Set([]byte("k"), []byte("w")), &closedErr)
	assert.ErrorAs(t, e.Delete([]byte("k")), &closedErr)
	assert.ErrorAs(t, e.commitBufferedOps([]storage.BufferedOp{{Key: []byte("k"), Value: []byte("w")}}), &closedErr)
	assert.False(t, cs.usedAfterFree.Load(), "no call used the released provider")
}

func TestNew_ReleasesTheProviderOfAFailedOpenAndOnClose(t *testing.T) {
	var stores []*closingStore
	creator := func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
		cs := newClosingStore()
		stores = append(stores, cs)
		return cs, nil
	}
	require.NoError(t, RegisterProvider("close-test-provider", ProviderCredentials{}, creator))

	// An object that is not the format marker fails the open: the store is
	// not one this engine wrote.
	failing := func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
		cs := newClosingStore()
		if err := cs.Put("stray", strings.NewReader("x")); err != nil {
			return nil, err
		}
		stores = append(stores, cs)
		return cs, nil
	}
	require.NoError(t, RegisterProvider("close-test-failed-open", ProviderCredentials{}, failing))

	e, err := New("close-test-failed-open", map[string]string{})
	require.Nil(t, e)
	var missing *FormatMarkerMissingError
	require.ErrorAs(t, err, &missing)
	require.Len(t, stores, 1)
	assert.Equal(t, int32(1), stores[0].closes.Load(), "a failed open releases the provider it created")

	stores = nil
	e, err = New("close-test-provider", map[string]string{})
	require.NoError(t, err)
	require.Len(t, stores, 1)
	assert.Zero(t, stores[0].closes.Load(), "an open engine keeps its provider")
	require.NoError(t, e.Close())
	assert.Equal(t, int32(1), stores[0].closes.Load(), "Close releases the provider")
}
