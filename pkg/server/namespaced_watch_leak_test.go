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
	"runtime"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ===========================================================================
// Handover lifecycle (go-qrdb#499)
//
// namespacedWatchService forwards every namespaced watch through a goroutine
// of its own — namespacedWatcher.pump for the key/prefix stream,
// pumpEntityEvents for the entity stream — and each hands off on an UNBUFFERED
// channel. A consumer ends a watch by cancelling the context it opened it
// with, so the moment a cancel races a delivery the handover has no reader.
// Without a select on that context the send parks for the life of the process
// and the deferred close never runs: one goroutine per cancelled watch, in a
// wrapper every namespaced (tenant, database) deployment puts on EVERY watch.
//
// The proof is a goroutine count, not an argument, and it needs a delivery
// genuinely in flight at the cancel — a cancel with nothing pending lets even
// the unguarded loop see the inner channel close and exit. These run the whole
// chain: real watch.Hub, real service.WatchService, real wrapper.
// ===========================================================================

// namespacedWatchGoroutineSlack is the tolerance on the post-churn goroutine
// count. The runtime starts and retires goroutines unrelated to the subject, so
// exact equality would be flaky; the leak guarded against is one goroutine per
// iteration, two orders of magnitude above this.
const namespacedWatchGoroutineSlack = 10

// namespacedWatchChurn is the number of watches abandoned per leak test. It is
// large enough that one leaked goroutine per iteration cannot hide inside the
// slack above.
const namespacedWatchChurn = 200

// settledNamespacedGoroutines samples the live goroutine count until it holds
// steady across consecutive samples, so a baseline is never taken while warm-up
// is still winding down. It gives up after a short deadline and returns the
// last observation.
func settledNamespacedGoroutines() int {
	deadline := time.Now().Add(5 * time.Second)
	prev, stable := -1, 0
	for {
		runtime.GC()
		got := runtime.NumGoroutine()
		if got == prev {
			if stable++; stable == 2 {
				return got
			}
		} else {
			prev, stable = got, 0
		}
		if time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// namespacedGoroutinesAtMost waits up to timeout for the live goroutine count
// to fall to want or below and returns the final observation. A healthy run
// costs one sample.
func namespacedGoroutinesAtMost(want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for {
		runtime.GC()
		got := runtime.NumGoroutine()
		if got <= want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// assertNoNamespacedWatchLeak churns abandon cycles and fails if the goroutine
// count does not return to its baseline. One warm-up cycle runs first so
// anything the hub or service starts lazily is part of the baseline rather than
// counted as a leak.
func assertNoNamespacedWatchLeak(t *testing.T, abandon func()) {
	t.Helper()

	abandon()
	base := settledNamespacedGoroutines()
	t.Logf("baseline goroutines: %d", base)

	for i := 0; i < namespacedWatchChurn; i++ {
		abandon()
	}

	got := namespacedGoroutinesAtMost(base+namespacedWatchGoroutineSlack, 30*time.Second)
	t.Logf("after %d abandoned namespaced watches: %d goroutines", namespacedWatchChurn, got)
	if got > base+namespacedWatchGoroutineSlack {
		t.Fatalf("goroutine leak: baseline %d, %d after %d abandoned watches", base, got, namespacedWatchChurn)
	}
}

// recvNamespacedResponse waits for one response from a namespaced watcher,
// failing if the channel closes or nothing arrives. Taking delivery proves the
// pump is running and the inner watcher is registered, so the event queued
// after it is genuinely in flight rather than racing setup.
func recvNamespacedResponse(t *testing.T, ch <-chan watch.WatchResponse) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("namespaced watch channel closed before the first event arrived")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for a namespaced watch event")
	}
}

// recvNamespacedEntityEvent is recvNamespacedResponse for the entity stream.
func recvNamespacedEntityEvent(t *testing.T, ch <-chan service.WatchEntityEvent) {
	t.Helper()
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("namespaced entity watch channel closed before the first event arrived")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for a namespaced entity event")
	}
}

// abandonNamespacedWatch runs one cycle of the lifecycle that leaks on the
// key/prefix stream: open a namespaced watch, take delivery of one response so
// the pump is demonstrably live, queue a second in the inner watcher's buffer,
// then cancel and never read again. The second response is the point — the pump
// is guaranteed to pick it up and attempt a handover to a consumer that has
// gone away.
func abandonNamespacedWatch(t *testing.T, hub *watch.Hub, svc *namespacedWatchService, prefix bool) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	const key = "leak"
	var (
		w   watch.Watcher
		err error
	)
	if prefix {
		w, err = svc.WatchPrefix(ctx, key)
	} else {
		w, err = svc.Watch(ctx, key)
	}
	if err != nil {
		cancel()
		t.Fatalf("open namespaced watch: %v", err)
	}

	notifyNamespacedPut(hub, namespaceTestPrefix+key, 1)
	recvNamespacedResponse(t, w.Events())

	notifyNamespacedPut(hub, namespaceTestPrefix+key, 2)
	cancel()
}

