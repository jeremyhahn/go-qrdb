# Identity and RBAC — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-02

## Exported surface

`sdk/go`:

```go
func (c *Client) Users()       transport.UserClient
func (c *Client) Roles()       transport.RoleClient
func (c *Client) Permissions() transport.PermissionClient  // nil if unsupported
func (c *Client) RBAC()        transport.RBACClient        // nil if unsupported
```

`Permissions()` and `RBAC()` return **nil** when the transport does not
implement the corresponding provider interface — nil-check them.

Built-in role reconciliation, re-exported from `sdk/go` and carried by every
transport's `RoleClient`:

```go
// pkg/role — the servicer surface.
ReconcileBuiltInRoles(ctx context.Context, tenantID string) (*ReconcileReport, error)

type ReconcileReport struct {
    TenantID string
    Roles    []RoleReconcileResult // only CHANGED roles; empty means converged
}
func (r *ReconcileReport) Changed() bool

type RoleReconcileResult struct {
    RoleID, Name string
    Created      bool
    Added        []permission.Permission
    Removed      []permission.Permission
}
func (r RoleReconcileResult) Changed() bool

// Drift, visible on read.
type RetiredPermissionError struct {
    RoleID, Name, TenantID string
    Permissions            []permission.Permission
}
func RetiredPermissions(perms []permission.Permission) []permission.Permission
func (r *Role) RetiredPermissionError() error   // nil when there is no drift

// sdk/go re-exports: ReconcileReport, RoleReconcileResult,
// RetiredPermissionError, RetiredPermissions.
// sdk/go/transport: RoleClient.ReconcileBuiltInRoles(ctx, *ReconcileRolesRequest)
//                   (*ReconcileRolesResponse, error)
```

### User entity — `pkg/user/types.go`

```go
type User struct {
	ID           uint64             `json:"id"`           // store-assigned Snowflake; never zero when persisted
	Username     string             `json:"username"`     // unique within tenant; maps to cert CN
	Email        string             `json:"email"`        // CANONICAL LOGIN KEY, unique within tenant
	Name         string             `json:"name"`         // display only; never a lookup key
	TenantID     string             `json:"tenantID"`     // maps to cert OU
	RoleIDs      []string           `json:"roleIDs"`      // system-scoped role identifiers
	Lifecycle    Lifecycle          `json:"lifecycle"`    // CANONICAL account state
	Enabled      bool               `json:"enabled"`      // DERIVED: Lifecycle == active
	System       bool               `json:"system"`       // built-in, cannot be deleted
	GroupIDs     []string           `json:"groupIDs"`     // opaque membership ids; qrdb owns no Group entity
	AuthMethods  []string           `json:"authMethods"`  // permitted methods (policy), lowercased
	LastAuth     *time.Time         `json:"lastAuth"`     // high-water mark; never moves backwards
	RoleBindings []RoleBinding      `json:"roleBindings"` // {RoleID, TenantID} — tenant-scoped RBAC
	Attributes   map[string]string  `json:"attributes"`   // ABAC
	CertBindings []CertBinding      `json:"certBindings"` // mTLS SPKI thumbprints
	Factors      []FactorEnrollment `json:"factors"`      // opaque; qrdb never interprets Data
	CreatedAt    time.Time          `json:"createdAt"`
	UpdatedAt    time.Time          `json:"updatedAt"`
}
```

`User.CanAuthenticate()` is the check a login or credential-registration
ceremony makes. It is `Lifecycle == active` and nothing else.

### Lifecycle — `pkg/user/lifecycle.go`

```go
type Lifecycle string
const (
	LifecyclePending   Lifecycle = "pending"   // created, never activated
	LifecycleActive    Lifecycle = "active"    // the ONLY state that may authenticate
	LifecycleSuspended Lifecycle = "suspended" // administrative hold
	LifecycleLocked    Lifecycle = "locked"    // automated lockout
	LifecycleDisabled  Lifecycle = "disabled"  // administratively deactivated
	LifecycleDeleting  Lifecycle = "deleting"  // TERMINAL: deletion in progress
)
```

