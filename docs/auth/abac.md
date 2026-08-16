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

With zero policies configured, `Decorate` returns the inner authorizer
unchanged (zero overhead). Deny rules of **all** applicable policies are
evaluated before any allow rule (deny-overrides combining), so an allow in
one policy can never shadow a deny in another.

### Tenant scoping

Policies are owned by `metadata.tenant`. A request targeting tenant *T*
(from `tenant.FromContext`) is evaluated against *T*'s policies **plus the
reserved system tenant's policies** — system-tenant policies are
platform-wide guardrails. One tenant's policies never affect another
tenant. When the target *is* the system tenant, the set is evaluated once.

The check subject is built from the `auth.Identity`: `Subject`,
`TenantID`, `Roles` map directly; `Metadata` becomes the ABAC attribute
map (`subject.attrs.<key>` in conditions); `Authenticated` is true only
for a non-nil identity. A nil identity matches no policy and always falls
through to the inner authorizer. The target tenant is available in
conditions as `request.tenant`.

Policies are loaded once at construction. Hot-reload is a documented
follow-up; restart the node (or rebuild the embedded decorator) to pick up
policy changes.

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
)
```

SDK re-exports (`sdk/go/types_abac.go`): `ABACPolicy`, `ABACRule`,
`ABACEffectAllow/Deny`, `DecorateAuthorizer`, `ABACWithPolicyDir`,
`ABACWithPolicies`, `LoadABACPolicyDir`, `LoadABACPolicyFile`,
`ABACPolicyDeniedError`, plus `DaemonAuthABACConfig` in the daemon config
aliases.

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
    match: { department: engineering, level: { gte: 3 } }
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
