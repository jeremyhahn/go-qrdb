# Rate Limiting — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

## Exported surface

`pkg/ratelimit`:

```go
type Policy struct {
	RatePerSecond float64
	Burst         int
}

type Limiter interface {
	Allow(key string, policy Policy) (allowed bool, retryAfter time.Duration)
	Close() error
}

type Registry struct{ /* unexported */ }

func NewRegistry() *Registry
func (r *Registry) Register(perm permission.Permission, policy Policy)
func (r *Registry) Lookup(perm permission.Permission) (Policy, bool)
func (r *Registry) Entries() map[permission.Permission]Policy

type TokenBucketLimiter struct{ /* unexported */ }

func NewTokenBucketLimiter(opts ...TokenBucketOption) *TokenBucketLimiter
func WithIdleTTL(ttl time.Duration) TokenBucketOption
func WithSweepInterval(interval time.Duration) TokenBucketOption
```

`pkg/auth/middleware`:

```go
func WithRateLimiter(l ratelimit.Limiter) Option
func WithRateLimitPolicies(policies *ratelimit.Registry) Option

type RateLimitedError struct {
	Permission permission.Permission
	Subject    string
	TenantID   string
	RetryAfter time.Duration
}
```

`pkg/auth/wiring`:

```go
type AuthMiddlewareConfig struct {
	// ... existing fields ...
	RateLimitPolicies *ratelimit.Registry
	RateLimiter       ratelimit.Limiter
}
```

`sdk/go` (an embedder consumes go-qrdb exclusively through `sdk/go`, so
every type and constructor above is re-exported, under the `RateLimit*`
prefix for the two ambiguous names):

```go
type RateLimitPolicy   = ratelimit.Policy
type RateLimitRegistry = ratelimit.Registry
type RateLimiter       = ratelimit.Limiter
type TokenBucketLimiter = ratelimit.TokenBucketLimiter
type TokenBucketOption  = ratelimit.TokenBucketOption

func NewRateLimitRegistry() *RateLimitRegistry
func NewTokenBucketLimiter(opts ...TokenBucketOption) *TokenBucketLimiter
func WithRateLimitIdleTTL(ttl time.Duration) TokenBucketOption
func WithRateLimitSweepInterval(interval time.Duration) TokenBucketOption
```

`sdk/go/node`:

```go
func WithRateLimitPolicies(policies *ratelimit.Registry) NodeOption
func WithRateLimiter(l ratelimit.Limiter) NodeOption
```

Both thread into the same `wiring.AuthMiddlewareConfig` the embedded node's
`wireClientAndServer` builds for `cmd/qrdbd` itself, so a Policy declared
through the SDK is enforced identically on every transport the node's auth
middleware fronts.

## Wiring

`BuildAuthMiddleware` installs rate limiting **only** when
`cfg.RateLimitPolicies` is non-nil. When it is set and `cfg.RateLimiter` is
nil, `BuildAuthMiddleware` constructs the production
`ratelimit.NewTokenBucketLimiter()`; supplying `cfg.RateLimiter` reuses an
already-running instance (its own bucket set and reaper goroutine) instead of
starting a second one. `cfg.RateLimiter` is ignored when
`cfg.RateLimitPolicies` is nil.

| `RateLimitPolicies` | `RateLimiter` | Result |
|---|---|---|
| nil | (ignored) | no rate limiting installed — behavior unchanged from before this feature |
| non-nil | nil | production `TokenBucketLimiter` constructed and installed |
| non-nil | non-nil | the supplied `Limiter` installed, no new instance constructed |

### The invitation-redeem and holder-act limiters

