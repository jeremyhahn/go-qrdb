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
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// StreamAdmitter re-runs the per-operation tenant admission check for an
// identity that was authenticated when a long-lived stream opened. The auth
// middleware's AdmitStream satisfies it: it reads the tenant's status
// through the shared status cache, passes the system tenant, and fails
// closed when the status cannot be read. On admission it answers how long
// the admission holds: no longer than the status it was decided against is
// served, which for an answer from a cache entry about to expire is only
// what is left of that entry.
type StreamAdmitter interface {
	AdmitStream(ctx context.Context, identity *auth.Identity) (time.Duration, error)
}

// admittedWatch wraps a WatchServicer so that every watch it opens
// re-admits the opening identity before each event is forwarded. Admission
// at the transport runs once, when the stream opens; without this a watch
// opened before its tenant was suspended would keep delivering after it.
//
// A refused watch ends with the admitter's typed error: Watch and
// WatchPrefix close their event channel and report it from Err, which
// every transport already maps the way it maps a refused request;
// WatchEntity and WatchEntityType deliver one terminal Canceled event
// carrying the reason, the same shape any reasoned end takes.
//
// The check runs on delivery. A stream with nothing to deliver holds no
// data the tenant may no longer read, and is refused on its next event. A
// delivery its consumer is not taking keeps both guards while it waits (see
// relayAdmitted): the admission is renewed each time it lapses, and the
// inner stream ending -- a seal, a drop, a refusal beneath -- ends it.
type admittedWatch struct {
	WatchServicer
	admitter StreamAdmitter
	clock    clock.Clock
}

// admittedBacklog bounds the responses a relay holds while its consumer is
// not reading. The relay keeps reading the inner stream so that its end is
// seen at once; a consumer that lets more than this queue up is ended with
// a WatchCanceledError rather than buffered without limit.
const admittedBacklog = 256

// NewAdmittedWatch wraps svc so each event is admitted for the identity
// in the opening context before it is forwarded. It returns svc unchanged
// when svc or admitter is nil.
func NewAdmittedWatch(svc WatchServicer, admitter StreamAdmitter) WatchServicer {
	return newAdmittedWatch(svc, admitter, clock.Real())
}

// newAdmittedWatch is NewAdmittedWatch timing admission lapses on clk.
func newAdmittedWatch(svc WatchServicer, admitter StreamAdmitter, clk clock.Clock) WatchServicer {
	if svc == nil || admitter == nil {
		return svc
	}
	return &admittedWatch{WatchServicer: svc, admitter: admitter, clock: clk}
}

// admit returns the admission check for the identity ctx carries. The
// check runs under a context detached from the stream's cancellation, so
// its outcome reflects the tenant rather than a client that hung up; the
// admitter bounds it with its own timeout. A context with no identity is
// refused by the admitter.
func (a *admittedWatch) admit(ctx context.Context) func() (time.Duration, error) {
	identity := auth.IdentityFromContext(ctx)
	checkCtx := context.WithoutCancel(ctx)
	return func() (time.Duration, error) { return a.admitter.AdmitStream(checkCtx, identity) }
}

// Watch opens a key watch whose events are each admitted before delivery.
func (a *admittedWatch) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	inner, err := a.WatchServicer.Watch(ctx, key, opts...)
	if err != nil {
		return nil, err
	}
	return newAdmittedWatcher(inner, a.clock, a.admit(ctx)), nil
}

// WatchPrefix opens a prefix watch whose events are each admitted before
// delivery.
func (a *admittedWatch) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	inner, err := a.WatchServicer.WatchPrefix(ctx, prefix, opts...)
	if err != nil {
		return nil, err
	}
	return newAdmittedWatcher(inner, a.clock, a.admit(ctx)), nil
}