// abandonNamespacedEntityWatch is abandonNamespacedWatch for the entity stream,
// whose handover lives in a different method and so gets its own measurement.
func abandonNamespacedEntityWatch(t *testing.T, hub *watch.Hub, svc *namespacedWatchService, byType bool) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	const entityType = "user"
	const id = "u1"
	key := string(entityType) + "/" + namespaceTestPrefix + id

	var (
		ch  <-chan service.WatchEntityEvent
		err error
	)
	if byType {
		ch, err = svc.WatchEntityType(ctx, &service.WatchEntityTypeRequest{EntityType: entityType})
	} else {
		ch, err = svc.WatchEntity(ctx, &service.WatchEntityRequest{EntityType: entityType, ID: id})
	}
	if err != nil {
		cancel()
		t.Fatalf("open namespaced entity watch: %v", err)
	}

	notifyNamespacedPut(hub, key, 1)
	recvNamespacedEntityEvent(t, ch)

	notifyNamespacedPut(hub, key, 2)
	cancel()
}

// TestNamespacedWatcher_AbandonedKeyConsumerDoesNotLeak churns key watches
// whose consumer disappears mid-handover and asserts the goroutine count
// returns to baseline. Against the unguarded send in namespacedWatcher.pump
// this fails with one parked goroutine per iteration.
func TestNamespacedWatcher_AbandonedKeyConsumerDoesNotLeak(t *testing.T) {
	hub, svc := namespacedEntityWatch(t)
	assertNoNamespacedWatchLeak(t, func() { abandonNamespacedWatch(t, hub, svc, false) })
}

// TestNamespacedWatcher_AbandonedPrefixConsumerDoesNotLeak is the same
// assertion for the prefix watch, which reaches the same pump through a
// different service method.
func TestNamespacedWatcher_AbandonedPrefixConsumerDoesNotLeak(t *testing.T) {
	hub, svc := namespacedEntityWatch(t)
	assertNoNamespacedWatchLeak(t, func() { abandonNamespacedWatch(t, hub, svc, true) })
}

// TestNamespacedWatchService_AbandonedEntityConsumerDoesNotLeak is the second
// leaking handover: pumpEntityEvents, reached by WatchEntity. It is a separate
// goroutine on a separate channel and would survive a fix applied only to the
// watcher pump.
func TestNamespacedWatchService_AbandonedEntityConsumerDoesNotLeak(t *testing.T) {
	hub, svc := namespacedEntityWatch(t)
	assertNoNamespacedWatchLeak(t, func() { abandonNamespacedEntityWatch(t, hub, svc, false) })
}

// TestNamespacedWatchService_AbandonedEntityTypeConsumerDoesNotLeak is the
// type-level entity watch, which reaches pumpEntityEvents with filtering on.
func TestNamespacedWatchService_AbandonedEntityTypeConsumerDoesNotLeak(t *testing.T) {
	hub, svc := namespacedEntityWatch(t)
	assertNoNamespacedWatchLeak(t, func() { abandonNamespacedEntityWatch(t, hub, svc, true) })
}