Two further throttles are not part of `AuthMiddleware`: invitation redemption
and share-holder acts (the ceremony's quorum challenges and the embedded
transport's anonymous holder claims). Each defaults to a built-in token bucket
and takes a `ratelimit.Limiter` from the composer instead:
`server.WithInvitationRedeemLimiter` / `node.WithInvitationRedeemLimiter` and
`server.WithHolderActLimiter` / `node.WithHolderActLimiter`. Both reach
`lifecycle.Infra` in the standalone and the clustered boot paths. The redeem
limiter is asked once per source address and once per tenant, each under its own
key prefix and the policy `auth.invitation_redeem_limit` resolves. The built-in
buckets read the node's `clock.Clock` (`WithClock`); a supplied limiter owns its
own time. The caller that supplies a limiter closes it.

## Enforcement point

`AuthMiddleware.authorize` — the single function every transport's
authorization call funnels through (`RESTMiddleware`, `UnaryInterceptor`
shared by gRPC and the unix-socket listener, `AuthorizeQUIC`, `AuthorizeMCP`)
— runs the rate-limit check first, ahead of the `IdentityGated` short-circuit
and the tenant-IAM-sealed allowance, whenever both `Limiter` and `Registry`
are configured and the caller's identity is non-nil. It is a no-op (falls
through unchanged) when either is nil, or when `perm` carries no `Policy` in
the `Registry`.

## Invariants

- **Rate limiting never runs against a nil identity.** `authorize`'s only
  caller path that reaches it with `identity == nil` is the
  `permission.IdentityGated` case, which returns `auth.ErrUnauthenticated`
  before any permission-checking logic runs; every other caller of
  `authorize` has already established a non-nil identity.
- **An unregistered permission is unlimited, not denied.** `Registry.Lookup`
  returning `ok == false` means "no policy," and `checkRateLimit` treats that
  identically to rate limiting being unconfigured at all: it returns nil.
- **A nil `Registry` answers unlimited for every permission**, and a nil
  receiver's `Entries()` returns nil — both matching "rate limiting not
  configured," so a caller holding a possibly-nil `*Registry` needs no
  separate nil check before calling either method.
- **`Registry.Register` panics** on an empty permission, a non-positive
  `RatePerSecond` or `Burst`, or a duplicate registration for the same
  permission. All three are construction-time bugs: an accidentally-invalid
  or accidentally-overwritten `Policy` must fail loudly at startup, not
  silently change behavior for every caller of that permission from then on.
- **The bucket key is `tenantid.Normalize(tenantID) + "\x00" + subject + "\x00" + permission`.**
  The NUL separator cannot appear in any of the three components, so it
  cannot be forged into colliding two distinct triples into one bucket. The
  tenant is normalized because an empty `TenantID` IS the system tenant
  (`tenantid.IsSystem`): keying on the raw value would give one system-tenant
  caller two buckets -- `""` and `tenantid.System` -- to alternate between and
  double its effective budget.
- **`TokenBucketLimiter.Allow` is lock-free**: it never blocks on a mutex,
  only retries its own compare-and-swap. A `Policy` with a non-positive
  `RatePerSecond` or `Burst` is always refused (`allowed == false`,
  `retryAfter == 0`) rather than dividing by zero or admitting unbounded
  traffic.
- **The reaper only ever makes the next request more permissive, never
  less.** A bucket concurrently touched by `Allow` at the instant the
  reaper's sweep reads it stale can be reclaimed just after that `Allow`
  call committed; the next `Allow` for that key then starts a fresh bucket
  at full burst. This is documented and accepted: rate limiting is a
  capacity control, not a security boundary, so trading a rare extra token
  for a sweep that never locks against `Allow` is the correct side of that
  trade.

## Error and status mapping

| Transport | Mapping |
|---|---|
| REST | `mapHTTPAuthError` returns **429** for `*RateLimitedError`; `RESTMiddleware` sets the `Retry-After` header (RFC 9110 §10.2.3, delay-seconds, rounded up to at least 1) before writing the response |
| gRPC / unix | `mapGRPCAuthError` returns `codes.ResourceExhausted`. The unix-socket listener registers the same `UnaryInterceptor`, so a caller over its unix-transport quota sees the identical code a TCP gRPC caller would. |
| QUIC | `authmw.AuthFailureRateLimited` maps to `wire.StatusResourceExhausted` (`pkg/api/quic/auth.go`); the frame carries `RateLimitedError.Error()` verbatim. *(Corrected 2026-10-06: this said QUIC had no status-code plane for authorization outcomes.)* |
| MCP | The tool-authorization middleware returns `RESOURCE_EXHAUSTED` (−32008) in the tool result's error envelope, alongside the same error text it always returned. The code comes from `authmw.GRPCError`, the classifier REST and gRPC answer from, so the auth-failure classes keep their own codes instead of collapsing into "retryable or not" -- rate-limited is never confused with, say, a credential problem. (Two of the classes, `AuthFailureUnavailable` and `AuthFailureAuditUnavailable`, deliberately share `Unavailable` regardless: that pair exists for control flow inside `pkg/auth/middleware`, not for the wire, so every transport answers both alike.) |

