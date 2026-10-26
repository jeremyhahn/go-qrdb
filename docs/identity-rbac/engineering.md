# Identity and RBAC — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-09
> Conflicts with this document are defects. Fix them here first.

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

Tenant-issued delegations (see
[architecture.md](architecture.md#tenant-issued-delegations)):

```go
// pkg/auth/delegation — the extension's content; no signing, no verification.
var OID asn1.ObjectIdentifier                    // 1.3.6.1.4.1.29377.101.24 (tp-delegation)
const DefaultLifetime, MaxLifetime = time.Hour, 24 * time.Hour
const MaxScope, MaxPrincipalLen = 64, 64
type Grant struct{ Tenant, Principal string; Scope []string }
func (g Grant) Validate() error                   // *GrantError
func (g Grant) Extension() (pkix.Extension, error) // always Critical
func FromCertificate(c *x509.Certificate) (g Grant, found bool, err error) // *ExtensionError
func CheckLifetime(c *x509.Certificate) error     // *LifetimeError

// sdk/go re-exports: DelegationGrant, DelegationGrantError,
// DelegationExtensionError, DelegationLifetimeError, DelegationOID,
// DelegationDefaultLifetime, DelegationMaxLifetime, DelegationMaxScope,
// DelegationFromCertificate, DelegationCheckLifetime.

// sdk/go/node — what an embedder sets to accept delegations.
func WithServicePrincipal(name string) NodeOption // the ONE principal accepted
// sdk.RegisterPermissionRoleGrant(name, perms...) — the principal's own grants,
// the upper bound of every delegation's scope.
//
// DelegatedContext presents a delegation the embedder holds in process
// (chain, leaf first) to the node's own delegation verifier and returns a
// context bound to the delegated identity in the delegating tenant.
// ErrNoServicePrincipal when none is configured; otherwise the verifier's
// typed refusal: *auth.DelegationRefusedError (auth.ErrForeignDelegationPrincipal
// for another principal; an x509.CertificateInvalidError with Reason
// x509.Expired once it has lapsed) or auth.ErrCertificateRevoked.
func (n *Node) DelegatedContext(ctx context.Context, chain []*x509.Certificate) (context.Context, error)

// pkg/auth/middleware — the one check both paths run.
func (m *AuthMiddleware) AuthenticateDelegation(ctx context.Context, chain []*x509.Certificate) (context.Context, *auth.Identity, error)

// sdk/go/transport: CertClient.IssueDelegation(ctx, *IssueDelegationRequest)
//                   (*IssuedDelegation, error)
```

### User entity — `pkg/user/types.go`

```go
type User struct {
	ID           uint64            `json:"id"`                     // store-assigned Snowflake; never zero when persisted
	Username     string            `json:"username"`               // unique within tenant; maps to cert CN
	Email        string            `json:"email,omitempty"`        // CANONICAL LOGIN KEY, unique within tenant
	Name         string            `json:"name,omitempty"`         // display only; never a lookup key
	TenantID     string            `json:"tenantID"`               // maps to cert OU
	RoleIDs      []string          `json:"roleIDs"`                // system-scoped role identifiers
	Lifecycle    Lifecycle         `json:"lifecycle,omitempty"`    // CANONICAL account state
	System       bool              `json:"system"`                 // built-in, cannot be deleted
	AuthMethods  []string          `json:"authMethods,omitempty"`  // permitted methods (policy), lowercased
	LastAuth     *time.Time        `json:"lastAuth,omitempty"`     // high-water mark; never moves backwards
	RoleBindings []RoleBinding     `json:"roleBindings,omitempty"` // {RoleID, TenantID} — tenant-scoped RBAC
	Attributes   map[string]string `json:"attributes,omitempty"`   // ABAC
	CertBindings []CertBinding     `json:"certBindings,omitempty"` // mTLS SPKI thumbprints
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
}
```

`User` has no factors field: enrolled auth factors live in `pkg/auth.AuthFactor`,
reached through `service.UserAuthFactorServicer`.

`User.CanAuthenticate()` is the check a login or credential-registration
ceremony makes. It is `Lifecycle == active` and nothing else: an absent
state fails closed exactly like an unrecognized one.

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

`User.MatchesCert(cert)` is the authoritative check — SPKI-based and
expiry-checked — and `auth/middleware.finalizeIdentity` is the one caller that
decides a login with it:

```go
// pkg/auth/middleware/cert_binding.go
func checkCertBinding(ctx context.Context, u *user.User, method authMethod, policy auth.MTLSBindingPolicy) error
func checkUnresolvedCertLogin(ctx context.Context, identity *auth.Identity, method authMethod, policy auth.MTLSBindingPolicy) error
```

It admits in exactly three cases and refuses otherwise:

| Case | Result | Why |
|---|---|---|
| `len(u.CertBindings) == 0`, policy `enrolled` | admit | an empty set states no restriction, the same statement an empty `AuthMethods` makes; requiring a binding of every account locks out every account nobody enrolled |
| `len(u.CertBindings) == 0`, policy `required`, cert-bound method | **refuse** | `*CertBindingRequiredError{AccountResolved: true}` — the fleet said every certificate login proves possession |
| method is not cert-bound (`certBoundMethods`) | admit | a forwarded on-behalf assertion and a capability module's session token present no certificate of the SUBJECT's — the connection's certificate is the forwarder's |
| `u.MatchesCert(cert)` | admit | possession proved by the handshake |
| enrolled, cert-bound method, no readable peer certificate | **refuse** | the account has enrolled certificates and this request produced none to compare |
| enrolled, cert-bound method, thumbprint matches nothing | **refuse** | `*CertBindingNotPermittedError` |

### Binding writes — `pkg/user/store.go`

Both binding verbs go through one guarded path:

```go
func (s *kvUserStore) AddCertBinding(ctx, userID uint64, thumbprint string, notAfterUnix int64) (*User, error)
func (s *kvUserStore) RemoveCertBinding(ctx, userID uint64, thumbprint string) (*User, error)
// both:
func (s *kvUserStore) mutateCertBindings(ctx, op string, userID uint64, mutate certBindingMutator) (*User, error)
```

`mutateCertBindings` reads the primary record's RAW bytes, hands the decoded
record to the mutator, and commits the resulting set **against those same
bytes** via `updateUserAt` — the half of `UpdateUser` that takes the base rather
than re-reading it.

**Why the split.** `UpdateUser` reads the record and guards on its own read. A
caller that read the record itself and then called `UpdateUser` would hold two
reads with a window between them that the conditional commit does not cover. On
`CertBindings` that window matters: an enrolment that read the set before a
concurrent withdrawal committed would put the withdrawn credential back, on an
account whose operator was told the withdrawal succeeded. Passing `base` straight
through makes that a **lost swap** instead.

A lost swap is then **retried**, not reported, on the argument `mergeLastAuth`
makes: "add T" and "remove T" are complete specifications of what to do to the
set, independent of what the caller last saw it contain, so re-reading and
re-applying against the record that actually won is always correct. The bound
(`certBindingMutationAttempts`) is small and there is no sleep — re-reading is
the only thing that can change the outcome. Exhausting it reports
`*ConcurrentUpdateError`: an operator withdrawing a compromised key must never
read a success that did not happen.

**Index handles are claimed, then their holder is read — and the holder can
leave in between.** Every handle `updateUserAt` takes (username, email, each
thumbprint it keeps or adds) goes through `claimOrHolder`: `PutIfAbsent`, and
on a held key a read of who holds it. A concurrent update of the same handle
can release it between the two steps; the key is then free, so the claim is
taken again rather than read as a conflict with "user 0". A key that changes
hands on every one of `handleClaimAttempts` claims fails with
`*HandleContendedError`. The rollback's `restoreHandle` uses the same claim, and
a key it finds re-inserted for the **same** user — the winning update kept the
handle — is already restored; only a key held by another user is
`*HandleReclaimedError`. Without both, two concurrent withdrawals of the
bootstrap admin's last two keys could fail the loser with an
`*IndexCompensationError` instead of judging it against the winner and refusing
it as a lockout.

**Withdrawal deliberately does not validate the thumbprint's shape.**
`IsValidSPKIThumbprint` gates writes only, so a stored record can hold a
non-canonical string; refusing to withdraw
one would make exactly the entries the gate exists to keep out the only entries
nobody can remove. A withdrawal that matches nothing returns
`*CertBindingNotFoundError` (404 / `codes.NotFound`) rather than succeeding
vacuously, and a withdrawal that empties the set emits an audit event carrying
`last_binding_removed: true`, because that is the moment the account widens.

`checkUnresolvedCertLogin` covers the subject that resolves to **no account**
(user store answered not-found, or no user store is wired): it admits under
`enrolled` and refuses a cert-bound method under `required` with
`*CertBindingRequiredError{AccountResolved: false}`. The sealed-tenant branch of
`finalizeIdentity` returns before either check, deliberately — see the
architecture's list of what `required` does not reach.

The policy is `auth.MTLSBindingPolicy` (`pkg/auth/binding_policy.go`), zero value
`MTLSBindingRequired`, resolved from configuration by
`auth.ParseMTLSBindingPolicy` (an unknown spelling is
`*InvalidMTLSBindingPolicyError`, never a guess) and threaded
`wiring.AuthMiddlewareConfig.MTLSBindingPolicy` →
`middleware.WithMTLSBindingPolicy`. The boot refusal is
`firstboot.VerifyAdminCertBinding(ctx, AdminLookup, now)` →
`*firstboot.AdminCertBindingRequiredError{Reason}` or `*firstboot.AdminLookupError`;
`firstboot.Initialize` runs the same predicate before its first write when
`Dependencies.MTLSBindingPolicy` is `required`, and `bootstrap.SystemConfig`
forwards the value to it.

`*CertBindingRequiredError` classifies as `AuthFailureUnauthenticated` for the
same reason as the rule it sits beside: the caller has no enrolled key to have
possessed, and enrolling the `PresentedThumbprint` it carries is what makes the
same certificate work.

`*CertBindingNotPermittedError` classifies as `AuthFailureUnauthenticated`
(401), not `AuthFailureUnauthorized` (403). The contrast with
`*AuthMethodNotPermittedError` beside it is deliberate: there the credential IS
the account's and the WAY in is refused, so retrying is futile; here the
credential was never the account's, and presenting the enrolled certificate
works. The error carries the presented thumbprint, which is the value an
operator enrols if the refusal turns out to be a missed enrolment.

The certificate is re-read from the connection state on the context rather than
carried on the identity: `Identity.Metadata` is a map the fallback and
on-behalf paths build themselves, so a thumbprint carried there would be a
claim, while the TLS state is the fact the authenticator read.

`certBoundMethods` is a map keyed by the normalized method name, so adding a
second certificate-presenting authenticator to the chain is one entry, and a
reader asking what the gate applies to gets a list rather than a condition.

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
| `ScopeSelf` | own tenant only | **own tenant only** (identical to `ScopeSelfStrict`) |
| `ScopeCross` | denied | any tenant |
| `ScopeTenantOnly` | own tenant only | **denied, structurally** |
| `ScopeSelfStrict` | own tenant only | **own tenant only** |

`ScopeSelfStrict` holds the `escrow:*`, `certs:*` and `trust_anchors:*`
families (`pkg/permission/self_strict_scope.go`); the two exceptions are
`certs:revoke:cross` (`ScopeCross`) and `certs:delegate` (`ScopeTenantOnly`).
A certificate minted for another tenant's user authenticates as that user, so a
system-tenant lift on `certs:*` would be a lift to every tenant's identities.
`TestCertsPermissions_AreSelfStrict` pins it. `certs:issue` signs a CSR for an
existing account and `certs:enroll` binds a bring-your-own certificate; the
built-in SO and Admin roles hold both. `ScopeSelf` and `ScopeSelfStrict` behave
the same; a new permission takes `ScopeSelf`.

The audit query is not decided by scope at all: its permission is checked
against the caller, and the filter is then confined to the caller's own tenant
by `tenant.ConfineAuditFilter`, on every transport, before the node-local
logger — which holds every tenant's events — is read. `tenant.Confine` is the
general form: an empty requested tenant means the caller's own, any other is a
`*tenant.CrossTenantDeniedError`, and unlike `tenant.EffectiveTenant` there is
no system-tenant lift.

**Every `ScopeCross` permission has an SDK name.** `sdk/go/types_permission_cross.go`
re-exports each one (`sdk.PermissionCertsRevokeCross`,
`sdk.PermissionAuditVerify`, the `tenants`,
`volumepool`, `registration_config` and `credentials` families,
the node-barrier `seal` and `ceremony` permissions, and the cluster `admin`,
`rebalance` and `backup`
permissions), and `TestSDK_CrossScopePermissions_AreComplete`
compares that list with the registry, so a cross-scope permission added without
an SDK name is a red test rather than a string a consumer has to restate.

**A denial is a type, and only a denial is.** The RBAC authorizer answers a
refused grant or a failed scope with `*rbac.PermissionDeniedError`, re-exported
as `sdk.PermissionDeniedError`. Every other error from `Authorize` — a role the
store cannot resolve, a cancelled context — means the authorizer could not
decide, and a consumer tells the two apart with `errors.As` instead of reading
every error as a denial.

**Choose by asking whose material the permission acts on, not who usually calls
it.** `ScopeTenantOnly` means *"tenant-admin-only, system-admin-denied, no
cross-tenant"*, and it exists for material held **on a tenant's behalf** — the
canonical case is per-tenant barrier key custody, which the platform operator
must not hold. A permission that reads the **daemon's own** state is not that,
even when it lives in a tenant-facing family.

**There is no default scope.** Every permission in `All()` declares its scope,
`LookupScope` returns `*UndeclaredScopeError` for one that does not, and the
authorizer denies on that error. `TestEveryPermissionDeclaresItsScope` in
`pkg/permission/scope_gate_test.go` walks the registry and fails the build on
the first undeclared member, so a permission added without a scope is a red
test rather than a quiet admit. `ScopeSelf` is the zero value of `Scope`, and
it is declared like the others — `pkg/permission/self_scope.go` names each
self-scoped permission individually.

**Declare each member of a family by name, never the family through a helper**:
a helper that later gains a member tags that member silently.
`orchestrator:health` sits in the broker data-plane family
(`brokerDataPlaneScopePermissions()`, `ScopeTenantOnly`) but reads the daemon's
own state, so it is listed in `nodeStateScopePermissions()` and declared
`ScopeSelf` by name in `self_scope.go`. Under the family's scope the operator
persona, the one that exists to watch the node, could not read the
orchestrator's health.

**A tenant cannot re-scope a built-in permission.** Per-tenant scope
definitions (`permission.TenantScopeStore`) exist for caller-defined
permissions only. `SetScope` refuses any permission that has a built-in scope
registration with `*BuiltInScopeOverrideError`, and `ResolveScope` fails closed
with the same error -- so the authorizer denies -- if it finds a stored
definition of one. Otherwise a tenant could
undo a platform decision such as the cross-scope tenant plan permissions
(`tenant_provisioning_scope.go`) and widen its own Admin back onto the platform
operator's surface.

**`secrets:*` is the family-call hazard's mirror image: one member of a
`ScopeTenantOnly` family that needed a DIFFERENT scope, not a wider one.**
`pkg/permission/broker_scope.go` registers the broker-domain family --
`secrets`, `trust`, `acme`, `mcp`, `agent:runtime` -- and every member except
`secrets:*` is correctly `ScopeTenantOnly`: those surfaces are held on a
tenant's behalf, so the platform operator must not exercise them.
`secrets:read/write/delete/list` name a per-tenant VAULT instead, and a vault
is exactly the case `ScopeSelfStrict` exists for: the system tenant needs one
over its own namespace as much as any other tenant does, and `ScopeTenantOnly`
would deny it that outright. `init()` tags the two groups with separate
`RegisterScope` calls, each naming its members explicitly, so an edit to the
family's list cannot move `secrets:*` onto the family's scope.

**`mvcc:compact` is `ScopeSelfStrict` for the same reason.** Compaction
discards a database's revision history, and each tenant -- the system tenant
included -- compacts only databases it owns. The compaction floor is per database
([../key-value/architecture.md](../key-value/architecture.md)), so the
permission's material is the tenant's own history, and a system lift onto
another tenant's would be a cross-tenant act.

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

### A scope check only constrains what the request lets it constrain

`ScopeSelf` compares the identity's tenant against `tenant.FromContext(ctx)`.
On the ordinary authenticated path **that context tenant is bound FROM the
identity**, so for a request that does not otherwise name a target the
comparison is the caller's tenant against the caller's tenant — vacuously true,
for every caller, always. The permission is checked; the scope is not.

For most permissions this is harmless, because the SERVICE behind them is
resolved per `(tenant, database)` and confined by its namespace whether or not
the scope check does anything. **It is not harmless for a permission whose
operation names a global identifier and whose service is a process-wide
singleton** — there the scope check is the only confinement, and it is inert.

The remedy is not a different default: it is for the handler to establish the
TARGET and authorize against it, through `AuthMiddleware.AuthorizeTenant` (or a
capability-specific wrapper over it), which injects the target so the same
scope rule evaluates against the request rather than against the ambient
tenant. The certificate-authority surface is the worked example — see
[../certificate-authority/architecture.md](../certificate-authority/architecture.md#which-tenant-a-certs-call-may-act-on),
where list takes the target from the request, revoke resolves it from the
serial, and issue and enroll act on the caller's own tenant.

**When the target cannot be established at all, that is not a self-scope
operation.** A revocation of a serial no record matches names no tenant, so it
gets its own cross-scope permission (`certs:revoke:cross`) rather than riding
on a self-scope one whose check can never fire. Declaring a second, cross-scope
permission for the unattributable case is the pattern.

**There is no `audit:read:cross`.** No caller reads another tenant's audit
events (the query is confined by `tenant.ConfineAuditFilter`), so no permission
names such a read.

**Pin the scope of each member of a family, not the family.** A test that loops
over the same list the registration loops over cannot see one permission's
classification change. A family's scope test enumerates the **exceptions** and
asserts every other member tenant-only, which is the fail-closed direction: a
permission added to the family and not listed as an exception is asserted
tenant-only, so getting a new permission's scope wrong fails a test.

## Stored ABAC policies

The stored-policy API keeps ABAC policy documents (`pkg/auth/abac.Policy`) as
records of a tenant. It stores and returns them; **it does not feed the
evaluator**, which reads only the `policy_dir` files and the SDK's
`ABACPolicySpec`.

| Symbol | Where | What |
|---|---|---|
| `PolicyServicer` | `pkg/service/policy_service.go` | `CreatePolicy`, `GetPolicy`, `ListPolicies`, `UpdatePolicy`, `DeletePolicy`. Every transport's policy endpoints delegate to it |
| `PolicyService`, `NewPolicyService(tenant.StoreResolver)` | `pkg/service/policy_service.go` | the implementation; a nil resolver is `*SubsystemNotConfiguredError` |
| `server.WithPolicyService` | `pkg/server/server.go` | hands the service to the REST, gRPC, Unix, QUIC and MCP servers. The node lifecycle builds it over the node's `StoreResolver` whenever it has one |
| `abac_policies:create`, `:read`, `:list`, `:update`, `:delete` | `pkg/permission/constants.go` | one permission per verb, ScopeSelf (`selfScopedPermissions`), granted to the built-in Admin role |
| `transport.ABACPolicyClient`, `ABACPolicyRequest`, `ABACPolicyListRequest`, `ABACPolicyListResponse` | `sdk/go/transport/policy_types.go` | the SDK surface, `Client.ABACPolicies()`, implemented by all six transports |
| `qrdb abac policy` | `cmd/qrdb/abac_policy.go` | the CLI ([cli.md](cli.md#abac-policies)) |

Invariants:

- **The caller's own tenant, only.** An empty tenant argument or
  `metadata.tenant` means `tenant.FromContext`; any other value is
  `*PolicyCrossTenantError`, a system-tenant caller included. A write fills an
  empty `metadata.tenant` with the resolved tenant.
- **Validated before stored.** Create and update validate and compile the
  document as the evaluator would, and work on a copy: the caller's policy is
  never modified. A name is 1 to 253 bytes of letters, digits, `.`, `_` and
  `-`, starting with a letter or digit, because it is a key segment.
- **Guarded writes.** Create is a put-if-absent; update is a guarded swap
  against the document read; delete is a guarded delete. A lost race is
  `*PolicyConflictError` with the store unchanged. A tenant store without the
  guarded-write capability (`kvstore.AtomicKVStore`) fails closed with
  `*PolicyStoreUnsupportedError`; nothing falls back to an unguarded write.
- **The document is never normalised.** Reads return the stored JSON decoded
  as written, so `spec.enabled` keeps its presence (the gRPC message carries it
  as `optional bool`). The server sets `metadata.created` on create and
  `metadata.updated` on create and update; an update keeps `created`.
- **List is sorted by name** and returns the whole set, unpaginated.

## Invitation self-enrolment

`pkg/service.InvitationServicer` is the one surface every transport's invite
and redeem verbs delegate to:

```go
type InvitationServicer interface {
    InviteUser(ctx context.Context, req *InviteUserRequest) (*Invitation, error)
    RedeemInvitation(ctx context.Context, req *RedeemInvitationRequest) (*EnrolledCertificate, error)
}
```

`InviteUser` depends on `InvitationStore` (`*invite.TenantStore`, over the
same `tenant.StoreResolver` user/role IAM data already routes through —
`lifecycle.Node.StoreResolver`) and `InvitationUsers`
(`user.UserServicer` + `user.CertActivator`); `RedeemInvitation` additionally
needs `cert.CertProvider` (CSR issuance), `trustanchor.Source`
(bring-your-own verification) and a `TenantGetter` (an unauthenticated
caller names the tenant, so its existence is checked before anything else
touches it). `lifecycle.Node.Invitations` is non-nil exactly when
`Node.CertEnrollment` is: both require a certificate provider, and
redemption issues through the same Key B CA `SignCertificate` does.

**No separate target-authorization machinery.** A tenant Admin does everything
in its own tenant except SO/custodian custody (see
[../multi-tenancy/architecture.md](../multi-tenancy/architecture.md)), so
`InviteUser` and the pending-account check in `RedeemInvitation` both call the
SAME `certTargetGuard` function `certs:issue` does. Nothing here re-derives who
may hold a certificate.

**The invitation-store layer (`pkg/invite`) is a bearer-secret store, not a
generic KV wrapper.** `Entity.TokenHash` is the only representation of the
token that ever reaches storage; `Entity.KeyPin`, when set, is NOT a secret
(it is the public SPKI thumbprint of the key the invitation is pinned to) —
the redeemer proves possession of the matching private key by signing the
CSR it presents, not by presenting the pin itself.

**A miss and a storage fault are different errors, end to end.**
`kvstore.KVStore.Get` is specified as `(nil, nil)` on a missing key, so
`pkg/dao`'s `genericDAO.Get` treats a nil read as `dao.NotFoundError` and
returns every other failure unchanged — `dao.IsNotFound` is the one check a
consumer makes to tell them apart, and it is never duplicated with a
package-local type. `DAOStore.find` and `.checkUsable`
(`pkg/invite/store.go`) use it to decide between the two cases a read can
mean: `ErrNotFound` (or, in `checkUsable`, "not yet redeemed") for a
confirmed miss, and `*invite.StoreError` wrapping the cause for anything
else — a storage or transport fault must never be reported as the token
being unknown, inside `pkg/invite` or by the service above it. `*StoreError`
classifies as Internal (`pkg/api/errclass`'s generic `*<pkg>.StoreError`
row, the same one `user`/`role`/`cert`/`tenant` use for their own backing
stores), so it renders as a server fault on every transport rather than the
Unauthenticated a bad token gets.

`InvitationService.RedeemInvitation` keeps the single opaque
`*InvitationInvalidError` for a bad, expired, used or wrong-tenant token —
and for a sealed or deleted tenant, which the SAME opaque answer must cover
since telling it apart from a bad token would let an unauthenticated caller
enumerate tenants. It does NOT fold a `*invite.StoreError` into that answer:
`errors.As` at the `Lookup` and `Redeem` call sites lets the fault through
unchanged, so an outage is retried and alerted on rather than silently read
as every presented token being forged for as long as it lasts. See
[../../pkg/invite/store_failures_test.go](../../pkg/invite/store_failures_test.go)
and [../../pkg/service/invitation_test.go](../../pkg/service/invitation_test.go)
for the tests that pin this down.

**No invitation for a tenant's first admin.**
`service.TenantOnboardingService.OnboardTenant` enrols the first admin by
certificate (`bootstrap.AdminEnrollCert`) and has no deferred-admin mode, so
`TenantProvisionResult` carries no invitation token.

**gRPC and the Unix socket carry invitations through the shared protobuf
stubs.** `InviteUser` and `RedeemInvitation` are `qrdb.v1.QRDBService` RPCs
(`pkg/api/grpc/proto/qrdbv1/qrdb.proto`), generated with `protoc` plus
`protoc-gen-go`/`protoc-gen-go-grpc` via `make proto`. The Unix socket
transport shares the TCP gRPC server's `qrdbServiceServer` implementation
(`RegisterService`/`WithRegisterInvitationService`) and registers
`RedeemInvitation` as a public method standalone, exactly as the TCP server
does, so it is correct even when the Unix socket is enabled without TCP
gRPC. `InviteUser` is authorized like `IssueCert`/`EnrollCert`:
`authorizeCertTenant` against the caller's own tenant, no `(tenant, id)`
target-table entry needed. Every transport — REST, native QUIC, gRPC, the
Unix socket, MCP and the embedded transport — carries the full surface.

## Invariants

- **A delegation certificate is never a user login.** Once the authenticator
  finds the delegation extension it either admits the certificate as the
  delegated service identity or refuses it; there is no fallback to the CN/OU
  user path. A malformed or non-critical extension is a refusal, not "no
  delegation".
- **A delegation's issuer is the delegating tenant's own login CA, and only
  that.** The check is on the leaf's DIRECT issuer against the tenant's
  registered Key B certificate, so the cluster CA, a static client CA, a trust
  anchor and every other tenant's CA are refused even where they chain.
- **A delegated identity's permissions are recomputed on every request** as
  the grant's scope ∩ `permission.HasRoleGrant(principal, p)`. Nothing is
  cached across requests, so withdrawing a host grant takes effect at once,
  and the identity carries no roles for a role-based path to widen.
- **Delegated requests are never unaudited.** The authorization record goes to
  the delegating tenant's trail; an absent logger or a failed write refuses the
  request with 503 regardless of the audit fail-open setting.
- **A forwarded delegated request is re-verified on the leader.** The follower
  carries the delegation certificate inside the signed on-behalf assertion and
  the leader runs its own authenticator over it, so a follower cannot widen,
  extend or re-tenant a delegation. A follower that cannot sign the assertion
  refuses to forward (`UnsignedForwardRefusedError`) rather than forwarding
  unsigned.
- **Issuance is the tenant's act alone.** `IssueDelegation` is confined with
  `tenant.Confine`, refuses the system tenant and a delegated caller, validates
  every scope entry against the permission registry, and writes `cert.delegate`
  to the tenant's trail before the CA signs.
- **`ReconcileBuiltInRoles` is the ONLY writer to a built-in role's
  permissions.** `UpdateRole` returns `SystemRoleImmutableError{Op:
  "re-permissioned"}` and `DeleteRole` returns `SystemRoleImmutableError{Op:
  "deleted"}` for every other caller. Reconciliation
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
- **`DeleteRole` strips the role from every holder before the record goes.**
  It first marks the role `Deleting`, a persisted flag that is never cleared.
  From that write on the role grants nothing (`ResolvePermissions` skips it),
  and both `ValidateAssignable` and `UpdateRole` refuse it with
  `RoleDeletingError`. It then calls the store's `HolderRevoker`
  (`user.NewRoleRevoker` over the user store). The revoker sweeps the role's
  tenant index and rewrites every account that holds the role in `RoleIDs` or
  `RoleBindings`. Each rewrite is a compare-and-swap against the bytes just
  read, so a concurrent edit to the account is re-read and stripped again,
  never overwritten. The sweep repeats until a pass changes nothing, at most
  eight passes, after which it returns `RoleRevocationIncompleteError`. Only
  then are the record and its indexes deleted.
  - **A strip that fails part way leaves the role in place, still marked.**
    The failure is returned as `HolderRevocationError`, and retrying
    `DeleteRole` finishes the job, because both the mark and the strip are
    idempotent.
  - **A store built without a revoker refuses `DeleteRole`** with
    `HolderRevokerRequiredError` rather than leave the role on its holders.
  - **The sweep reads one tenant.** Holders always live in the role's own
    tenant, because assignment refuses a role from another tenant.
  - **Two windows remain:**
    - an account already in the terminal `deleting` lifecycle refuses every
      edit, so it keeps the id until its own delete removes it;
    - an assignment validated before the mark, but committed after the final
      sweep, leaves the id behind.

    Either case resolves to `RoleNotFoundError` at permission resolution
    (see Error semantics), never to a grant.
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
- **`UpdateUser`, `EnableUser`, `DisableUser` and `DeleteUser` commit conditionally** against
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
- **Enrolment is a login gate, not only a lookup seam.** Binding a thumbprint
  to an account narrows what may authenticate as it; binding the WRONG
  thumbprint, or binding one and then rotating the key without enrolling the
  new one, refuses that account. Enrolment is additive, so the recovery is to
  enrol the new key — ideally before retiring the old one, which never
  interrupts access.
- **Withdrawal is the only unenrolment, and it is a separate authority.**
  `users:remove_cert_binding` is not folded into `users:add_cert_binding`
  because enrolment only narrows an account while withdrawal of the LAST
  binding widens it back to CN/OU admission. Both permissions declare
  `ScopeSelf` beside their definitions (`pkg/permission/constants.go`) rather
  than in `self_scope.go`, because these two decide which certificates
  authenticate as an account.
- **Withdrawal and revocation are independent.** Revocation refuses a
  certificate; a binding is over the key and outlives every certificate issued
  for it, so a re-issue for the same key authenticates until the binding is
  withdrawn. Revoking does not drop a matching binding, because a revoke
  performed for routine rotation would then silently unenrol the key being
  rotated.
- **`RoleBindings` are additive to `RoleIDs`**, not a replacement. Effective
  permissions are the union.
- **Effective permissions resolve through one PDP** on every transport;
  `ResolveRolePermissions` exposes that resolution for inspection.
- **`Parse` rejects unregistered permission strings.** The vocabulary cannot be
  extended at runtime by a caller.
- **`Lifecycle` is the only account-state field.** There is no `Enabled`
  boolean on the domain type or on any transport; a second field describing the same state is how a
  record ends up claiming to be locked and active at once.
- **An absent state fails closed.** The store writes `Lifecycle` on every
  create, so a persisted record always carries one. A record without it, or with
  a state this build does not recognize, cannot authenticate and cannot be
  transitioned. There is no decode-time fallback: such a record must be
  recreated.
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
  Refusing only the lifecycle transition would leave every other field writable,
  so a caller could bind a certificate to an account whose deletion had begun —
  the exact orphan the marker exists to prevent.
- **`AuthMethods` is ENFORCED at qrdb's authentication gate**, not merely
  stored. `pkg/auth/middleware` carries the name of the authenticator that
  succeeded and refuses with `AuthMethodNotPermittedError` (403 /
  `PermissionDenied`) when the account's policy is non-empty and does not list
  it. Two limits are stated rather than assumed: an EMPTY policy states no
  restriction; and the check
  applies only where the method is OBSERVABLE — a forwarded on-behalf assertion
  and a capability module's session token carry none qrdb can see, and the layer
  that ran that ceremony enforces the policy there. That distinction is carried
  by an explicit flag (`authMethod.known`), never by an empty name: an
  authenticator that names itself `""` is observed, not unobservable, and
  conflating the two would let it walk past a non-empty policy through the ordinary
  chain. An observed method whose name does not normalize fails closed.
- **A forwarded on-behalf request is not read until its presenter is a
  cluster node, and then only under a bound.** The leader must read the body
  to check the assertion's body digest. Before it reads anything, it
  requires the verified, unrevoked peer certificate to be a cluster node's
  (`forward.ClusterPeer`). Any other certificate that chains to the cluster
  CA, such as a user's, can never present an honored assertion, and is
  refused with the same opaque 401 as every other assertion failure. A
  cluster node's body is then read under `forward.MaxBodyBytes`, which is
  the largest body any REST handler forwards (a test in `pkg/api/rest` keeps
  every forwarding handler's bound within it).
- **A forwarded holder stays a holder.** The on-behalf assertion
  (`forward.CallerAssertion`) carries the caller's subject and tenant, and,
  for a caller that authenticated with a holder certificate, its
  `holder_profile` (`security-officer` or `custodian`). The field is signed
  with the rest of the assertion and omitted for users. The leader re-marks such an identity, so
  `finalizeIdentity` never resolves it to a user account; an assertion naming
  a profile the leader does not recognise is refused with the usual opaque
  401. A holder request is never forwarded unsigned, because an unsigned
  forward is authorized as the forwarding node: a follower with no signing
  identity, or whose signing fails, answers **502** with a fixed message
  (`rest.UnsignedForwardRefusedError`) and logs the cause; the request never
  reaches the leader. A request authenticated with a tenant-issued delegation
  is refused the same way. A user request in the same situation is forwarded
  without an assertion.
- **A forwarded delegation is verified again on the leader.** For a caller
  that authenticated with a tenant-issued delegation certificate, the
  assertion carries the certificate itself (`delegation`, base64 DER), signed
  with the rest of it. The leader rebuilds the identity from that certificate
  through its own mTLS authenticator (`middleware.WithDelegationVerifier`) --
  chain to the tenant's own login CA, lifetime, revocation, principal -- and
  refuses the assertion when the certificate's subject or tenant differs from
  the asserted ones, when it also claims a holder profile, or when the leader
  accepts no delegations at all.
- **The rule is one exported function, `CheckAuthMethod(u, method)`**
  (`pkg/auth/middleware`, re-exported as `sdk.CheckAuthMethod` with
  `sdk.AuthMethodNotPermittedError`). The middleware calls it for every method
  its authenticator chain observes; a layer that runs its own ceremony (a PIV or
  WebAuthn login handler) calls the same function rather than restating the
  rule. `TestCheckAuthMethod_IsTheMiddlewareRule` pins that the two agree on
  every observed method.
- **The method tokens are qrdb constants.** `auth.MethodMTLS` (`"mtls"`),
  `auth.MethodPIV` (`"piv"`) and `auth.MethodWebAuthn` (`"webauthn"`), each
  re-exported by the SDK. A method is a way in, not an enrolled factor, so
  there is no `"fido2"` method: a FIDO2 authenticator is presented through the
  WebAuthn ceremony. PIV and mTLS are separate tokens so a policy can permit one
  without the other.
- **The chain is assembled in one place, `auth.AuthenticatorChain`.**
  `wiring.BuildAuthMiddleware` hands it the mTLS authenticator and the
  composer's `[]auth.AuthenticatorPlacement` (`AuthMiddlewareConfig.ExtraAuthenticators`,
  which `node.WithAuthenticators` fills) and installs the result in order.
  Positions are validated against one map (`chainPositionNames`), so the zero
  `ChainPosition` is refused with `*auth.InvalidChainPositionError` rather than
  read as a default. A placement with a nil authenticator installs nothing but
  its position still counts: a `ChainReplaceMTLS` placement removes mTLS
  whether or not it carries an authenticator, and `node.WithAuthenticators`
  with no authenticators records its position for the same reason. A chain
  left empty is `*auth.EmptyAuthenticatorChainError`. The transports need no
  per-position wiring: each runs the one middleware's chain.
  `TestWithAuthenticators_PositionDecidesACertificateRequestOnEveryTransport`
  pins the three positions on REST, gRPC, Unix, MCP and QUIC with a request
  that presents an enrolled client certificate, and
  `TestWithAuthenticators_PositionDecidesTheEmbeddedCaller` pins them on the
  embedded client's forwarding caller.
- **The vocabulary is the node's own auth chain, enforced on write.** It is
  the built-in `mtls`, `piv` and `webauthn` plus the `Name()` of every
  authenticator in THAT node's chain — including those an embedder plugs in
  through `wiring.AuthMiddlewareConfig.ExtraAuthenticators` or the SDK node's
  `WithAuthenticators`. The middleware owns
  it (`AuthMiddleware.IsAuthMethod` / `AuthMethods`, fixed at `New`), and the
  node's user stores receive it through `user.WithAuthMethodVocabulary`,
  wired by `lifecycle.BuildServices` (`Infra.AuthMethodVocabulary`), the SDK
  node and `qrdbd` from the node's own middleware. Because the store is built
  before the middleware that enriches from it, the wiring goes through a
  late-bound `middleware.AuthMethodVocabulary`, bound once the middleware
  exists; unbound, or with auth disabled, it is the built-in tokens. There is
  no process-wide registry: two nodes in one process have two chains, and
  `TestAuthMethodVocabulary_IsPerNode` pins that `oauth` is writable only on
  the node whose chain has it. A login method a host wants policies to name
  must be an authenticator in the chain. The store refuses a token outside its
  vocabulary with `*UnknownAuthMethodError`, which unwraps to a
  `*ValidationError` (400 / `INVALID_ARGUMENT` on every transport);
  `CreateUser` and `UpdateUser` are the only write paths and both apply it —
  to a policy being WRITTEN only. The package-level `NormalizeAuthMethods`
  has no node and validates against the built-ins.
- **Legacy entries cannot lock an account out.** A stored policy is never
  re-validated: it reads back unchanged, an update that does not replace it
  carries it along, and the gate compares entries with
  `auth.CanonicalAuthMethod` (trim and lowercase, no vocabulary check). An
  admin replacing a legacy policy may KEEP an entry the old policy held even
  if the vocabulary no longer knows it, but may not ADD a new unknown one; once
  dropped, it cannot be re-added.
- **`EnableUser` / `DisableUser` are transitions, not boolean sets** — to
  `active` and `disabled` respectively. Both are refused on a `deleting` record.
- **`AuthMethods` are vocabulary tokens and case-FOLDED.** They are trimmed and
  de-duplicated by `NormalizeAuthMethods`, a blank entry is refused rather than
  dropped, and an entry outside the vocabulary is refused (above). On an update, `nil` means "leave alone" and an empty non-nil
  slice means "clear".
- **There is no group-membership field.** No qrdb authorization path would
  read one.
- **`LastAuth` only moves forward.** A value at or before the stored one is
  ignored, so overlapping logins written out of order cannot roll an account's
  last-seen time backwards. It is stored in UTC.
- **A `LastAuth`-only update is a commutative merge and RETRIES a lost commit**
  rather than returning `ConcurrentUpdateError`. Every other field is a
  replacement, where a lost race means the caller's view was stale and only the
  caller can decide what to do; `max(stored, requested)` has nothing stale to
  reconcile, so re-reading and recomputing is always correct. It matters because
  of who writes it: `LastAuth` is written on the login path, and routing a login
  through the general update path would lose its timestamp — and lose it
  *below* the true high-water mark — whenever an administrator was editing the
  same account. The retry is bounded (`lastAuthMergeAttempts`) with no sleep,
  and writes nothing at all when the stored value is already at or past the
  request. `lastAuthOnly` is an exhaustive field list: a field added to
  `UpdateUserRequest` without being named there would be silently dropped by the
  fast path, and a test asserts the list covers every field.
- **`System: true` records are undeletable**, and exactly one is ever produced:
  the cluster-wide bootstrap admin `pkg/firstboot.Initialize` creates in the
  system tenant. `DeleteUser` refuses it with `SystemUserImmutableError`
  before the delete cascade touches anything. `CreateUserRequest.System` is
  the only way to request one, and it is set from a single call site inside
  `pkg/firstboot` — no wire request type on any transport declares a `system`
  input, so no transport handler can forward one through, regardless of the
  caller's permissions. A per-tenant admin minted by
  `pkg/bootstrap.ProvisionTenant` deliberately leaves this false: those
  accounts must stay deletable when their tenant is deprovisioned, or
  deprovisioning could never fully remove a tenant's users.
- **At most one System user exists, recorded by a uniqueness sentinel.** The
  sentinel names the System record and is written in the same write as the
  flag: on the atomic create path it is one more guard and write in the
  create's guarded batch, and `PromoteToSystemUser` — which first boot calls
  on an existing admin record that lacks the flag — writes the flag and the
  sentinel as ONE `kvstore.MultiGuardedBatchKVStore.PutAllIfAllUnchanged`,
  guarded on the sentinel being absent and the record being unchanged since
  it was read. No crash can leave one without the other, a concurrent
  promotion of a different record fails the guard, and a concurrent update of
  the admin is retried rather than overwritten. A store without that
  capability is refused (`ErrStoreNotAtomic`) instead of being promoted in two
  steps, and a lost response is `CommitOutcomeUnknownError`: the retry answers
  from what committed.
- **MFA state and `Factors` metadata are computed AT READ TIME from the real
  enrollment records, never stored on the domain `User`.** There is no
  `User.Factors` field: `pkg/user.FactorLister` is the narrow interface
  every transport's projection consumes, backed in production by
  `service.UserAuthFactorServicer` — the SAME store `/v1/users/factors`
  reads and writes. `user.ResolveFactorView` lists the account's factors
  ONCE and derives everything from that one list: `Factors` (`ID`,
  `FactorName`, `CreatedAt` for each active factor; `Data` is dropped
  unconditionally), `MFAEnabled` (true iff at least one active factor),
  `MFAMethods` (distinct factor types in first-registration order) and
  `MFAEnrolledAt` (earliest active registration). One list means `Factors`
  and the MFA fields can never describe two different moments. A failed list
  is `*user.FactorLookupError` — 503 on REST, `Unavailable` on gRPC, QUIC and
  MCP, the typed error on embedded — never a response reporting the account
  as having no second factor. No transport's create or update request
  declares a `factors` input, so enrollment stays its own factor-specific
  verb, never `CreateUser`/`UpdateUser`.
- **The system-tenant rule reads `identity.TenantID`, so that field must
  arrive NORMALIZED.** `rbac.IsSystemTenant(identity)` is what lifts a
  `ScopeCross` permission and what denies a `ScopeTenantOnly` one; it delegates
  to `tenantid.IsSystem`, whose empty-string branch is safe only because
  the boundary already folded `""`. `auth/middleware.finalizeIdentity` performs
  that fold — on the identity as well as the context; folding only the
  context would leave the authorizer reading the raw value.
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

## Authorization model

Writing `RoleIDs`, `RoleBindings`, or `Attributes` requires `roles:assign` —
the same permission the RBAC model already defines for granting a role
directly — in addition to the route's own `users:create`/`users:update`
permission. `pkg/auth/middleware.AuthorizeRoleAssignmentWrite` (`UpdateUser`)
and `AuthorizeRoleAssignmentWriteOnCreate` (`CreateUser`, `RoleIDs` only —
`RoleBindings`/`Attributes` are not settable at create time) are the ONE
gate, called by every transport. Embedded calls it on the `AuthMiddleware`
installed with `embedded.WithAuthMiddleware`; `sdk/go/node` builds its
network `AuthMiddleware` before the embedded transport and hands it the same
instance, so the two authorize identically — and neither authorizes anything
when auth is disabled.

Each gate is two steps with different conditions:

1. **Authorization** — `roles:assign` in the target tenant. A nil
   `*AuthMiddleware` (no RBAC configured) skips this step, as every
   `AuthMiddleware` gate does.
2. **Validation** — never skipped. A `RoleBindings` entry naming any tenant
   other than the target user's own is refused with
   `*user.RoleBindingTenantError` (the store's own rule, below, applied
   before any role is resolved; it runs after authorization, so a caller
   refused `roles:assign` learns nothing about the binding). Then
   `role.ValidateAssignable` confirms every `RoleIDs` entry and every
   binding's role exists and belongs to the target tenant. There is no exception for
   built-ins: every tenant seeds its own copy, and the role store resolves an
   id only in the tenant that owns it, so a system-tenant built-in written
   onto a tenant-A principal would make every later decision for it fail as
   unavailable. A role reference with no role service to check it is
   `*role.ServiceRequiredError`: a refusal, never an unvalidated write.

The user store then applies `user.NormalizeRoleBindings` to the request's
bindings against the decoded record, before the first index mutation, so it
holds on every path that writes a user and with or without the gate:

- **Each `TenantID` is stored as `tenantid.Normalize` folds it.** `boundRoleIDs`
  compares it with `tenant.FromContext`, which is always normalized, so a
  binding stored as `""` would never match a request. The request's slice is
  copied, not edited.
- **Every binding must name its holder's own tenant**, for a system-tenant
  user exactly as for any other, so no binding grants anything outside its
  holder's tenant. Any other is refused with `*user.RoleBindingTenantError`
  (`InvalidArgument`, 400). This does not by itself confine a system-tenant
  Admin: `tenant.EffectiveTenant` / `tenant.OwningTenant` route a
  system-tenant caller into the tenant a request names and the ScopeSelf rule
  admits it there, so it can still administer a tenant's users and roles.
  That gap is the strict tenant-isolation design's to close.

`AssignRole` and `UnassignRole` pass the same gate on every transport,
embedded included: `AssignRole` is `AuthorizeRoleAssignmentWrite` with the
one role being added (`roles:assign` in the user's tenant, and the role must
exist there), and `UnassignRole` is `AuthorizeRoleUnassignment`
(`roles:unassign` in the user's tenant; removing a grant names no role to
validate).

Under strict separation of duties (`middleware.WithStrictSeparation`, set from
`wiring.ResolveStrictSeparation`, the one resolver, which FIPS 140-3 mode
forces on), every one of those gates also refuses, with `SORoleRequiredError`,
a write that changes whether the target holds its tenant's `so` role unless the
caller is a Security Officer of that same tenant; outside it the `so` role is
assigned like any other. Both sides are read by
`heldRoleIDs`: the target's `RoleIDs` and `RoleBindings`, and the caller's
`Roles` and `RoleBindings` (`callerRoleIDs`), counting a binding only where it
is scoped to the target tenant. The gate reads the record before the write;
to bind the decision to the version the write replaces, each handler also
passes `AuthMiddleware.SORoleGuard` as `user.UpdateUserRequest.Guard`. The
user store calls the guard with the decoded record it commits over and the
record as the request writes it, before any index mutation, and the commit is
conditional on those bytes, so a Security Officer grant that lands after the
gate is either seen by the guard (`SORoleRequiredError`) or fails the write
(`ConcurrentUpdateError`); it is never overwritten.

The resolved posture is recorded and its changes are audited.
`wiring.RecordSeparationPosture` compares the posture
`ResolveStrictSeparation` gives with the replicated system record
`__sys:cluster/separation_of_duties` (`wiring.SeparationPostureKey`, value
`strict` or `standard`). It runs once first boot has run: in the daemon at
every start (`recordSeparationPostureAtStart` in `cmd/qrdbd`), and in an
embedded node in `Node.FirstBoot` and `Node.FirstBootWithCeremony`, through
`lifecycle.Node.RecordSeparationPosture`. When no posture is recorded, or the
recorded one differs, it **writes the record first**: `PutIfAbsent` on a first
recording, `CompareAndSwap` against the value it read on a change, retried
from a fresh read up to three times. Only then does it emit **one** compliance
audit event: actor `system`, resource `auth/separation_of_duties`, action
`posture-change` (`wiring.ActionSeparationPostureChange`), with the attributes
`previous` (`none` when no posture was recorded), `posture`, and
`fips_forced`. The event is sent through `audit.EmitBestEffort`. Only the
node whose conditional write wins emits it, so nodes starting together under
one change leave exactly one event. An unchanged posture writes and emits
nothing. A record that cannot be read or written answers
`*wiring.SeparationPostureError` (`Op` is `read`, `create`, `swap` or
`contended`), which refuses the daemon's start and fails the embedded first
boot. The write needs no Raft opcode of its own. Embedders read the posture
the node resolved through `node.Node.StrictSeparation()`, and the resolver
itself as `sdk.ResolveStrictSeparation`.

The target tenant for `UpdateUser` is resolved by READING THE EXISTING RECORD
(`userSvc.GetUser(ctx, id).TenantID`), never taken from the caller's ambient
context tenant: `UpdateUser` routes its target purely by id, so a
system-tenant administrator updating a tenant-A user is authorized, and has
its `RoleIDs` validated, against tenant A. `CreateUser`'s target tenant is
the EFFECTIVE tenant the new record will be created in
(`tenant.EffectiveTenant`), the resolution `routingUserService.CreateUser`
performs itself.

The gate's refusals are typed, and every transport renders them through the
one classification table (`pkg/api/errclass`), not a mapper of its own. A role
that does not exist is `*role.ReferenceError` (it wraps the lookup's
`*role.RoleNotFoundError`) and a role that may not be granted where it is
being granted is `*role.NotAssignableError`; both are `InvalidArgument` (400),
DELIBERATELY not the `NotFound` a missing user gets: `UpdateUser` is a
single-subject method whose id names the USER, and the SDK's not-found
reconstruction rebuilds `transport.UserNotFoundError` from that id whenever
the status means "not found", so a bad ROLE reference reported that way would
name the wrong subject as missing. The `ReferenceError` row sits above the
`RoleNotFoundError` row for exactly that reason. `*role.ServiceRequiredError`
is `Unimplemented` (501): a composition fault, not a caller error. A target
user the gate cannot find is the ordinary `*user.UserNotFoundError`.

`pkg/auth/rbac.RBACAuthorizer.Authorize` consumes `RoleBindings` on the READ
side. `finalizeIdentity` clears `auth.Identity.RoleBindings` and
`Attributes` unconditionally before the user lookup, then fills them from the
record, so the store is their only source and a withdrawn binding stops
applying on every path — found, not found, tenant sealed, and no user store
at all, where an authenticator-asserted binding has no record behind it. The
bindings whose `TenantID` equals the check's target tenant are resolved
SEPARATELY from `identity.Roles`, in that tenant, and the two permission sets
are unioned. Resolving both as one role-id list in one tenant would fail
the whole call whenever the tenants disagreed,
because `kvRoleStore.ResolvePermissions` errors on any id absent from the
tenant it resolves against. The FIPS 140-3 SO deny-list and the strict
separation-of-duties Owner strip apply to that effective set: a bound SO
role is resolved in its tenant and denied like a held one, and a bound Owner
role is stripped in the tenant that issued it.
`pkg/auth/rbac/role_binding_test.go` proves it against a tenant-aware role
double that fails, as production does, on an id from the wrong tenant.

**Entry checks run in the caller's own tenant.** Every transport's entry
permission check — the REST route middleware, the gRPC and unix interceptor,
QUIC's `AuthorizeQUIC`, MCP's `AuthorizeMCP`, and the embedded transport's
per-operation `authorize`/`authorizeTenant`/`authorizeUserTarget`/
`authorizeRoleTarget`/`authorizeIdentityTenant` helpers — authorizes in the
tenant the authenticated context is bound to. There is no route-level
re-targeting to a tenant, user or role the request names: with bindings
confined to their holder's own tenant there is nothing for one to find. For
a caller outside the system tenant, a request naming another tenant's user
or role is answered not-found by the routed services, and one naming another
tenant outright on a create, list or name lookup is refused by
`tenant.EffectiveTenant` with `*tenant.CrossTenantDeniedError`
(`PermissionDenied`). A system-tenant caller is routed into the tenant it
names (the known gap in [architecture.md](architecture.md)). An operation
whose handler acts on a tenant the request names re-checks at the handler, in
that tenant: the per-tenant barrier operations (`AuthorizeTenantTarget`), a
tenant's own record, quotas and entitlements, certificate issuance and
revocation (`AuthorizeCertTenant`, `AuthorizeCertRevoke`), audit queries
(`AuthorizeAuditQuery`), and enabling encryption
(`AuthorizeBarrierEnableEncryption`).

**The embedded tenant-administration surface calls the network transports'
helpers.** `GetTenant`, `ListTenants`, `UpdateTenant`, `DeleteTenant`,
`ReconcileDeletedTenants` and every `TenantBarrier*` operation on the
embedded transport (`sdk/go/transport/embedded/tenant.go`,
`tenant_seal.go`) call the same helper the network transports resolve to:
`AuthMiddleware.AuthorizeTenant` for the record and
`ScopeTenantOnly` barrier operations, `AuthorizeTenantUpdate` for a rename
that may also change the isolation tier, `AuthorizeTenantBarrierInit` and
`AuthorizeTenantBarrierSealedOp` for the two bootstrap/sealed-window
fallbacks, and `FilterTenantList` to narrow `ListTenants` the same way REST's
`handleListTenants` does. The per-tenant share-claim and quorum handshakes
carry no RBAC permission on any transport (`permission.IdentityGated`); on
the embedded transport both call `requireIdentity` (the `IdentityGated`
floor), as every other `IdentityGated` operation does.
`tenant_authz_test.go`'s
`TestTenantClientAuthorizationSurface_CoversEveryMethod` enumerates every
`transport.TenantClient` method via reflection and fails the build if a new
one ships with no declared authorization mechanism;
`tenant_authz_restparity_test.go` runs the same (identity, operation, target)
matrix through both the embedded transport and a REST server sharing one
`AuthMiddleware` and one `*authtest.Tenants` service, and fails if the two
ever disagree.

**The embedded transport's ENTIRE operation surface is authorized, not only
tenant administration.** One pluggable auth chain serves every transport, so
an embedded KV `Put`, a `GetUser`, a
`DeleteRole`, a `RevokeCert` — everything `*Transport` exports — authorizes
through the SAME call and the SAME `pkg/permission` entry the equivalent
network route does. `sdk/go/transport/embedded/transport_authz_registry.go`
is a transcription of `pkg/api/quic/dispatch.go`'s method→permission table
(the one dispatcher that already states every operation's permission in
one place) onto the embedded method names, checked with
`AuthMiddleware.AuthorizeTenant(ctx, tenant.FromContext(ctx), perm)` for
every operation — ambient-self throughout: a RoleBinding is confined to its
holder's own tenant, so there is no per-request target to resolve. `transport_identity_authz.go` carries the user/role entry
checks (`authorizeUserTarget`/`authorizeRoleTarget`), which authorize in the
caller's own tenant and rely on the routed user/role service
(`tenant.OwningTenant`) to confine a by-id lookup. `transport_authz_coverage_test.go`'s
`TestTransportClientAuthorizationSurface_CoversEveryMethod` widens the
tenant-only buildgate above to reflect over every exported method of the
concrete `*Transport` type (not `transport.Client`, which omits RAG,
embedding, auth-factor, SQL, and the guarded-write KV verbs), so a method
added without stating its authorization mechanism fails the build.

**Sub-clients are a separate concrete type, invisible to reflection over
`*Transport` — a second gate closes that blind spot.** `FactorClient`'s
four methods (`Enroll`/`List`/`Remove`/`Verify`, each keyed by a `userID`),
`VolumePoolClient`, `PermissionClient`, and `RBACClient.ListPermissions`
authorize as follows. `FactorClient`'s four call
`authorizeUserTarget` against the SAME `user_authfactors` permissions the
network's `userauthfactor.*` family checks for the identical underlying
service (`Verify` maps to `user_authfactors:login`, the challenge-response
check a login flow performs). `VolumePoolClient`/`PermissionClient` and
`RBACClient.ListPermissions` authorize ambient-self, the same way the rest
of `operationPermissions` does. `RBACClient.Authorize` stays unauthorized
on purpose: it decides about an explicit identity ARGUMENT, never the
ambient caller, so it is a policy-evaluation primitive, not an
authorization checkpoint of its own. On the network transports the server
makes that decision instead: `role.Authorize` and its siblings are
identity-gated, and evaluating an identity other than the caller needs
`permissions:resolve` in its tenant.

**The network `RBACClient` has one implementation.**
`transport.NewRemoteRBACClient` (`sdk/go/transport/rbac_remote.go`) is built
over the small `transport.RBACEndpoints` seam (`ResolvePermissions`,
`ListOwnPermissions`, `EvaluateAuthorization`). Each of REST, gRPC, Unix,
QUIC and MCP supplies only that seam's wire calls, in its
`transport_rbac.go`. A denial becomes `*transport.AuthorizationDeniedError`,
which unwraps to `auth.ErrUnauthorized`. `ListPermissions(identity)` reuses
the resolve-permissions endpoint behind its unchanged `permissions:resolve`
guard. Wildcard expansion lives in `permission.ExpandAll`, the one rule the
server's own-permissions answer and the client's `ListPermissions` share.
`transport_authz_subclients_test.go`'s
`TestSubClientAuthorizationSurface_CoversEveryMethod` reflects over each
sub-client's own concrete type the same way the `*Transport` gate does, so
a future sub-client method is caught by construction. A behavioral
sibling, `TestAmbientOperations_DenyANoPermissionIdentity`, invokes every
declared operation (bare and dotted) with a no-permission identity and
asserts a real denial — declaring a permission is not the same as the
permission actually gating the call. A service-nil guard checked before
`authorize()` would answer an unauthenticated caller "not implemented"
instead of a refusal on a misconfigured node. `authorize()` itself fails
CLOSED on an unregistered method key (`UnregisteredOperationError`) rather than silently admitting one,
making the declaration gates load-bearing rather than advisory.

`SealStore*` has no `pkg/permission` entry of its own because no network
route exposes it (REST, gRPC, QUIC, MCP). It is gated by
`permission.SealStoreManage` (`seal:store_manage`, `ScopeCross`,
SO-exclusive — not granted to the narrower Admin role), not by the bare
`IdentityGated` floor.

`Health` and the sealed-window bootstrap/unseal verbs (`BarrierInitialize`,
`BarrierUnseal`, `BarrierInitializeShamir`, `OfficerRegister`,
`RecoveryOfficerRegister`, `CeremonyInitialize`, `CeremonySignCSR`) are
public on the embedded transport, matching every network listener's own
sealed-window posture: an embedded node has no IAM store to authenticate
against while sealed, so the embedder must be able to drive these
in-process, gated instead by the SO-PIN or passphrase the sealed-window
ceremony itself checks. `BarrierStatus`/`CeremonyStatus`/`Status` stay
RBAC-gated, since REST gates them outside the sealed window too.

`operationPermissions` is checked against `pkg/api/quic`'s own
method-permission table (`quic.Server.MethodPermissions()`) by
`transport_authz_quic_drift_test.go`, so the transcription this whole
mechanism depends on cannot silently drift from what QUIC enforces: every
QUIC method is either mapped to its embedded counterpart and checked for
permission equality, or named in a small, reasoned exclusion list (public /
identity-gated / target-resolved), method by method. A bare string transform
would not do, because many QUIC methods collide with an unrelated embedded key (e.g.
`cert.List` strips to `List`, already KV's unrelated key).

`sdk/go/transport/embedded/probe_kv_users_roles_test.go` is the headline
probe: a tenant-scoped identity cannot read, write, or delete another
tenant's user or role, and an identity holding none of the relevant
permissions cannot touch KV at all.

**Login attempts are rate-limited the same way an authenticated caller's
requests are, from before there is an identity to key the check against.**
`AuthMiddleware.CheckLoginRateLimit` (`pkg/auth/middleware/ratelimit.go`) is
the seam: it builds a synthetic `auth.Identity` from the login attempt's
RESOLVED principal (never the caller-supplied lookup key, which is
attacker-controlled before any proof is verified) and hands it to
`checkRateLimit`, the exact engine and `(tenant, subject, permission)` key
`authorize()` uses — and audits a refusal the same way `authorize()` audits
every decision. The embedded `LoginClient.FinishLogin`
(`sdk/go/transport/embedded/login.go`) charges it under
`permission.UserAuthFactorsLogin` right after resolving the principal and
before verifying its proof, so repeated guesses against one real account
are throttled regardless of whether each individual guess's signature
would fail.

`BeginLogin` is rate-limited too, under a SEPARATE sentinel
permission, `permission.LoginBeginAttempt` — deliberately not
`UserAuthFactorsLogin`. The two throttles are different threat models
(volumetric challenge-issuance spam at `BeginLogin`, keyed by a fixed
tenant-wide subject since there is no resolved principal yet, vs.
per-account brute force at `FinishLogin`, keyed by the resolved
principal) that need independently tunable `ratelimit.Policy` values:
sharing one `Permission` key would make one declared `Policy` drain BOTH
buckets, so the two could not be tuned apart. `LoginBeginAttempt`, like
`IdentityGated`, is deliberately not registered in the permission
catalog — it exists purely as a rate-limit dimension key, never an RBAC
grant. `sdk/go/node`'s wiring of `wiring.AuthMiddlewareConfig`'s
`RateLimitPolicies`/`RateLimiter` (sourced from the node's own
`WithRateLimitPolicies`/`WithRateLimiter` options) applies to both
throttles on every transport of an embedded node, including this one.

Every REST user, role and stored-policy route that takes a body (among them
`POST /users`, `/users/lookup`, `/roles`, `/roles/reconcile`,
`/roles/assign` and `/roles/unassign`) decodes it under `maxIAMRequestBytes` (1 MiB):
a body past it is 413 (`RequestTooLargeError`) and one that does not decode
400, before anything is written.

The binding fixture in `pkg/auth/authtest` is the one every transport's test
drives, over the real built-in role store. Tenant A's administrator updates
the tenant-A user and cannot see the tenant-B one
(`TestTenantAdmin_ActsInOwnTenantOnly`), and writing a binding that names tenant B, or the system tenant, is refused with
`*user.RoleBindingTenantError` and stored nowhere
(`TestCrossTenantBinding_WriteRefused`). Both run on REST, gRPC, unix,
QUIC, MCP and embedded.

`Attributes` reaches `pkg/auth/abac` through `auth.Identity.Attributes`, a
field kept separate from `Identity.Metadata` and exposed in its own
namespace: `abac.Subject.UserAttributes`, read by `subject.user_attrs.<key>`
in a condition and by a `user_attrs.<key>` match key. `Metadata` is
authentication-derived (certificate fields, token claims) and becomes
`Subject.Attributes` (`subject.attrs.<key>`, bare match keys); `Attributes`
is written by anyone holding `roles:assign`, a weaker trust level. A bare
name never reads the user namespace, so an attribute called `organization`,
`dns_san` or `tenant_source` can never satisfy a policy written against the
authenticated value — whether or not the authenticator set one.
`pkg/auth/abac/decorator_test.go` attempts exactly that forgery for each.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Certificate presents no matching binding | authentication fails | **yes** |
| User not in `active` lifecycle | authentication fails | **yes** |
| Lifecycle state unrecognized by this build | authentication fails | **yes** |
| Transition the table does not connect (incl. out of `deleting`) | `LifecycleTransitionError` | **yes** |
| Undefined or not-creatable lifecycle state | `InvalidLifecycleError` naming the allowed set | **yes** |
| Blank `authMethods` entry | `ValidationError`, nothing written | **yes** |
| Unregistered permission string | `Parse` error | **yes** |
| Missing permission for the operation | permission denied | **yes** |
| Deleting a `System: true` user or a built-in role | refused | **yes** |
| Re-permissioning a built-in role through `UpdateRole` | `SystemRoleImmutableError{Op: "re-permissioned"}` | **yes** |
| Reconciling another tenant's built-ins as a normal tenant caller | `tenant.CrossTenantDeniedError` | **yes** |
| Reconciling a sealed barrier tenant | `tenant.ErrTenantSealed`, no fallback to the shared store | **yes** |
| The linearizable user lookup for a NON-system tenant does not answer | each attempt bounded at 2 s and retried while the store's shard elects a leader (`service.LeadershipTransient`), up to 10 s (`service.DefaultSettlePolicy`) or the request's own deadline; then `StoreUnavailableError` (503, retryable): the StaleRead fallback holds the system tenant only and is not consulted, because its "no such user" says nothing about another tenant | **yes** |
| The user lookup fails with anything but an election (a corrupt record, a decrypt refusal, a cancelled request) | that error, at once, with no retry | **yes** |
| The user lookup meets a sealed node barrier (`seal.ErrBarrierSealed` from go-quicraft on a node re-sealed at runtime, or `seal.ErrSealed`) | `*middleware.BarrierSealedError` (503, retryable once unsealed) — never `StoreUnavailableError`, and never logged as a store outage | **yes** |
| The linearizable role resolution for a NON-system tenant (permission resolution, the SO deny-list, or the strict-SoD Owner strip) does not answer within the same settle policy | `*rbac.ResolveError` wrapping `context.DeadlineExceeded` (503, retryable): the StaleRead role fallback holds the system tenant only and is not consulted | **yes** |
| A role a user still holds was deleted (one of the two windows `DeleteRole`'s holder strip leaves, see Invariants), on ANY tenant including the system tenant | `*rbac.ResolveError` wrapping `*role.RoleNotFoundError` (**not** retried against the fallback — a stale replica could still grant the deleted role's permissions — and **not** reclassified as retryable) | **yes** |
| Stored role carries a permission this build does not define | reported on read via `RetiredPermissions`; **no read fails** | — |
| Duplicate username within a tenant | refused | yes |
| Stored ABAC policy document or name invalid, does not decode, or does not compile | `*service.PolicyInvalidError` (400 / `INVALID_ARGUMENT`), nothing written | **yes** |
| Policy create of a name the tenant already holds | `*service.PolicyExistsError` (409 / `ALREADY_EXISTS`) | **yes** |
| Policy get, update or delete of a name the tenant does not hold | `*service.PolicyNotFoundError` (404 / `NOT_FOUND`) | — |
| Policy update or delete loses a guarded write to a concurrent writer | `*service.PolicyConflictError` (409 / `ABORTED`), store unchanged; re-read and retry | **yes** |
| Policy call naming a tenant other than the caller's own, a system-tenant caller included | `*service.PolicyCrossTenantError` (403 / `PERMISSION_DENIED`) | **yes** |
| Stored policy record does not decode, or its name does not match its key | `*service.PolicyCorruptError` (500 / `DATA_LOSS`) | **yes** |
| Tenant store without guarded writes (`kvstore.AtomicKVStore`) | `*service.PolicyStoreUnsupportedError` (501 / `UNIMPLEMENTED`), no unguarded fallback | **yes** |
| `Permissions()` / `RBAC()` on an unsupporting transport | returns **nil** — callers must nil-check | — |

**The StaleRead user fallback answers for the system tenant only.** qrdbd
builds it over a store confined to the system tenant's identity namespace, so
it holds no other tenant's records, and its "no such user" says nothing about
another tenant. For a non-system tenant the primary's transient error stands,
and the caller gets a retryable 503 rather than a definitive 403.

**The StaleRead role fallbacks have the same confinement, for the same
reason.** `rbac.RBACAuthorizer.resolvePermissionSet` consults its
`rolesFallback` (`rbac.WithRoleServiceFallback`, qrdbd's `bootRoleSvc`), and
the middleware's TenantOwner check (`tenantOwner`) consults its own
(`middleware.WithRoleResolverFallback`), only when `tenantid.IsSystem` holds
for the tenant in question; otherwise the primary's transient error stands.

**Both lookups are bounded on every tenant, and the size of the bound
depends on whether a fallback can answer in the primary's place.** The bound
itself is unconditional: a stalled ReadIndex must end in a retryable answer,
not a hang on the caller's own outer context, which a request that set no
deadline at all would never resolve. Where the StaleRead fallback holds the
tenant (the system tenant, with a fallback configured), `lookupUser` bounds
the primary (linearizable) read to `authLookupSyncTimeout` (500 ms) and
`resolvePermissionSet` bounds the primary resolve to `resolveSyncTimeout`
(500 ms), so a blip fails over to the fallback promptly. For every other
tenant there is nothing to fail over to, and the bound is the attempt bound
of `service.DefaultSettlePolicy` (2 s, one election timeout at go-quicraft's
default timing), which the wait below re-issues. A miss comes back as the
retryable `StoreUnavailableError` or `*rbac.ResolveError`/503 the rows above
describe; a correctly enrolled non-system tenant user gets the retryable
error, never a silent deny. A 500 ms bound on a tenant with no fallback would
make nothing faster: it would only turn a ReadIndex slowed past 500 ms (by an
election on a loaded host, for example) into a 503 on a request that would
have been answered.

**The user lookup waits out an election of its store's shard.** The bound above
limits one attempt, not the request. When an attempt fails because the
shard holding the user is electing a leader or moving it — quicraft's
timeout, aborted, not-leader, shard-not-ready or ReadIndex-not-ready errors,
or a `*service.NotLeaderError`, as `service.LeadershipTransient` names them —
`lookupUser` tries again every 100 ms until a leader answers, the request's
context ends, or the settle budget (10 s) passes, and only then lets the
error stand as the retryable 503. Without the wait, a request that arrives
during an election fails outright, with a 503 for a tenant whose users are all
intact. Any other error ends the lookup at once, so a
wait never masks a definitive answer. The wait lives in `lookupUser` alone
because every transport authenticates through it; `resolvePermissionSet`
waits the same way, under the same policy.

**The attempt bound is one election timeout, and the budget ends the last
attempt.** `service.SettleLeadership` holds the policy for both waits: it
derives each attempt's context, which ends at the attempt bound or at the end
of the budget, whichever is first. The attempt bound is what lets the wait
outlast an election. A follower forwards a ReadIndex to the replica it knows as
leader, and go-quicraft answers one lost with a leader killed before it replied
with nothing: the read waits for its context. So an attempt issued just after
the leader dies costs its whole bound, and only a fresh attempt reaches the
next leader. A bound longer than an election timeout buys nothing, because a
live leader confirms a ReadIndex in one heartbeat round and one that cannot
reach a quorum within an election timeout steps down, and a longer bound lets
two lost reads spend the whole budget while the shard is still electing.

**The two grant-voiding controls need no fallback, or any store lookup, at
all.** A system role's identifier is always its canonical, deterministic
derivation (`pkg/role.SystemRoleID`): system roles are undeletable
(`kvRoleStore.DeleteRole` refuses on `System: true`) and their name is
immutable, so no system role is ever persisted under any other identifier.
`roleIDsHoldSystemRole` (the SO deny-list) and `stripOwnerRole` (the
strict-separation-of-duties Owner strip) match a role id against the role
NAME and the canonical identifier directly, in memory, with no store
consulted and nothing to fail over to.

**A ResolveError's class depends on WHAT the primary returned, not just THAT
it returned an error.** `resolvePermissionSet` wraps whatever the primary
returns in `*rbac.ResolveError`, and the primary can fail two different ways:
TRANSIENTLY (a timeout, the store itself unreachable, or — for a
barrier-routed tenant — its role store resolved to a sealed barrier) or
DEFINITIVELY (the role was deleted or never existed — `*role.RoleNotFoundError`,
which `kvRoleStore.GetRole`'s tenant-ownership check also answers with for a
cross-tenant id — the request was malformed — `*role.ValidationError` — or a
resolver broke its contract). Only the transient case is retried against the
fallback, and only the transient case classifies as a retryable
503/Unavailable. `pkg/auth/middleware`'s `resolveErrorIsTransient` is an
explicit ALLOW-list of safe-to-retry causes, not a list of exclusions, so a
future definitive error type this package does not yet recognise falls
through unclassified instead of silently becoming retryable:

- `context.DeadlineExceeded` / `context.Canceled`.
- `tenant.ErrTenantSealed` / `seal.ErrSealed` — a barrier-isolated tenant's
  role store (`routingRoleService.storeFor` → `resolver.Resolve`) resolved to
  a sealed barrier, or sealed one step later; retryable once unsealed, the
  same class every other data-plane read gives this condition.
- `seal.ErrBarrierSealed` — go-quicraft's sentinel for the same read on a
  node barrier re-sealed at runtime; retryable once unsealed.
- A cause implementing a small, unexported `transientMarker` interface
  (`ReadTransient() bool`), consulted via `errors.As`. `*role.StoreError`
  (fields `Op`, `Detail`, `Err`) and `*tenant.StoreError` (fields `Op`,
  `Err` — it carries no `Detail` or `TenantID`) both implement it: an
  unexported `transient` field, set ONLY by each package's own
  `newStoreError` constructor at the point the underlying store failure is
  first observed, backs the method. This is deliberately NOT a match on
  `Op`/`Detail` — those are free-text labels naming WHERE a failure happened
  (`GetRole`'s own key read vs `GetRoleByName`'s name-index read vs the
  tenant-metadata read `storeFor`'s resolve depends on), not WHAT KIND of
  failure it is, and a rename of one would silently change what
  classifies as retryable with no compiler or test signal. `newStoreError`
  decides transience from the cause it is actually given: a cryptographic
  refusal from a sealed-barrier-backed store's Decrypt
  (`seal.ErrDecryptionFailed` — a tampered ciphertext, or the wrong key) is
  marked definitive, never transient, because no retry decrypts a tampered
  ciphertext; every other observed cause (ReadIndex not ready, leader lost,
  timeout) is marked transient. One consequence of that rule is worth
  stating plainly: a follower that has not yet applied a tenant barrier
  rekey, asked to decrypt a record sealed under the new epoch's key with its
  still-stale key material, gets exactly this cryptographic refusal — and so
  reads it as a DEFINITIVE deny (fail-closed), not a retryable 503, until
  that follower catches up to the rekey. A corrupt stored record (an unmarshal
  failure) is definitive for a different reason: it is built as a bare
  `&StoreError{...}` composite literal that never calls `newStoreError` at
  all, so its `transient` field keeps the zero value, `false` — the decoder
  that found the corruption has no "cause" to hand `newStoreError` in the
  first place. A hand-built `*role.StoreError`/`*tenant.StoreError` from
  outside its producing package cannot set the `transient` field at all —
  neither type exports it, and neither's exported fields include it — so the
  only way to assert a `StoreError` is transient is to actually produce one
  through the real store code, which is what
  `pkg/auth/middleware/resolve_producer_test.go` does.

`DeleteRole` strips the role from its holders before removing it, but an
account can still carry a deleted role's id through one of the two windows
that strip leaves (see Invariants). A classifier that marked every
`ResolveError` retryable would turn that permanent 401/403 into a permanent
retryable 503 for every request such an account made. The fallback-consult
rule has the same split: even for the system tenant, a DEFINITIVE `RoleNotFoundError` or `ValidationError` is never
retried against the fallback, because a StaleRead replica that has not
caught up to the deletion yet could still answer with the just-deleted
role's permissions — resurrecting a revoked grant rather than reporting the
deletion.

The nil returns are the sharp edge here: they are not error values, so an
unchecked call panics rather than failing cleanly.

## Persistence

- Users and roles live under the internal `__sys:` namespace, Raft-replicated,
  invisible to user KV enumeration.
- Roles are at `__sys:roles/id/<id>` with deterministic identifiers for the built-in
  five: RFC 9562 §5.8 **UUIDv8 over SHA-256** of a fixed
  `"go-qrdb:role-namespace/v8/"` prefix plus the length-framed tenant ID and
  role name (`pkg/role.SystemRoleID`).
- User lookup by certificate thumbprint is a secondary index —
  `GET /users/by-cert-thumbprint/{thumbprint}`.
- The email login key is a tenant-scoped secondary index at
  `__sys:user_idx/email/{tenantID}/{normalized-email}`, maintained by the same
  functions that write the record so the two cannot drift.
- Stored ABAC policies are tenant-scoped: each is one JSON record at
  `__sys:abac_policy/<name>` in the tenant's own store, resolved through
  `tenant.StoreResolver`, so no tenant's policies share a keyspace with
  another's.
- The separation-of-duties posture last recorded, `strict` or `standard`, is
  the cluster record `__sys:cluster/separation_of_duties`, beside
  `__sys:cluster/isolation_policy`. It is written only through
  `wiring.RecordSeparationPosture` (see
  [Authorization model](#authorization-model)).
- Email lookup is `POST /users/lookup` with the key in the **body**. It is
  deliberately not a path parameter: the canonical login identifier would then
  be captured by access logs, proxies, tracing and browser history on every
  successful read.

**Identifier scheme.** The derivation is RFC 9562 §5.8 UUIDv8 over SHA-256 of
a fixed `"go-qrdb:role-namespace/v8/"` prefix plus the length-framed tenant ID
and role name. SHA-1 does not exist under FIPS 140-3, and RFC 4122 fixes v3 to
MD5 and v5 to SHA-1, so v8 — the version reserved for application-defined
derivations — is the only name-based form an approved hash can take. There is
only the one scheme: no alternate identifier a role can be persisted under,
and no resolution step a caller must go through to find it.

| Direction | How it works |
|-----------|----------------------|
| Identifier → role | A stored identifier is looked up verbatim, so `GetRole`, `ListRoles` and `ResolvePermissions` resolve it directly. |
| Tenant + name → identifier | `pkg/role.SystemRoleID` derives the identifier directly from the tenant ID and role name. Bootstrap and first boot use it when assigning system roles. |
| "Is this identifier role X?" (voiding a grant) | `roleIDsHoldSystemRole` (the SO deny-list) and `stripOwnerRole` (the strict-separation-of-duties Owner strip) compare a role id against the role NAME and the canonical identifier directly, in memory — see the paragraph above. |
| Cluster permissions | `admin:add_member`, `admin:remove_member`, `admin:drain`, `admin:snapshot`, `admin:start_shard`, `admin:stop_shard`, `admin:shard_state`, `admin:topology`, `admin:replica_state` (every replica's applied index and state-machine hash, held by no built-in role but Owner), `admin:node_compaction` (the node keyspace's compaction policy, which the built-in Admin role carries and only the system tenant's Admin can therefore use), every `rebalance:*` and every `backup:*` permission are `ScopeCross` (`pkg/permission/cluster_admin_scope.go`): they act on shards every tenant shares and name no tenant, so as ScopeSelf the built-in Admin and SO roles would let any tenant's Admin remove replicas from the system shard. `admin:peers` stays ScopeSelf for the etcd listener's tenant-facing `Cluster.MemberList`. See [../auth/roles.md](../auth/roles.md#cluster-membership-permissions-are-platform-operator-only). |
| Node-barrier permissions | Every `seal:*` permission but `seal:status` -- including `seal:officer_register`, `seal:officer_list` and `seal:officer_revoke` -- and `ceremony:initialize` and `ceremony:sign_csr` are `ScopeCross` (`pkg/permission/node_barrier_scope.go`): the node barrier is a platform singleton, so ScopeSelf -- which compares the caller's tenant with itself for an operation that names no tenant -- would let any tenant's SO rekey it, and register officers into its unseal and recovery domains, each of which receives a share of the node key at every distribution. See [../auth/roles.md](../auth/roles.md#node-barrier-permissions-are-platform-operator-only). |
| "Does this caller hold role X?" (admitting) | `role.HoldsSystemRole` matches only the identifier the role store persists the role under, in the caller's own tenant. It never matches the role *name*: a control that ADMITS a caller must under-match, where a control that voids a grant over-matches. The SO dual-factor gate uses it. `Identity.Roles` holds role identifiers, so comparing a role name against it (`Identity.HasRole("so")`) refuses every real holder and admits any identity whose roles carry the literal string. |

No capability is absent from a FIPS build as a result: the v8 derivation is the
only one compiled, in every build.

**Compatibility**: the thumbprint derivation (base64url SHA-256 over SPKI) is
part of the on-disk contract. Changing it invalidates every existing binding.

## Test strategy

**Integration target:** `make integration-test-identity-rbac` (no Docker; selection in `test/integration/features.mk`). It runs the role, user and invitation CLI suites per protocol. Per-transport accounting is the `identity-rbac` row of `test/integration/COVERAGE.md`.

- **Unit**: `MatchesCert` for each `CertBindingKind`, thumbprint stability
  across certificate renewal for the same key, effective-permission resolution
  over `RoleIDs` ∪ `RoleBindings`, `Parse` rejecting unregistered strings, and
  the undeletability of system users and built-in roles.
- **Renewal stability is its own test** — it is the property the SPKI choice
  exists for, and it is asserted through the login path as well as on the
  model, because a renewal that stopped authenticating would be a lockout.
- **The binding gate is tested against the REAL authenticator and the REAL
  user store**, never a double. The predicate lives on `user.User`, so a
  narrower double that "checks the binding" would assert the double rather than
  the authentication path. Both directions of the empty-set admission are
  asserted on ONE account, so the only difference between them is the
  enrolment.
- **Withdrawal is tested through the same real authenticator**, for the same
  reason: a withdrawn key must stop authenticating, a re-issue for that key
  must stop authenticating with it, and a retained key must keep working — all
  three on one account, so the only difference is the withdrawal. The
  last-binding widening is asserted deliberately rather than discovered.
- **The binding-write race is a test, and it is red against the obvious
  implementation.** A withdrawal racing an enrolment of an unrelated key must
  not end with the withdrawn binding present; the interleaving is FORCED
  through a store hook rather than raced with goroutines, because a raced
  version passes against broken code most of the time. The mirror case — an
  enrolment racing a withdrawal — is asserted too, so neither verb can be fixed
  by breaking the other.
- **Cross-transport reachability is a conformance matrix**
  (`sdk/go/conformance`): the whole enrol → withdraw → verify cycle over all six
  transports. A withdrawal reachable on four of them is a surface that
  disagrees with itself, and the disagreement is invisible from inside any one
  of them.
- **First boot is tested in both postures**: bootstrapped with no `CertBinder`
  (admin unenrolled, authenticates) and with one (admin enrolled, its own
  certificate admitted and another certificate naming it refused).
- **Authorization tests are security tests**: every transport must reach the
  same decision for the same identity and permission.
- **Error tests** per function.
- **Invitation redemption is tested against REAL tenant-routed user, role
  and invitation stores** (`pkg/service.newInvitationFixture`), never a
  double for the store itself: a replay, a cross-tenant redemption, the
  inviter redeeming with their own key, a pinned invitation presented with
  the wrong key, and a concurrent-activation race are all asserted through
  the actual CAS commit path, not a mock that could assert the bug. Each
  wired transport (REST, native QUIC, embedded, MCP) repeats the
  unauthenticated-redemption and opaque-refusal assertions at its own
  handler, so a transport that forgot to skip authentication for this one
  verb fails there, not only in the shared service test.
- **Delegations are tested against REAL certificates from a REAL tenant CA.**
  `pkg/clienttrust` signs delegations with per-tenant login CAs and asserts the
  authenticator refuses one naming another principal, one signed by another
  tenant's CA (or by the cluster CA), one that is revoked, one whose
  revocation cannot be read (503, not 401) or that no revocation checker can
  judge, one over the lifetime cap, one delegating the system tenant, and one
  whose extension is malformed; and that plain `crypto/x509` rejects every
  delegation as an unhandled critical extension. `pkg/auth/delegation`
  asserts the DER round trip and refuses a non-critical extension.
  `pkg/auth/middleware` asserts the scope ∩ host-grant intersection and the
  503 with no audit logger; `pkg/api/rest` asserts a forwarded delegation is
  verified again by the leader.
- **The CLI is tested end to end on every transport.**
  `TestCADelegate_CLI_PerProtocol` (`make integration-test-certificate-authority`)
  issues a delegation with `qrdb ca delegate`, presents it from a node that
  accepts that principal, and asserts admission inside the intersection (a
  read, and a write of the tenant's own database) and refusal outside its
  scope, for another principal, from another tenant's CA, on another tenant's
  data, after revocation, after expiry and past 24h of validity even when the
  tenant's own CA signed it; and that issuance refuses an unknown permission,
  a lifetime over 24h and a built-in role's name.
  `TestABACPolicy_CLI_PerProtocol` (`make integration-test-identity-rbac`)
  drives the stored-policy lifecycle with `qrdb abac policy` on every
  transport of one node: create, get, update (the creation time survives,
  `spec.enabled` flips), list and delete, and the refusals of a duplicate
  name, a missing policy and another tenant.
- **An embedder reaches the delegation seams through `sdk/go` alone**: the
  external-composer build gate (`test/buildgates`) reads a grant with
  `sdk.DelegationFromCertificate` and composes `node.WithServicePrincipal` from
  a module of its own.

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
