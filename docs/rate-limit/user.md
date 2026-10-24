# Rate Limiting — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

This feature has two audiences: the **embedder** who wires qrdb into a
process and decides which operations need a capacity bound, and the **API
caller** who occasionally gets told to slow down. There is no CLI or
runtime configuration surface — see [cli.md](cli.md) and
[api.md](api.md).

## For an embedder: declaring a limit

Declare a `Policy` for the permission you want bounded, and pass the
resulting `Registry` when building the process's auth middleware:

```go
import (
	"github.com/jeremyhahn/go-qrdb/pkg/auth/wiring"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

policies := ratelimit.NewRegistry()
policies.Register(permission.KVPut, ratelimit.Policy{
	RatePerSecond: 100, // sustained requests per second, per caller
	Burst:         200, // requests a caller may make instantaneously
})

mw, err := wiring.BuildAuthMiddleware(wiring.AuthMiddlewareConfig{
	Enabled:           true,
	RoleService:       roleService,
	UserService:       userService,
	RateLimitPolicies: policies,
	// RateLimiter is optional — leave it nil to get the production
	// token-bucket engine, or supply your own if you already run one
	// shared across other subsystems.
})
```

That is the whole surface. No further wiring is needed per transport: every
one of REST, gRPC, the unix-socket gRPC listener, QUIC, MCP and the
embedded transport already
calls through the same authorization function this `Policy` is checked
inside, so declaring it once throttles the permission everywhere it is
reachable.

**Only declare permissions that actually need a bound.** A permission with
no `Policy` is simply unthrottled — see
[architecture.md](architecture.md) for why that is the deliberate default
rather than a gap to fill in for every permission in the registry.

**Picking a number.** `RatePerSecond` and `Burst` are your call, not a
derived constant — there is no protocol fact to compute a request-frequency
budget from the way, for example, a request-body-size bound can be derived
from a maximum entry size. Base it on what the operation and its backing
store can actually sustain per caller, and revisit it if it turns out to be
wrong; changing it requires a process restart.

## For an API caller: what a refusal looks like

If the operation you are calling has a declared `Policy` and you exceed it,
you get told to slow down rather than that you are not allowed to do it at
all:

- **REST**: `429 Too Many Requests`, with a `Retry-After` header giving the
  number of seconds to wait, and a JSON body naming the refusal.
- **gRPC / unix-socket gRPC**: `codes.ResourceExhausted`, distinct from
  `PermissionDenied` — your credential and your grant are both fine, so
  retrying after the indicated wait is the correct response, not
  re-authenticating.
- **QUIC**: status `StatusResourceExhausted`, with the permission and the
  reason in the frame.
- **MCP**: error code `RESOURCE_EXHAUSTED` (−32008) in the tool result,
  with the refusal text.
- **Embedded (Go)**: a `*middleware.RateLimitedError` carrying the
  permission, subject, tenant and `RetryAfter`.

**What to do**: back off for at least the duration the refusal names (REST's
`Retry-After` header, `RetryAfter` on the embedded error, or the wait
mentioned in the error message on the other transports), then retry the same request. A refusal here never means
your request was malformed or your grant was insufficient — those are
different error classes with different codes.

## Troubleshooting

**A caller keeps getting throttled well under what you expect its traffic
to be.** Rate limiting is scoped per **(tenant, subject, permission)**
triple — check whether the caller is issuing requests as several distinct
identities you did not expect to share one budget, or as one identity you
expected to have a higher `Burst` than it was given.

**An identity with no tenant set shares a budget with one naming the
system tenant explicitly.** An empty `TenantID` IS the system tenant for
rate-limiting purposes (the same rule `tenant.FromContext` uses elsewhere):
the two are folded onto one bucket before it is keyed, so they cannot be
two separate budgets to alternate between.

**Throttling behaves differently right after a restart than it did before.**
Buckets are in-memory only; a restart resets every caller's bucket to full
burst. This is not persisted state, and is not meant to be.

**You expected a caller connecting through two different nodes to share one
budget, and it did not.** Rate limiting is enforced **per node**, not
cluster-wide — see
[architecture.md](architecture.md#what-this-feature-deliberately-does-not-do). A caller load-balanced across nodes gets
one bucket per node it reaches, not one shared bucket for the cluster.

**You declared a `Policy` and nothing changed.** Confirm the permission
string matches exactly what the transport resolves for the route, gRPC
method, or MCP tool you are calling — `Registry.Lookup` is an exact match on
`permission.Permission`, not a prefix or pattern match.
