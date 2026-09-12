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

package server

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ===========================================================================
// The namespacing wrapper is the only production consumer of the entity watch,
// so it is where a terminal event either survives or is lost. These tests run
// the WHOLE chain — real watch.Hub, real service.WatchService, real wrapper —
// because the fail-open they guard against lives in the seam between the last
// two: a terminal event carries no entity ID, so the type-level watch's
// namespace filter drops it on the floor and the consumer is back to an
// ordinary closed channel.
// ===========================================================================

// namespaceTestPrefix is the composite (tenant, database) namespace the wrapper
// scopes to in these tests.
const namespaceTestPrefix = "t1/db1/"

// namespacedEntityWatch builds a live hub and the namespaced watch service over
// the real WatchService. The hub is closed at test end.
func namespacedEntityWatch(t *testing.T) (*watch.Hub, *namespacedWatchService) {
	t.Helper()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	return hub, newNamespacedWatchService(service.NewWatchService(hub), namespaceTestPrefix)
}

// floodNamespacedUntilEvicted publishes live events for key until the hub has
// evicted every watcher. Nothing reads the entity channel, so the relay and the
// wrapper's pump both park on a handover and further events pile up in the
// watcher's buffer until the hub drops it for overflow.
func floodNamespacedUntilEvicted(hub *watch.Hub, key string) {
	const maxEvents = 1 << 16
	for i := 0; i < maxEvents && hub.WatchCount() > 0; i++ {
		notifyNamespacedPut(hub, key, int64(i+1))
	}
}

// notifyNamespacedPut publishes one live put event for key at revision rev.
func notifyNamespacedPut(hub *watch.Hub, key string, rev int64) {
	hub.NotifyPut([]byte(key), &kv.KeyValue{
		Key:            []byte(key),
		Value:          []byte("v"),
		CreateRevision: rev,
		ModRevision:    rev,
	}, nil)
}

// awaitHubEviction waits for the hub to drop every watcher.
func awaitHubEviction(t *testing.T, hub *watch.Hub) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for hub.WatchCount() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("hub still holds %d watchers; eviction never happened", hub.WatchCount())
		}
		time.Sleep(time.Millisecond)
	}
}

// drainNamespacedEntityEvents reads ch to close and returns everything it
// delivered.
func drainNamespacedEntityEvents(t *testing.T, ch <-chan service.WatchEntityEvent) []service.WatchEntityEvent {
	t.Helper()
	var got []service.WatchEntityEvent
	timeout := time.After(30 * time.Second)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, evt)
		case <-timeout:
			t.Fatalf("timed out draining the namespaced entity watch after %d events", len(got))
			return got
		}
	}
}

// assertNamespacedOverflowTerminal asserts got ends with the terminal event
// naming an overflow eviction.
func assertNamespacedOverflowTerminal(t *testing.T, got []service.WatchEntityEvent) {
	t.Helper()

	if len(got) == 0 {
		t.Fatal("the namespaced entity watch delivered nothing at all")
	}
	last := got[len(got)-1]
	if !last.Canceled {
		t.Fatalf("an evicted entity watch reached the namespaced consumer without a terminal event; last event was %+v", last)
	}
	if last.CancelReason != watch.ErrWatcherOverflow.Error() {
		t.Fatalf("CancelReason = %q, want the watcher error verbatim: %q", last.CancelReason, watch.ErrWatcherOverflow.Error())
	}
}

