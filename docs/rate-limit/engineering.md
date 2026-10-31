# Rate Limiting — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-27
> Conflicts with this document are defects. Fix them here first.

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
func WithClock(c clock.Clock) TokenBucketOption
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
	// ... other fields ...
	RateLimitPolicies *ratelimit.Registry
	RateLimiter       ratelimit.Limiter
}
```

`sdk/go` (an embedder consumes go-qrdb exclusively through `sdk/go`, so
the `pkg/ratelimit` surface above, except `WithClock`, is re-exported, with
the `RateLimit` prefix where the bare name would be ambiguous):

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

Both thread into the same `wiring.AuthMiddlewareConfig`
`Node.wireClientAndServer` builds for `cmd/qrdbd` itself, so a Policy declared
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
| nil | (ignored) | no rate limiting installed |
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

### The client-address limiter

`pkg/clientlimit.Limiter` is the pre-authentication limit
([architecture.md](architecture.md#the-pre-authentication-client-address-limit)).
`pkg/server.Server` builds one from `config.Config.Transport.ClientRateLimit`
(`openClientLimiter`; none when the section is unset) and hands the same
instance to every listener through each package's `WithClientLimiter`
option: `pkg/api/rest` and `pkg/api/mcp` wrap their handler in
`Limiter.HTTP`, `pkg/api/grpc` and `pkg/api/unix` install its unary and
stream interceptors ahead of auth, and `pkg/api/quic` calls `Admit` per
request before the method authorizer. A nil `*Limiter` admits everything, so
each listener calls it unconditionally. `sdk/go/server` names the config
(`ClientRateLimitConfig`) and the refusal (`ClientRateLimitedError`); it
exports no constructor, so an embedder cannot build a second bucket set. The
daemon carries the section from its config file in `applyConfigToFlags`
and `buildServerConfig` (`cmd/qrdbd/start.go`).

The Go SDK's `Connect` treats the limiter's refusal of its health probe as
the server's answer: REST returns the decoded 429, gRPC and the Unix socket
the `ResourceExhausted` status, QUIC the `RESOURCE_EXHAUSTED` frame
(`callerRefusal`, which also stops its connect retry), each a
`*transport.RequestError` carrying `RetryAfter`, never a
`*transport.ConnectionError`.

## Enforcement point

`AuthMiddleware.authorize` — the single function every transport's
authorization call funnels through (`RESTMiddleware`, `UnaryInterceptor` and
`StreamInterceptor` shared by gRPC and the unix-socket listener,
`AuthorizeQUIC`, `AuthorizeMCP`) — re-verifies a delegated identity's
certificate, then runs the rate-limit check (`checkRateLimit`), ahead of the `IdentityGated` short-circuit
and the tenant-IAM-sealed allowance, whenever both `Limiter` and `Registry`
are configured and the caller's identity is non-nil. It is a no-op (falls
through unchanged) when either is nil, or when `perm` carries no `Policy` in
the `Registry`.

## Invariants

- **Rate limiting never runs against a nil identity.** `authorize` calls
  `checkRateLimit` only when `identity` is non-nil; a nil identity goes
  straight to the authorization decision.
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
  at full burst. This is accepted: rate limiting is a
  capacity control, not a security boundary, so trading a rare extra token
  for a sweep that never locks against `Allow` is the correct side of that
  trade.

## Error and status mapping

| Transport | Mapping |
|---|---|
| REST | `mapHTTPAuthError` returns **429** for `*RateLimitedError`; `RESTMiddleware` sets the `Retry-After` header (RFC 9110 §10.2.3, delay-seconds, rounded up to at least 1) before writing the response |
| gRPC / unix | `mapGRPCAuthError` returns `codes.ResourceExhausted`. The unix-socket listener registers the same `UnaryInterceptor`, so a caller over its unix-transport quota sees the identical code a TCP gRPC caller would. |
| QUIC | `authmw.AuthFailureRateLimited` maps to `wire.StatusResourceExhausted` (`pkg/api/quic/auth.go`); the frame carries `RateLimitedError.Error()` verbatim. |
| MCP | The tool-authorization middleware (`pkg/api/mcp/auth.go`) returns `RESOURCE_EXHAUSTED` (−32008) in the tool result's error envelope, alongside the error text. The code comes from `authmw.GRPCError`, the classifier REST and gRPC answer from, so each auth-failure class keeps its own code. `AuthFailureUnavailable` and `AuthFailureAuditUnavailable` both map to `Unavailable` on every transport: the pair exists for control flow inside `pkg/auth/middleware`, not for the wire. |
| MySQL | The client-address refusal is an ERR packet with error 1226, `ER_USER_LIMIT_REACHED`, SQLSTATE `42000` (`pkg/sql/mysql/client_limit.go`), returned for the connection, from go-mysql-server's connection-authenticated hook, and for each statement. The message is `RateLimitedError.Error()`. |
| PostgreSQL | A refused connection gets an ErrorResponse of severity `FATAL`, SQLSTATE `08004` (`sqlserver_rejected_establishment_of_sqlconnection`), written by an authentication strategy that runs ahead of the sealed-window gate and the password exchange; a refused statement (simple query, Parse, or Execute) gets one of severity `ERROR`, SQLSTATE `53400` (`configuration_limit_exceeded`), and the session stays open (`pkg/sql/pgsql/client_limit.go`). The connection refusal is written in the startup exchange because psql-wire v0.20.0 closes a connection whose session middleware returns an error without sending an ErrorResponse. |

## Test strategy

**Integration target:** `make integration-test-rate-limit` (no Docker; selection in `test/integration/features.mk`). It runs `TestClientRateLimit_CLI_PerProtocol` (`test/integration/client_rate_limit_cli_test.go`): a self-spawned `qrdbd` whose config file sets `transport.client_rate_limit`, a loopback bucket drained over REST with `X-Forwarded-For` headers the limiter must ignore, then the `qrdb` CLI refused with a message naming the rate limit over REST, gRPC, QUIC and MCP, and served over the Unix socket, whose callers have their own bucket; then a MySQL client (`go-sql-driver/mysql`) refused with error 1226 and a PostgreSQL client (`pgx`) refused with SQLSTATE `08004`. It also runs `TestClientRateLimit_Embedded_PerPermissionPolicy` (`test/integration/client_rate_limit_embedded_test.go`): an in-process node built with `node.WithRateLimitPolicies` admits a `Policy`'s burst through its embedded client and refuses the next call with `*middleware.RateLimitedError`, naming the permission and a positive `RetryAfter`. Per-transport accounting is the `rate-limit` row of `test/integration/COVERAGE.md`.

- **Unit, `pkg/clientlimit`**: keying (IPv4, IPv6 `/64`, Unix peer),
  trusted-proxy `Forwarded` and `X-Forwarded-For` walking, headers ignored
  from an untrusted peer, refusal with `RetryAfter`, and the HTTP 429.
- **Unit, each listener** (`pkg/api/{rest,grpc,quic,mcp,unix}`
  `client_limit_test.go`): a drained address refused before auth, with the
  transport's status, under `-race`; `pkg/sql/{mysql,pgsql}`
  `client_limit_test.go`: a drained address refused at connection and per
  statement with the protocol's error code; `pkg/server` builds one limiter
  and shares it; `cmd/qrdbd` carries every field of the `transport:` section
  of a config file to the server, and fails when the section gains a field
  its table does not name.
- **Unit, the Go SDK transports**: `Connect` against a rate-limited health
  answer returns the typed refusal with its `RetryAfter`, and QUIC does not
  retry it.

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
- **Wiring coverage**: the `authorize`-ordering test, the REST 429 test and
  the gRPC `ResourceExhausted` test go through `authorize`, so they fail if
  the enforcement call inside it is removed; the `checkRateLimit` tests call
  the helper directly and do not cover the wiring.

## Known gaps

- **No YAML/CLI configuration surface for a `Policy`.** `Policy` values are
  declared in Go code, at `Registry.Register` call sites, not read from
  `pkg/config` or a daemon flag. See [cli.md](cli.md). Only the
  client-address limit is configured from YAML.
- **The default `TokenBucketLimiter` a `BuildAuthMiddleware` call constructs
  is never explicitly closed.** `AuthMiddleware` has no `Close`/shutdown
  lifecycle hook, so the reaper goroutine a default limiter starts
  runs for the life of the process — acceptable for a daemon whose node
  lifetime is the process lifetime, but an embedder that repeatedly
  constructs and discards `AuthMiddleware` instances (for example, an
  in-process test harness that stands up many short-lived nodes) should
  supply its own `Limiter` via `RateLimiter` and own its `Close()` call,
  rather than rely on the default.
