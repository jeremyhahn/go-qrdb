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

// ===========================================================================
// Entity-watch termination against the REAL hub
//
// These tests deliberately do NOT use mockWatchHub/mockWatcher. That double
// hands back a watcher whose Err() is whatever the test set and whose channel
// the test closes by hand, so it can assert only that the relay reads a field
// the test itself populated. The subject here is what the relay does when a
// REAL hub ends a watch for a real reason, so a real hub is what it runs
// against: the eviction, the error and the channel close are all the
// production ones.
// ===========================================================================

// entityWatchHub builds a live hub registered for cleanup and the WatchService
// over it.
func entityWatchHub(t *testing.T) (*watch.Hub, WatchServicer) {
	t.Helper()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	return hub, NewWatchService(hub)
}

// notifyEntityPut publishes one live put event for key at revision rev.
func notifyEntityPut(hub *watch.Hub, key string, rev int64) {
	hub.NotifyPut([]byte(key), &kv.KeyValue{
		Key:            []byte(key),
		Value:          []byte("v"),
		CreateRevision: rev,
		ModRevision:    rev,
	}, nil)
}

// awaitEviction waits for the hub to drop every watcher, which is how the
// eviction the test provoked becomes observable from outside.
func awaitEviction(t *testing.T, hub *watch.Hub) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for hub.WatchCount() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("hub still holds %d watchers; eviction never happened", hub.WatchCount())
		}
		time.Sleep(time.Millisecond)
	}
}

// drainEntityEvents reads ch to close and returns every event it delivered.
func drainEntityEvents(t *testing.T, ch <-chan WatchEntityEvent) []WatchEntityEvent {
	t.Helper()
	var got []WatchEntityEvent
	timeout := time.After(30 * time.Second)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, evt)
		case <-timeout:
			t.Fatalf("timed out draining the entity watch channel after %d events", len(got))
			return got
		}
	}
}

// floodUntilEvicted publishes live events for key until the hub has evicted
// every watcher. Nothing reads the entity channel, so the relay parks on its
// first handover past the channel buffer and every further event piles up in
// the watcher's own buffer until the hub drops it for overflow. The bound is
// generous and independent of the buffer sizes; the loop stops as soon as the
// eviction lands.
func floodUntilEvicted(hub *watch.Hub, key string) {
	const maxEvents = 1 << 16
	for i := 0; i < maxEvents && hub.WatchCount() > 0; i++ {
		notifyEntityPut(hub, key, int64(i+1))
	}
}

// terminalOf returns the last event of got, failing when got is empty.
func terminalOf(t *testing.T, got []WatchEntityEvent) WatchEntityEvent {
	t.Helper()
	if len(got) == 0 {
		t.Fatal("the entity watch delivered nothing at all")
	}
	return got[len(got)-1]
}

// assertOverflowTerminal asserts got ends with the terminal event naming an
// overflow eviction, and that nothing before it claims to be terminal.
func assertOverflowTerminal(t *testing.T, got []WatchEntityEvent, entityType EntityType) {
	t.Helper()

	last := terminalOf(t, got)
	if !last.Canceled {
		t.Fatalf("an evicted entity watch closed without a terminal event; last event was %+v", last)
	}
	if last.CancelReason != watch.ErrWatcherOverflow.Error() {
		t.Fatalf("CancelReason = %q, want the watcher error verbatim: %q", last.CancelReason, watch.ErrWatcherOverflow.Error())
	}
	if last.EntityType != entityType {
		t.Errorf("terminal event EntityType = %q, want %q", last.EntityType, entityType)
	}
	for i, evt := range got[:len(got)-1] {
		if evt.Canceled {
			t.Fatalf("event %d of %d is marked Canceled but is not the last: %+v", i, len(got), evt)
		}
	}
}

// ---------------------------------------------------------------------------
// Overflow eviction — no doubles at all
// ---------------------------------------------------------------------------

