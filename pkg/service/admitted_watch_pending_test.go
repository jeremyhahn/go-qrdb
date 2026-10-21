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

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// These tests hold a delivery its consumer is not taking -- nothing reads
// the stream until the assertion -- and drive what happens meanwhile with a
// fake clock and channels (#809).

// signalAdmitter is a switchAdmitter that reports each admission it decides.
type signalAdmitter struct {
	switchAdmitter
	calls chan struct{}
}

func newSignalAdmitter() *signalAdmitter {
	return &signalAdmitter{calls: make(chan struct{}, admittedBacklog+8)}
}

func (a *signalAdmitter) AdmitStream(ctx context.Context, identity *auth.Identity) (time.Duration, error) {
	holds, err := a.switchAdmitter.AdmitStream(ctx, identity)
	a.calls <- struct{}{}
	return holds, err
}

// awaitAdmissions waits for n more admissions.
func awaitAdmissions(t *testing.T, a *signalAdmitter, n int) {
	t.Helper()
	for range n {
		select {
		case <-a.calls:
		case <-time.After(10 * time.Second):
			t.Fatal("admission never ran")
		}
	}
}

// lapseAdmission waits for the relay's lapse timer to be armed and moves
// the clock past it.
func lapseAdmission(t *testing.T, clk *clocktest.Fake) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := clk.BlockUntil(ctx, 1); err != nil {
		t.Fatalf("lapse timer never armed: %v", err)
	}
	clk.Advance(testStatusTTL)
}

var keyWatchOpeners = []struct {
	name string
	fn   func(WatchServicer, context.Context) (watch.Watcher, error)
}{
	{"Watch", func(s WatchServicer, ctx context.Context) (watch.Watcher, error) { return s.Watch(ctx, "k") }},
	{"WatchPrefix", func(s WatchServicer, ctx context.Context) (watch.Watcher, error) { return s.WatchPrefix(ctx, "p/") }},
}

// TestAdmittedWatch_SuspendedWhileBlockedEndsTheWatch: a response admitted
// before its consumer stopped reading is not handed over once the tenant is
// suspended; the admission lapses, is refused, and the watch ends with the
// refusal.
func TestAdmittedWatch_SuspendedWhileBlockedEndsTheWatch(t *testing.T) {
	for _, open := range keyWatchOpeners {
		t.Run(open.name, func(t *testing.T) {
			clk := clocktest.New()
			inner := newAdmitTestWatcher()
			admitter := newSignalAdmitter()
			ctx, _ := acmeIdentityCtx()
			w, err := open.fn(newAdmittedWatch(&admitTestService{watcher: inner}, admitter, clk), ctx)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer w.Close()

			inner.events <- watch.WatchResponse{Revision: 1}
			awaitAdmissions(t, admitter, 1)
			refusal := &tenantRefusedError{tenantID: "acme"}
			admitter.set(refusal)
			lapseAdmission(t, clk)
			awaitAdmissions(t, admitter, 1)

			if resp, ok := <-w.Events(); ok {
				t.Fatalf("retained response delivered after suspension: %+v", resp)
			}
			var got *tenantRefusedError
			if !errors.As(w.Err(), &got) || got != refusal {
				t.Fatalf("Err() = %v, want the admitter's refusal", w.Err())
			}
			select {
			case <-inner.closed:
			default:
				t.Fatal("inner watcher not closed after refusal")
			}
		})
	}
}

// TestAdmittedWatch_InnerEndWhileBlockedEndsTheWatch: a seal ends the inner
// watcher while a response waits on its consumer; the watch ends at once
// and the response is never handed over.
func TestAdmittedWatch_InnerEndWhileBlockedEndsTheWatch(t *testing.T) {
	for _, open := range keyWatchOpeners {
		t.Run(open.name, func(t *testing.T) {
			inner := newAdmitTestWatcher()
			sealed := errors.New("barrier sealed")
			inner.err = sealed
			admitter := newSignalAdmitter()
			ctx, _ := acmeIdentityCtx()
			w, err := open.fn(newAdmittedWatch(&admitTestService{watcher: inner}, admitter, clocktest.New()), ctx)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer w.Close()

			inner.events <- watch.WatchResponse{Revision: 1}
			awaitAdmissions(t, admitter, 1)
			close(inner.events)
			select {
			case <-inner.closed:
			case <-time.After(10 * time.Second):
				t.Fatal("the relay did not end with its inner watcher")
			}
			if resp, ok := <-w.Events(); ok {
				t.Fatalf("response delivered after the inner watcher ended: %+v", resp)
			}
			if !errors.Is(w.Err(), sealed) {
				t.Fatalf("Err() = %v, want the inner watcher's error", w.Err())
			}
		})
	}
}

