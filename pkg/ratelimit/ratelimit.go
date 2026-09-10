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

// Package ratelimit is the single general-purpose request-throttling engine
// for go-qrdb's middleware core (go-qrdb#201).
//
// Before this package, every service that needed to throttle a caller wrote
// its own limiter: fourteen independent implementations were measured across
// four repositories, eight of them in go-xkms alone, including two files
// that both defined a type named TenantRateLimiter. Auth and audit had
// already been centralized the same way qrdb centralizes everything a
// transport needs (pkg/auth, pkg/auth/middleware, pkg/permission, pkg/audit,
// with every transport a thin adapter); rate limiting was the concern that
// grew in the layers above instead of the core, and it multiplied because
// there was nowhere else for it to go.
//
// Policy is declared once per permission, in a Registry, the same way a
// permission itself is declared once in pkg/permission's global registry --
// not per call site, and not per transport. pkg/auth/middleware enforces the
// declared Policy at the one place every transport already funnels through
// to authorize a request (AuthMiddleware.authorize), so a module that
// declares a Policy for a permission is throttled identically whether the
// call arrived over REST, gRPC, the unix-socket gRPC listener, native QUIC,
// or MCP -- the same property auth and audit already hold.
//
// Rate limiting is deliberately NOT default-deny. Authorization denies a
// permission with no grant because an ungranted permission must never be
// reachable; a permission with no declared Policy is simply unthrottled,
// because most operations need no capacity bound at all and inventing one
// without a real constraint to derive it from is the mistake go-qrdb#231
// made four times over for a request-body bound before settling on deriving
// it from the protocol's own framing. A rate limit has no equivalent
// protocol fact to derive from -- it is a capacity decision an operation's
// owner takes deliberately, one permission at a time, by calling
// Registry.Register.
//
// Genuinely distinct limiters are out of scope and stay where they are: an
// ACME per-tenant issuance quota and an SDN per-peer packet shaper enforce
// domain-specific policy that has nothing to do with "how many times may
// this caller invoke this permission per second," and forcing them through
// this package would not remove duplication, it would misname two different
// problems as one.
package ratelimit

import "time"

// Policy declares a token-bucket rate limit for one permission: tokens are
// added continuously at RatePerSecond, up to a maximum of Burst held at
// once. Burst also bounds how many requests are admitted instantaneously
// after the caller's bucket has been idle -- a caller that has not used an
// operation in a while may still burst up to Burst requests before the
// sustained rate applies.
//
// Both fields are required to be positive; a zero-value Policy declares
// nothing and Registry.Register rejects it, so an accidentally-omitted
// Policy fails at construction rather than admitting or refusing every
// request silently.
type Policy struct {
	// RatePerSecond is the sustained number of requests per second a single
	// caller may make against the permission this Policy is declared for.
	RatePerSecond float64

	// Burst is the maximum number of tokens the bucket holds, and so the
	// largest number of requests a caller may make instantaneously.
	Burst int
}

// Limiter enforces per-key request throttling under a Policy. A key scopes
// one bucket to one caller's use of one permission (see
// pkg/auth/middleware's rateLimitKey); the Limiter itself is
// permission-agnostic and knows nothing about identities, transports, or
// permissions -- it only ever sees the key and the Policy to enforce against
// it.
type Limiter interface {
	// Allow reports whether one permit is available for key under policy,
	// consuming it immediately if so. When allowed is false, retryAfter
	// estimates the minimum wait before the next permit becomes available,
	// computed from the deficit between the bucket's current tokens and the
	// one token being requested.
	//
	// A Policy with a non-positive RatePerSecond or Burst is invalid; an
	// implementation refuses it (allowed is always false) rather than
	// dividing by zero or admitting unbounded traffic. Registry.Register
	// already rejects such a Policy at declaration time, so this is a
	// defense against a Limiter used directly, without a Registry, against a
	// hand-built Policy.
	Allow(key string, policy Policy) (allowed bool, retryAfter time.Duration)

	// Close releases any background resources the Limiter holds (for
	// TokenBucketLimiter, its idle-bucket eviction goroutine). It is safe to
	// call once during shutdown; calling Allow after Close is safe but its
	// buckets will no longer be reaped.
	Close() error
}
