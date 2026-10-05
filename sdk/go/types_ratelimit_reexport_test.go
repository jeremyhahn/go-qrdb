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

package sdk_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// This file is deliberately in the EXTERNAL test package (sdk_test), for
// the same reason types_dao_reexport_test.go is: an identifier missing
// from the re-export fails to COMPILE here exactly as it would for a real
// consumer (go-xkms).

// TestSDK_TokenBucketOptionsCrossTheBoundaryUnderTheRateLimitPrefix proves
// sdk.WithRateLimitIdleTTL and sdk.WithRateLimitSweepInterval -- NOT the
// upstream pkg/ratelimit.WithIdleTTL/WithSweepInterval names, which name no
// rate-limiting concept on their own and would read as belonging to
// whichever other subsystem's idle-eviction option gets added next to the
// SDK root -- construct a working sdk.TokenBucketLimiter.
func TestSDK_TokenBucketOptionsCrossTheBoundaryUnderTheRateLimitPrefix(t *testing.T) {
	limiter := sdk.NewTokenBucketLimiter(
		sdk.WithRateLimitIdleTTL(time.Minute),
		sdk.WithRateLimitSweepInterval(time.Millisecond),
	)
	require.NotNil(t, limiter)
	defer func() { require.NoError(t, limiter.Close()) }()

	var limiterIface sdk.RateLimiter = limiter
	allowed, _ := limiterIface.Allow("k", sdk.RateLimitPolicy{RatePerSecond: 1, Burst: 1})
	require.True(t, allowed, "a fresh bucket must admit its first request")
}