// WatchEntity opens an entity watch whose events are each admitted before
// delivery.
func (a *admittedWatch) WatchEntity(ctx context.Context, req *WatchEntityRequest) (<-chan WatchEntityEvent, error) {
	return a.admitEntityWatch(ctx, req.EntityType, func(watchCtx context.Context) (<-chan WatchEntityEvent, error) {
		return a.WatchServicer.WatchEntity(watchCtx, req)
	})
}

// WatchEntityType opens an entity-type watch whose events are each
// admitted before delivery.
func (a *admittedWatch) WatchEntityType(ctx context.Context, req *WatchEntityTypeRequest) (<-chan WatchEntityEvent, error) {
	return a.admitEntityWatch(ctx, req.EntityType, func(watchCtx context.Context) (<-chan WatchEntityEvent, error) {
		return a.WatchServicer.WatchEntityType(watchCtx, req)
	})
}

// admitEntityWatch opens the underlying entity watch under a context of its
// own and relays it through admitEntityEvents, which cancels that context
// when the relay ends: a refusal releases the underlying subscription at
// once, not when the stream's context ends, which a caller still holding the
// closed channel may never do.
func (a *admittedWatch) admitEntityWatch(ctx context.Context, entityType EntityType, open func(context.Context) (<-chan WatchEntityEvent, error)) (<-chan WatchEntityEvent, error) {
	watchCtx, release := context.WithCancel(ctx)
	in, err := open(watchCtx)
	if err != nil {
		release()
		return nil, err
	}
	return admitEntityEvents(ctx, entityType, in, a.clock, a.admit(ctx), release), nil
}

// admitEntityEvents relays in to the returned channel through
// relayAdmitted. A refusal delivers one terminal Canceled event carrying the
// admitter's error and closes the channel. However the relay ends, it calls
// release, which cancels the context the producer of in was opened under.
func admitEntityEvents(ctx context.Context, entityType EntityType, in <-chan WatchEntityEvent, clk clock.Clock, admit func() (time.Duration, error), release context.CancelFunc) <-chan WatchEntityEvent {
	out := make(chan WatchEntityEvent)
	go func() {
		defer close(out)
		defer release()
		terminal := func(ev WatchEntityEvent) bool { return ev.Canceled }
		if err := relayAdmitted(in, out, ctx.Done(), clk, admit, terminal); err != nil {
			sendTerminalEntityEvent(ctx, entityType, err.Error(), out)
		}
	}()
	return out
}

// relayAdmitted relays in to out, admitting each item as it arrives, until
// in closes or stop does; it returns the error that ended it otherwise.
//
// A delivery out is not taking is not left parked: while anything waits,
// the relay keeps reading in, holding what arrives in arrival order, so
// in's end is seen while the consumer is still not reading. In ends the
// relay at once and what it held is dropped -- data in admitted to a
// stream that has since ended, by a seal or anything else, is not handed
// over afterwards -- save the terminal items among it (terminal reports
// them), which carry a reason and no data. And an admission covers a
// waiting delivery only for as long as admit answers it holds -- what is
// left of the status-cache entry it was decided against, not a fresh
// lifetime of its own: each time it lapses the identity is admitted again,
// so whatever the consumer finally takes was admitted against a status the
// cache would itself still serve, and a suspension refuses it no later than
// the status cache's TTL after the change. A refused item ends the relay
// only once what it held is handed over (see deliverAdmitted): those were
// admitted before the refusal and reach the consumer ahead of its end.
// Holding more than admittedBacklog items ends the relay with a
// WatchCanceledError.
func relayAdmitted[T any](in <-chan T, out chan<- T, stop <-chan struct{}, clk clock.Clock, admit func() (time.Duration, error), terminal func(T) bool) error {
	lapse := clk.NewTimer(0)
	lapse.Stop()
	defer lapse.Stop()
	var held []T
	for {
		var send chan<- T
		var head T
		var lapsed <-chan time.Time
		if len(held) > 0 {
			send, head, lapsed = out, held[0], lapse.C()
		}
		select {
		case <-stop:
			return nil
		case v, ok := <-in:
			if !ok {
				deliverTerminal(held, out, stop, terminal)
				return nil
			}
			if len(held) == admittedBacklog {
				return &WatchCanceledError{Reason: "watch consumer fell behind"}
			}
			holds, err := admit()
			if err != nil {
				deliverAdmitted(held, in, out, stop, lapse.C(), terminal)
				return err
			}
			lapse.Reset(holds)
			held = append(held, v)
		case send <- head:
			var zero T
			held[0] = zero
			held = held[1:]
		case <-lapsed:
			holds, err := admit()
			if err != nil {
				return err
			}
			lapse.Reset(holds)
		}
	}
}