| From \ To | pending | active | suspended | locked | disabled | deleting |
|---|---|---|---|---|---|---|
| **pending**   | — | ✓ | ✗ | ✗ | ✓ | ✓ |
| **active**    | ✗ | — | ✓ | ✓ | ✓ | ✓ |
| **suspended** | ✗ | ✓ | — | ✓ | ✓ | ✓ |
| **locked**    | ✗ | ✓ | ✓ | — | ✓ | ✓ |
| **disabled**  | ✗ | ✓ | ✗ | ✗ | — | ✓ |
| **deleting**  | ✗ | ✗ | ✗ | ✗ | ✗ | — |

`—` on the diagonal is admitted as a no-op, including `deleting → deleting`,
which is what keeps a partially completed deletion resumable. Only `pending`,
`active` and `disabled` are directly creatable.

### Cert binding — `pkg/user/cert_binding.go`

```go
type CertBindingKind int
const (
	CertBindingSPKIThumbprint      CertBindingKind = iota // canonical default
	CertBindingFullCertFingerprint                        // exact-cert pinning, NOT the default
	CertBindingPIVAttested                                // established via PIV attestation
)
```

`User.MatchesCert(cert)` is the authoritative check.

### Permission vocabulary — `pkg/permission`

```go
type Permission string
type Category struct {
	Name        string       // display name, e.g. "KV Service"
	Group       string       // logical grouping, e.g. "Data Services"
	Permissions []Permission
}

func Register(perms ...Permission)
func All() []Permission                    // sorted
func IsRegistered(p Permission) bool
func Parse(s string) (Permission, error)   // validates against the registry
func Resource(p Permission) string         // before the colon
func Action(p Permission) string           // after the colon
func Categories() []Category
func ByResource(resource string) []Permission
```

All permissions are registered by the `init()` in `pkg/permission/constants.go`,
which walks the category definitions and calls `Register` for each.

### Permission scope — `pkg/permission/scope.go`

A permission's **scope** classifies its tenancy reach, and the RBAC authorizer
evaluates it **after** the grant check. That ordering is what makes a wrong
scope structural rather than fixable by a role: no grant can satisfy a scope
the identity fails.

| scope | tenant-local identity | system-tenant identity |
|---|---|---|
| `ScopeSelf` (**the default**) | own tenant only | **lifted across all tenants** |
| `ScopeCross` | denied | any tenant |
| `ScopeTenantOnly` | own tenant only | **denied, structurally** |

**Choose by asking whose material the permission acts on, not who usually calls
it.** `ScopeTenantOnly` means *"tenant-admin-only, system-admin-denied, no
cross-tenant"*, and it exists for material held **on a tenant's behalf** — the
canonical case is per-tenant barrier key custody, which the platform operator
must not hold. A permission that reads the **daemon's own** state is not that,
even when it lives in a tenant-facing family.

**A permission takes `ScopeSelf` by not being registered with a scope**, so the
hazard is the opposite of the usual one: the mistake is not forgetting to
classify, it is classifying a whole family in one call. Measured:
`RegisterScope(ScopeTenantOnly, brokerDataPlaneScopePermissions()...)` tagged
the entire broker data-plane family, and `orchestrator:health` rode along. The
result was that the operator persona — the one that exists to watch the node —
was the single persona that could not see whether the node's orchestrator was
alive:

```
GET /api/v1/orchestrator/health
403 permission denied: admin cannot health on orchestrator:
    tenant-only permission denied to system tenant
```

It now sits in `nodeStateScopePermissions()`, registered but deliberately
untagged.

**The counter-example is in the same family, and it is the more instructive
one.** `orchestrator:status` looks like the same case and is not: it is
declared as *"read access to orchestration SESSION status"* and sits in the run
of session verbs, so on the vocabulary as written it is tenant-owned metadata.
Relaxing its scope would let a system-tenant grant read every tenant's session
status — a widening wearing a fix's clothes. A daemon-level `/orchestrator/status`
endpoint does return the same structural 403, but the cause there is **one
permission serving two resources**, and the remedy for that is a separate
service-level permission, not a scope change on the session one.

