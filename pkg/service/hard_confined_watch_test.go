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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// fakeWatcher is a minimal watch.Watcher double that lets a test push
// events directly onto its channel.
type fakeWatcher struct {
	events  chan watch.WatchResponse
	closed  bool
	closeCh chan struct{}
}

func newFakeWatcher() *fakeWatcher {
	return &fakeWatcher{events: make(chan watch.WatchResponse, 8), closeCh: make(chan struct{})}
}

func (w *fakeWatcher) Events() <-chan watch.WatchResponse { return w.events }
func (w *fakeWatcher) WatchID() int64                     { return 1 }
func (w *fakeWatcher) Close() error {
	if !w.closed {
		w.closed = true
		close(w.events)
	}
	return nil
}
func (w *fakeWatcher) Err() error { return nil }

// fakeWatch is a minimal WatchServicer double recording whether Watch /
// WatchPrefix was reached, so a refusal can be proven to have
// short-circuited BEFORE a watcher was ever created, and returning a
// fakeWatcher a test can push synthetic events onto for WatchPrefix.
type fakeWatch struct {
	WatchServicer
	calledWatch       bool
	calledWatchPrefix bool
	prefixWatcher     *fakeWatcher
}

func (f *fakeWatch) Watch(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	f.calledWatch = true
	return nil, nil
}

func (f *fakeWatch) WatchPrefix(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
	f.calledWatchPrefix = true
	f.prefixWatcher = newFakeWatcher()
	return f.prefixWatcher, nil
}

func TestNewHardConfinedWatch_NilServicePassesThroughNil(t *testing.T) {
	if got := NewHardConfinedWatch(nil); got != nil {
		t.Fatalf("NewHardConfinedWatch(nil) = %v, want nil", got)
	}
}

func TestHardConfinedWatch_RefusesTheConfinedKey(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)

	if _, err := w.Watch(ctx, confinedKey); err == nil {
		t.Fatal("Watch(confinedKey) succeeded, want a refusal")
	} else {
		var confErr *HardConfinedKeyError
		if !errors.As(err, &confErr) {
			t.Fatalf("error = %v (%T), want *HardConfinedKeyError", err, err)
		}
	}
	if fake.calledWatch {
		t.Fatal("Watch delegated to the underlying servicer despite the refusal; a watcher must never be created")
	}
}

func TestHardConfinedWatch_OrdinaryKeyPassesThrough(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)

	if _, err := w.Watch(ctx, "ordinary/key"); err != nil {
		t.Fatalf("Watch(ordinary key): %v", err)
	}
	if !fake.calledWatch {
		t.Fatal("Watch did not reach the underlying servicer for an ordinary key")
	}
}

// TestHardConfinedWatch_WatchPrefixFiltersConfinedEventsFromAWideOpenSweep
// is B2's Watch regression proof: a prefix watch spanning the whole
// keyspace ("" or "__sys:", the reviewer's own probe shape) must not be
// refused outright -- that would break a legitimate broad watch, the same
// false positive go-qrdb#555's registry-level attempt made for Range --
// and must never deliver an event naming the confined key, from any
// revision.
func TestHardConfinedWatch_WatchPrefixFiltersConfinedEventsFromAWideOpenSweep(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)

	watcher, err := w.WatchPrefix(ctx, "")
	if err != nil {
		t.Fatalf("WatchPrefix(\"\"): %v", err)
	}
	if !fake.calledWatchPrefix {
		t.Fatal("WatchPrefix(\"\") did not reach the underlying servicer; a wide-open watch must be filtered, not refused")
	}

	fake.prefixWatcher.events <- watch.WatchResponse{
		WatchID:  1,
		Revision: 1,
		Events: []*watch.Event{
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte(confinedKey), Value: []byte("the cluster CA's private key")}},
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte("ordinary/a"), Value: []byte("1")}},
		},
	}
	fake.prefixWatcher.Close()

	select {
	case resp, ok := <-watcher.Events():
		if !ok {
			t.Fatal("Events() closed before delivering the one response pushed")
		}
		if len(resp.Events) != 1 || string(resp.Events[0].Kv.Key) != "ordinary/a" {
			t.Fatalf("WatchPrefix delivered %v, want exactly the non-confined event", resp.Events)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the filtered watch event")
	}

	if err := watcher.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestHardConfinedWatch_WatchPrefixFiltersADeleteEventsPrevKv proves a
// delete event -- which carries its key only in PrevKv, Kv being the
// post-event (absent) state -- is also filtered.
func TestHardConfinedWatch_WatchPrefixFiltersADeleteEventsPrevKv(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)

	watcher, err := w.WatchPrefix(ctx, InternalKeyPrefixSystem)
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}

	fake.prefixWatcher.events <- watch.WatchResponse{
		WatchID:  1,
		Revision: 2,
		Events: []*watch.Event{
			{Type: watch.EventDelete, PrevKv: &kv.KeyValue{Key: []byte(confinedKey), Value: []byte("gone now")}},
		},
	}
	fake.prefixWatcher.Close()

	select {
	case resp, ok := <-watcher.Events():
		if ok && len(resp.Events) != 0 {
			t.Fatalf("WatchPrefix delivered a delete event for the confined key: %v", resp.Events)
		}
	case <-time.After(time.Second):
		// The pump drops the response's entire Events slice to empty and
		// still forwards it (not drop the response wholesale), so either
		// an empty-Events response or the channel closing without one is
		// acceptable; a timeout here would mean the pump wedged, which is
		// the real failure this guards against.
		t.Fatal("timed out waiting for the filtered (now-empty) watch response")
	}

	if err := watcher.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestHardConfinedWatch_WatchPrefixOnAnOrdinaryPrefixIsNotWrapped(t *testing.T) {
	ctx := context.Background()
	fake := &fakeWatch{}
	w := NewHardConfinedWatch(fake)

	watcher, err := w.WatchPrefix(ctx, "ordinary/")
	if err != nil {
		t.Fatalf("WatchPrefix(ordinary prefix): %v", err)
	}
	if !fake.calledWatchPrefix {
		t.Fatal("WatchPrefix did not reach the underlying servicer for an ordinary prefix")
	}
	// An ordinary prefix that cannot overlap the confined namespace gets
	// the RAW watcher back, unwrapped -- proven here by identity.
	if watcher != fake.prefixWatcher {
		t.Fatal("WatchPrefix wrapped a watcher for a prefix that cannot overlap the confined namespace")
	}
}