// deliverAdmitted hands out held, in order, before a refusal ends the relay:
// each item was admitted before the refused one arrived, so the consumer
// takes it ahead of the end, as it would have had it been reading. The
// items keep the admission they were held under and nothing more: when it
// lapses the rest is dropped, and in ending drops it as it does in
// relayAdmitted, save the terminal items. What in delivers meanwhile was
// never admitted and is discarded. Stop ends the hand-over at once.
func deliverAdmitted[T any](held []T, in <-chan T, out chan<- T, stop <-chan struct{}, lapsed <-chan time.Time, terminal func(T) bool) {
	for len(held) > 0 {
		select {
		case <-stop:
			return
		case <-lapsed:
			return
		case _, ok := <-in:
			if !ok {
				deliverTerminal(held, out, stop, terminal)
				return
			}
		case out <- held[0]:
			held = held[1:]
		}
	}
}

// deliverTerminal hands out the terminal items of held, in order, until
// stop closes.
func deliverTerminal[T any](held []T, out chan<- T, stop <-chan struct{}, terminal func(T) bool) {
	for _, v := range held {
		if !terminal(v) {
			continue
		}
		select {
		case out <- v:
		case <-stop:
			return
		}
	}
}

// admittedWatcher forwards an inner watcher's responses, admitting each
// one first. A refusal records the admitter's error, closes the inner
// watcher and closes Events, so Err reports the refusal.
type admittedWatcher struct {
	inner  watch.Watcher
	events chan watch.WatchResponse
	stop   chan struct{}
	once   sync.Once
	errMu  sync.Mutex
	err    error
}

var _ watch.Watcher = (*admittedWatcher)(nil)

func newAdmittedWatcher(inner watch.Watcher, clk clock.Clock, admit func() (time.Duration, error)) *admittedWatcher {
	w := &admittedWatcher{
		inner:  inner,
		events: make(chan watch.WatchResponse),
		stop:   make(chan struct{}),
	}
	go w.forward(clk, admit)
	return w
}

// forward relays the inner watcher's responses through relayAdmitted until
// it closes, the watcher is closed, or the relay is refused. However the
// relay ends, it closes the inner watcher, releasing what it holds at once
// rather than when the caller gets round to Close.
func (w *admittedWatcher) forward(clk clock.Clock, admit func() (time.Duration, error)) {
	defer close(w.events)
	terminal := func(resp watch.WatchResponse) bool { return resp.Canceled && len(resp.Events) == 0 }
	if err := relayAdmitted(w.inner.Events(), w.events, w.stop, clk, admit, terminal); err != nil {
		w.errMu.Lock()
		w.err = err
		w.errMu.Unlock()
	}
	_ = w.inner.Close()
}

// Events returns the admitted responses; the channel closes when the
// watch ends.
func (w *admittedWatcher) Events() <-chan watch.WatchResponse { return w.events }

// WatchID returns the inner watcher's ID.
func (w *admittedWatcher) WatchID() int64 { return w.inner.WatchID() }

// Close ends the watch.
func (w *admittedWatcher) Close() error {
	w.once.Do(func() { close(w.stop) })
	return w.inner.Close()
}

// Err returns the admission refusal when one ended the watch, and the
// inner watcher's error otherwise.
func (w *admittedWatcher) Err() error {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	if w.err != nil {
		return w.err
	}
	return w.inner.Err()
}
