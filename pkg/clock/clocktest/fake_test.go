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

package clocktest

import (
	"context"
	"errors"
	"testing"
	"time"
)

// guard bounds every wait so a broken fake fails the test rather than hanging
// it. It is a failure ceiling, never a value a test depends on.
const guard = 5 * time.Second

func recv(t *testing.T, ch <-chan time.Time) time.Time {
	t.Helper()
	select {
	case at := <-ch:
		return at
	case <-time.After(guard):
		t.Fatal("expected a delivery that never came")
		return time.Time{}
	}
}

func assertEmpty(t *testing.T, ch <-chan time.Time) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("unexpected delivery")
	default:
	}
}

func TestNew_StartsAtAFixedInstant(t *testing.T) {
	if a, b := New().Now(), New().Now(); !a.Equal(b) {
		t.Fatalf("two fakes start at %v and %v, want the same instant", a, b)
	}
}

func TestNewAt_StartsWhereAsked(t *testing.T) {
	at := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	if got := NewAt(at).Now(); !got.Equal(at) {
		t.Fatalf("Now() = %v, want %v", got, at)
	}
}

func TestAdvance_MovesNowAndNothingElse(t *testing.T) {
	f := New()
	start := f.Now()
	f.Advance(90 * time.Second)
	if got := f.Since(start); got != 90*time.Second {
		t.Fatalf("Since(start) = %v, want 90s", got)
	}
	if got := f.Until(start.Add(2 * time.Minute)); got != 30*time.Second {
		t.Fatalf("Until(start+2m) = %v, want 30s", got)
	}
}

func TestAdvance_NonPositiveIsANoOp(t *testing.T) {
	f := New()
	start := f.Now()
	f.Advance(0)
	f.Advance(-time.Hour)
	if !f.Now().Equal(start) {
		t.Fatalf("Now() = %v after a no-op advance, want %v", f.Now(), start)
	}
}

func TestTimer_FiresOnlyOnceItsDeadlinePasses(t *testing.T) {
	f := New()
	start := f.Now()
	timer := f.NewTimer(10 * time.Second)

	f.Advance(9 * time.Second)
	assertEmpty(t, timer.C())

	f.Advance(time.Second)
	if at := recv(t, timer.C()); !at.Equal(start.Add(10 * time.Second)) {
		t.Fatalf("timer delivered %v, want its deadline %v", at, start.Add(10*time.Second))
	}
	if f.Waiters() != 0 {
		t.Fatalf("Waiters() = %d after a one-shot fired, want 0", f.Waiters())
	}
}

func TestTimer_StopPreventsFiring(t *testing.T) {
	f := New()
	timer := f.NewTimer(time.Second)
	if !timer.Stop() {
		t.Fatal("Stop on an armed timer = false, want true")
	}
	if timer.Stop() {
		t.Fatal("Stop on a stopped timer = true, want false")
	}
	f.Advance(time.Hour)
	assertEmpty(t, timer.C())
}

func TestTimer_ResetRearmsFromNow(t *testing.T) {
	f := New()
	timer := f.NewTimer(10 * time.Second)
	f.Advance(8 * time.Second)

	if !timer.Reset(10 * time.Second) {
		t.Fatal("Reset on an armed timer = false, want true")
	}
	f.Advance(9 * time.Second) // 17s in: the original deadline is long past
	assertEmpty(t, timer.C())
	f.Advance(time.Second)
	recv(t, timer.C())
}

func TestTimer_ResetDiscardsAnUnreadFiring(t *testing.T) {
	f := New()
	timer := f.NewTimer(time.Second)
	f.Advance(time.Second) // fired, unread
	if timer.Reset(time.Minute) {
		t.Fatal("Reset on a fired timer = true, want false")
	}
	assertEmpty(t, timer.C())
}

func TestAfter_DeliversOnceDue(t *testing.T) {
	f := New()
	ch := f.After(time.Minute)
	f.Advance(59 * time.Second)
	assertEmpty(t, ch)
	f.Advance(time.Second)
	recv(t, ch)
}