**So the question to ask about a scope is what the permission is DECLARED to
cover, not which endpoint prompted the complaint.**

**Pin the scope of each member of a family, not the family.** Before that fix
nothing asserted the scope of any individual permission in the family — the
registration looped over one list and so did the test — so a single
permission's classification could change without a failing test. The test now
enumerates the **exceptions** and asserts everything else is tenant-only, which
is the fail-closed direction: a permission added to the family and not listed
as an exception is asserted tenant-only, so getting a new permission's scope
wrong fails a test rather than passing a typed-in column.

## Invariants

- **`ReconcileBuiltInRoles` is the ONLY writer to a built-in role's
  permissions.** `UpdateRole` returns `SystemRoleImmutableError{Op:
  "re-permissioned"}` and `DeleteRole` returns `SystemRoleImmutableError{Op:
  "deleted"}` for every other caller, and that does not change. Reconciliation
  is admitted because its target is not caller-supplied: it is `BuiltInRoles()`,
  the definition the guard exists to protect. It writes the primary record
  directly rather than going through `UpdateRole`, so the guard has no
  bypass-flag to be set by mistake.
- **Reconciliation is idempotent and writes nothing when converged.** A built-in
  already in agreement is skipped entirely — no `Put`, no `UpdatedAt` bump, no
  audit record. This runs on every boot of every node; a no-op write would
  replicate a Raft entry each time. The test asserts on the store's write count,
  not only on an empty report.
- **Reconciliation touches only `IsSystemRoleName` names.** Custom roles are
  never read and never written. Owner is converged when present but **never
  created**: it is posture-conditional, so seeding it would give a strict
  separation-of-duties deployment a superuser.
- **`BuiltInRoles()` is resolved at CALL time**, from the global permission
  registry. A composer that contributes permissions through its modules must
  call `ReconcileBuiltInRoles` AFTER module registration, or it converges every
  tenant onto a definition missing its own verbs and reports success.
- **`Role.RetiredPermissions` is derived, never persisted.** `GetRole` populates
  it from the live registry on every read; `marshalRole` — the single seam every
  store write passes through — clears it before marshalling. The seam is
  structural rather than a rule, because `UpdateRole` writes back the value
  `GetRole` returned, so a plain `json.Marshal` at either call site would persist
  one build's registry snapshot into the record and the next build would read it
  back as stored grant data.
- **`Username` is unique within a tenant**, not globally.
- **`Email` is the canonical login key**, unique within a tenant and normalized
  by `NormalizeEmail` (lowercase, trimmed) on both the record and its index key.
  Tenant-scoped rather than global on purpose: one person may hold accounts in
  two tenants, and every login path already supplies the tenant. It is a
  separate field from `Username` because system users are named `admin`, not an
  address.
- **An empty `Email` must be DECLARED** via `CreateUserRequest.WithoutEmailLoginKey`.
  A certificate-only or bootstrap identity is a legitimate state; an ordinary
  user silently created with no entry in the login index is not.
- **Every secondary-index mutation is atomic.** Handles are claimed with
  `PutIfAbsent` and released with `CompareAndDelete` against the owning id, so a
  handle cannot be claimed twice and a release cannot evict an entry another
  account has legitimately taken. A store lacking `kvstore.AtomicKVStore` is
  refused (`ErrStoreNotAtomic`) rather than degraded to check-then-write.
- **`Update`, `Enable`, `Disable` and `Delete` commit conditionally** against
  the version they read. A lost commit is `ConcurrentUpdateError` (re-read and
  retry), which is a different statement from a store failure.
- **A store error from a conditional commit is AMBIGUOUS**, per
  `kvstore.AtomicKVStore`: the outcome is unknown, so the record is read back to
  decide. Only an inconclusive read yields `CommitOutcomeUnknownError`.
- **Compensation undoes only what its own invocation did**, and a losing update
  restores exactly the handles the winning record still relies on — "we lost"
  does not imply "the winner retired the same handles".
- **The SPKI thumbprint is base64url SHA-256 over `SubjectPublicKeyInfo`.**
  Computing it over the public key rather than the certificate is what makes a
  binding survive renewal.
