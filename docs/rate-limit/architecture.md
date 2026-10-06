# Rate Limiting — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

A single request-throttling engine, enforced at the one place every
transport already authorizes a request, so a caller is limited identically
regardless of which protocol carried its request.

## Problem

Rate limiting kept getting written again at the point of need instead of
once at the point every request already passes through. Auth and audit had
already been centralized the same way qrdb centralizes everything a
transport needs — one interface in the core, several transport-specific
adapters that are all thin — but throttling was left to grow independently
in each layer above, and it multiplied: measured across this codebase and
its downstream consumers, over a dozen independent limiter types existed,
several duplicating each other's name and purpose within the same
repository.

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

Enforcement lives inside the auth middleware's single `authorize` call,
which every transport's request path already reaches after authentication
and before dispatching to a handler: REST middleware, the gRPC unary
interceptor (shared by the TCP and unix-socket listeners), the QUIC method
authorizer, and the MCP tool-invocation authorizer all fall through the same
function. A caller past its `Policy`'s burst is refused there, before the
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

- **A rate-limiting middleware per transport.** Rejected — this is exactly
  the shape that produced the duplication in the first place: each
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
  one without a real constraint to derive it from produces exactly the kind
  of wrong, guessed number the request-body-size bound in
  [key-value](../key-value/) went through several rounds correcting before
  settling on deriving the number from the protocol's own framing instead of
  guessing it. A rate limit has no equivalent protocol fact to derive from —
  it is a capacity decision an operation's owner takes deliberately, one
  permission at a time.
- **A dependency on an external token-bucket library.** Rejected in favor of
  a lock-free implementation in this package: the engine is small, the
  concurrency property (no lock contention across keys) is exactly the one
  this codebase already prefers everywhere else, and it keeps the primitive
  free of a dependency this module does not otherwise need.
- **A global (not per-caller) bucket.** Rejected — a single shared bucket
  lets one caller's traffic starve every other caller's quota for the same
  operation, which defeats the purpose of a per-caller capacity control.

## Trust boundaries

- **Rate limiting is a capacity control, not a security boundary.** Its
  absence for a given permission is never a denial; it is silence, exactly
  like an unconfigured `Policy`. Do not rely on it to prevent an
  unauthorized action — that is authorization's job, and it always runs
  after the rate-limit check regardless of whether the check refused.
- **Keys are scoped by tenant, subject, and permission together.** Two
  tenants may share a subject name without sharing a bucket, and one
  caller's exhaustion of one permission's bucket never touches another
  permission's bucket for the same caller.
- **The reaper's eviction race can only make the next request MORE
  permissive, never less** (see `pkg/ratelimit`'s package documentation for
  the exact race and why the direction it fails in is the correct one for a
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
- **No default policies.** No permission ships throttled out of the box.
  Declaring a `Policy` — an actual requests-per-second number — is a
  capacity decision for the operation's owner to take deliberately, the same
  way granting a permission to a role is a decision RBAC never makes for an
  embedder.
- **No dynamic policy reload.** A `Registry` is built once, at process
  wiring time, and is read-only afterward; changing a `Policy` requires a
  restart. A live-reload path (the way ABAC policies can be reloaded) is a
  natural extension this feature does not implement yet.
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
