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

package clock

import (
	"testing"
	"time"
)

// guard bounds every wait in this file so a broken clock fails the test rather
// than hanging it. It is a failure ceiling, never a value a test depends on.
const guard = 5 * time.Second

func TestReal_NowTracksTheTimePackage(t *testing.T) {
	before := time.Now()
	got := Real().Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("Now() = %v, want within [%v, %v]", got, before, after)
	}
}

func TestReal_SinceAndUntilAreSignedByDirection(t *testing.T) {
	c := Real()
	if d := c.Since(time.Now().Add(-time.Hour)); d < time.Hour {
		t.Errorf("Since(an hour ago) = %v, want at least an hour", d)
	}
	if d := c.Until(time.Now().Add(time.Hour)); d <= 0 || d > time.Hour {
		t.Errorf("Until(in an hour) = %v, want in (0, 1h]", d)
	}
	if d := c.Until(time.Now().Add(-time.Hour)); d >= 0 {
		t.Errorf("Until(an hour ago) = %v, want negative", d)
	}
}

func TestReal_SleepWaitsAtLeastTheDuration(t *testing.T) {
	const d = 2 * time.Millisecond
	start := time.Now()
	Real().Sleep(d)
	if elapsed := time.Since(start); elapsed < d {
		t.Fatalf("Sleep(%v) returned after %v", d, elapsed)
	}
}

func TestReal_AfterDelivers(t *testing.T) {
	select {
	case <-Real().After(time.Millisecond):
	case <-time.After(guard):
		t.Fatal("After never fired")
	}
}

func TestReal_TimerFiresStopsAndResets(t *testing.T) {
	timer := Real().NewTimer(time.Hour)
	if !timer.Stop() {
		t.Fatal("Stop on an armed timer = false, want true")
	}
	if timer.Stop() {
		t.Fatal("Stop on a stopped timer = true, want false")
	}
	if timer.Reset(time.Millisecond) {
		t.Fatal("Reset on a stopped timer = true, want false")
	}
	select {
	case <-timer.C():
	case <-time.After(guard):
		t.Fatal("reset timer never fired")
	}
}

func TestReal_TickerTicksAndResets(t *testing.T) {
	ticker := Real().NewTicker(time.Hour)
	defer ticker.Stop()
	ticker.Reset(time.Millisecond)
	for range 2 {
		select {
		case <-ticker.C():
		case <-time.After(guard):
			t.Fatal("reset ticker never ticked")
		}
	}
}
