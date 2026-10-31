# Rate Limiting — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-27
> Conflicts with this document are defects. Fix them here first.

A single request-throttling engine, enforced at the one place every
transport already authorizes a request, so a caller is limited identically
regardless of which protocol carried its request.

## Problem

A limiter written at each point of need, in each transport or service layer,
duplicates the same policy in several places and lets the copies drift. Auth
and audit avoid that with one interface in the core and thin per-transport
adapters; throttling needs the same shape.

## Shape chosen

**One `Limiter` engine, one `Registry` of declared policy, enforced from
inside the authorization call every transport already makes.**

```go
type Policy struct {
	RatePerSecond float64
	Burst         int
}

type Limiter interface {
	Allow(key string, policy Policy) (allowed bool, retryAfter time.Duration)
	Close() error
}
```

`Registry` maps a `permission.Permission` to the `Policy` throttling it,
mirroring `pkg/permission`'s own global registry: a permission is declared
once, and a `Policy` for it is declared once, at whatever call site the
operation's owner chooses — not per route, not per gRPC method, not per MCP
tool, and never per call site inside a handler.

Keying policy by **permission** rather than by transport-specific name is
the load-bearing decision. Several names across REST, gRPC, the unix-socket
gRPC listener, native QUIC and MCP already resolve to the same permission —
that resolution is what the existing per-transport permission registries
do — so a `Policy` declared once per permission is enforced identically
everywhere that permission is checked, with no second declaration to drift
out of step with the first the way per-transport declarations would.

Enforcement lives inside `AuthMiddleware.authorize`, which every
transport's request path already reaches after authentication and before
dispatching to a handler: the REST middleware, the gRPC unary and stream
interceptors (shared by the TCP and unix-socket listeners), the QUIC method
authorizer, and the MCP tool-invocation authorizer all fall through the same
function. A delegated identity's certificate is re-verified first; then a
caller past its `Policy`'s burst is refused, before the
RBAC/ABAC authorizer or the audit logger's store lookups do any further work
on its behalf — throttling is cheaper than authorizing, so it runs first.

### Engine: a lock-free token bucket

The reference `Limiter`, `TokenBucketLimiter`, holds one bucket per
`(tenant, subject, permission)` key, refilled continuously at
`Policy.RatePerSecond` up to `Policy.Burst`. Each bucket is an
`atomic.Pointer` to an immutable snapshot, updated by a compare-and-swap
retry loop rather than a mutex: concurrent callers for different keys never
contend, and concurrent callers for the same key retry their own attempt
rather than blocking on one another. A background goroutine reclaims buckets
idle past a configurable TTL, bounding memory for a caller population that
comes and goes rather than staying fixed.

### Alternatives rejected

- **A rate-limiting middleware per transport.** Rejected — this is the
  shape that duplicates policy: each
  transport's adapter would need its own declaration of the same policy,
  and nothing would stop the two from drifting.
- **Policy keyed by route, gRPC method, or MCP tool name.** Rejected — see
  above: several such names already resolve to one permission, so keying at
  that finer grain reintroduces the "declare it again for the next
  transport" problem this package exists to remove.
- **Default-deny for an unregistered permission.** Rejected. Authorization
  denies an ungranted permission because an ungranted permission must never
  be reachable at all; a permission with no declared `Policy` is simply
  unthrottled, because most operations need no capacity bound, and inventing
  one without a real constraint to derive it from produces a guessed
  number. Unlike the request-body-size bound in [key-value](../key-value/),
  which derives from the protocol's own framing, a rate limit has no
  protocol fact to derive from: it is a capacity decision an operation's
  owner takes deliberately, one permission at a time.
- **A dependency on an external token-bucket library.** Rejected in favor of
  a lock-free implementation in this package: the engine is small, the
  concurrency property (no lock contention across keys) is exactly the one
  this codebase already prefers everywhere else, and it keeps the primitive
  free of a dependency this module does not otherwise need.
- **A global (not per-caller) bucket.** Rejected — a single shared bucket
  lets one caller's traffic starve every other caller's quota for the same
  operation, which defeats the purpose of a per-caller capacity control.

## The pre-authentication client-address limit

