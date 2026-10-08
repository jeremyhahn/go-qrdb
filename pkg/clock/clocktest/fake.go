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

// Package clocktest provides Fake, a clock.Clock whose time moves only when a
// test advances it.
//
// A test that exercises expiry builds a Fake, hands it to the component under
// test, waits with BlockUntil for the component to have armed its timer or
// ticker, and calls Advance. Nothing in that sequence depends on wall-clock
// time, so it is exact on a loaded machine and instantaneous on an idle one.
package clocktest

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock"
)

// epoch is the instant a Fake built with New starts at. It is fixed so a test's
// timestamps are reproducible; it is deliberately not time.Now.
var epoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// Fake is a manually advanced clock.Clock. All methods are safe for concurrent
// use.
//
// A Fake delivers on a timer or ticker channel the way the time package does: a
// buffer of one, and a send that is dropped when the receiver has not drained
// the previous one. Advance over a window longer than a ticker's interval
// therefore leaves one tick waiting, not one per interval.
type Fake struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*waiter
	// changed is closed and replaced whenever the set of waiters changes, which
	// is what BlockUntil waits on.
	changed chan struct{}
}

var _ clock.Clock = (*Fake)(nil)

// waiter is one armed timer, ticker or sleep.
type waiter struct {
	when   time.Time
	period time.Duration // zero for a one-shot
	ch     chan time.Time
}

// New returns a Fake that starts at a fixed instant.
func New() *Fake { return NewAt(epoch) }

// NewAt returns a Fake that starts at t.
func NewAt(t time.Time) *Fake {
	return &Fake{now: t, changed: make(chan struct{})}
}

// Now returns the fake's current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Since returns the time elapsed since t on the fake's clock.
func (f *Fake) Since(t time.Time) time.Duration { return f.Now().Sub(t) }

// Until returns the duration until t on the fake's clock.
func (f *Fake) Until(t time.Time) time.Duration { return t.Sub(f.Now()) }

// Sleep blocks until the fake has been advanced by at least d. A non-positive d
// returns immediately.
func (f *Fake) Sleep(d time.Duration) {
	if d <= 0 {
		return
	}
	<-f.After(d)
}

// After returns a channel that receives the fake's time once it has been
// advanced by d.
func (f *Fake) After(d time.Duration) <-chan time.Time {
	return f.arm(d, 0).ch
}

// NewTimer returns a Timer that fires once the fake has been advanced by d.
func (f *Fake) NewTimer(d time.Duration) clock.Timer {
	return &fakeTimer{fake: f, w: f.arm(d, 0)}
}

// NewTicker returns a Ticker that fires each time the fake has been advanced by
// d. A d that is not positive panics, as it does for time.NewTicker.
func (f *Fake) NewTicker(d time.Duration) clock.Ticker {
	if d <= 0 {
		panic("clocktest: non-positive interval for NewTicker")
	}
	return &fakeTicker{fake: f, w: f.arm(d, d)}
}

// Advance moves the fake forward by d, firing every timer and ticker that comes
// due on the way in the order of their deadlines. A timer that is due fires with
// the fake reading its own deadline, so a receiver that reads Now sees the
// moment its timer was set for rather than the end of the window. A non-positive
// d is a no-op.
func (f *Fake) Advance(d time.Duration) {
	if d <= 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	target := f.now.Add(d)
	for {
		next := f.nextDueLocked(target)
		if next == nil {
			break
		}
		f.now = next.when
		f.fireLocked(next)
	}
	f.now = target
}

// Waiters returns the number of timers, tickers and sleeps currently armed.
func (f *Fake) Waiters() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.waiters)
}

// BlockUntil returns once at least n timers, tickers or sleeps are armed on the
// fake, or with ctx's error if ctx ends first. A test calls it after starting a
// goroutine that waits on the clock and before Advance, so the advance cannot
// land before the goroutine has armed its wait.
func (f *Fake) BlockUntil(ctx context.Context, n int) error {
	for {
		f.mu.Lock()
		if len(f.waiters) >= n {
			f.mu.Unlock()
			return nil
		}
		changed := f.changed
		f.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// arm registers a waiter due d from now. A d that is not positive is due at the
// current instant and fires on the next Advance, however small.
func (f *Fake) arm(d, period time.Duration) *waiter {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := &waiter{when: f.now.Add(d), period: period, ch: make(chan time.Time, 1)}
	f.addLocked(w)
	return w
}

func (f *Fake) addLocked(w *waiter) {
	f.waiters = append(f.waiters, w)
	f.notifyLocked()
}

// removeLocked disarms w, reporting whether it was armed.
func (f *Fake) removeLocked(w *waiter) bool {
	for i, armed := range f.waiters {
		if armed == w {
			f.waiters = append(f.waiters[:i], f.waiters[i+1:]...)
			f.notifyLocked()
			return true
		}
	}
	return false
}

func (f *Fake) notifyLocked() {
	close(f.changed)
	f.changed = make(chan struct{})
}

// nextDueLocked returns the armed waiter with the earliest deadline at or
// before target, or nil. Ties go to the one armed first, so equal deadlines fire
// in a stable order.
func (f *Fake) nextDueLocked(target time.Time) *waiter {
	due := make([]*waiter, 0, len(f.waiters))
	for _, w := range f.waiters {
		if !w.when.After(target) {
			due = append(due, w)
		}
	}
	if len(due) == 0 {
		return nil
	}
	sort.SliceStable(due, func(i, j int) bool { return due[i].when.Before(due[j].when) })
	return due[0]
}

// fireLocked delivers w's tick and either re-arms it, for a ticker, or disarms
// it. The send never blocks: the channel holds one value and a full one is
// skipped.
func (f *Fake) fireLocked(w *waiter) {
	select {
	case w.ch <- f.now:
	default:
	}
	if w.period > 0 {
		w.when = w.when.Add(w.period)
		return
	}
	f.removeLocked(w)
}

type fakeTimer struct {
	fake *Fake
	w    *waiter
}

func (t *fakeTimer) C() <-chan time.Time { return t.w.ch }

func (t *fakeTimer) Stop() bool {
	t.fake.mu.Lock()
	defer t.fake.mu.Unlock()
	return t.fake.removeLocked(t.w)
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.fake.mu.Lock()
	defer t.fake.mu.Unlock()
	active := t.fake.removeLocked(t.w)
	// Drain a fired-but-unread tick so the reset timer does not deliver a stale
	// one, as time.Timer.Reset does since Go 1.23.
	select {
	case <-t.w.ch:
	default:
	}
	t.w.when = t.fake.now.Add(d)
	t.fake.addLocked(t.w)
	return active
}

type fakeTicker struct {
	fake *Fake
	w    *waiter
}

func (t *fakeTicker) C() <-chan time.Time { return t.w.ch }

func (t *fakeTicker) Stop() {
	t.fake.mu.Lock()
	defer t.fake.mu.Unlock()
	t.fake.removeLocked(t.w)
}

func (t *fakeTicker) Reset(d time.Duration) {
	if d <= 0 {
		panic("clocktest: non-positive interval for Ticker.Reset")
	}
	t.fake.mu.Lock()
	defer t.fake.mu.Unlock()
	t.fake.removeLocked(t.w)
	t.w.period = d
	t.w.when = t.fake.now.Add(d)
	t.fake.addLocked(t.w)
}
