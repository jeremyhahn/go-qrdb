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

package statemachine

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
)

// pausingBackend is a ConcurrentBackend that stops in the middle of an
// applied batch: its Put of pauseKey announces itself on paused and waits
// for release before writing.
type pausingBackend struct {
	*ConcurrentBackend
	pauseKey string
	paused   chan struct{}
	release  chan struct{}
}

func (b *pausingBackend) Put(key, value []byte) error {
	if string(key) == b.pauseKey {
		close(b.paused)
		<-b.release
	}
	return b.ConcurrentBackend.Put(key, value)
}

// putEntry is a committed kv.OpPut entry for key and value.
func putEntry(index uint64, key, value string) sm.Entry {
	cmd := make([]byte, 0, 3+len(key)+len(value))
	cmd = append(cmd, kv.OpPut)
	cmd = binary.LittleEndian.AppendUint16(cmd, uint16(len(key)))
	cmd = append(cmd, key...)
	cmd = append(cmd, value...)
	return sm.Entry{Index: index, Cmd: cmd}
}

// TestKVStateMachine_GetHashSeesWholeBatches: go-quicraft's concurrent tier
// calls GetHash while Update applies a batch, and replicas compare the hash
// to detect divergence. A hash taken halfway through a batch is of a state no
// replica settles in, so GetHash waits for the batch and hashes it whole.
func TestKVStateMachine_GetHashSeesWholeBatches(t *testing.T) {
	backend := &pausingBackend{ConcurrentBackend: NewConcurrentBackend(), pauseKey: "k2", paused: make(chan struct{}), release: make(chan struct{})}
	k := NewKVStateMachine(backend)
	batch := []sm.Entry{putEntry(1, "k1", "v1"), putEntry(2, "k2", "v2")}

	applied := make(chan error, 1)
	go func() { applied <- k.Update(batch, make([]sm.Result, len(batch))) }()
	<-backend.paused

	hashed := make(chan uint64, 1)
	go func() {
		h, err := k.GetHash()
		if err != nil {
			t.Errorf("GetHash: %v", err)
		}
		hashed <- h
	}()
	select {
	case h := <-hashed:
		close(backend.release)
		<-applied
		t.Fatalf("GetHash returned %d halfway through a batch; it must wait for the whole batch", h)
	case <-time.After(100 * time.Millisecond):
	}
	close(backend.release)
	if err := <-applied; err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := <-hashed

	whole := NewKVStateMachine(NewConcurrentBackend())
	if err := whole.Update(batch, make([]sm.Result, len(batch))); err != nil {
		t.Fatalf("Update: %v", err)
	}
	want, err := whole.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	if got != want {
		t.Fatalf("GetHash taken across a batch = %d, want the whole batch's %d", got, want)
	}
}

// pausingWatch is a WatchManager that stops Update after its notification
// for pauseKey: announced on paused, held until release.
type pausingWatch struct {
	pauseKey string
	paused   chan struct{}
	release  chan struct{}
}

func (w *pausingWatch) Notify(key string, _ []byte, _ uint64, _ bool) {
	if key == w.pauseKey {
		close(w.paused)
		<-w.release
	}
}

// TestConcurrentStateMachine_GetHashSeesWholeBatches is
// TestKVStateMachine_GetHashSeesWholeBatches for the concurrent state
// machine, which go-quicraft hashes the same way while it applies.
func TestConcurrentStateMachine_GetHashSeesWholeBatches(t *testing.T) {
	watch := &pausingWatch{pauseKey: "k1", paused: make(chan struct{}), release: make(chan struct{})}
	csm, _ := newTestConcurrentSM(WithConcurrentWatchManager(watch))
	batch := []sm.Entry{putEntry(1, "k1", "v1"), putEntry(2, "k2", "v2")}

	applied := make(chan error, 1)
	go func() { applied <- csm.Update(context.Background(), batch, make([]sm.Result, len(batch))) }()
	<-watch.paused

	hashed := make(chan uint64, 1)
	go func() {
		h, err := csm.GetHash()
		if err != nil {
			t.Errorf("GetHash: %v", err)
		}
		hashed <- h
	}()
	select {
	case h := <-hashed:
		close(watch.release)
		<-applied
		t.Fatalf("GetHash returned %d halfway through a batch; it must wait for the whole batch", h)
	case <-time.After(100 * time.Millisecond):
	}
	close(watch.release)
	if err := <-applied; err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := <-hashed

	whole, _ := newTestConcurrentSM()
	if err := whole.Update(context.Background(), batch, make([]sm.Result, len(batch))); err != nil {
		t.Fatalf("Update: %v", err)
	}
	want, err := whole.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	if got != want {
		t.Fatalf("GetHash taken across a batch = %d, want the whole batch's %d", got, want)
	}
}

// TestKVStateMachine_LookupDoesNotWaitForABatch: only GetHash waits for a
// batch; a read still runs while Update applies one, which is what the
// concurrent tier is for.
func TestKVStateMachine_LookupDoesNotWaitForABatch(t *testing.T) {
	backend := &pausingBackend{ConcurrentBackend: NewConcurrentBackend(), pauseKey: "k2", paused: make(chan struct{}), release: make(chan struct{})}
	k := NewKVStateMachine(backend)
	batch := []sm.Entry{putEntry(1, "k1", "v1"), putEntry(2, "k2", "v2")}
	applied := make(chan error, 1)
	go func() { applied <- k.Update(batch, make([]sm.Result, len(batch))) }()
	<-backend.paused
	defer func() {
		close(backend.release)
		<-applied
	}()

	got, err := backend.Get([]byte("k1"))
	if err != nil || string(got) != "v1" {
		t.Fatalf("a read during a batch = %q, %v; want the applied k1", got, err)
	}
}