The per-permission mechanism above throttles an authenticated principal, so
it cannot see a flood that never gets that far: handshake, login and
enrollment attempts reach a transport before any principal exists. A second,
separate control closes that gap: `pkg/clientlimit`'s `Limiter`, a token
bucket keyed by **client address**, configured once per node as
`transport.client_rate_limit` and shared by every listener the node runs.

- **Where it runs.** Each transport admits a request through it before
  anything else runs on the request's behalf -- authentication, the
  public-route allowlist, the sealed-window and awaiting-admin gates: the
  REST and MCP HTTP handlers (HTTP/2 and HTTP/3), the gRPC unary and stream
  interceptors, the native QUIC request dispatcher, the
  Unix-socket gRPC listener, and the MySQL and PostgreSQL wire listeners,
  which charge the accepted connection's peer address once when the
  connection is established and again for every statement it sends. One
  `Limiter` per node, so an address's budget is the same whichever protocol
  carries its requests.
- **gRPC-Web is charged at the HTTP boundary.** Every request under the
  gRPC-Web prefix is admitted by the REST listener's limiter before the
  gRPC-Web handler runs: a CORS preflight, another method or media type the
  handler answers itself, and a call it translates alike. A call has to be
  charged there, because the gRPC server refuses an unknown service or
  method before its interceptors run; charged only by an interceptor, a call
  to a method that does not exist would spend nothing and stay unlimited.
  The limiter marks the translated request's context as charged
  (`Limiter.HTTPOnce`), and the gRPC interceptors, which share that one
  `Limiter`, skip a request it has marked (`Limiter.Admitted`), so a call
  that does reach them is charged exactly once. A refused gRPC-Web request
  gets the REST listener's 429 with Retry-After.
- **The key.** An IPv4 address, or an IPv6 `/64` prefix, so one host cannot
  mint fresh buckets from its own subnet. A Unix-socket peer has no IP
  address and is keyed by the socket, so every local caller shares one
  bucket, separate from every network address's. That is the design, not a
  missing key: a Unix socket carries no client address to tell its callers
  apart, and every one of them is already on the node's own host.
- **Forwarded headers.** Read only from a peer inside
  `trusted_proxies`, and only from the one field that proxy's entry names
  (`header: forwarded` for RFC 7239 `Forwarded`, `header: x-forwarded-for`).
  The chain is walked from the right to the first address that is not itself
  a trusted proxy, and every hop is taken from the field the trusted hop that
  reported it writes, so a chain may cross proxies that write different
  fields. The field a reporting proxy does not write is never read: a client
  behind an `X-Forwarded-For` proxy that sends its own `Forwarded` header
  gains nothing, since the proxy passes that header through untouched.
  Overlapping entries that name different headers are refused at startup,
  so every trusted address has exactly one field. With no
  trusted proxies -- the default -- no header is ever read, so a client
  cannot pick its own bucket. `Forwarded` is parsed to the exact RFC 7239
  §4 grammar, with RFC 7230 §3.2.6 quoted-strings and quoted-pairs: a field
  line that does not match it -- an unterminated quoted-string that would
  swallow the hop the proxy appended after it, an unquoted `host:port`, a
  parameter repeated in one element, whitespace around `;` or `=` -- fails
  closed. No hop in it is believed, the other field is not consulted in
  its place, and the request is keyed by the last trusted hop's address. An
  exhausted field, or a hop that is not an IP address, ends the walk the
  same way.
