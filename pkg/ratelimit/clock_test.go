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

package ratelimit_test

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// TestWithClock_RefillFollowsTheInjectedClock: the bucket refills as the
// composer's clock advances, with no wall-clock time passing.
func TestWithClock_RefillFollowsTheInjectedClock(t *testing.T) {
	fake := clocktest.New()
	l := ratelimit.NewTokenBucketLimiter(ratelimit.WithClock(fake), ratelimit.WithSweepInterval(time.Hour))
	t.Cleanup(func() { _ = l.Close() })
	policy := ratelimit.Policy{RatePerSecond: 1, Burst: 1}

	if ok, _ := l.Allow("k", policy); !ok {
		t.Fatal("the first request was refused")
	}
	if ok, _ := l.Allow("k", policy); ok {
		t.Fatal("the second request inside the burst window was admitted")
	}
	fake.Advance(2 * time.Second)
	if ok, _ := l.Allow("k", policy); !ok {
		t.Fatal("the bucket did not refill after the injected clock advanced")
	}
}

func TestWithClock_NilKeepsTheWallClock(t *testing.T) {
	l := ratelimit.NewTokenBucketLimiter(ratelimit.WithClock(nil), ratelimit.WithSweepInterval(time.Hour))
	t.Cleanup(func() { _ = l.Close() })
	policy := ratelimit.Policy{RatePerSecond: 1, Burst: 1}

	if ok, _ := l.Allow("k", policy); !ok {
		t.Fatal("the first request was refused")
	}
	if ok, _ := l.Allow("k", policy); ok {
		t.Fatal("the second request inside the burst window was admitted")
	}
}
