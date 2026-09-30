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
	"time"
)

// defaultIdleTTL is how long a per-key bucket may sit unused before the
// reaper reclaims it. Ten minutes comfortably outlasts any Policy's refill
// window this package expects to enforce (sub-second to low-minutes rates),
// so a caller that pauses and resumes never sees state it did not itself
// build up, while a caller that stops entirely does not hold memory forever.
const defaultIdleTTL = 10 * time.Minute

// defaultSweepInterval is how often the reaper scans for idle buckets.
// One tenth of defaultIdleTTL bounds the worst-case memory overrun (an idle
// bucket outlives its TTL by at most one sweep interval) without scanning
// the whole key set often enough to matter next to the request rate a
// production node serves.
const defaultSweepInterval = defaultIdleTTL / 10

// bucketState is one caller's token bucket at a point in time. It is
// immutable once constructed: TokenBucketLimiter.Allow replaces it wholesale
// via atomic.Pointer.CompareAndSwap rather than mutating fields in place, so
// concurrent callers racing on the same key never observe a torn read
// between tokens and lastRefill.
type bucketState struct {
	tokens     float64
	lastRefill int64 // UnixNano
	lastAccess int64 // UnixNano; read only by the reaper
}

// TokenBucketLimiter is a lock-free, per-key token-bucket Limiter. Each
// key's bucket is an *atomic.Pointer[bucketState] stored in a sync.Map;
// Allow refills and consumes it through a compare-and-swap retry loop, never
// a mutex -- the same "atomic over unsafe pointers, lock-free preferred"
// posture the rest of go-qrdb's hot paths take.
//
// The zero value is not usable; construct with NewTokenBucketLimiter.
type TokenBucketLimiter struct {
	buckets sync.Map // string -> *atomic.Pointer[bucketState]

	idleTTL       time.Duration
	sweepInterval time.Duration
	clock         func() time.Time
	// ticks, when non-nil, drives the reaper instead of a wall-clock ticker
	// on sweepInterval. Only this package's tests set it (withSweepTicks), so
	// a test decides exactly when a sweep happens instead of waiting for one.
	ticks <-chan time.Time

	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// TokenBucketOption configures a TokenBucketLimiter constructed by
// NewTokenBucketLimiter.
type TokenBucketOption func(*TokenBucketLimiter)

// WithIdleTTL overrides how long an unused bucket is kept before the
// background reaper reclaims it. Non-positive values are ignored (the
// default applies) rather than disabling the reaper -- a Limiter that never
// reclaims memory is not a configuration this package offers, because every
// production embedder of it serves long-lived, ever-growing caller sets.
func WithIdleTTL(ttl time.Duration) TokenBucketOption {
	return func(l *TokenBucketLimiter) {
		if ttl > 0 {
			l.idleTTL = ttl
		}
	}
}

// WithSweepInterval overrides how often the reaper scans for idle buckets.
// Non-positive values are ignored.
func WithSweepInterval(interval time.Duration) TokenBucketOption {
	return func(l *TokenBucketLimiter) {
		if interval > 0 {
			l.sweepInterval = interval
		}
	}
}

// withClock overrides the limiter's notion of "now". Unexported: it exists
// for this package's own deterministic tests, not for production callers --
// a Limiter whose clock an embedder could desynchronize from wall time would
// throttle against a rate that is no longer the one it reports.
func withClock(now func() time.Time) TokenBucketOption {
	return func(l *TokenBucketLimiter) {
		l.clock = now
	}
}

// withSweepTicks drives the reaper from ticks instead of a wall-clock ticker.
// Unexported for the same reason as withClock: it exists so this package's
// tests can trigger a sweep explicitly rather than sleep until one happens.
func withSweepTicks(ticks <-chan time.Time) TokenBucketOption {
	return func(l *TokenBucketLimiter) {
		l.ticks = ticks
	}
}

// NewTokenBucketLimiter constructs a TokenBucketLimiter and starts its
// background idle-bucket reaper. Callers must call Close when the limiter is
// no longer needed to stop the reaper goroutine.
func NewTokenBucketLimiter(opts ...TokenBucketOption) *TokenBucketLimiter {
	l := &TokenBucketLimiter{
		idleTTL:       defaultIdleTTL,
		sweepInterval: defaultSweepInterval,
		clock:         time.Now,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
	}
	for _, opt := range opts {
		opt(l)
	}
	go l.reap()
	return l
}

// Allow implements Limiter. It is lock-free: concurrent callers for
// different keys never contend, and concurrent callers for the SAME key
// retry their own compare-and-swap rather than blocking on one another.
func (l *TokenBucketLimiter) Allow(key string, policy Policy) (allowed bool, retryAfter time.Duration) {
	if policy.RatePerSecond <= 0 || policy.Burst <= 0 {
		return false, 0
	}

	slotAny, _ := l.buckets.LoadOrStore(key, new(atomic.Pointer[bucketState]))
	slot, ok := slotAny.(*atomic.Pointer[bucketState])
	if !ok {
		// Every value this package ever stores in l.buckets is a
		// *atomic.Pointer[bucketState] -- there is no other writer of this
		// map, so this is a construction-time bug in this package, not a
		// caller-reachable condition.
		panic("ratelimit: bucket slot has unexpected type")
	}

	nowNano := l.clock().UnixNano()
	for {
		cur := slot.Load()
		next, allowed, retryAfter := take(cur, nowNano, policy)
		// A lost race means another caller committed a newer state first;
		// the loop recomputes from that state. The decision itself is take's,
		// so every branch of it is reachable from a single-threaded test.
		if slot.CompareAndSwap(cur, next) {
			return allowed, retryAfter
		}
	}
}

// take is one token-bucket decision, as a pure function: given the bucket's
// current state (nil for a key never seen) at time nowNano, it returns the
// state to commit, whether the request is allowed, and when a denied request
// may retry. Refill is capped at the policy's burst.
func take(cur *bucketState, nowNano int64, policy Policy) (next *bucketState, allowed bool, retryAfter time.Duration) {
	burst := float64(policy.Burst)
	tokens := burst
	if cur != nil {
		elapsed := time.Duration(nowNano - cur.lastRefill)
		tokens = min(cur.tokens+elapsed.Seconds()*policy.RatePerSecond, burst)
	}
	if tokens < 1 {
		deficit := 1 - tokens
		wait := time.Duration(deficit / policy.RatePerSecond * float64(time.Second))
		return &bucketState{tokens: tokens, lastRefill: nowNano, lastAccess: nowNano}, false, wait
	}
	return &bucketState{tokens: tokens - 1, lastRefill: nowNano, lastAccess: nowNano}, true, 0
}

// Close stops the background reaper. Safe to call more than once; only the
// first call has effect.
func (l *TokenBucketLimiter) Close() error {
	l.closeOnce.Do(func() {
		close(l.stop)
		<-l.done
	})
	return nil
}

// reap periodically evicts buckets that have not been touched within
// idleTTL, bounding the memory a caller set that comes and goes (rather than
// staying constant) consumes. It runs until Close closes l.stop.
func (l *TokenBucketLimiter) reap() {
	defer close(l.done)
	ticks := l.ticks
	if ticks == nil {
		ticker := time.NewTicker(l.sweepInterval)
		defer ticker.Stop()
		ticks = ticker.C
	}
	for {
		select {
		case <-l.stop:
			return
		case <-ticks:
			l.sweep()
		}
	}
}

// sweep performs one reaper pass.
//
// A bucket concurrently touched by Allow at the exact instant sweep reads it
// stale can be deleted just after that Allow call committed its own state --
// the next Allow for the same key then finds no entry and starts a fresh
// bucket at full burst instead of the one the racing call just consumed
// from. That only ever makes the NEXT request more permissive, never less:
// rate limiting is a capacity control here, not a security boundary (see
// package doc), so trading a vanishingly rare extra token for a sweep with
// no locking against Allow is the right side of that trade to fail open on.
func (l *TokenBucketLimiter) sweep() {
	deadline := l.clock().Add(-l.idleTTL).UnixNano()
	l.buckets.Range(func(key, value any) bool {
		slot, ok := value.(*atomic.Pointer[bucketState])
		if !ok {
			// See the matching guard in Allow: every value this package
			// stores here has this type, so this cannot happen without a
			// bug in this file.
			panic("ratelimit: bucket slot has unexpected type")
		}
		cur := slot.Load()
		if cur != nil && cur.lastAccess < deadline {
			l.buckets.Delete(key)
		}
		return true
	})
}
