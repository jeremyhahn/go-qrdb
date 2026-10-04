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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// ---------------------------------------------------------------------------
// Centralized request-throttling (go-qrdb#398, go-qrdb#201)
//
// A module declares a Policy for a permission once, in a Registry, and the
// auth middleware enforces it at the one place every transport already
// funnels through to authorize a request -- identically on REST, gRPC, the
// unix-socket gRPC listener, native QUIC, and MCP. Re-exported so go-xkms
// can retire its own fourteen independent limiter implementations in favour
// of this one without importing pkg/ratelimit directly.
// ---------------------------------------------------------------------------

// RateLimitPolicy declares a token-bucket rate limit for one permission:
// tokens are added continuously at RatePerSecond, up to a maximum of Burst
// held at once.
type RateLimitPolicy = ratelimit.Policy

// RateLimiter enforces per-key request throttling under a RateLimitPolicy.
// NewTokenBucketLimiter constructs the built-in implementation.
type RateLimiter = ratelimit.Limiter

// RateLimitRegistry is the single-source per-permission RateLimitPolicy
// declaration a module registers into with Register, and the auth
// middleware reads from via WithRateLimitPolicies (sdk/go/node) /
// wiring.AuthMiddlewareConfig.RateLimitPolicies.
type RateLimitRegistry = ratelimit.Registry

// NewRateLimitRegistry creates an empty RateLimitRegistry.
var NewRateLimitRegistry = ratelimit.NewRegistry

// TokenBucketLimiter is the built-in RateLimiter: a token bucket per key,
// swept for idle entries so a caller that stops making requests does not
// hold memory forever.
type TokenBucketLimiter = ratelimit.TokenBucketLimiter

// NewTokenBucketLimiter constructs a TokenBucketLimiter.
var NewTokenBucketLimiter = ratelimit.NewTokenBucketLimiter

// TokenBucketOption configures a TokenBucketLimiter at construction.
type TokenBucketOption = ratelimit.TokenBucketOption

// WithIdleTTL overrides how long an idle key's bucket is retained before the
// limiter's sweep reclaims it.
var WithIdleTTL = ratelimit.WithIdleTTL

// WithSweepInterval overrides how often the limiter sweeps idle buckets.
var WithSweepInterval = ratelimit.WithSweepInterval