- **The refusal.** `RateLimitedError`, naming the address and the wait: 429
  with `Retry-After` on REST and MCP, `ResourceExhausted` with a RetryInfo
  detail on gRPC and the Unix socket, `RESOURCE_EXHAUSTED` with RetryInfo on
  native QUIC. The SQL listeners answer in their own wire protocol's terms:
  MySQL with an ERR packet carrying error 1226, `ER_USER_LIMIT_REACHED`,
  SQLSTATE `42000` ([MySQL server error reference](https://dev.mysql.com/doc/mysql-errors/8.4/en/server-error-reference.html#error_er_user_limit_reached)),
  for the connection and for a statement alike; PostgreSQL with an
  ErrorResponse of severity `FATAL` and SQLSTATE `08004`,
  `sqlserver_rejected_establishment_of_sqlconnection`, for the connection,
  and of severity `ERROR` and SQLSTATE `53400`, `configuration_limit_exceeded`,
  for a statement (a simple query, a Parse, or an Execute), which leaves the
  session open ([PostgreSQL error codes](https://www.postgresql.org/docs/current/errcodes-appendix.html)).
  The Go SDK's `Connect` returns the refusal as it is, rather than as a
  connection failure, so the CLI says the caller is rate limited.
- **Off by default.** The zero value applies no limit: a request-rate bound
  is a capacity decision for the operator, as a per-permission `Policy` is
  for an operation's owner.

## Trust boundaries

- **Rate limiting is a capacity control, not a security boundary.** Its
  absence for a given permission is never a denial; it is silence, exactly
  like an unconfigured `Policy`. Do not rely on it to prevent an
  unauthorized action — that is authorization's job, and it runs on every
  request the rate-limit check admits.
- **Keys are scoped by tenant, subject, and permission together.** Two
  tenants may share a subject name without sharing a bucket, and one
  caller's exhaustion of one permission's bucket never touches another
  permission's bucket for the same caller. The subject is
  `Identity.Subject`, which for a caller resolved to an account is its
  immutable user id, so renaming the account or changing its email keeps
  its buckets rather than opening fresh ones. The tenant is normalized
  (`tenantid.Normalize`) before it is folded into the key: an identity
  with an empty `TenantID` IS the system tenant, so it spends the system
  tenant's bucket rather than a second, fresh one obtained by alternating
  between the empty string and the system tenant's canonical id.
- **The reaper's eviction race can only make the next request MORE
  permissive, never less** (see the documentation of `TokenBucketLimiter`'s
  `sweep` method in `pkg/ratelimit/token_bucket.go` for the exact race and why the direction it fails in is the correct one for a
  capacity control rather than a security boundary).
- **Invitation redemption throttles OUTSIDE this mechanism**, on the same
  `ratelimit.Limiter`/`Policy` primitives but not through the per-permission
  Registry `AuthMiddleware.authorize` consults: redemption is
  UNAUTHENTICATED, so there is no subject and no permission to key a bucket
  by. `service.InvitationService` draws directly on two buckets of its own
  — the caller's source address, then the tenant the request names
  (`auth.invitation_redeem_limit.{per_source,per_tenant}`) — before any
  other check runs. See
  [../identity-rbac/architecture.md#invitation-self-enrolment](../identity-rbac/architecture.md#invitation-self-enrolment).

## What this feature deliberately does not do

- **No cluster-wide accounting.** Each node's `TokenBucketLimiter` tracks
  its own buckets in memory; a caller distributed across several nodes (by a
  load balancer, or by connecting to different nodes over different
  transports) is limited per node, not against one shared budget across the
  cluster. A cluster-wide limiter would need a coordination mechanism this
  feature does not build.
- **No second charge to the client for forwarded cluster traffic.** A
  request a replica forwards to the leader is charged, on the leader, to
  the forwarding node's address unless that node is listed in
  `trusted_proxies`. This is the design: the entry node already charged the
  client's own address when the request arrived, so the leader's bucket
  bounds what one node may forward, not what one client may send.
- **No default policies.** No permission ships throttled out of the box.
  Declaring a `Policy` — an actual requests-per-second number — is a
  capacity decision for the operation's owner to take deliberately, the same
  way granting a permission to a role is a decision RBAC never makes for an
  embedder.
- **No dynamic policy reload.** A `Registry` is built once, at process
  wiring time, and is read-only afterward; changing a `Policy` requires a
  restart. Unlike ABAC policies, rate-limit policies have no live-reload
  path.
- **No per-tenant or per-role policy tiers.** A `Policy` is declared per
  permission, not per tenant or per role holding that permission. A caller
  who needs, for example, a higher burst for one tenant than another has to
  build that on top of this mechanism; it is not a dimension this `Registry`
  carries.
- **Genuinely distinct limiters are out of scope.** A per-tenant issuance
  quota, or a per-peer packet shaper, enforces domain-specific policy that
  has nothing to do with "how many times may this caller invoke this
  permission per second." Forcing those through this mechanism would not
  remove duplication — it would misname two different problems as one.
