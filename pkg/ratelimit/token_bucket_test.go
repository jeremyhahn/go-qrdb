// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock for deterministic token-bucket
// tests: real time never has to pass for a refill to be observed.
type fakeClock struct {
	now atomic.Int64 // UnixNano
}

func newFakeClock(start time.Time) *fakeClock {
	c := &fakeClock{}
	c.now.Store(start.UnixNano())
	return c
}

func (c *fakeClock) Now() time.Time {
	return time.Unix(0, c.now.Load())
}

func (c *fakeClock) Advance(d time.Duration) {
	c.now.Add(int64(d))
}

func newTestLimiter(t *testing.T, clock *fakeClock) *TokenBucketLimiter {
	t.Helper()
	l := NewTokenBucketLimiter(withClock(clock.Now), WithSweepInterval(time.Millisecond))
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestTokenBucketLimiter_AllowWithinBurst(t *testing.T) {
	clock := newFakeClock(time.Now())
	l := newTestLimiter(t, clock)
	policy := Policy{RatePerSecond: 1, Burst: 3}

	for i := 0; i < 3; i++ {
		allowed, retryAfter := l.Allow("caller-a", policy)
		if !allowed {
			t.Fatalf("request %d: want allowed, got denied (retryAfter=%v)", i, retryAfter)
		}
		if retryAfter != 0 {
			t.Fatalf("request %d: want zero retryAfter on an allowed request, got %v", i, retryAfter)
		}
	}
}

func TestTokenBucketLimiter_DeniesOverBurst(t *testing.T) {
	clock := newFakeClock(time.Now())
	l := newTestLimiter(t, clock)
	policy := Policy{RatePerSecond: 1, Burst: 2}

	for i := 0; i < 2; i++ {
		if allowed, _ := l.Allow("caller-b", policy); !allowed {
			t.Fatalf("request %d: want allowed (within burst), got denied", i)
		}
	}

	allowed, retryAfter := l.Allow("caller-b", policy)
	if allowed {
		t.Fatal("want the request past the burst to be denied")
	}
	if retryAfter <= 0 {
		t.Fatalf("want a positive retryAfter for a denied request, got %v", retryAfter)
	}
}

func TestTokenBucketLimiter_RefillsOverTime(t *testing.T) {
	clock := newFakeClock(time.Now())
	l := newTestLimiter(t, clock)
	policy := Policy{RatePerSecond: 2, Burst: 1}

	if allowed, _ := l.Allow("caller-c", policy); !allowed {
		t.Fatal("want the first request within a fresh burst to be allowed")
	}
	if allowed, _ := l.Allow("caller-c", policy); allowed {
		t.Fatal("want the second request to be denied before any refill")
	}

	// At 2 tokens/sec, half a second refills exactly one token.
	clock.Advance(500 * time.Millisecond)

	if allowed, _ := l.Allow("caller-c", policy); !allowed {
		t.Fatal("want a request to be allowed once the bucket has refilled")
	}
}

func TestTokenBucketLimiter_RetryAfterNamesTheWait(t *testing.T) {
	clock := newFakeClock(time.Now())
	l := newTestLimiter(t, clock)
	policy := Policy{RatePerSecond: 1, Burst: 1}

	if allowed, _ := l.Allow("caller-d", policy); !allowed {
		t.Fatal("want the first request to be allowed")
	}
	_, retryAfter := l.Allow("caller-d", policy)

	// At 1 token/sec starting from zero tokens, the deficit is one full
	// token, so retryAfter must be close to one second.
	if retryAfter < 900*time.Millisecond || retryAfter > 1100*time.Millisecond {
		t.Fatalf("want retryAfter near 1s for a 1 token/sec policy at zero tokens, got %v", retryAfter)
	}
}

func TestTokenBucketLimiter_KeysAreIndependent(t *testing.T) {
	clock := newFakeClock(time.Now())
	l := newTestLimiter(t, clock)
	policy := Policy{RatePerSecond: 1, Burst: 1}

	if allowed, _ := l.Allow("tenant-a\x00alice\x00kv:put", policy); !allowed {
		t.Fatal("want alice's first request allowed")
	}
	if allowed, _ := l.Allow("tenant-a\x00alice\x00kv:put", policy); allowed {
		t.Fatal("want alice's second request (over burst) denied")
	}
	// A different caller's bucket must not have been touched by alice's
	// exhaustion -- this is the property that makes throttling per-caller
	// rather than global.
	if allowed, _ := l.Allow("tenant-a\x00bob\x00kv:put", policy); !allowed {
		t.Fatal("want bob's first request allowed despite alice's bucket being empty")
	}
}

func TestTokenBucketLimiter_InvalidPolicyIsAlwaysDenied(t *testing.T) {
	clock := newFakeClock(time.Now())
	l := newTestLimiter(t, clock)

	tests := []struct {
		name   string
		policy Policy
	}{
		{"zero rate", Policy{RatePerSecond: 0, Burst: 5}},
		{"negative rate", Policy{RatePerSecond: -1, Burst: 5}},
		{"zero burst", Policy{RatePerSecond: 5, Burst: 0}},
		{"negative burst", Policy{RatePerSecond: 5, Burst: -1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if allowed, retryAfter := l.Allow("caller-e", tc.policy); allowed || retryAfter != 0 {
				t.Fatalf("want an invalid policy to deny with zero retryAfter, got allowed=%v retryAfter=%v", allowed, retryAfter)
			}
		})
	}
}

// TestTokenBucketLimiter_ReapEvictsIdleBuckets drives the reaper with an
// explicit tick instead of a wall-clock ticker, so nothing waits on real time.
// The tick channel is unbuffered: the second send cannot complete until the
// reaper has returned from the first sweep and is back in its select, which is
// what makes the eviction observable deterministically.
func TestTokenBucketLimiter_ReapEvictsIdleBuckets(t *testing.T) {
	clock := newFakeClock(time.Now())
	ticks := make(chan time.Time)
	l := NewTokenBucketLimiter(withClock(clock.Now), WithIdleTTL(time.Second), withSweepTicks(ticks))
	t.Cleanup(func() { _ = l.Close() })
	policy := Policy{RatePerSecond: 1, Burst: 1}

	if allowed, _ := l.Allow("caller-f", policy); !allowed {
		t.Fatal("want the first request allowed")
	}
	// Consume the burst so a survived bucket would still refuse the next
	// request; only reclamation (a fresh bucket at full burst) explains an
	// allow after this point.
	if allowed, _ := l.Allow("caller-f", policy); allowed {
		t.Fatal("want the second request denied before any reap")
	}

	// A sweep before the bucket is idle keeps it.
	ticks <- time.Time{}
	ticks <- time.Time{}
	if _, loaded := l.buckets.Load("caller-f"); !loaded {
		t.Fatal("a bucket touched within idleTTL was reclaimed")
	}

	clock.Advance(2 * time.Second)
	ticks <- time.Time{}
	ticks <- time.Time{}
	if _, loaded := l.buckets.Load("caller-f"); loaded {
		t.Fatal("want the idle bucket reclaimed by the reaper")
	}
	if allowed, _ := l.Allow("caller-f", policy); !allowed {
		t.Fatal("a reclaimed key must start again at full burst")
	}
}

// TestTokenBucketLimiter_RefillIsCappedAtBurst: however long a key sits idle,
// it never accrues more than burst tokens.
func TestTokenBucketLimiter_RefillIsCappedAtBurst(t *testing.T) {
	clock := newFakeClock(time.Now())
	l := newTestLimiter(t, clock)
	policy := Policy{RatePerSecond: 10, Burst: 3}

	if allowed, _ := l.Allow("caller-h", policy); !allowed {
		t.Fatal("want the first request allowed")
	}
	clock.Advance(time.Hour) // 36000 tokens' worth of refill
	for i := range 3 {
		if allowed, _ := l.Allow("caller-h", policy); !allowed {
			t.Fatalf("request %d of the burst was denied", i+1)
		}
	}
	if allowed, _ := l.Allow("caller-h", policy); allowed {
		t.Fatal("an hour idle refilled beyond burst")
	}
}

// TestTake is the decision function on its own: a new key, a spend, a refill
// and a denial with its retry-after, every branch without a goroutine.
func TestTake(t *testing.T) {
	policy := Policy{RatePerSecond: 2, Burst: 2}
	const second = int64(time.Second)

	next, allowed, wait := take(nil, 0, policy)
	if !allowed || wait != 0 || next.tokens != 1 {
		t.Fatalf("new key: next=%+v allowed=%v wait=%v, want 1 token left and allowed", next, allowed, wait)
	}
	next, allowed, _ = take(next, 0, policy)
	if !allowed || next.tokens != 0 {
		t.Fatalf("second spend: next=%+v allowed=%v", next, allowed)
	}
	denied, allowed, wait := take(next, 0, policy)
	if allowed || wait != 500*time.Millisecond || denied.tokens != 0 {
		t.Fatalf("empty bucket: next=%+v allowed=%v wait=%v, want denied with 500ms", denied, allowed, wait)
	}
	refilled, allowed, _ := take(denied, second/2, policy)
	if !allowed || refilled.tokens != 0 || refilled.lastRefill != second/2 || refilled.lastAccess != second/2 {
		t.Fatalf("half a second later: next=%+v allowed=%v, want one token earned and spent", refilled, allowed)
	}
	capped, _, _ := take(refilled, 100*second, policy)
	if capped.tokens != 1 {
		t.Fatalf("long idle: tokens=%v, want burst-1", capped.tokens)
	}
}

func TestTokenBucketLimiter_CloseStopsReaper(t *testing.T) {
	l := NewTokenBucketLimiter(WithSweepInterval(time.Millisecond))
	if err := l.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
	// A second Close must not panic or block -- shutdown paths that call
	// Close from more than one place must be safe.
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: unexpected error: %v", err)
	}
}

func TestTokenBucketLimiter_ConcurrentAllowNeverExceedsBurst(t *testing.T) {
	clock := newFakeClock(time.Now())
	l := newTestLimiter(t, clock)
	policy := Policy{RatePerSecond: 1, Burst: 10}

	var allowedCount atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if allowed, _ := l.Allow("caller-g", policy); allowed {
				allowedCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := allowedCount.Load(); got != int64(policy.Burst) {
		t.Fatalf("want exactly burst (%d) requests allowed out of 100 concurrent callers, got %d", policy.Burst, got)
	}
}