// TestAdmittedWatch_ActiveBlockedDeliversInOrder: responses that queue
// behind a consumer of an active tenant are re-admitted when their
// admission lapses and then delivered, in order.
func TestAdmittedWatch_ActiveBlockedDeliversInOrder(t *testing.T) {
	for _, open := range keyWatchOpeners {
		t.Run(open.name, func(t *testing.T) {
			clk := clocktest.New()
			inner := newAdmitTestWatcher()
			admitter := newSignalAdmitter()
			ctx, _ := acmeIdentityCtx()
			w, err := open.fn(newAdmittedWatch(&admitTestService{watcher: inner}, admitter, clk), ctx)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer w.Close()

			for rev := int64(1); rev <= 3; rev++ {
				inner.events <- watch.WatchResponse{Revision: rev}
			}
			awaitAdmissions(t, admitter, 3)
			lapseAdmission(t, clk)
			awaitAdmissions(t, admitter, 1)
			for rev := int64(1); rev <= 3; rev++ {
				if resp, ok := <-w.Events(); !ok || resp.Revision != rev {
					t.Fatalf("response = %+v, %v; want revision %d", resp, ok, rev)
				}
			}
		})
	}
}

// entityCtxService is an entity watch source that keeps the context its
// watch was opened under, which the relay cancels when it ends.
type entityCtxService struct {
	WatchServicer
	in  chan WatchEntityEvent
	ctx context.Context
}

func (s *entityCtxService) WatchEntity(ctx context.Context, _ *WatchEntityRequest) (<-chan WatchEntityEvent, error) {
	s.ctx = ctx
	return s.in, nil
}

func (s *entityCtxService) WatchEntityType(ctx context.Context, _ *WatchEntityTypeRequest) (<-chan WatchEntityEvent, error) {
	s.ctx = ctx
	return s.in, nil
}

// TestAdmittedWatch_EntitySuspendedWhileBlockedDeliversTheRefusal: the
// entity relay's analog of the key watch case -- the consumer, once it
// reads, gets the terminal refusal and not the retained event.
func TestAdmittedWatch_EntitySuspendedWhileBlockedDeliversTheRefusal(t *testing.T) {
	for _, open := range entityWatchOpeners {
		t.Run(open.name, func(t *testing.T) {
			clk := clocktest.New()
			in := make(chan WatchEntityEvent, 1)
			admitter := newSignalAdmitter()
			ctx, _ := acmeIdentityCtx()
			out, err := open.fn(newAdmittedWatch(&admitTestService{entity: in}, admitter, clk), ctx)
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			in <- WatchEntityEvent{EntityType: admitTestEntity, ID: "u1"}
			awaitAdmissions(t, admitter, 1)
			refusal := &tenantRefusedError{tenantID: "acme"}
			admitter.set(refusal)
			lapseAdmission(t, clk)
			awaitAdmissions(t, admitter, 1)

			ev, ok := <-out
			if !ok || !ev.Canceled || ev.CancelReason != refusal.Error() || ev.ID != "" {
				t.Fatalf("first read = %+v (open %v), want the terminal refusal", ev, ok)
			}
			if ev, ok := <-out; ok {
				t.Fatalf("event after terminal: %+v", ev)
			}
		})
	}
}

// A response admitted and held for a consumer not yet reading reaches it
// ahead of the refusal of the next one: the refusal ends the watch, it does
// not take back what was admitted before it.
func TestAdmittedWatch_RefusedAfterAdmittedDeliversTheAdmittedFirst(t *testing.T) {
	for _, open := range keyWatchOpeners {
		t.Run(open.name, func(t *testing.T) {
			clk := clocktest.New()
			inner := newAdmitTestWatcher()
			admitter := newSignalAdmitter()
			ctx, _ := acmeIdentityCtx()
			w, err := open.fn(newAdmittedWatch(&admitTestService{watcher: inner}, admitter, clk), ctx)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer w.Close()

			inner.events <- watch.WatchResponse{Revision: 1}
			awaitAdmissions(t, admitter, 1)
			refusal := &tenantRefusedError{tenantID: "acme"}
			admitter.set(refusal)
			inner.events <- watch.WatchResponse{Revision: 2}
			awaitAdmissions(t, admitter, 1)

			if resp, ok := <-w.Events(); !ok || resp.Revision != 1 {
				t.Fatalf("first read = %+v (open %v), want the admitted revision 1", resp, ok)
			}
			if resp, ok := <-w.Events(); ok {
				t.Fatalf("refused response delivered: %+v", resp)
			}
			var got *tenantRefusedError
			if !errors.As(w.Err(), &got) || got != refusal {
				t.Fatalf("Err() = %v, want the admitter's refusal", w.Err())
			}
		})
	}
}