- **`CertBindingSPKIThumbprint` is the canonical default.**
  `CertBindingFullCertFingerprint` pins an exact certificate and breaks on
  renewal — that is its purpose, and it is not the default.
- **`RoleBindings` are additive to `RoleIDs`**, not a replacement. Effective
  permissions are the union.
- **Effective permissions resolve through one PDP** on every transport;
  `ResolveRolePermissions` exposes that resolution for inspection.
- **`Parse` rejects unregistered permission strings.** The vocabulary cannot be
  extended at runtime by a caller.
- **`FactorEnrollment.Data` is opaque.** qrdb stores it and never interprets it;
  verification is the registered `authfactors.Factor`'s job.
- **`Lifecycle` is the single writable source of truth for account state.**
  `Enabled` is re-derived from it on every write and every decode, and no
  request field sets `Enabled` directly — two writable sources are how a record
  ends up claiming to be locked and active at once.
- **`normalize()` runs after every decode.** A record written before the field
  existed has its state derived from `Enabled` (`true → active`,
  `false → disabled`), so no migration pass is needed and no pre-upgrade account
  is locked out by an empty state. A state that is present but UNRECOGNIZED is
  left exactly as written — it came from a newer node, and `CanAuthenticate`
  already fails closed on it.
- **`deleting` is terminal and closes the registration-during-deletion window.**
  A deletion writes it before its first mutation; from that instant
  `CanAuthenticate` is false and neither `EnableUser`, `DisableUser` nor
  `UpdateUser` can move the record to another state.
- **`DeleteUser` CLAIMS the record by marking it `deleting` before the cascade**
  (a conditional write against the version it read), and the cascade then
  commits against the marked version. Until that mark lands the delete has not
  won the record — another writer can still bind a credential or move a handle,
  and the cascade would be sweeping a snapshot that no longer describes the
  account. A delete that fails part way leaves an account that cannot
  authenticate and cannot be re-enabled; that is the correct direction to fail,
  and a retry of `DeleteUser` finishes it because re-asserting the mark is a
  no-op.
- **A terminal record accepts NO mutation.** `UpdateUser` refuses every edit
  (`onlyReassertsLifecycle` is the sole exception, and is an exhaustive field
  list), the `LastAuth` fast path refuses, and `AddCertBinding` refuses.
  Refusing only the lifecycle transition left every other field writable, so a
  caller could still bind a certificate to an account whose deletion had begun —
  the exact orphan the marker exists to prevent.
- **`AuthMethods` is ENFORCED at qrdb's authentication gate**, not merely
  stored. `pkg/auth/middleware` carries the name of the authenticator that
  succeeded and refuses with `AuthMethodNotPermittedError` (403 /
  `PermissionDenied`) when the account's policy is non-empty and does not list
  it. Two limits are stated rather than assumed: an EMPTY policy states no
  restriction, so accounts predating the field are unaffected; and the check
  applies only where the method is OBSERVABLE — a forwarded on-behalf assertion
  and a capability module's session token carry none qrdb can see, and the layer
  that ran that ceremony enforces the policy there. That distinction is carried
  by an explicit flag (`authMethod.known`), never by an empty name: an
  authenticator that names itself `""` is observed, not unobservable, and
  conflating the two let it walk past a non-empty policy through the ordinary
  chain. An observed method whose name does not normalize fails closed.
- **`EnableUser` / `DisableUser` are transitions, not boolean sets** — to
  `active` and `disabled` respectively. Both are refused on a `deleting` record.
- **`GroupIDs` are opaque and case-PRESERVING**; `AuthMethods` are opaque and
  case-FOLDED. Both are trimmed and de-duplicated by `NormalizeGroupIDs` /
  `NormalizeAuthMethods`, and a blank entry is refused rather than dropped.
  On an update, `nil` means "leave alone" and an empty non-nil slice means
  "clear".
- **`LastAuth` only moves forward.** A value at or before the stored one is
  ignored, so overlapping logins written out of order cannot roll an account's
  last-seen time backwards. It is stored in UTC.
