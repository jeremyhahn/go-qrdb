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

//go:build !integration

package node

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// TestEmbeddedNode_RateLimitPolicies_ThrottlesTheNPlusOnethCall is go-qrdb#398's
// verification, run against the real SDK surface: declare a Policy for one
// permission, build the node's auth middleware with it (WithRateLimitPolicies,
// over the real REST transport via the same wiring.BuildAuthMiddleware call
// cmd/qrdbd uses), and observe the limiter refuse the caller once its burst is
// spent.
func TestEmbeddedNode_RateLimitPolicies_ThrottlesTheNPlusOnethCall(t *testing.T) {
	policies := ratelimit.NewRegistry()
	// Burst of 1: the first request in any given window is admitted, and the
	// second is refused before the sustained rate would replenish a token.
	policies.Register(permission.AdminStatus, ratelimit.Policy{RatePerSecond: 0.001, Burst: 1})

	n, ca, adminKP := newAuthEnforcedRESTNode(t, WithRateLimitPolicies(policies))
	addr := n.Server().RESTAddr()
	require.NotEmpty(t, addr)

	clientCert, err := adminKP.TLSCertificate()
	require.NoError(t, err)

	first := restStatusRequest(t, addr, ca, &clientCert)
	require.Equal(t, http.StatusOK, first, "the first request must be admitted (burst=1)")

	second := restStatusRequest(t, addr, ca, &clientCert)
	require.Equal(t, http.StatusTooManyRequests, second,
		"the second request must be throttled once the burst is spent")
}

// TestEmbeddedNode_RateLimiter_OverridesTheEnforcementEngine proves
// WithRateLimiter's own contract: a caller-supplied ratelimit.Limiter runs
// in place of the built-in TokenBucketLimiter. A limiter that refuses
// EVERY request makes this observable without depending on token-bucket
// timing at all.
func TestEmbeddedNode_RateLimiter_OverridesTheEnforcementEngine(t *testing.T) {
	policies := ratelimit.NewRegistry()
	policies.Register(permission.AdminStatus, ratelimit.Policy{RatePerSecond: 1000, Burst: 1000})

	n, ca, adminKP := newAuthEnforcedRESTNode(t,
		WithRateLimitPolicies(policies),
		WithRateLimiter(alwaysDenyLimiter{}),
	)
	addr := n.Server().RESTAddr()
	require.NotEmpty(t, addr)

	clientCert, err := adminKP.TLSCertificate()
	require.NoError(t, err)

	status := restStatusRequest(t, addr, ca, &clientCert)
	require.Equal(t, http.StatusTooManyRequests, status,
		"a Policy with ample headroom must still be refused under a Limiter that denies everything")
}

// alwaysDenyLimiter is a ratelimit.Limiter that refuses every request,
// regardless of Policy -- enough to prove WithRateLimiter's override takes
// effect without depending on token-bucket timing.
type alwaysDenyLimiter struct{}

func (alwaysDenyLimiter) Allow(string, ratelimit.Policy) (allowed bool, retryAfter time.Duration) {
	return false, time.Second
}

func (alwaysDenyLimiter) Close() error { return nil }