// TestNamespacedWatchService_WatchEntity_ForwardsTheTerminalEvent asserts a
// hub-side eviction survives the namespacing wrapper on a single-entity watch.
func TestNamespacedWatchService_WatchEntity_ForwardsTheTerminalEvent(t *testing.T) {
	hub, svc := namespacedEntityWatch(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntity(ctx, &service.WatchEntityRequest{EntityType: "user", ID: "u1"})
	if err != nil {
		t.Fatalf("WatchEntity: %v", err)
	}

	floodNamespacedUntilEvicted(hub, "user/"+namespaceTestPrefix+"u1")
	awaitHubEviction(t, hub)

	assertNamespacedOverflowTerminal(t, drainNamespacedEntityEvents(t, ch))
}

// TestNamespacedWatchService_WatchEntityType_ForwardsTheTerminalEvent is the
// case the conversion exists for. A type-level watch spans the store, so the
// wrapper drops every event whose ID falls outside the namespace — and the
// terminal event carries no ID at all, so an unconverted filter discards it and
// the consumer sees the eviction as a clean close even though the service layer
// reported it correctly.
func TestNamespacedWatchService_WatchEntityType_ForwardsTheTerminalEvent(t *testing.T) {
	hub, svc := namespacedEntityWatch(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntityType(ctx, &service.WatchEntityTypeRequest{EntityType: "order"})
	if err != nil {
		t.Fatalf("WatchEntityType: %v", err)
	}

	floodNamespacedUntilEvicted(hub, "order/"+namespaceTestPrefix+"o1")
	awaitHubEviction(t, hub)

	got := drainNamespacedEntityEvents(t, ch)
	assertNamespacedOverflowTerminal(t, got)

	// The namespace stripping still applies to the events before it.
	for _, evt := range got[:len(got)-1] {
		if evt.ID != "o1" {
			t.Fatalf("event ID = %q, want the namespace stripped to %q", evt.ID, "o1")
		}
	}
}

// TestNamespacedWatchService_WatchEntityType_FiltersForeignNamespaces asserts
// the conversion did not turn the filter off: an entity belonging to another
// database stays invisible, while the terminal event — which belongs to the
// stream itself rather than to any entity — is the one thing that passes
// unfiltered.
func TestNamespacedWatchService_WatchEntityType_FiltersForeignNamespaces(t *testing.T) {
	hub, svc := namespacedEntityWatch(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntityType(ctx, &service.WatchEntityTypeRequest{EntityType: "order"})
	if err != nil {
		t.Fatalf("WatchEntityType: %v", err)
	}

	// Both land in the watcher's buffer in this order, so a delivery of the
	// second proves the first was dropped rather than merely delayed.
	notifyNamespacedPut(hub, "order/t2/db9/o1", 1)
	notifyNamespacedPut(hub, "order/"+namespaceTestPrefix+"o1", 2)

	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("the watch closed before delivering the in-namespace event")
		}
		if evt.ID != "o1" || evt.Revision != 2 {
			t.Fatalf("delivered %+v, want the in-namespace event at revision 2", evt)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for the in-namespace event")
	}

	hub.Close()

	for _, evt := range drainNamespacedEntityEvents(t, ch) {
		if evt.Canceled {
			t.Fatalf("a clean end delivered a terminal event: %+v", evt)
		}
		if evt.ID != "o1" {
			t.Fatalf("an entity from another namespace was delivered: %+v", evt)
		}
	}
}

// TestNamespacedWatchService_WatchEntity_CleanEndCarriesNoTerminalEvent is the
// control on the wrapper: a watch that ended cleanly must not acquire a
// terminal event on its way through. It passes against the unconverted pump
// too, and guards the wrong fix — a wrapper that manufactures a terminal event
// of its own when the inner channel closes.
func TestNamespacedWatchService_WatchEntity_CleanEndCarriesNoTerminalEvent(t *testing.T) {
	hub, svc := namespacedEntityWatch(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntity(ctx, &service.WatchEntityRequest{EntityType: "user", ID: "u1"})
	if err != nil {
		t.Fatalf("WatchEntity: %v", err)
	}

	hub.Close()

	for _, evt := range drainNamespacedEntityEvents(t, ch) {
		if evt.Canceled || evt.CancelReason != "" {
			t.Fatalf("a clean end delivered a terminal event: %+v", evt)
		}
	}
}
