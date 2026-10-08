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

package lifecycle

import (
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// Quorum-challenge throttle policies. A quorum challenge is requested once
// per share submission, so a holder needs a handful a minute at most; the
// policies leave room for that many holders retrying at once while bounding
// what one caller can make the node do.
var (
	// quorumCallerPolicy throttles one identified caller of one scope:
	// an authenticated identity, or the TLS certificate a connection
	// presented.
	quorumCallerPolicy = ratelimit.Policy{RatePerSecond: 2, Burst: 10}

	// quorumAnonymousPolicy throttles everything a scope receives from
	// callers that identify themselves neither way. They share one bucket,
	// so it is wider than one caller's.
	quorumAnonymousPolicy = ratelimit.Policy{RatePerSecond: 5, Burst: 20}
)

// quorumLimiter is the token-bucket engine behind every node's
// quorum-challenge throttle. It is the shared pkg/ratelimit engine, and it
// lives as long as the process: ceremony services are carried across
// in-process data-plane restarts, and its keys name the scope instance, so
// nodes in one process never share a bucket. Idle buckets are reaped.
var quorumLimiter = sync.OnceValue(func() ratelimit.Limiter {
	return ratelimit.NewTokenBucketLimiter()
})

// QuorumLimit is the ceremony.QuorumLimitFunc every composer
// installs: the anonymous bucket under quorumAnonymousPolicy, every
// identified caller under quorumCallerPolicy.
func QuorumLimit(key string, anonymous bool) (bool, time.Duration) {
	policy := quorumCallerPolicy
	if anonymous {
		policy = quorumAnonymousPolicy
	}
	return quorumLimiter().Allow(key, policy)
}

// QuorumLimitWith returns the throttle QuorumLimit applies, over limiter
// instead of the process-wide token bucket. It is how a composer's
// ratelimit.Limiter becomes the holder-act throttle; a nil limiter returns
// QuorumLimit itself, the software default.
func QuorumLimitWith(limiter ratelimit.Limiter) ceremony.QuorumLimitFunc {
	if limiter == nil {
		return QuorumLimit
	}
	return func(key string, anonymous bool) (bool, time.Duration) {
		policy := quorumCallerPolicy
		if anonymous {
			policy = quorumAnonymousPolicy
		}
		return limiter.Allow(key, policy)
	}
}

var _ ceremony.QuorumLimitFunc = QuorumLimit
