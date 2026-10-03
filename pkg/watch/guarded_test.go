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
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
)

// guardedOverHub opens a real hub watcher on key and guards it with ended.
func guardedOverHub(t *testing.T, ended <-chan struct{}, cause func() error) (*Hub, Watcher, *Guarded) {
	t.Helper()
	hub := NewHub()
	t.Cleanup(hub.Close)
	inner, err := hub.Watch(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	return hub, inner, NewGuarded(inner, ended, cause)
}

func putK(hub *Hub, rev int64) {
	hub.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), Value: []byte("v"), CreateRevision: rev, ModRevision: rev}, nil)
}

// drainClosed reads ch until it closes, failing after 10s.
func drainClosed(t *testing.T, ch <-chan WatchResponse) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("events channel still open after 10s")
		}
	}
}

// TestGuarded_ForwardsUntilEnded: events pass through until ended closes;
// then the stream closes, Err reports the cause, and the inner watcher is
// closed with it.
func TestGuarded_ForwardsUntilEnded(t *testing.T) {
	ended := make(chan struct{})
	cause := errors.New("sealed")
	hub, inner, g := guardedOverHub(t, ended, func() error { return cause })
	putK(hub, 1)
	select {
	case resp := <-g.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("forwarded %d events, want 1", len(resp.Events))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no event forwarded")
	}
	if err := g.Err(); err != nil {
		t.Fatalf("Err before the end = %v, want nil", err)
	}
	close(ended)
	drainClosed(t, g.Events())
	if err := g.Err(); !errors.Is(err, cause) {
		t.Fatalf("Err after the end = %v, want the cause", err)
	}
	drainClosed(t, inner.Events())
	if g.WatchID() != inner.WatchID() {
		t.Fatalf("WatchID = %d, want the inner watcher's %d", g.WatchID(), inner.WatchID())
	}
}

// TestGuarded_CloseEndsWithoutTheCause: closing the guarded watcher ends the
// stream and the inner watcher, and Err never reports a cause that did not
// happen.
func TestGuarded_CloseEndsWithoutTheCause(t *testing.T) {
	_, inner, g := guardedOverHub(t, make(chan struct{}), func() error {
		t.Error("cause consulted although the watch was not ended by it")
		return nil
	})
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	drainClosed(t, g.Events())
	drainClosed(t, inner.Events())
	if err := g.Err(); err != nil {
		t.Fatalf("Err after Close = %v, want nil", err)
	}
}

// TestGuarded_InnerEndPassesThrough: when the inner watcher ends on its own,
// the guarded stream closes and reports the inner watcher's error.
func TestGuarded_InnerEndPassesThrough(t *testing.T) {
	_, inner, g := guardedOverHub(t, make(chan struct{}), func() error { return errors.New("unused") })
	_ = inner.Close()
	drainClosed(t, g.Events())
	if got, want := g.Err(), inner.Err(); !errors.Is(got, want) && got != want {
		t.Fatalf("Err = %v, want the inner watcher's %v", got, want)
	}
}