// The entity relay hands over an admitted, held event before the terminal
// refusal of the next one.
func TestAdmittedWatch_EntityRefusedAfterAdmittedDeliversTheAdmittedFirst(t *testing.T) {
	for _, open := range entityWatchOpeners {
		t.Run(open.name, func(t *testing.T) {
			clk := clocktest.New()
			in := make(chan WatchEntityEvent, 2)
			admitter := newSignalAdmitter()
			ctx, _ := acmeIdentityCtx()
			out, err := open.fn(newAdmittedWatch(&admitTestService{entity: in}, admitter, clk), ctx)
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			in <- WatchEntityEvent{EntityType: admitTestEntity, ID: "u1"}
			awaitAdmissions(t, admitter, 1)
			refusal := &tenantRefusedError{tenantID: "acme"}
			admitter.set(refusal)
			in <- WatchEntityEvent{EntityType: admitTestEntity, ID: "u2"}
			awaitAdmissions(t, admitter, 1)

			if ev, ok := <-out; !ok || ev.ID != "u1" || ev.Canceled {
				t.Fatalf("first read = %+v (open %v), want the admitted u1", ev, ok)
			}
			ev, ok := <-out
			if !ok || !ev.Canceled || ev.CancelReason != refusal.Error() || ev.ID != "" {
				t.Fatalf("second read = %+v (open %v), want the terminal refusal", ev, ok)
			}
			if ev, ok := <-out; ok {
				t.Fatalf("event after terminal: %+v", ev)
			}
		})
	}
}

// TestAdmittedWatch_EntityInnerEndWhileBlockedEndsTheWatch: the entity
// source closing -- a seal ends it -- while an event waits ends the relay at
// once, releasing the source; the event is never handed over.
func TestAdmittedWatch_EntityInnerEndWhileBlockedEndsTheWatch(t *testing.T) {
	for _, open := range entityWatchOpeners {
		t.Run(open.name, func(t *testing.T) {
			svc := &entityCtxService{in: make(chan WatchEntityEvent, 1)}
			admitter := newSignalAdmitter()
			ctx, _ := acmeIdentityCtx()
			out, err := open.fn(newAdmittedWatch(svc, admitter, clocktest.New()), ctx)
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			svc.in <- WatchEntityEvent{EntityType: admitTestEntity, ID: "u1"}
			awaitAdmissions(t, admitter, 1)
			close(svc.in)
			select {
			case <-svc.ctx.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("the relay did not end with its source")
			}
			if ev, ok := <-out; ok {
				t.Fatalf("event delivered after the source ended: %+v", ev)
			}
		})
	}
}

// TestAdmittedWatch_EntityActiveBlockedDeliversInOrder: events of an active
// tenant queued behind their consumer are re-admitted on lapse and then
// delivered in order.
func TestAdmittedWatch_EntityActiveBlockedDeliversInOrder(t *testing.T) {
	for _, open := range entityWatchOpeners {
		t.Run(open.name, func(t *testing.T) {
			clk := clocktest.New()
			in := make(chan WatchEntityEvent, 3)
			admitter := newSignalAdmitter()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = auth.WithIdentity(ctx, &auth.Identity{Subject: "bob", TenantID: "acme"})
			out, err := open.fn(newAdmittedWatch(&admitTestService{entity: in}, admitter, clk), ctx)
			if err != nil {
				t.Fatalf("open: %v", err)
			}

			ids := []string{"u1", "u2", "u3"}
			for _, id := range ids {
				in <- WatchEntityEvent{EntityType: admitTestEntity, ID: id}
			}
			awaitAdmissions(t, admitter, len(ids))
			lapseAdmission(t, clk)
			awaitAdmissions(t, admitter, 1)
			for _, id := range ids {
				if ev, ok := <-out; !ok || ev.ID != id {
					t.Fatalf("event = %+v, %v; want %s", ev, ok, id)
				}
			}
		})
	}
}

