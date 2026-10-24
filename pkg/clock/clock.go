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

// Package clock is the one injectable time seam of go-qrdb.
//
// Code whose behaviour depends on the passage of time -- a lease expiring, a
// grace period running out, a retention window closing, a keep-alive cadence --
// takes a Clock instead of calling the time package's clock functions. In
// production that Clock is Real, which is the time package. In a test it is a
// clocktest.Fake, whose time moves only when the test says so, so an expiry that
// takes ten seconds of wall-clock time is exercised in microseconds and cannot
// flake on a loaded machine. That is what makes the rule "no time.Sleep in
// tests" enforceable: a test that must wait for time to pass advances the fake
// instead.
//
// The seam covers reading the time (Now, Since, Until) and waiting on it (Sleep,
// After, NewTimer, NewTicker) and bounding a call by it (WithTimeout):
// context.WithTimeout runs on the runtime's clock, which a Clock cannot reach,
// so a component that bounds a call by its Clock uses WithTimeout instead.
package clock

import "time"

// Clock is a source of time and of waits on it.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// Since returns the time elapsed since t.
	Since(t time.Time) time.Duration
	// Until returns the duration until t.
	Until(t time.Time) time.Duration
	// Sleep pauses the calling goroutine for at least d.
	Sleep(d time.Duration)
	// After waits for d to elapse and then sends the current time on the
	// returned channel. Unlike a Timer it cannot be stopped.
	After(d time.Duration) <-chan time.Time
	// NewTimer creates a Timer that fires once after d.
	NewTimer(d time.Duration) Timer
	// NewTicker creates a Ticker that fires every d. d must be positive.
	NewTicker(d time.Duration) Ticker
}

// Timer is a single event, the Clock-backed counterpart of time.Timer.
type Timer interface {
	// C returns the channel the timer's time is sent on when it fires.
	C() <-chan time.Time
	// Stop prevents the timer from firing. It reports whether the call stopped
	// the timer; false means it had already fired or been stopped.
	Stop() bool
	// Reset re-arms the timer to fire after d, reporting whether it had been
	// active.
	Reset(d time.Duration) bool
}

// Ticker delivers ticks at an interval, the Clock-backed counterpart of
// time.Ticker.
type Ticker interface {
	// C returns the channel ticks are sent on.
	C() <-chan time.Time
	// Stop turns the ticker off. No tick is sent after it returns.
	Stop()
	// Reset changes the interval to d. d must be positive.
	Reset(d time.Duration)
}

// Real returns the Clock backed by the time package. It is stateless and safe
// for concurrent use.
func Real() Clock { return realClock{} }

type realClock struct{}

var _ Clock = realClock{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) Since(t time.Time) time.Duration        { return time.Since(t) }
func (realClock) Until(t time.Time) time.Duration        { return time.Until(t) }
func (realClock) Sleep(d time.Duration)                  { time.Sleep(d) }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) NewTimer(d time.Duration) Timer         { return realTimer{timer: time.NewTimer(d)} }
func (realClock) NewTicker(d time.Duration) Ticker       { return realTicker{ticker: time.NewTicker(d)} }

type realTimer struct{ timer *time.Timer }

func (t realTimer) C() <-chan time.Time        { return t.timer.C }
func (t realTimer) Stop() bool                 { return t.timer.Stop() }
func (t realTimer) Reset(d time.Duration) bool { return t.timer.Reset(d) }

type realTicker struct{ ticker *time.Ticker }

func (t realTicker) C() <-chan time.Time   { return t.ticker.C }
func (t realTicker) Stop()                 { t.ticker.Stop() }
func (t realTicker) Reset(d time.Duration) { t.ticker.Reset(d) }