## Test strategy

**Integration target:** `make integration-test-rate-limit` (no Docker; selection in `test/integration/features.mk`). The feature has no non-Docker integration test yet, so the target runs zero tests and says so; this is a recorded gap (the `rate-limit` row marks it), not a pass. Enforcement is proved by the unit tests below. Per-transport accounting is the `rate-limit` row of `test/integration/COVERAGE.md`.

- **Unit, `pkg/ratelimit`**: burst admission and refusal, refill over a
  simulated clock, retry-after computation, independent keys, invalid-policy
  refusal, idle-bucket reclamation, concurrent `Allow` under `-race` never
  exceeding burst, `Registry` panics for each invalid-construction case, and
  a nil-`Registry` unlimited-lookup case. **No unit test waits on real
  time.** The decision is a pure function (`take`: current state, now,
  policy in; next state, verdict and retry-after out), tested branch by
  branch, and `Allow`'s compare-and-swap loop only retries it. The
  refill cap is exercised by an hour of simulated idle. The reaper is driven by
  explicit ticks (`withSweepTicks`) on an unbuffered channel, so a second tick
  proves the first sweep finished. The package's coverage is therefore the same
  on every run; only the two unreachable type-assertion panics are uncovered.
- **Unit, `pkg/auth/middleware`**: `checkRateLimit` no-op paths (unconfigured
  limiter, unconfigured registry, unregistered permission), denial and its
  error fields, per-caller independence, an identity with an empty `TenantID`
  sharing the system tenant's bucket rather than a second one obtained by
  alternating `""` and `tenantid.System`, `authorize` running the rate-limit
  check ahead of the RBAC authorizer (asserted by counting the authorizer's
  own calls), the `IdentityGated`-with-nil-identity path not panicking, the
  REST 429 status and `Retry-After` header, and the gRPC
  `codes.ResourceExhausted` mapping through a real `UnaryInterceptor` call
  against a permission resolved from the compiled proto descriptor (not a
  test-only stub).
- **Unit, `pkg/auth/wiring`**: the three-row wiring table above, each as its
  own test — unconfigured stays unthrottled, `RateLimitPolicies` alone
  constructs and enforces the default limiter, and a supplied `RateLimiter`
  is the one consulted rather than a fresh default.
- **Mutation discipline**: every integration-level test in this feature
  (the `authorize`-ordering test, the REST 429 test, the gRPC
  `ResourceExhausted` test) was confirmed to fail when the enforcement call
  inside `authorize` was disabled, and to pass once restored — the
  unit-level `checkRateLimit` tests, which call the helper directly rather
  than through `authorize`, were confirmed to keep passing throughout, since
  they are not testing the wiring.

## Known gaps

- **No YAML/CLI configuration surface.** `Policy` values are declared in Go
  code, at `Registry.Register` call sites, not read from `pkg/config` or a
  daemon flag. See [cli.md](cli.md).
- **The default `TokenBucketLimiter` a `BuildAuthMiddleware` call constructs
  is never explicitly closed.** `AuthMiddleware` has no `Close`/shutdown
  lifecycle hook today, so the reaper goroutine a default limiter starts
  runs for the life of the process — acceptable for a daemon whose node
  lifetime is the process lifetime, but an embedder that repeatedly
  constructs and discards `AuthMiddleware` instances (for example, an
  in-process test harness that stands up many short-lived nodes) should
  supply its own `Limiter` via `RateLimiter` and own its `Close()` call,
  rather than rely on the default.