// TestRelayAdmitted_BacklogOverflowEndsTheRelay: a consumer that lets more
// than admittedBacklog items queue is ended rather than buffered without
// limit.
// lapseAfter asserts that the delivery held under the last admission lapses
// exactly d after it was admitted: not a nanosecond sooner, and not a moment
// later -- the one admission that follows is the lapse's, nothing else
// arriving to be admitted.
func lapseAfter(t *testing.T, clk *clocktest.Fake, admitter *signalAdmitter, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := clk.BlockUntil(ctx, 1); err != nil {
		t.Fatalf("lapse timer never armed: %v", err)
	}
	clk.Advance(d - time.Nanosecond)
	if n := clk.Waiters(); n != 1 {
		t.Fatalf("admission lapsed before %v (waiters %d)", d, n)
	}
	clk.Advance(time.Nanosecond)
	awaitAdmissions(t, admitter, 1)
}

// heldAdmissions are the two answers an admission can hold a delivery
// under: one from a status-cache entry about to expire, which holds only
// for what is left of that entry, and one from a fresh status read, which
// holds for the cache's whole TTL.
var heldAdmissions = []struct {
	name  string
	holds time.Duration
}{
	{"CacheEntryAboutToExpire", 10 * time.Millisecond},
	{"FreshRead", testStatusTTL},
}

// TestAdmittedWatch_HeldDeliveryExpiresWithItsAdmission: a response held
// while its consumer is not reading is re-admitted when the admission it
// arrived under runs out -- what was left of the status-cache entry, not a
// fresh TTL -- so a suspension refuses it no later than the cache would
// itself have stopped serving the status.
func TestAdmittedWatch_HeldDeliveryExpiresWithItsAdmission(t *testing.T) {
	for _, open := range keyWatchOpeners {
		for _, held := range heldAdmissions {
			t.Run(open.name+"/"+held.name, func(t *testing.T) {
				clk := clocktest.New()
				inner := newAdmitTestWatcher()
				admitter := newSignalAdmitter()
				admitter.holdFor(held.holds)
				ctx, _ := acmeIdentityCtx()
				w, err := open.fn(newAdmittedWatch(&admitTestService{watcher: inner}, admitter, clk), ctx)
				if err != nil {
					t.Fatalf("open: %v", err)
				}
				defer w.Close()

				inner.events <- watch.WatchResponse{Revision: 1}
				awaitAdmissions(t, admitter, 1)
				refusal := &tenantRefusedError{tenantID: "acme"}
				admitter.set(refusal)
				lapseAfter(t, clk, admitter, held.holds)

				if resp, ok := <-w.Events(); ok {
					t.Fatalf("held response delivered after suspension: %+v", resp)
				}
				var got *tenantRefusedError
				if !errors.As(w.Err(), &got) || got != refusal {
					t.Fatalf("Err() = %v, want the admitter's refusal", w.Err())
				}
			})
		}
	}
}

// TestAdmittedWatch_EntityHeldDeliveryExpiresWithItsAdmission is
// TestAdmittedWatch_HeldDeliveryExpiresWithItsAdmission for entity events:
// the held event is replaced by the terminal refusal once the admission it
// arrived under runs out.
func TestAdmittedWatch_EntityHeldDeliveryExpiresWithItsAdmission(t *testing.T) {
	for _, open := range entityWatchOpeners {
		for _, held := range heldAdmissions {
			t.Run(open.name+"/"+held.name, func(t *testing.T) {
				clk := clocktest.New()
				in := make(chan WatchEntityEvent, 1)
				admitter := newSignalAdmitter()
				admitter.holdFor(held.holds)
				ctx, _ := acmeIdentityCtx()
				out, err := open.fn(newAdmittedWatch(&admitTestService{entity: in}, admitter, clk), ctx)
				if err != nil {
					t.Fatalf("open: %v", err)
				}

				in <- WatchEntityEvent{EntityType: admitTestEntity, ID: "u1"}
				awaitAdmissions(t, admitter, 1)
				refusal := &tenantRefusedError{tenantID: "acme"}
				admitter.set(refusal)
				lapseAfter(t, clk, admitter, held.holds)

				ev, ok := <-out
				if !ok || !ev.Canceled || ev.CancelReason != refusal.Error() || ev.ID != "" {
					t.Fatalf("first read = %+v (open %v), want the terminal refusal", ev, ok)
				}
				if ev, ok := <-out; ok {
					t.Fatalf("event after terminal: %+v", ev)
				}
			})
		}
	}
}

