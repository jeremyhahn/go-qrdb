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
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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
	// The tenant is normalized: an empty TenantID IS the system tenant, and
	// keying on the raw value would give one system-tenant caller two
	// buckets -- "" and tenantid.System -- to alternate between.
	return tenantid.Normalize(identity.TenantID) + "\x00" + identity.Subject + "\x00" + perm.String()
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

// CheckLoginRateLimit enforces perm's declared Policy against a login
// attempt's CLAIMED principal, before any authenticator chain has run and so
// before there is a real auth.Identity to key checkRateLimit against. It is
// the one seam a pre-authentication login ceremony (the embedded
// transport's BeginLogin/FinishLogin, sdk/go/transport/embedded/login.go)
// uses to reach the SAME rate-limit engine and the SAME per-(tenant,
// subject, permission) bucket authorize()'s checkRateLimit enforces for an
// authenticated caller -- there is no second limiter and no second key
// scheme, only a second place identity comes from.
//
// principal must be the RESOLVED account a login attempt is against (e.g.
// auth.Principal.Username), never a caller-supplied hint: a hint is
// attacker-controlled before any proof is checked, so keying on it would
// let an attacker pick a fresh, unthrottled bucket for every guess by
// varying the hint while still targeting the same account. tenantID is
// normalized the same way every other tenant value is
// (tenantid.Normalize), so "" and the system tenant's canonical id share
// one bucket.
//
// A nil receiver, no limiter, no declared policies, or no Policy registered
// for perm is a no-op, exactly like checkRateLimit.
//
// A refusal is audited -- the same compliance property authorize() gives
// every other decision (go-qrdb#335): a login rate-limit refusal is itself
// a security-relevant event (credential-stuffing, a compromised account
// being hammered), and leaving it unrecorded was the one authorize()-style
// decision in this package with no audit trail at all. The nil-identity
// guard mirrors authorize()'s own: identity is never nil here (it is
// constructed two lines above), but auditDecision's contract is checked at
// its call site, not assumed.
func (m *AuthMiddleware) CheckLoginRateLimit(ctx context.Context, tenantID, principal string, perm permission.Permission) error {
	if m == nil {
		return nil
	}
	identity := &auth.Identity{Subject: principal, TenantID: tenantid.Normalize(tenantID)}
	err := m.checkRateLimit(identity, perm)
	if err == nil {
		return nil
	}
	if auditErr := m.auditDecision(ctx, identity, perm, err); auditErr != nil {
		return auditErr
	}
	return err
}