// TestWatchService_WatchEntity_OverflowEvictionCarriesItsReason drives a real
// overflow eviction and asserts the consumer receives a terminal event naming
// it rather than a bare closed channel. Red against a relay that returns on
// channel close without reading the watcher's Err().
func TestWatchService_WatchEntity_OverflowEvictionCarriesItsReason(t *testing.T) {
	hub, svc := entityWatchHub(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntity(ctx, &WatchEntityRequest{EntityType: "user", ID: "u1"})
	if err != nil {
		t.Fatalf("WatchEntity: %v", err)
	}

	floodUntilEvicted(hub, "user/u1")
	awaitEviction(t, hub)

	assertOverflowTerminal(t, drainEntityEvents(t, ch), "user")
}

// TestWatchService_WatchEntityType_OverflowEvictionCarriesItsReason is the same
// assertion for the type-level watch, which opens its watcher through the hub's
// prefix method and shares the same relay goroutine.
func TestWatchService_WatchEntityType_OverflowEvictionCarriesItsReason(t *testing.T) {
	hub, svc := entityWatchHub(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntityType(ctx, &WatchEntityTypeRequest{EntityType: "order"})
	if err != nil {
		t.Fatalf("WatchEntityType: %v", err)
	}

	floodUntilEvicted(hub, "order/o1")
	awaitEviction(t, hub)

	assertOverflowTerminal(t, drainEntityEvents(t, ch), "order")
}

// ---------------------------------------------------------------------------
// A compaction that outran the replay
// ---------------------------------------------------------------------------

// stubHistory is a watch.HistoryReplayer whose replay fails, which is how the
// hub reaches its compaction-raced-the-replay eviction. It stands in for the
// MVCC store adapter, not for the watcher: the hub, the watcher and the service
// under test are all the production ones.
type stubHistory struct {
	current   int64
	compacted int64
	replayErr error
}

func (s *stubHistory) CurrentRevision() int64              { return s.current }
func (s *stubHistory) CompactedRevision(_, _ []byte) int64 { return s.compacted }

func (s *stubHistory) Replay(_, _ int64, _ func([]byte) bool, _ bool, _ func(*watch.Event) bool) error {
	return s.replayErr
}

var _ watch.HistoryReplayer = (*stubHistory)(nil)

// assertCompactedTerminal asserts got ends with the terminal event carrying the
// compaction reason verbatim. Verbatim is the payoff: the reason text is what
// transport-level classification reads, so a compacted entity replay names
// itself with exactly the string every wire transport puts on the wire.
func assertCompactedTerminal(t *testing.T, got []WatchEntityEvent) {
	t.Helper()

	last := terminalOf(t, got)
	if !last.Canceled {
		t.Fatalf("a failed replay closed without a terminal event; last event was %+v", last)
	}
	if last.CancelReason != watch.ErrCompacted.Error() {
		t.Fatalf("CancelReason = %q, want the watcher error verbatim: %q", last.CancelReason, watch.ErrCompacted.Error())
	}
}

// TestWatchService_WatchEntity_CompactedReplayCarriesItsReason asserts a
// compaction that outran an in-progress replay reaches the entity consumer as a
// named terminal event. Red against a relay that never reads Err().
func TestWatchService_WatchEntity_CompactedReplayCarriesItsReason(t *testing.T) {
	hub, svc := entityWatchHub(t)
	hub.SetHistory(&stubHistory{
		current:   10,
		replayErr: errors.New("history scan hit a compacted value"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntity(ctx, &WatchEntityRequest{EntityType: "user", ID: "u1", StartRevision: 5})
	if err != nil {
		t.Fatalf("WatchEntity: %v", err)
	}

	assertCompactedTerminal(t, drainEntityEvents(t, ch))
}

// TestWatchService_WatchEntityType_CompactedReplayCarriesItsReason is the same
// assertion for the type-level watch.
func TestWatchService_WatchEntityType_CompactedReplayCarriesItsReason(t *testing.T) {
	hub, svc := entityWatchHub(t)
	hub.SetHistory(&stubHistory{
		current:   10,
		replayErr: errors.New("history scan hit a compacted value"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntityType(ctx, &WatchEntityTypeRequest{EntityType: "order", StartRevision: 5})
	if err != nil {
		t.Fatalf("WatchEntityType: %v", err)
	}

	assertCompactedTerminal(t, drainEntityEvents(t, ch))
}

// ---------------------------------------------------------------------------
// The other half: a clean end says nothing
// ---------------------------------------------------------------------------

// assertNoTerminal asserts no event in got claims to be terminal.
func assertNoTerminal(t *testing.T, got []WatchEntityEvent) {
	t.Helper()
	for _, evt := range got {
		if evt.Canceled {
			t.Fatalf("a clean end delivered a terminal event: %+v", evt)
		}
		if evt.CancelReason != "" {
			t.Fatalf("a clean end delivered an event carrying a reason: %+v", evt)
		}
	}
}

// TestWatchService_WatchEntity_CleanEndCarriesNoTerminalEvent is the other half
// of the contract: the terminal event has to MEAN something, so a watch that
// ended with no reason recorded must not carry one. A hub close cancels its
// watchers without recording an error, which is the clean end.
//
// It passes against the unfixed relay too, and is here BECAUSE of that: it is
// the guard on the obvious wrong fix — a terminal event emitted unconditionally
// on channel close — which would make "ended cleanly" and "ended for a reason"
// indistinguishable again one level up. It is a control, not the proof; the
// eviction tests above are the proof.
func TestWatchService_WatchEntity_CleanEndCarriesNoTerminalEvent(t *testing.T) {
	hub, svc := entityWatchHub(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntity(ctx, &WatchEntityRequest{EntityType: "user", ID: "u1"})
	if err != nil {
		t.Fatalf("WatchEntity: %v", err)
	}

	notifyEntityPut(hub, "user/u1", 1)
	first := <-ch
	if first.Canceled {
		t.Fatalf("a live event arrived marked Canceled: %+v", first)
	}

	hub.Close()

	assertNoTerminal(t, drainEntityEvents(t, ch))
}

// TestWatchService_WatchEntityType_CleanEndCarriesNoTerminalEvent is the same
// control for the type-level watch.
func TestWatchService_WatchEntityType_CleanEndCarriesNoTerminalEvent(t *testing.T) {
	hub, svc := entityWatchHub(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := svc.WatchEntityType(ctx, &WatchEntityTypeRequest{EntityType: "order"})
	if err != nil {
		t.Fatalf("WatchEntityType: %v", err)
	}

	notifyEntityPut(hub, "order/o1", 1)
	first := <-ch
	if first.Canceled {
		t.Fatalf("a live event arrived marked Canceled: %+v", first)
	}

	hub.Close()

	assertNoTerminal(t, drainEntityEvents(t, ch))
}

// ---------------------------------------------------------------------------
// In-band cancellation from the hub implementation
// ---------------------------------------------------------------------------

// TestWatchService_WatchEntity_InBandCancellationEndsTheStream covers the other
// terminal shape. WatchHub is an injected interface — pkg/server/lifecycle and
// pkg/server/tenant_host both hand the service a hub from outside — and a hub
// that fronts a remote stream reports a cancellation the way every wire client
// does: in-band on the response, not by recording an error and closing. A relay
// that only reads Err() drops that, which is the same fail-open one layer out.
//
// This is the one terminal case a real hub cannot produce, because the
// in-process hub never sets Canceled itself; the watcher double is what stands
// in for a hub that does. It keeps its event channel OPEN after the terminal
// response, so a relay that ignored the frame would hang here rather than fail.
func TestWatchService_WatchEntity_InBandCancellationEndsTheStream(t *testing.T) {
	events := make(chan watch.WatchResponse, 1)
	events <- watch.WatchResponse{WatchID: 7, Canceled: true, CancelReason: "hub evicted the watcher"}

	hub := &mockWatchHub{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{eventsCh: events, watchID: 7}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := NewWatchService(hub).WatchEntity(ctx, &WatchEntityRequest{EntityType: "user", ID: "u1"})
	if err != nil {
		t.Fatalf("WatchEntity: %v", err)
	}

	got := drainEntityEvents(t, ch)
	if len(got) != 1 {
		t.Fatalf("delivered %d events, want exactly the terminal one: %+v", len(got), got)
	}
	if !got[0].Canceled || got[0].CancelReason != "hub evicted the watcher" {
		t.Fatalf("terminal event = %+v", got[0])
	}
	if got[0].EntityType != "user" {
		t.Errorf("terminal event EntityType = %q, want %q", got[0].EntityType, "user")
	}
}

// TestWatchService_WatchEntityType_InBandCancellationEndsTheStream is the same
// assertion for the type-level watch, which opens through WatchPrefix.
func TestWatchService_WatchEntityType_InBandCancellationEndsTheStream(t *testing.T) {
	events := make(chan watch.WatchResponse, 1)
	events <- watch.WatchResponse{WatchID: 9, Canceled: true, CancelReason: "server shutting down"}

	hub := &mockWatchHub{
		watchPrefixFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return &mockWatcher{eventsCh: events, watchID: 9}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := NewWatchService(hub).WatchEntityType(ctx, &WatchEntityTypeRequest{EntityType: "order"})
	if err != nil {
		t.Fatalf("WatchEntityType: %v", err)
	}

	got := drainEntityEvents(t, ch)
	if len(got) != 1 {
		t.Fatalf("delivered %d events, want exactly the terminal one: %+v", len(got), got)
	}
	if !got[0].Canceled || got[0].CancelReason != "server shutting down" {
		t.Fatalf("terminal event = %+v", got[0])
	}
}
