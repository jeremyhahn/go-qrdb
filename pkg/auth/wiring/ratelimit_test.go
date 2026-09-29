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

package wiring

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestBuildAuthMiddleware_NoRateLimitPoliciesIsUnthrottled proves the
// default posture: an AuthMiddlewareConfig that never sets
// RateLimitPolicies enforces no throttling at all, identical to the
// pre-go-qrdb#201 behavior, however many times a granted permission is
// exercised.
func TestBuildAuthMiddleware_NoRateLimitPoliciesIsUnthrottled(t *testing.T) {
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:     true,
		RoleService: adminRoleService(),
		UserService: &fakeUserService{},
	})
	require.NoError(t, err)
	require.NotNil(t, mw)

	for i := 0; i < 20; i++ {
		ctx := auth.WithIdentity(context.Background(), adminIdentity())
		require.NoError(t, mw.AuthorizeTenant(ctx, tenantid.System, permission.AdminStatus))
	}
}

// TestBuildAuthMiddleware_RateLimitPoliciesEnforced proves that supplying
// RateLimitPolicies is sufficient by itself -- BuildAuthMiddleware
// constructs the production TokenBucketLimiter with no RateLimiter override
// -- and that the resulting AuthMiddleware refuses a caller past its burst
// with the transport-agnostic *middleware.RateLimitedError.
func TestBuildAuthMiddleware_RateLimitPoliciesEnforced(t *testing.T) {
	policies := ratelimit.NewRegistry()
	policies.Register(permission.AdminStatus, ratelimit.Policy{RatePerSecond: 1, Burst: 1})

	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:           true,
		RoleService:       adminRoleService(),
		UserService:       &fakeUserService{},
		RateLimitPolicies: policies,
	})
	require.NoError(t, err)
	require.NotNil(t, mw)

	ctx := auth.WithIdentity(context.Background(), adminIdentity())
	require.NoError(t, mw.AuthorizeTenant(ctx, tenantid.System, permission.AdminStatus))

	ctx2 := auth.WithIdentity(context.Background(), adminIdentity())
	err = mw.AuthorizeTenant(ctx2, tenantid.System, permission.AdminStatus)
	require.Error(t, err)
	var rateLimited *middleware.RateLimitedError
	assert.True(t, errors.As(err, &rateLimited), "want *middleware.RateLimitedError, got %T: %v", err, err)
}

// TestBuildAuthMiddleware_RateLimiterOverrideIsUsed proves that a caller
// supplying its own ratelimit.Limiter (e.g. one shared with another
// subsystem) has it installed instead of a fresh TokenBucketLimiter -- the
// override, not just RateLimitPolicies, has to be threaded through.
func TestBuildAuthMiddleware_RateLimiterOverrideIsUsed(t *testing.T) {
	policies := ratelimit.NewRegistry()
	policies.Register(permission.AdminStatus, ratelimit.Policy{RatePerSecond: 1, Burst: 1})
	spy := &spyLimiter{}

	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:           true,
		RoleService:       adminRoleService(),
		UserService:       &fakeUserService{},
		RateLimitPolicies: policies,
		RateLimiter:       spy,
	})
	require.NoError(t, err)
	require.NotNil(t, mw)

	ctx := auth.WithIdentity(context.Background(), adminIdentity())
	require.NoError(t, mw.AuthorizeTenant(ctx, tenantid.System, permission.AdminStatus))

	assert.Equal(t, int32(1), spy.calls, "want the supplied limiter, not a fresh default, to have been consulted")
}

// spyLimiter is a ratelimit.Limiter that always allows, recording how many
// times it was asked -- enough to prove BuildAuthMiddleware wired the
// supplied instance through rather than constructing its own.
type spyLimiter struct {
	calls int32
}

func (s *spyLimiter) Allow(string, ratelimit.Policy) (bool, time.Duration) {
	s.calls++
	return true, 0
}

func (s *spyLimiter) Close() error { return nil }
