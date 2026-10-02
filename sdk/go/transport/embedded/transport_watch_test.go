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

package embedded

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Watch lifecycle against the REAL hub
//
// These tests deliberately do NOT use mockWatchService/stubWatcher
// (transport_coverage_test.go). That double hands back a watcher whose event
// channel is already closed and whose Err() is always nil, so it can neither
// park a sender nor carry a terminal reason — it would report both go-qrdb#406
// and go-qrdb#405 as absent. The subject here is what the forwarding goroutine
// does against a watcher that behaves like the production one, so the
// production one is what it runs against.
// ===========================================================================

// realWatchTransport builds an embedded transport over a real watch.Hub driven
// through the real service.WatchService. The hub is closed at test end.
func realWatchTransport(t *testing.T) (*Transport, *watch.Hub) {
	t.Helper()
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	return newExtTransport(t, WithWatchService(service.NewWatchService(hub))), hub
}

// notifyPut publishes one live put event for key at revision rev.
func notifyPut(hub *watch.Hub, key string, rev int64) {
	hub.NotifyPut([]byte(key), &kv.KeyValue{
		Key:            []byte(key),
		Value:          []byte("v"),
		CreateRevision: rev,
		ModRevision:    rev,
	}, nil)
}

// recvWatch waits for one response from ch, failing if the channel closes or
// nothing arrives. It proves the forwarding goroutine is running and the
// watcher is registered, so a subsequent event is genuinely in flight rather
// than racing setup.
func recvWatch(t *testing.T, ch <-chan transport.WatchResponse) transport.WatchResponse {
	t.Helper()
	select {
	case resp, ok := <-ch:
		if !ok {
			t.Fatal("watch channel closed before the first event arrived")
		}
		return resp
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a watch event")
		return transport.WatchResponse{}
	}
}

// watchGoroutineSlack is the tolerance on the post-churn goroutine count. The
// runtime starts and retires goroutines that have nothing to do with the
// subject (GC workers, the timer proc), so an exact equality would be flaky;
// the leak it guards against is one goroutine per iteration, two orders of
// magnitude above this.
const watchGoroutineSlack = 10

// settledGoroutineCount samples the live goroutine count until it holds steady
// across three consecutive samples, so a baseline is never taken while the
// warm-up cycle is still winding down. It gives up after a short deadline and
// returns the last observation.
func settledGoroutineCount() int {
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

// goroutinesAtMost waits up to timeout for the live goroutine count to fall to
// want or below and returns the final observation. It returns as soon as the
// bound is met, so a healthy run costs one sample.
func goroutinesAtMost(want int, timeout time.Duration) int {
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

// abandonWatch runs one full cycle of the lifecycle that leaks: open a watch,
// take delivery of one event so the forwarder is demonstrably live, queue a
// second event in the watcher's buffer, then cancel and never read the channel
// again.
//
// The second event is the whole point. It is queued BEFORE the cancel, so the
// hub's eviction leaves it in the watcher's buffer and a closed buffered
// channel still yields its contents: the forwarder is guaranteed to pick it up
// and attempt a handover to a consumer that has gone away. That is the state
// go-qrdb#406 parks in forever — which is exactly what sdk/go's
// Coordinator.Watch drain does when its context is cancelled.
func abandonWatch(t *testing.T, tr *Transport, hub *watch.Hub, target, key string, prefix bool) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	var (
		ch  <-chan transport.WatchResponse
		err error
	)
	if prefix {
		ch, err = tr.WatchPrefix(ctx, target, transport.WatchOptions{})
	} else {
		ch, err = tr.Watch(ctx, target, transport.WatchOptions{})
	}
	if err != nil {
		cancel()
		t.Fatalf("open watch: %v", err)
	}

	notifyPut(hub, key, 1)
	recvWatch(t, ch)

	notifyPut(hub, key, 2)
	cancel()
}

// TestEmbeddedWatch_AbandonedPrefixConsumerDoesNotLeak churns prefix watches
// whose consumer disappears mid-handover and asserts the goroutine count
// returns to its baseline (go-qrdb#406). Against the unguarded send this fails
// with one parked goroutine per iteration.
func TestEmbeddedWatch_AbandonedPrefixConsumerDoesNotLeak(t *testing.T) {
	const churn = 200

	tr, hub := realWatchTransport(t)

	// One warm-up cycle so anything the transport or hub starts lazily is part
	// of the baseline rather than counted as a leak.
	abandonWatch(t, tr, hub, "leak/", "leak/k", true)
	base := settledGoroutineCount()
	t.Logf("baseline goroutines: %d", base)

	for i := 0; i < churn; i++ {
		abandonWatch(t, tr, hub, "leak/", "leak/k", true)
	}

	got := goroutinesAtMost(base+watchGoroutineSlack, 30*time.Second)
	t.Logf("after %d abandoned watches: %d goroutines", churn, got)
	if got > base+watchGoroutineSlack {
		t.Fatalf("goroutine leak: baseline %d, %d after %d abandoned watches", base, got, churn)
	}
}

// TestEmbeddedWatch_AbandonedKeyConsumerDoesNotLeak is the same assertion for
// the exact-key watch, which opens its watcher through a different service
// method and shares the same forwarding goroutine.
func TestEmbeddedWatch_AbandonedKeyConsumerDoesNotLeak(t *testing.T) {
	const churn = 200

	tr, hub := realWatchTransport(t)

	abandonWatch(t, tr, hub, "leak-key", "leak-key", false)
	base := settledGoroutineCount()
	t.Logf("baseline goroutines: %d", base)

	for i := 0; i < churn; i++ {
		abandonWatch(t, tr, hub, "leak-key", "leak-key", false)
	}

	got := goroutinesAtMost(base+watchGoroutineSlack, 30*time.Second)
	t.Logf("after %d abandoned watches: %d goroutines", churn, got)
	if got > base+watchGoroutineSlack {
		t.Fatalf("goroutine leak: baseline %d, %d after %d abandoned watches", base, got, churn)
	}
}

// TestEmbeddedWatch_CancelClosesTheChannel asserts the channel-closed-exactly-
// once contract across a cancellation.
//
// It is NOT the leak proof, and it is here partly to say so: it passes against
// the unguarded send too, because a cancel with no delivery in flight lets the
// old range loop see the hub's close and exit normally. Only a reader that has
// already gone away WHILE a send is in flight — abandonWatch — reproduces
// go-qrdb#406.
func TestEmbeddedWatch_CancelClosesTheChannel(t *testing.T) {
	tr, hub := realWatchTransport(t)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := tr.WatchPrefix(ctx, "close/", transport.WatchOptions{})
	if err != nil {
		cancel()
		t.Fatalf("WatchPrefix: %v", err)
	}

	notifyPut(hub, "close/k", 1)
	recvWatch(t, ch)
	cancel()

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range ch {
		}
	}()

	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("watch channel was never closed after cancellation")
	}
}