- **A `LastAuth`-only update is a commutative merge and RETRIES a lost commit**
  rather than returning `ConcurrentUpdateError`. Every other field is a
  replacement, where a lost race means the caller's view was stale and only the
  caller can decide what to do; `max(stored, requested)` has nothing stale to
  reconcile, so re-reading and recomputing is always correct. It matters because
  of who writes it: `LastAuth` is written on the login path, and routing a login
  through the general update path made its timestamp lost — and lost *below* the
  true high-water mark — whenever an administrator happened to be editing the
  same account. The retry is bounded (`lastAuthMergeAttempts`) with no sleep,
  and writes nothing at all when the stored value is already at or past the
  request. `lastAuthOnly` is an exhaustive field list: a field added to
  `UpdateUserRequest` without being named there would be silently dropped by the
  fast path, and a test asserts the list covers every field.
- **`System: true` records are undeletable.**
- **The system-tenant rule reads `identity.TenantID`, so that field must
  arrive NORMALIZED.** `rbac.IsSystemTenant(identity)` is what lifts a
  `ScopeCross` permission and what denies a `ScopeTenantOnly` one; it delegates
  to `tenant.IsSystemTenant`, whose empty-string branch is safe only because
  the boundary already folded `""`. `auth/middleware.finalizeIdentity` performs
  that fold — on the identity as well as the context, which is the fix for
  #188; folding only the context left the authorizer reading the raw value.
  See [../multi-tenancy/engineering.md](../multi-tenancy/engineering.md) for
  the fold contract and for which token shapes mint an absent tenant.
- **`rbac.IsSystemTenant(nil)` is true, and unreachable through the
  authorizer.** `RBACAuthorizer.Authorize` rejects a nil identity with
  `auth.ErrUnauthenticated` as its first statement, far above the system-tenant
  rule, so the nil branch is live only for a direct caller of the exported
  predicate (`sdk.IdentityIsSystemTenant` aliases it) and no such caller exists
  in this repository. The branch means "an internal, unauthenticated pipeline",
  not "an unauthenticated request"; the guard is what keeps those two apart and
  is pinned by a test.