// TestRelayAdmitted_RenewalHoldsForWhatItsAdmissionAnswers: a renewed
// admission sets the next lapse to what that admission answers, so a
// renewal answered from an expiring cache entry is itself renewed when
// that entry expires.
func TestRelayAdmitted_RenewalHoldsForWhatItsAdmissionAnswers(t *testing.T) {
	clk := clocktest.New()
	admitter := newSignalAdmitter()
	admitter.holdFor(testStatusTTL, 10*time.Millisecond)
	in := make(chan int, 1)
	in <- 1
	stop := make(chan struct{})
	done := make(chan error, 1)
	admit := func() (time.Duration, error) { return admitter.AdmitStream(context.Background(), nil) }
	go func() {
		done <- relayAdmitted(in, make(chan int), stop, clk, admit, func(int) bool { return false })
	}()
	awaitAdmissions(t, admitter, 1)
	lapseAfter(t, clk, admitter, testStatusTTL)
	refusal := &tenantRefusedError{tenantID: "acme"}
	admitter.set(refusal)
	lapseAfter(t, clk, admitter, 10*time.Millisecond)
	select {
	case err := <-done:
		var got *tenantRefusedError
		if !errors.As(err, &got) || got != refusal {
			t.Fatalf("relayAdmitted = %v, want the admitter's refusal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("refused renewal did not end the relay")
	}
}

func TestRelayAdmitted_BacklogOverflowEndsTheRelay(t *testing.T) {
	in := make(chan int, admittedBacklog+1)
	for i := range admittedBacklog + 1 {
		in <- i
	}
	err := relayAdmitted(in, make(chan int), make(chan struct{}), clocktest.New(), func() (time.Duration, error) { return testStatusTTL, nil }, func(int) bool { return false })
	var canceled *WatchCanceledError
	if !errors.As(err, &canceled) {
		t.Fatalf("relayAdmitted = %v, want *WatchCanceledError", err)
	}
}

// TestRelayAdmitted_StopEndsAWaitingDelivery: stop ends the relay while a
// delivery waits, with no error.
func TestRelayAdmitted_StopEndsAWaitingDelivery(t *testing.T) {
	in := make(chan int, 1)
	in <- 1
	stop := make(chan struct{})
	admitted := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- relayAdmitted(in, make(chan int), stop, clocktest.New(), func() (time.Duration, error) { admitted <- struct{}{}; return testStatusTTL, nil }, func(int) bool { return false })
	}()
	<-admitted
	close(stop)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("relayAdmitted = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not end the relay")
	}
}

// TestDeliverTerminal_HandsOverOnlyTerminalItems: when the source ends, a
// held reason still reaches the consumer and held data does not.
func TestDeliverTerminal_HandsOverOnlyTerminalItems(t *testing.T) {
	out := make(chan WatchEntityEvent)
	held := []WatchEntityEvent{{ID: "u1"}, {Canceled: true, CancelReason: "compacted"}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		deliverTerminal(held, out, make(chan struct{}), func(ev WatchEntityEvent) bool { return ev.Canceled })
	}()
	if ev := <-out; !ev.Canceled || ev.CancelReason != "compacted" {
		t.Fatalf("delivered %+v, want only the terminal event", ev)
	}
	<-done
}

// TestDeliverTerminal_StopEndsTheHandOver: stop ends a terminal hand-over
// its consumer is not taking.
func TestDeliverTerminal_StopEndsTheHandOver(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	deliverTerminal([]int{1}, make(chan int), stop, func(int) bool { return true })
}

func TestDeliverAdmitted_HandsOverInOrderAndDiscardsUnadmitted(t *testing.T) {
	in := make(chan int, 1)
	in <- 99
	out := make(chan int)
	done := make(chan struct{})
	go func() {
		defer close(done)
		deliverAdmitted([]int{1, 2}, in, out, make(chan struct{}), nil, func(int) bool { return false })
	}()
	for want := 1; want <= 2; want++ {
		if got := <-out; got != want {
			t.Fatalf("delivered %d, want %d", got, want)
		}
	}
	<-done
}

func TestDeliverAdmitted_LapseDropsTheRest(t *testing.T) {
	out := make(chan int)
	lapsed := make(chan time.Time, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		deliverAdmitted([]int{1, 2}, make(chan int), out, make(chan struct{}), lapsed, func(int) bool { return false })
	}()
	if got := <-out; got != 1 {
		t.Fatalf("delivered %d, want 1", got)
	}
	lapsed <- time.Time{}
	<-done
}

func TestDeliverAdmitted_InEndDropsData(t *testing.T) {
	in := make(chan WatchEntityEvent)
	close(in)
	held := []WatchEntityEvent{{ID: "u1"}}
	deliverAdmitted(held, in, nil, make(chan struct{}), nil, func(ev WatchEntityEvent) bool { return ev.Canceled })
}

func TestDeliverAdmitted_StopEndsTheHandOver(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	deliverAdmitted([]int{1}, make(chan int), make(chan int), stop, nil, func(int) bool { return false })
}