func TestTicker_TicksEachIntervalWhileDrained(t *testing.T) {
	f := New()
	start := f.Now()
	ticker := f.NewTicker(time.Second)
	defer ticker.Stop()

	for i := 1; i <= 3; i++ {
		f.Advance(time.Second)
		if at := recv(t, ticker.C()); !at.Equal(start.Add(time.Duration(i) * time.Second)) {
			t.Fatalf("tick %d at %v, want %v", i, at, start.Add(time.Duration(i)*time.Second))
		}
	}
}

func TestTicker_ASlowReceiverGetsOneTickNotOnePerInterval(t *testing.T) {
	f := New()
	ticker := f.NewTicker(time.Second)
	defer ticker.Stop()

	f.Advance(5 * time.Second)
	recv(t, ticker.C())
	assertEmpty(t, ticker.C())
}

func TestTicker_StopEndsTicks(t *testing.T) {
	f := New()
	ticker := f.NewTicker(time.Second)
	ticker.Stop()
	f.Advance(time.Hour)
	assertEmpty(t, ticker.C())
	if f.Waiters() != 0 {
		t.Fatalf("Waiters() = %d after Stop, want 0", f.Waiters())
	}
}

func TestTicker_ResetChangesTheInterval(t *testing.T) {
	f := New()
	ticker := f.NewTicker(time.Second)
	defer ticker.Stop()
	ticker.Reset(time.Minute)

	f.Advance(59 * time.Second)
	assertEmpty(t, ticker.C())
	f.Advance(time.Second)
	recv(t, ticker.C())
}

func TestNewTicker_NonPositiveIntervalPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewTicker(0) did not panic")
		}
	}()
	New().NewTicker(0)
}

func TestTicker_ResetNonPositiveIntervalPanics(t *testing.T) {
	ticker := New().NewTicker(time.Second)
	defer func() {
		if recover() == nil {
			t.Fatal("Reset(0) did not panic")
		}
	}()
	ticker.Reset(0)
}

func TestAdvance_FiresInDeadlineOrder(t *testing.T) {
	f := New()
	late := f.NewTimer(3 * time.Second)
	early := f.NewTimer(time.Second)

	f.Advance(3 * time.Second)

	// Each timer saw the clock at its own deadline, not at the window's end.
	e, l := recv(t, early.C()), recv(t, late.C())
	if !e.Before(l) {
		t.Fatalf("early timer saw %v, late timer %v, want early first", e, l)
	}
}

func TestSleep_ReturnsOnceAdvancedPastTheDuration(t *testing.T) {
	f := New()
	done := make(chan struct{})
	go func() {
		f.Sleep(10 * time.Second)
		close(done)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), guard)
	defer cancel()
	if err := f.BlockUntil(ctx, 1); err != nil {
		t.Fatalf("the sleeper never armed: %v", err)
	}
	f.Advance(9 * time.Second)
	select {
	case <-done:
		t.Fatal("Sleep returned before its duration passed")
	default:
	}
	f.Advance(time.Second)
	select {
	case <-done:
	case <-time.After(guard):
		t.Fatal("Sleep did not return after the clock passed its duration")
	}
}

func TestSleep_NonPositiveReturnsImmediately(t *testing.T) {
	f := New()
	f.Sleep(0)
	f.Sleep(-time.Second)
	if f.Waiters() != 0 {
		t.Fatalf("Waiters() = %d, want 0", f.Waiters())
	}
}

func TestBlockUntil_ReturnsWhenEnoughWaitersArm(t *testing.T) {
	f := New()
	ctx, cancel := context.WithTimeout(context.Background(), guard)
	defer cancel()

	armed := make(chan error, 1)
	go func() { armed <- f.BlockUntil(ctx, 2) }()

	f.NewTimer(time.Second)
	f.NewTicker(time.Second)
	if err := <-armed; err != nil {
		t.Fatalf("BlockUntil = %v, want nil", err)
	}
}

func TestBlockUntil_ReturnsTheContextErrorWhenItEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New().BlockUntil(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("BlockUntil = %v, want context.Canceled", err)
	}
}