- **Built-in role IDs are deterministic** UUIDv8 over SHA-256 — see
  [../firstboot/](../firstboot/engineering.md) and the identifier-compatibility
  rules under [Persistence](#persistence).

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Certificate presents no matching binding | authentication fails | **yes** |
| User not in `active` lifecycle | authentication fails | **yes** |
| Lifecycle state unrecognized by this build | authentication fails | **yes** |
| Transition the table does not connect (incl. out of `deleting`) | `LifecycleTransitionError` | **yes** |
| Undefined or not-creatable lifecycle state | `InvalidLifecycleError` naming the allowed set | **yes** |
| Blank `groupIDs` / `authMethods` entry | `ValidationError`, nothing written | **yes** |
| Unregistered permission string | `Parse` error | **yes** |
| Missing permission for the operation | permission denied | **yes** |
| Deleting a `System: true` user or a built-in role | refused | **yes** |
| Re-permissioning a built-in role through `UpdateRole` | `SystemRoleImmutableError{Op: "re-permissioned"}` | **yes** |
| Reconciling another tenant's built-ins as a normal tenant caller | `tenant.CrossTenantDeniedError` | **yes** |
| Reconciling a sealed barrier tenant | `tenant.ErrTenantSealed`, no fallback to the shared store | **yes** |
| Stored role carries a permission this build does not define | reported on read via `RetiredPermissions`; **no read fails** | — |
| Duplicate username within a tenant | refused | yes |
| `Permissions()` / `RBAC()` on an unsupporting transport | returns **nil** — callers must nil-check | — |

The nil returns are the sharp edge here: they are not error values, so an
unchecked call panics rather than failing cleanly.

## Persistence

- Users and roles live under the internal `__sys:` namespace, Raft-replicated,
  invisible to user KV enumeration.
- Roles are at `__sys:role/<id>` with deterministic identifiers for the built-in
  six: RFC 9562 §5.8 **UUIDv8 over SHA-256** of a fixed
  `"go-qrdb:role-namespace/v8/"` prefix plus the length-framed tenant ID and
  role name (`pkg/role.SystemRoleID`).
- User lookup by certificate thumbprint is a secondary index —
  `GET /users/by-cert-thumbprint/{thumbprint}`.
- The email login key is a tenant-scoped secondary index at
  `__sys:user_idx/email/{tenantID}/{normalized-email}`, maintained by the same
  functions that write the record so the two cannot drift.
- Email lookup is `POST /users/lookup` with the key in the **body**. It is
  deliberately not a path parameter: the canonical login identifier would then
  be captured by access logs, proxies, tracing and browser history on every
  successful read.

**Identifier compatibility.** The derivation was UUIDv5 (name-based, SHA-1)
until the FIPS 140-3 migration: SHA-1 does not exist under FIPS 140-3, and RFC
4122 fixes v3 to MD5 and v5 to SHA-1, so v8 — the version reserved for
application-defined derivations — is the only name-based form an approved hash
can take. The 36-character UUID text shape is unchanged, so no index key or wire
format moved with it.

These identifiers are persisted data (role primary keys, both secondary indexes,
and every user record's `RoleIDs`), so the change is versioned rather than a flag
day: **writes emit only the v8 form, reads accept both.** A cluster seeded before
the migration keeps its roles under their UUIDv5 identifiers — seeding is
name-unique and tolerates already-exists, so those records are never replaced —
and nothing already persisted becomes unresolvable:

| Direction | How it stays correct |
|-----------|----------------------|
| Identifier → role | Unchanged: a stored identifier is looked up verbatim, so `GetRole`, `ListRoles` and `ResolvePermissions` resolve a legacy record as they always did. |
| Tenant + name → identifier | `role.ResolveSystemRoleID` answers from the name index rather than from a hash, so a grant binds to the record that exists. Bootstrap and first boot use it when assigning system roles. |
| "Is this identifier role X?" | `role.SystemRoleIDs` returns both the canonical and the persisted identifier. The RBAC authorizer matches against the whole set, so the SO deny-list (FIPS 140-3 AS10.03) and the strict-separation-of-duties Owner strip still void a grant made before the migration. |

No capability is absent from a FIPS build as a result: the v8 derivation is the
only one compiled, in every build.

**Compatibility**: the thumbprint derivation (base64url SHA-256 over SPKI) is
part of the on-disk contract. Changing it invalidates every existing binding.

## Test strategy

- **Unit**: `MatchesCert` for each `CertBindingKind`, thumbprint stability
  across certificate renewal for the same key, effective-permission resolution
  over `RoleIDs` ∪ `RoleBindings`, `Parse` rejecting unregistered strings, and
  the undeletability of system users and built-in roles.
- **Renewal stability is its own test** — it is the property the SPKI choice
  exists for.
- **Authorization tests are security tests**: every transport must reach the
  same decision for the same identity and permission.
- **Error tests** per function.

## Observability

Authentication and authorization decisions are audited through
[../audit/](../audit/), including the actor, the role at event time, the
resource, the action and the outcome (`success`, `failure:<reason>`,
`denied:<reason>`).

**The audit record is part of the decision, not a side effect of it.** When the
audit sink cannot record an authorization decision, the middleware **refuses
the request** — `*audit.GateError` wrapping the sink's error, answered as
HTTP 503 / gRPC `Unavailable`. The refusal is uniform across every exit from
`authorize()`: the RBAC grant, the RBAC denial, the `IdentityGated`
short-circuit, the sealed-tenant `Unavailable` and the rate-limit refusal.

An operator may select the availability reading with
`auth.audit_failure_policy: "fail-open"`, which is compliance-relevant and
documented in [../audit/engineering.md](../audit/engineering.md#configuration).
It is not a fallback: nothing degrades to it, and selecting it warns at every
startup.

The refusal caused by an audit outage cannot itself be audited — the only sink
is the one that failed. It is carried instead by
`qrdb_auth_audit_write_failures_total{outcome="denied"|"permitted"}` and an
`ERROR` line on the operational log stream.
