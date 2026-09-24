# ABAC Authorization Decorator

`pkg/auth/abac` layers attribute-based access control over the RBAC
authorizer. It is the canonical home of the policy engine ported from
go-quicbaas's IAM module (compiler, resource trie, condition parser,
evaluator); distribution concerns (policy storage, Raft sync, PEP cache)
were intentionally not ported.

## Decorator semantics

`abac.Decorate(inner, opts...)` returns an `auth.Authorizer` composing
ABAC with the wrapped (RBAC) authorizer:

1. **Explicit DENY match** → deny immediately with `*abac.PolicyDeniedError`
   (wraps `auth.ErrUnauthorized`, names the policy and rule). ABAC deny
   overrides an RBAC allow.
2. **Explicit ALLOW match** → allow immediately. ABAC can grant beyond RBAC.
3. **No rule matched** → delegate to the inner authorizer. RBAC decides.

With zero policies configured (and `WithLiveReload` not supplied), `Decorate`
returns the inner authorizer unchanged (zero overhead). Deny rules of
**all** applicable policies are evaluated before any allow rule
(deny-overrides combining), so an allow in one policy can never shadow a
deny in another.

### Tenant scoping

Policies are owned by `metadata.tenant`. A request targeting tenant *T*
(from `tenant.FromContext`) is evaluated against *T*'s policies **plus the
reserved system tenant's policies** — system-tenant policies are
platform-wide guardrails. One tenant's policies never affect another
tenant. When the target *is* the system tenant, the set is evaluated once.

The check subject is built from the `auth.Identity`: `Subject`,
`TenantID`, `Roles` map directly; `Authenticated` is true only for a
non-nil identity. A nil identity matches no policy and always falls through
to the inner authorizer. The target tenant is available in conditions as
`request.tenant`.

The subject carries two attribute namespaces, and they are never merged:

| Namespace | Source | Condition | `match` key |
|---|---|---|---|
| authenticated | `Identity.Metadata` — certificate fields, token claims | `subject.attrs.<key>` | `<key>` |
| user record | `Identity.Attributes` — the user's stored `attributes`, written by an administrator holding `roles:assign` | `subject.user_attrs.<key>` | `user_attrs.<key>` |

A bare name only ever reads the authenticated namespace. A user-record
attribute called `organization` is therefore invisible to a policy that
matches `organization`, whether or not the authenticator set one: nobody who
can edit a user record can forge a value a policy treats as coming from the
certificate or token.

### Live reload

The active policy set can be replaced at runtime, without restarting the
process, via `Decorator.Reload(policies []*abac.Policy) error`. Reload
compiles and validates the new set before swapping it in: a failed reload
(bad policy, parse error) returns the typed compilation/validation error and
leaves the previously active set fully in force — a reload never partially
applies. The swap is a single atomic pointer store, so `Authorize` reads the
active set lock-free and concurrently with any number of in-flight `Reload`
calls.

`Decorate` returns a `*Decorator` (which implements `Reload`) whenever it
wraps at all — i.e. whenever at least one policy was configured, or
`WithLiveReload()` was passed. `WithLiveReload` forces the wrapper even with
zero initial policies, so an embedder that starts with none can still add
its first policy later (e.g. from a downstream policy CRUD store) instead of
restarting.

`sdk/go/node.Node` exposes the seam as `Node.ABACPolicyReloader()
abac.PolicyReloader`, non-nil whenever the node was built with
`WithABACPolicies`, `WithABACPolicyDir`, or `WithABACLiveReload`. The
embedded in-process transport (`node.Client().RBAC()`) and, when auth is
enabled, every network transport's `AuthMiddleware` (REST, gRPC, unix, QUIC,
MCP, MySQL, PostgreSQL) share the SAME composed authorizer instance
(`pkg/auth/wiring.AuthorizerConfig.Authorizer` /
`AuthMiddlewareConfig.Authorizer`), so a single `Reload` call is visible to
every transport atomically — qrdb stays the one authorization decision
point rather than each transport caching an independently-reloadable copy.

## Configuration

qrdbd (`auth.abac` in the daemon YAML; config-only, no CLI flag):

```yaml
auth:
  enabled: true
  abac:
    enabled: true
    policy_dir: /etc/qrdb/abac
```

`enabled: true` with an empty `policy_dir` fails validation. A missing
directory, unparsable file, or invalid/uncompilable policy aborts the boot
(fail closed). The decorator wraps the single authorizer handed to the
auth middleware, so every transport (REST/gRPC/QUIC/MCP/Unix) enforces the
composed decision, and the middleware audits it on the one path.

Embedded hosts (xkms, quicbaas) enable it per node — via the SDK only:

```go
n, err := node.NewStandaloneNode(ctx, cfg, nil,
    node.WithABACPolicyDir("/etc/xkms/abac"),          // and/or
    node.WithABACPolicies([]*qrdbsdk.ABACPolicy{...}), // programmatic
    node.WithABACLiveReload(),                         // keep Reload available even from zero policies
)

// Later, e.g. from a policy CRUD handler:
if reloader := n.ABACPolicyReloader(); reloader != nil {
    if err := reloader.Reload(updatedPolicies); err != nil {
        // typed error (e.g. *abac.CompilationFailedError); the previous
        // policy set is still fully in force.
    }
}
```

SDK re-exports (`sdk/go/types_abac.go`): `ABACPolicy`, `ABACRule`,
`ABACEffectAllow/Deny`, `ABACPolicyReloader`, `DecorateAuthorizer`,
`ABACWithPolicyDir`, `ABACWithPolicies`, `ABACWithLiveReload`,
`LoadABACPolicyDir`, `LoadABACPolicyFile`, `ABACPolicyDeniedError`, plus
`DaemonAuthABACConfig` in the daemon config aliases. `node.WithABACLiveReload`
and `Node.ABACPolicyReloader` live in `sdk/go/node`, imported directly (not
re-exported through `sdk/go`).

## Policy schema

```yaml
apiVersion: abac.qrdb.io/v1
kind: Policy
metadata:
  name: engineering-guardrail
  tenant: system        # system = platform-wide; otherwise tenant-scoped
  priority: 100         # higher evaluates first
spec:
  subjects:             # who the policy applies to (any one selector matches)
    users: ["alice"]
    groups: ["editors"]
    roles: ["admin"]
    services: ["api-service"]
    match: { department: engineering, level: { gte: 3 }, user_attrs.region: us-east }
    authenticated: true # any authenticated subject
    any: false          # everyone, including anonymous
  variables:
    env: production
  rules:
    - name: deny-secret-reads
      effect: deny      # allow | deny
      priority: 10
      actions: ["read", "write*"]        # "*" and trailing-* prefix match
      resources: ["/secrets/**"]         # exact, *, **, ${var}, in-segment globs
      when:                              # all must be true
        - expression: subject.attrs.clearance < 3
      unless:                            # all must be false
        - expression: request.tenant == "system"
```

Condition expressions support comparison (`==`, `!=`, `>`, `>=`, `<`,
`<=`, `between`), string (`startswith`, `endswith`, `contains`, `matches`,
`like`), set (`in` — including CIDR for IPs — `notin`, `containsall`,
`containsany`, `overlaps`, `disjoint`), existence (`exists`, `notexists`),
and logical (`and`, `or`, `not`) operators over `subject.*`, `request.*`,
`resource`, and `time.*` fields.
