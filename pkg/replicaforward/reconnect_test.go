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

package replicaforward

import (
	"context"
	"testing"
	"time"
)

func TestReconnect_AFailedAttemptIsMadeAgainUntilOneReaches(t *testing.T) {
	attempts := 0
	reached := Reconnect(context.Background(), 10*time.Second, func() bool {
		attempts++
		return attempts == 3
	})
	if !reached || attempts != 3 {
		t.Fatalf("Reconnect = %v after %d attempts; want the third attempt to reach the target", reached, attempts)
	}
}

func TestReconnect_TheFirstAttemptThatReachesEndsIt(t *testing.T) {
	attempts := 0
	start := time.Now()
	if !Reconnect(context.Background(), 10*time.Second, func() bool { attempts++; return true }) || attempts != 1 {
		t.Fatalf("attempts = %d; want one", attempts)
	}
	if waited := time.Since(start); waited >= ReconnectBaseDelay {
		t.Fatalf("waited %v before a first attempt that reached; want no wait", waited)
	}
}

func TestReconnect_GivesUpWhenTheBudgetHasPassed(t *testing.T) {
	const budget = 300 * time.Millisecond
	attempts := 0
	start := time.Now()
	if Reconnect(context.Background(), budget, func() bool { attempts++; return false }) {
		t.Fatal("Reconnect reached a target no attempt reached")
	}
	if attempts < 2 {
		t.Fatalf("attempts = %d; want the failed attempt made again within the budget", attempts)
	}
	if waited := time.Since(start); waited < budget {
		t.Fatalf("gave up after %v; want the whole budget of %v spent", waited, budget)
	}
}

func TestReconnect_AnEndedContextStopsTheWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	if Reconnect(ctx, time.Hour, func() bool { attempts++; cancel(); return false }) {
		t.Fatal("Reconnect reached a target no attempt reached")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d after the context ended; want one", attempts)
	}

	deadline, cancelDeadline := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancelDeadline()
	start := time.Now()
	if Reconnect(deadline, time.Hour, func() bool { return false }) {
		t.Fatal("Reconnect reached a target no attempt reached")
	}
	if waited := time.Since(start); waited > 10*time.Second {
		t.Fatalf("waited %v; want the wait bounded by the context, not the budget", waited)
	}
}

func TestJittered_StaysWithinTheJitter(t *testing.T) {
	const d = time.Second
	lo, hi := time.Duration(float64(d)*(1-ReconnectJitter)), time.Duration(float64(d)*(1+ReconnectJitter))
	for range 1000 {
		if got := jittered(d); got < lo || got > hi {
			t.Fatalf("jittered(%v) = %v; want within [%v, %v]", d, got, lo, hi)
		}
	}
}

func TestConfig_ConnectTimeoutIsHalfTheForwards(t *testing.T) {
	if got := (Config{Timeout: 4 * time.Second}).ConnectTimeout(); got != 2*time.Second {
		t.Fatalf("ConnectTimeout = %v; want half of 4s", got)
	}
	if got := (Config{}).ConnectTimeout(); got != DefaultTimeout/2 {
		t.Fatalf("ConnectTimeout = %v; want half the default", got)
	}
}
