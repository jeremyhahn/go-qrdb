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

package middleware

import (
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// WithRateLimiter installs the enforcement engine behind every transport's
// authorize() call (go-qrdb#201). Nil -- the default when this option is
// never applied -- runs no rate-limit check at all: zero behavior change for
// an embedder that does not opt in, and zero cost beyond one nil comparison
// per authorize() call for one that installs a limiter but declares no
// policies via WithRateLimitPolicies.
//
// ratelimit.NewTokenBucketLimiter is the production implementation; tests
// and embedders needing different semantics may supply any ratelimit.Limiter.
func WithRateLimiter(l ratelimit.Limiter) Option {
	return func(m *AuthMiddleware) {
		m.limiter = l
	}
}

// WithRateLimitPolicies installs the single-source per-permission Policy
// declarations (ratelimit.Registry) that WithRateLimiter enforces. A
// permission with no registered Policy is unlimited -- see ratelimit.Registry
// for why that is opt-in rather than default-deny.
func WithRateLimitPolicies(policies *ratelimit.Registry) Option {
	return func(m *AuthMiddleware) {
		m.limits = policies
	}
}

// rateLimitKey scopes one token bucket to one caller's use of one
// permission. Both TenantID and Subject are part of the key -- not Subject
// alone -- because two tenants may legitimately share a subject name (the
// same username enrolled independently in two tenants is not the same
// caller), and Permission is part of it so that one caller's heavy use of
// one operation cannot exhaust a quota that another operation would still
// have been happy to grant it.
//
// The NUL separator cannot appear in a TenantID, Subject, or
// permission.Permission (all are constrained to printable identifier
// characters elsewhere in the stack), so it cannot be forged by a crafted
// subject name colliding two distinct triples into the same bucket.
func rateLimitKey(identity *auth.Identity, perm permission.Permission) string {
	return identity.TenantID + "\x00" + identity.Subject + "\x00" + perm.String()
}

// checkRateLimit enforces the Policy declared for perm, if any, against
// identity's own bucket. It is a no-op whenever rate limiting is not wired
// (m.limiter or m.limits is nil) or perm carries no declared Policy.
//
// Callers must have already established that identity is non-nil; authorize
// -- the only caller -- does so before invoking this method.
func (m *AuthMiddleware) checkRateLimit(identity *auth.Identity, perm permission.Permission) error {
	if m.limiter == nil || m.limits == nil {
		return nil
	}
	policy, ok := m.limits.Lookup(perm)
	if !ok {
		return nil
	}
	if allowed, retryAfter := m.limiter.Allow(rateLimitKey(identity, perm), policy); !allowed {
		return &RateLimitedError{
			Permission: perm,
			Subject:    identity.Subject,
			TenantID:   identity.TenantID,
			RetryAfter: retryAfter,
		}
	}
	return nil
}