// ===========================================================================
// Terminal reason (go-qrdb#405)
//
// The hub ends a watch it evicts by recording the reason on the watcher and
// closing the event channel; it never emits a terminal response itself. Turning
// that into a frame the caller can read is the transport's job, and every wire
// transport does it on the channel-closed path. These assert embedded now does
// the same, and — the other half of the contract — that a watch which ended
// cleanly stays distinguishable from one that did not.
// ===========================================================================

// drainWatch reads ch to close and returns every response it delivered.
func drainWatch(t *testing.T, ch <-chan transport.WatchResponse) []transport.WatchResponse {
	t.Helper()
	var got []transport.WatchResponse
	timeout := time.After(30 * time.Second)
	for {
		select {
		case resp, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, resp)
		case <-timeout:
			t.Fatalf("timed out draining the watch channel after %d responses", len(got))
			return got
		}
	}
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

// TestEmbeddedWatch_OverflowEvictionCarriesItsReason drives a real overflow
// eviction — no doubles anywhere — and asserts the consumer receives a terminal
// frame naming it rather than a bare closed channel.
func TestEmbeddedWatch_OverflowEvictionCarriesItsReason(t *testing.T) {
	tr, hub := realWatchTransport(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := tr.WatchPrefix(ctx, "overflow/", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}

	// Nothing reads ch yet, so the forwarder parks on its first handover and
	// every further event piles up in the watcher's own buffer until the hub
	// evicts it for overflow. The bound is generous and independent of the
	// hub's buffer size; the loop stops as soon as the eviction lands.
	const maxEvents = 1 << 16
	for i := 0; i < maxEvents && hub.WatchCount() > 0; i++ {
		notifyPut(hub, "overflow/k", int64(i+1))
	}
	awaitEviction(t, hub)

	got := drainWatch(t, ch)
	if len(got) == 0 {
		t.Fatal("the watch delivered nothing at all")
	}

	last := got[len(got)-1]
	if !last.Canceled {
		t.Fatalf("an evicted watch closed without a terminal frame; last response was %+v", last)
	}
	if last.CancelReason != watch.ErrWatcherOverflow.Error() {
		t.Fatalf("CancelReason = %q, want %q", last.CancelReason, watch.ErrWatcherOverflow.Error())
	}
	if last.WatchID == 0 {
		t.Error("terminal frame carries no WatchID")
	}

	var canceled *transport.WatchCanceledError
	if !errors.As(last.Err(), &canceled) {
		t.Fatalf("Err() = %T (%v), want *transport.WatchCanceledError", last.Err(), last.Err())
	}
	if canceled.Reason != watch.ErrWatcherOverflow.Error() {
		t.Errorf("typed reason = %q, want %q", canceled.Reason, watch.ErrWatcherOverflow.Error())
	}
}

// stubHistory is a watch.HistoryReplayer whose replay fails, which is how the
// hub reaches its compaction-raced-the-replay eviction. It stands in for the
// MVCC store adapter, not for the watcher: the hub, the watcher, the service
// and the transport under test are all the production ones.
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

// TestEmbeddedWatch_CompactedReplayClassifiesLikeTheWireTransports asserts the
// payoff of carrying the reason verbatim: a compaction that outran a replay
// classifies into the same typed error on embedded as it does on REST, gRPC,
// QUIC and MCP, because the reason text reaching
// transport.WatchResponse.Err() is the same text those transports put on the
// wire.
func TestEmbeddedWatch_CompactedReplayClassifiesLikeTheWireTransports(t *testing.T) {
	tr, hub := realWatchTransport(t)
	hub.SetHistory(&stubHistory{
		current:   10,
		replayErr: errors.New("history scan hit a compacted value"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := tr.Watch(ctx, "history", transport.WatchOptions{StartRevision: 5})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	got := drainWatch(t, ch)
	if len(got) == 0 {
		t.Fatal("the watch delivered nothing at all")
	}

	last := got[len(got)-1]
	if !last.Canceled {
		t.Fatalf("a failed replay closed without a terminal frame; last response was %+v", last)
	}
	if last.CancelReason != watch.ErrCompacted.Error() {
		t.Fatalf("CancelReason = %q, want %q", last.CancelReason, watch.ErrCompacted.Error())
	}

	var compacted *transport.WatchCompactedError
	if !errors.As(last.Err(), &compacted) {
		t.Fatalf("Err() = %T (%v), want *transport.WatchCompactedError", last.Err(), last.Err())
	}
}

// TestEmbeddedWatch_CleanEndCarriesNoTerminalFrame is the other half of #405:
// the terminal frame has to MEAN something, so a watch that ended with no
// reason must not carry one. A hub close cancels its watchers without recording
// an error, which is the clean end.
func TestEmbeddedWatch_CleanEndCarriesNoTerminalFrame(t *testing.T) {
	tr, hub := realWatchTransport(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := tr.WatchPrefix(ctx, "clean/", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("WatchPrefix: %v", err)
	}

	notifyPut(hub, "clean/k", 1)
	first := recvWatch(t, ch)
	if first.Canceled {
		t.Fatalf("a live event arrived marked Canceled: %+v", first)
	}

	hub.Close()

	for _, resp := range drainWatch(t, ch) {
		if resp.Canceled {
			t.Fatalf("a clean close delivered a terminal frame: %+v", resp)
		}
		if err := resp.Err(); err != nil {
			t.Fatalf("a clean close delivered a response with Err() = %v", err)
		}
	}
}

// TestEmbeddedWatch_InBandCancellationEndsTheStream covers the other terminal
// shape: a watch servicer that emits its own Canceled response rather than
// closing on an error. The forwarder must stop there, as every wire client
// does, and not sit waiting on a channel the server is finished with. The
// watcher here keeps its event channel OPEN after the terminal response, so a
// forwarder that ignored the frame would hang rather than fail.
func TestEmbeddedWatch_InBandCancellationEndsTheStream(t *testing.T) {
	w := newStubWatcher()
	w.ch <- watch.WatchResponse{WatchID: 7, Canceled: true, CancelReason: "hub evicted the watcher"}

	tr := newExtTransport(t, WithWatchService(&mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return w, nil
		},
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := tr.Watch(ctx, "inband", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	got := drainWatch(t, ch)
	if len(got) != 1 {
		t.Fatalf("delivered %d responses, want exactly the terminal one", len(got))
	}
	if !got[0].Canceled || got[0].CancelReason != "hub evicted the watcher" {
		t.Fatalf("terminal frame = %+v", got[0])
	}
}

// ===========================================================================
// Open-time refusal (go-qrdb#495)
//
// A start revision the hub refuses fails the OPEN call, before any watcher
// exists, so there is no stream for a terminal frame to travel on and no
// WatchResponse.Err() to classify it. The three request/response transports
// run that error through transport.ClassifyWatchError so the caller still gets
// the typed error (sdk/go/transport/{grpc,unix,rest}/transport_watch.go); the
// embedded transport translated it as an ordinary service error and handed back
// a generic *transport.RequestError, so errors.As for the documented remedy —
// drop StartRevision and retry live — matched on four transports and silently
// did not on the fifth.
// ===========================================================================

// openWatchFn names one of the two WatchClient entry points. They are separate
// interface methods reached by different callers, so every open-time case runs
// against both.
type openWatchFn func(*Transport, context.Context, string, transport.WatchOptions) (<-chan transport.WatchResponse, error)

// watchOpeners are the two entry points under test, by the name a caller knows.
var watchOpeners = map[string]openWatchFn{
	"Watch": func(tr *Transport, ctx context.Context, target string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
		return tr.Watch(ctx, target, opts)
	},
	"WatchPrefix": func(tr *Transport, ctx context.Context, target string, opts transport.WatchOptions) (<-chan transport.WatchResponse, error) {
		return tr.WatchPrefix(ctx, target, opts)
	},
}

// TestEmbeddedWatch_UnsupportedStartRevisionIsTyped asserts a hub with no
// history replayer refuses a start revision with the typed error a caller can
// match, on both entry points.
func TestEmbeddedWatch_UnsupportedStartRevisionIsTyped(t *testing.T) {
	for name, open := range watchOpeners {
		t.Run(name, func(t *testing.T) {
			tr, _ := realWatchTransport(t)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			ch, err := open(tr, ctx, "history", transport.WatchOptions{StartRevision: 5})
			if err == nil {
				t.Fatalf("%s opened a watch a hub with no history can not serve; channel %v", name, ch)
			}

			var unsupported *transport.WatchStartRevisionUnsupportedError
			if !errors.As(err, &unsupported) {
				t.Fatalf("%s open error = %T (%v), want *transport.WatchStartRevisionUnsupportedError", name, err, err)
			}
			if !strings.Contains(unsupported.Reason, watch.ErrStartRevisionUnsupported.Error()) {
				t.Errorf("Reason = %q, want it to carry %q", unsupported.Reason, watch.ErrStartRevisionUnsupported.Error())
			}
		})
	}
}

// TestEmbeddedWatch_CompactedStartRevisionIsTyped asserts a start revision
// already compacted out of the change history is refused with the typed error,
// on both entry points. It is the condition whose documented remedy differs
// from the unsupported one — resume from the compacted revision rather than
// abandon replay entirely — so telling them apart is the point.
func TestEmbeddedWatch_CompactedStartRevisionIsTyped(t *testing.T) {
	for name, open := range watchOpeners {
		t.Run(name, func(t *testing.T) {
			tr, hub := realWatchTransport(t)
			hub.SetHistory(&stubHistory{current: 10, compacted: 7})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			ch, err := open(tr, ctx, "history", transport.WatchOptions{StartRevision: 5})
			if err == nil {
				t.Fatalf("%s opened a watch from a compacted revision; channel %v", name, ch)
			}

			var compacted *transport.WatchCompactedError
			if !errors.As(err, &compacted) {
				t.Fatalf("%s open error = %T (%v), want *transport.WatchCompactedError", name, err, err)
			}
			if !strings.Contains(compacted.Reason, watch.ErrCompacted.Error()) {
				t.Errorf("Reason = %q, want it to carry %q", compacted.Reason, watch.ErrCompacted.Error())
			}
		})
	}
}

// TestEmbeddedWatch_AcceptedStartRevisionOpensCleanly is the negative half: a
// start revision the hub CAN serve still opens, delivers, and reports no error.
// Classification runs only on the failure path, and a test that only ever looks
// at failures would not notice if it started rejecting good opens.
func TestEmbeddedWatch_AcceptedStartRevisionOpensCleanly(t *testing.T) {
	for name, open := range watchOpeners {
		t.Run(name, func(t *testing.T) {
			tr, hub := realWatchTransport(t)
			hub.SetHistory(&stubHistory{current: 10, compacted: 2})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			ch, err := open(tr, ctx, "history", transport.WatchOptions{StartRevision: 5})
			if err != nil {
				t.Fatalf("%s refused a start revision the hub can serve: %T (%v)", name, err, err)
			}
			if ch == nil {
				t.Fatalf("%s returned a nil channel with a nil error", name)
			}
		})
	}
}

// TestEmbeddedWatch_NonWatchOpenErrorKeepsItsTranslation is the other negative:
// classifying the watch conditions must not displace the translation every
// other service error goes through. A refusal that is not a watch-history
// condition still arrives as the transport type translateError maps it to.
func TestEmbeddedWatch_NonWatchOpenErrorKeepsItsTranslation(t *testing.T) {
	for name, open := range watchOpeners {
		t.Run(name, func(t *testing.T) {
			refuse := func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
				return nil, &service.CapabilityUnsupportedError{Message: "watch is not configured on this shard"}
			}
			tr := newExtTransport(t, WithWatchService(&mockWatchService{
				watchFn:       refuse,
				watchPrefixFn: refuse,
			}))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			_, err := open(tr, ctx, "anything", transport.WatchOptions{})

			var unsupported *transport.CapabilityUnsupportedError
			if !errors.As(err, &unsupported) {
				t.Fatalf("%s open error = %T (%v), want *transport.CapabilityUnsupportedError", name, err, err)
			}
		})
	}
}
