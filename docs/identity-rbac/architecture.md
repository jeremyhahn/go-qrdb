# Identity and RBAC — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-09
> Conflicts with this document are defects. Fix them here first.

Users, roles, the permission vocabulary, and the single policy decision point
that authorizes every request on every transport.

## Problem

Every capability in the store needs an answer to "may this caller do this?", on
six transports, for two dozen subsystems. Answering it in each subsystem
produces two dozen slightly different answers. Answering it against a
per-transport model produces six.

## Shape chosen

**One identity model, one permission vocabulary, one PDP.**

```
mTLS certificate  →  identity (CN=username, OU=tenant, SPKI thumbprint)
                  →  user record (RoleIDs + RoleBindings + Attributes)
                  →  RBAC PDP  →  allow / deny on a resource:action permission
```

- **Authentication is passwordless mTLS.** The certificate *is* the credential.
- **Permissions are `resource:action` strings**, registered in one global
  registry with categories for UI rendering.
- **The PDP is single.** Every transport handler resolves the same permission
  constant against the same decision point.

### One authentication chain, with consumer authenticators placed in it

Authentication is one ordered, first-success-wins list of authenticators,
built once per node by `wiring.BuildAuthMiddleware` and shared by every
transport — REST, gRPC, gRPC over a Unix socket, native QUIC, MCP, MySQL,
PostgreSQL, and the embedded client's forwarding caller. The first
authenticator that returns an identity decides the request; nothing after it
runs. qrdb ships one authenticator, mTLS. A composer adds its own
(`node.WithAuthenticators`, or `AuthMiddlewareConfig.ExtraAuthenticators` for a
composer that wires the middleware itself) and names, for each, a position:

| Position | Chain | A request presenting a valid client certificate and the consumer's credential |
|---|---|---|
| `ChainBeforeMTLS` | consumer…, mTLS | is decided by the consumer authenticator; one it declines falls through to mTLS |
| `ChainAfterMTLS` | mTLS, consumer… | is decided by mTLS; the consumer authenticator never runs for it |
| `ChainReplaceMTLS` | consumer… (mTLS removed) | is decided by the consumer authenticator; a certificate alone authenticates nobody |

Positions combine: `auth.AuthenticatorChain` lays out every
`ChainBeforeMTLS` placement, then mTLS — or, when any placement is
`ChainReplaceMTLS`, those placements in its slot — then every `ChainAfterMTLS`
placement, each group in registration order.

**Why the position is the composer's to state.** A host that adds bearer
authentication with its own checks — token binding, revocation, audience —
needs those checks to run on every request that carries its bearer. Placed
after mTLS, they never run for a request that also presents a client
certificate, because mTLS has already decided it. Placing the bearer before
mTLS, or in its place, is how the host's checks become the deciding ones.

**Replacing mTLS leaves TLS in place.** `ChainReplaceMTLS` removes only the
mTLS authenticator's decision. Every listener still terminates TLS, still
verifies any client certificate presented against its CA pool, and still hands
the verified connection state to the chain (`auth.TLSStateFromContext`), where
the replacing authenticator may read it — to bind a bearer to the certificate
that carried it, for instance. The on-behalf forwarding path between cluster
nodes is not part of this chain and is unaffected.

**What is refused at construction.** The zero `ChainPosition` is not a
position, so a placement that never chose one fails node construction with
`*auth.InvalidChainPositionError`, as does any value outside the three.
`ChainReplaceMTLS` with no authenticator anywhere in the chain fails with
`*auth.EmptyAuthenticatorChainError`: a chain no request can pass is a wiring
bug, not a posture.

**What was rejected.** A mode in which every authenticator must agree. The
chain stays first-success-wins; a composer that wants its checks applied to
certificate-bearing requests places its authenticator ahead of mTLS or in its
place, rather than qrdb running a second, conjunctive chain beside the first.

### Identity mapping

| Certificate field | User field | Role |
|---|---|---|
| Subject CN | `Username` | unique identity within a tenant |
| Subject OU (the first) | `TenantID` | tenant isolation boundary; must agree with the SAN |
| SPIFFE ID SAN's trust domain | `TenantID` | tenant isolation boundary; must agree with the OU |
| SHA-256(SubjectPublicKeyInfo), base64url | `CertBindings[].Thumbprint` | **the authoritative mTLS identity** — `User.MatchesCert(cert)` |

**A tenant is a SPIFFE trust domain, and a certificate names it twice.** A
tenant's principal is `spiffe://<tenant>/<kind>/<name>`, kind `user` or `svc`:
the trust domain is the tenant ID verbatim. The first Subject OU and that trust
domain must both be present and must agree; there is no fallback from one to
the other. A certificate that names no tenant is under the cluster trust
domain (`auth.spiffe_trust_domain`, which defaults to
`auth.cluster_trust_domain`), and no tenant may be the cluster trust domain, so
no tenant shares the cluster's trust bundle. A certificate missing either name,
whose two names disagree (which is how a foreign trust domain presents), whose
OU names the cluster trust domain, or with no OU and a SAN under a foreign
trust domain is refused (`*auth.CertificateTenantError`). So is one whose two
names agree on a tenant that was never created or has been deleted
(`*auth.UnknownTenantError`): a trust domain names a tenant only while that
tenant exists. The system tenant always exists. Both of qrdb's CAs
always issue the pair and refuse to mint one whose SAN would disagree with its
OU. See
[../certificate-authority/](../certificate-authority/architecture.md#how-an-issued-certificate-names-its-tenant).

**The thumbprint is computed over the public key, not the certificate.** A
renewed certificate for the same key produces the same thumbprint, so bindings
survive renewal — which is what makes certificate rotation an operation rather
than a re-enrolment.

### The Subject is the user id

Once a request resolves to an account, its identity's `Subject` is that
account's **immutable user id**, in decimal — not the username, and not the
email. `auth.Identity.BindUser` is the one place it is set, from
`AuthMiddleware.finalizeIdentity` on the network transports, the on-behalf
assertion a forwarding node carries, and the embedded login ceremony
(`FinishLogin`, after any `node.WithIdentityHydrator` hydrator has run, so a
hydrator cannot name the account). `UserID` carries the same id and
`Username` the account's current name; `Identity.AccountName()` returns that
name for the readers that want one.

So every reader keyed on the Subject — the authorizer's decision cache, the
audit `actor`, the rate-limit bucket, an import session's owner, the enroller
recorded on a barrier or ceremony — names the account by an id that a rename
or an email change does not move. A caller that resolves to no account (a
holder certificate, a certificate naming no account under a non-`required`
binding policy) keeps the name its authenticator produced; one whose
unresolved Subject is shaped like a user id is refused
(`*UnresolvedUserSubjectError`, 401), so a CN of digits cannot pose as one.

The certificate CN still **selects** the account by its username. After a
rename, a certificate naming the old username selects nothing; re-certify the
same key under the new name and its SPKI binding carries over unchanged.

### Tenant ownership is a typed field

`auth.Identity.TenantOwner`, and its projection
`transport.Identity.TenantOwner` (JSON `tenantOwner`), say whether the
identity holds the **Admin** system role of its own `TenantID`. Consumers
authorize on this field. Exactly one function decides it:
`role.IsTenantOwner`. That function collects the identity's role ids, plus its
role bindings in `TenantID` (bindings in any other tenant are dropped), and
asks `role.HoldsSystemRole` whether one of them is the id the role store
persisted for that tenant's Admin role. It compares by id and never by name. A
built-in role can be neither renamed nor deleted, and no custom role can be
renamed to `admin`, so no rename can turn a role into the owner role. An
unseeded tenant has no owner. A missing role store or a store error is
returned to the caller and is never read as "not owner".

The step that establishes the identity sets the field:

| Path | Where it is set |
|---|---|
| Embedded login, `FinishLogin` | the identity hydrator. The default hydrator calls `role.IsTenantOwner` over the transport's role service and refuses the login without one. Every `WithIdentityHydrator` implementation must set the field from the same function, after its role set and `TenantID` are final. `FinishLogin` enforces that before it issues a bearer: it calls `role.IsTenantOwner` again over the transport's role service and refuses the login with that call's error (a missing role service included) or with `HydratorTenantOwnerMismatchError` when the hydrator's value differs. |
| Embedded `LoginClient.ResolveIdentity`, `SessionClient.ResolveIdentity` | carried by the session the issuer minted at login, projected by `identityToTransport` |
| Network transports (REST, gRPC, QUIC, MCP, unix socket) | the auth middleware, on every authenticated request, for mTLS, bearer and service principals alike, and for a tenant-issued delegation, presented, forwarded or bound in-process by `AuthenticateDelegation`. It clears whatever the authenticator or delegation verifier claimed, then calls `role.IsTenantOwner` over the role store given to `WithRoleResolver`. An on-behalf request is decided for the asserted caller on the serving node, never carried in from the forwarder. A delegated identity holds no role, so it is never its tenant's owner. No wire type returns the identity to the caller; server-side consumers read it from the request context. |

`identityToTransport` is the only function that produces a `transport.Identity`,
so every embedded projection agrees. The reverse mapping, `identityToDomain`,
used when a caller hands an identity back for `Authorize` or `ListPermissions`,
never takes `TenantOwner` from that input. The module's own authorizer does not
read the field either: it decides from roles and grants, as before.

Because the value is computed when the identity is established, its lifetime
follows the path. On the network transports the identity is established per
request, and the role store is read uncached each time, so a demotion or
promotion takes effect on the caller's next request. On the embedded login it
lives as long as the session bearer, as the role set does, and a demotion or
promotion shows at the next login.

The network decision fails closed. A middleware built without a role store
refuses every authenticated request, and `wiring.BuildAuthMiddleware` refuses to
build one without a `RoleService`, even when an `Authorizer` is supplied. A
role-store error refuses the request with `TenantOwnerUnresolvedError`, classed
`AuthFailureUnavailable`: retryable, 503 on REST, `UNAVAILABLE` on gRPC, and
each transport's unavailable status elsewhere. It is never read as "not owner". For the system tenant alone, a failed read is
retried once against the stale-read store given to `WithRoleResolverFallback`.
A sealed tenant is not retried. `Metadata` never carries ownership. A system-tenant
identity's `TenantOwner` refers to the system tenant alone and grants no power
in any other tenant.

### What a certificate actually claims

The three fields above are not equally trusted, and the difference is the whole
of mTLS security here:

| Field | What it is |
|---|---|
| Subject CN, Subject OU, SPIFFE tenant SAN | **an assertion by the CA** about who this certificate is for. They select WHICH account the login is attempted against. |
| SPKI thumbprint | **proof of possession**, established by the TLS handshake itself. The peer signed with the private key; nobody else could have. |

So the CN and the OU say who the holder claims to be, and only the thumbprint
says who the holder demonstrably is. An account is **enrolled** when it has at
least one `CertBindings` entry, and enrolment is what makes the difference
load-bearing:

- **An enrolled account authenticates only by possession.** The certificate is
  matched against the account's bindings with `User.MatchesCert`, which is
  SPKI-based and expiry-checked. A certificate whose CN and OU name that
  account but whose key it never enrolled is refused — even when the
  certificate is validly signed by the cluster CA, and even when its OU and
  SPIFFE SAN agree. **The SAN carries no extra weight: it is issued by the
  same CA as the OU.**
- **An unenrolled account authenticates by the CA's assertion alone**, exactly
  as every deployment did before enrolment was enforced. An empty binding set
  states no restriction, the same statement an empty `AuthMethods` makes.

**Enrolment narrows; withdrawal can widen.** The first binding closes an
account from "any certificate the CA signed naming it" to "only these keys".
Withdrawing the LAST one reopens it to the former. The two directions are
therefore separate authorities — `users:add_cert_binding` and
`users:remove_cert_binding` — rather than one "manage bindings" grant, because
a grant that lets a caller tighten an account's admission must not silently let
them loosen it.

**Withdrawal is not certificate revocation.** Revocation refuses a
*certificate*; a binding is over the *key*, and outlives every certificate
issued for it. A re-issue for the same key therefore authenticates again until
the binding is withdrawn — so withdrawal is the remediation for a compromised
key, and revocation is not. They stay independent operations: coupling them
would make a routine rotation's revoke silently unenrol the key it is rotating.

**An expired binding is not a withdrawn binding.** Expiry makes one binding
inert (`MatchesCert` is expiry-checked) while leaving the set non-empty, so the
account stays closed and authenticates on its other keys. Withdrawal removes
the entry, and when it was the last one the set becomes empty and the account
reopens. Both are correct; they are not interchangeable.

**By default this is fail-closed per FLEET** (`auth.mtls_binding_policy:
required`): FedRAMP, HIPAA and PCI DSS 4.0 require
possession-based authentication of every account, not only the ones an
operator happened to enrol. `auth.mtls_binding_policy: enrolled` is the
explicit, visible opt-out for a fleet still migrating — it does NOT meet those
controls, because an account with no bindings under it is exactly as
impersonable as the CA's issuance paths make it. Every composition in this
module (`qrdbd`, `sdk/go/node`, embedded) enrols the bootstrap admin's
certificate as part of first boot, so the default does not lock a fresh node
out of itself; see "The fleet-wide posture" below for how.

### The fleet-wide posture: `auth.mtls_binding_policy`

A fleet still migrating accounts onto certificate enrolment can say so, and
have the server relax to match:

| `auth.mtls_binding_policy` | An account with bindings | An account with none | A certificate naming no account |
|---|---|---|---|
| `required` (default) | admits only a matching, unexpired key | **refused** — `*CertBindingRequiredError`, 401 | **refused** — `*CertBindingRequiredError`, 401¹ |
| `enrolled` (non-compliant opt-out) | admits only a matching, unexpired key | admits on the CA's assertion | admits, unenriched — no roles, so RBAC denies |

¹ Except a holder certificate, which this column would otherwise describe: it
names no account by design, so it is excepted from the refusal under BOTH
postures — see "What `required` does not reach" below.

Under `required` **possession is the only way a certificate identifies anyone**,
so the residual hole above closes: an unguarded issuance path can no longer
mint an identity for an unenrolled account, because no unenrolled account
admits a certificate. An unrecognised value fails the boot rather than
resolving to either posture.

**`required` boots into "awaiting admin credential" rather than locking the
admin out, or refusing to boot, outright.** The bootstrap admin (`admin` in
the `system` tenant) must exist and hold at least one unexpired binding for
the node to serve normally; when it does not, the node comes up anyway,
serving ONLY ceremony status and ceremony-initialize (the admin-credential
retry) — every other request is refused with
`*middleware.AwaitingAdminCredentialError` until a certificate binds, with no
restart required once it does (`firstboot.AwaitingAdminCredentialProbe`,
`firstboot.WatchForAdminBound` — see
[../firstboot/architecture.md](../firstboot/architecture.md)). This posture
is decided in two places, because two different layers create the admin:

- **`firstboot.Initialize`** no longer refuses when it would create an admin
  it cannot bind (no `CertBinder`, or no admin certificate path): that is now
  the ordinary "awaiting" case. It still refuses outright, before writing
  anything, for a genuine wiring bug — a credential path configured with no
  way to bind it at all — and for a lifecycle or permitted-method block no
  certificate could fix regardless.
- **The composer**, after first boot, calls `firstboot.VerifyAdminCertBinding`
  and lets its result decide the "awaiting" posture rather than refusing to
  start. The daemon does this on every boot, reading the admin through the
  **linearizable** store: a follower's StaleRead copy may not yet hold a
  binding the leader enrolled, and judging it would wrongly enter "awaiting"
  on a boot that should serve normally. A read that cannot be served yet is
  retried for up to 30 seconds (`firstboot.VerifyAdminCertBindingWithin`),
  and only a lookup FAILURE (not a verdict) still logs and serves normally
  rather than entering "awaiting" over an infrastructure fault alone. **The
  daemon's own first boot DOES wire a `CertBinder`**
  (`firstboot.UserCertBinder` over its own user store) and an
  admin certificate path (the one `qrdb init` resolved through
  `--admin-csr`/`--admin-cert`/generate, carried from the ceremony to first
  boot through the replicated system store), so a fresh daemon under the
  `required` default serves normally rather than entering "awaiting" at all.
- **The SDK node** checks at start the same way (with the same bounded
  wait), and re-checks immediately after `Node.FirstBoot`/
  `Node.FirstBootWithCeremony` runs, so a node whose first boot just created
  the admin unbound enters "awaiting" in the same call rather than needing a
  restart. `Node.FirstBoot` runs first-boot under the node's own policy —
  raising, never lowering, what the embedder passed — and verifies the
  result against the store the node authenticates with.

**Only `Node.FirstBoot`/`Node.FirstBootWithCeremony` re-decide "awaiting"
immediately on an embedded node.** An embedder that calls
`firstboot.Initialize` (or the SDK's `FirstBootInitialize`) directly, without
going through one of those two methods, can create an admin with no binding
inside a node that is already serving; that node admits nobody by
certificate but does not enter "awaiting" either, since nothing told its
probe to re-check, until it is restarted, when its start check correctly
finds the now-existing, now-unbound admin and enters "awaiting" then.
Embedders should bootstrap through `Node.FirstBoot` or
`Node.FirstBootWithCeremony` to get that reconciliation without a restart.

**The same posture guards the admin at runtime.** Under `required`, every user
store the daemon and the SDK node build (`user.WithBootstrapAdminBindingRequired`,
applied through `lifecycle.UserStoreOptions`) refuses every change that would
leave the bootstrap admin unable to log in by certificate: deleting it,
renaming it, moving it to a lifecycle state that cannot authenticate, dropping
`mtls` from its permitted methods, or leaving it with no live binding —
whichever verb makes the change. The refusal is
`user.BootstrapAdminLockoutError`, on every transport, so a running node
cannot be talked into the state its next boot would refuse.

The guard is judged on the whole resulting record inside the store's
conditional write (`updateUserAt` for every update, the binding verbs among
them; `DisableUser`; `DeleteUser` before it marks the record), against the
version the write is conditioned on, so two changes that are each safe alone
cannot both commit. It refuses a change only when it breaks a requirement the
admin currently meets. That keeps repair possible, and it means an admin
already locked out by the clock — its last key expired — is not rescued by the
store: the recovery path is to set `auth.mtls_binding_policy: enrolled`,
restart, enrol a key, and return to `required`.

The admin is identified the way the boot check finds it — `admin` in the
`system` tenant — evaluated on the record as committed, before the change. A
rename is therefore itself refused, so under `required` the admin cannot stop
being the admin, and the username index keeps any other account from taking
the name. It is also the `System` user: first boot creates it with
`System: true` (and promotes an admin created before the flag existed), so
`DeleteUser` refuses it whatever the binding policy.

**A live binding is not the whole check.** The boot check also refuses an admin
whose lifecycle state does not permit a login (`AdminBindingCannotAuthenticate`)
and one whose permitted-method policy excludes `mtls`
(`AdminBindingMTLSNotPermitted`): either locks the admin out as surely as a
missing key.

Every successful check also records the admin's deadline:
`qrdb_auth_admin_cert_binding_not_after_timestamp_seconds` — the Unix time at
which the admin's last live binding expires, `+Inf` when one never expires —
and a `WARN` naming the remedy once it is under 30 days away. Under `required`,
that moment is when the node stops being able to boot. It is refreshed by every
boot check and by every committed change to the admin (it drops to `0` when the
admin is deleted, which only `enrolled` allows), and it is a timestamp rather
than a countdown so it stays true in between. The warning is the `required`
posture's: a store built without the option refreshes the metric but does not
warn, and a store logs it through its own logger (`user.WithLogger`). Alert on
the metric against the current time:

```
qrdb_auth_admin_cert_binding_not_after_timestamp_seconds - time() < 30 * 86400
```

A failure to *read* the admin while checking also refuses the boot: the posture
cannot be shown to hold, and starting anyway is the lockout the check prevents.

**What `required` does not reach**, stated rather than implied:

- **Forwarded on-behalf assertions and a capability module's session-token
  plane.** They present no certificate of the subject's — the certificate on
  those connections belongs to the forwarding node or the module — so there is
  nothing to compare. The origin node enforces the posture where the subject's
  certificate was presented; a fleet must therefore set the same value on every
  node.
- **A sealed tenant's IAM window.** While a dedicated-isolation tenant's barrier
  is sealed its user records are unreadable, so no binding can be checked; the
  identity proceeds unenriched to the `IdentityGated` barrier surface exactly as
  under `enrolled`. Refusing it would make the tenant's own unseal unreachable
  by certificate.
- **A holder certificate.** `auth.mtls_binding_policy` names an ACCOUNT's
  certificate enrolment, and a share holder (`auth.IsHolderIdentity`, a
  Security Officer or custodian the ceremony CA signed — see
  [../ceremony/architecture.md](../ceremony/architecture.md)) is by design
  never an account: it has no user record and no binding to check, so the
  posture's rationale does not reach it. It is ALWAYS admitted, with no roles,
  regardless of `required`/`enrolled` — the same as every certificate login no
  account resolves to, it still reaches only the `IdentityGated` surface
  (share claim begin/complete, quorum begin/submit). RBAC refuses it every
  other permission on its empty role set, holder or not — **including officer
  registration itself**: enrolling a NEW holder (`seal:officer_register`,
  `tenant_barrier:officer_register`, `databases:officer_register`) is a real,
  registered RBAC permission requiring an Admin or Security Officer role
  grant, never `IdentityGated`, on every transport. A holder certificate can
  only ever complete its own enrolment or claim its own share; it cannot
  register another holder. Its binding for the verbs it CAN reach is the
  ceremony/officer roster, checked by thumbprint at the handler layer — a
  holder-shaped certificate for a username never enrolled through officer
  registration authenticates exactly like an enrolled one and is refused when
  it tries to claim, at that roster check, not here.
- **Non-certificate methods.** The posture is about certificate logins; other
  methods are governed by the account's permitted-method policy.

**Migration and rollback (the direction reversed from an earlier release).** `required` is
the default, so a FRESH fleet needs no migration at all — every composition
enrols the bootstrap admin as part of first boot, and every other account is
enrolled the ordinary way (`POST /users/{id}/cert-bindings`) before it ever
logs in. An EXISTING fleet upgrading from a release that defaulted to
`enrolled` sets `auth.mtls_binding_policy: enrolled` explicitly first — the
pre-flight is listing the accounts whose `cert_bindings` are empty (see
[user.md](user.md#moving-a-fleet-to-required-certificate-enrolment)) — enrols
every one, then removes the explicit `enrolled` setting (or sets `required`
explicitly) on every node. Rolling back to the non-compliant opt-out is
setting `enrolled` and restarting; nothing is migrated in either direction,
because the policy is read at boot and stored nowhere.

**The consequence for certificate issuance** is that every path that mints a
certificate remains security-relevant for unenrolled accounts — an unguarded
one hands out an identity, not merely a misfiled certificate. Enrolment is what
contains that, one layer below issuance: see
[../certificate-authority/](../certificate-authority/architecture.md).

The check runs in `auth/middleware.finalizeIdentity`, the single funnel every
authenticated identity crosses on every transport, beside the lifecycle and
permitted-method gates. It applies only where the presented credential IS the
certificate on the connection: a forwarded on-behalf assertion and a capability
module's own session-token plane reach that funnel with no observable method,
and the certificate on those connections belongs to the forwarding node or the
module, not to the subject.

### Three authorization inputs on one user

| Field | Model | Meaning |
|---|---|---|
| `RoleIDs` | RBAC, system-scoped | roles held globally |
| `RoleBindings` | RBAC, own tenant only | `{RoleID, TenantID}` — additive to `RoleIDs`; `TenantID` must be the user's own tenant |
| `Attributes` | ABAC | key/value pairs evaluated by policy |

**A binding names only its holder's own tenant.** A binding naming any other
tenant is refused on write on every transport and by the store, for a
system-tenant user as for any other, so no binding grants anything outside
its holder's tenant. A tenant's own administrators act only in their tenant.
**Known gap:** a system-tenant Admin can still administer a tenant's users
and roles — the routed services resolve a system-tenant caller into the
tenant it names, and the system-tenant scope rule admits it there. Closing
that is the strict tenant-isolation design, which has not landed.

Tenant-scoped bindings are the reason a control-plane user can be tenant-admin
for tenant X without being an admin of the system tenant.

All three fields influence an authorization decision. `finalizeIdentity`
carries `RoleBindings` and `Attributes` onto the authenticated identity
alongside `RoleIDs`. The user store is their only source: whatever the
authenticator carried in is always cleared, and the store's current state
replaces it, so a withdrawn binding or a cleared attribute stops applying on
the next request — including when the record is not found, the tenant is
sealed, or no user store is configured at all (then the identity carries
none). `pkg/auth/rbac.RBACAuthorizer.
Authorize` resolves `RoleIDs` in the identity's own tenant and, separately,
resolves any `RoleBindings` entry whose `TenantID` equals the tenant the
check targets, in THAT tenant, then unions the two permission sets — a
binding for tenant A grants there and nowhere else. The FIPS 140-3 SO
deny-list and the strict separation-of-duties Owner strip both apply to the
effective role set: a bound SO role is denied exactly as a directly held
one, and a bound Owner role is stripped in the tenant that issued it.

`Attributes` reaches `pkg/auth/abac` in its own namespace
(`subject.user_attrs.<key>`, or a `user_attrs.<key>` match key), never in
the authenticated one (`subject.attrs.<key>`) that certificate fields and
token claims populate, so an attribute written to a user record can never
satisfy a policy written against an authentication-derived value of the same
name. See [../auth/abac.md](../auth/abac.md#tenant-scoping).

**Which tenant a request is authorized in.** Every transport, the embedded
one included, makes its entry permission check in the caller's own tenant —
the tenant the authenticated context is bound to. There is no route-level
re-targeting to a tenant the request names. For a caller outside the system
tenant, a request naming another tenant's user or role finds nothing (the
routed services hide it), and one naming another tenant outright (`tenantID`
on a create or a list) is refused with `tenant.CrossTenantDeniedError`. A
system-tenant caller is routed into the tenant it names; that is the known
gap above. An operation whose handler acts on a tenant
the request names — a per-tenant barrier operation, a tenant's own record,
certificate issuance and revocation, an audit query, enabling encryption —
re-checks its permission in that tenant at the handler.

Writing `RoleIDs`, `RoleBindings`, or `Attributes` — on `CreateUser` for
`RoleIDs`, on `UpdateUser` for all three — requires `roles:assign` in
addition to the route's own `users:create`/`users:update` permission, on
every transport including embedded, which authorizes through the node's own
`AuthMiddleware`. The check targets the tenant the record lives in (read from
the record on update, the effective create tenant on create). After it, a
`RoleBindings` entry naming any tenant other than the user's own is refused
with `user.RoleBindingTenantError`, with or without RBAC. Every
referenced role is validated to exist and to belong to that tenant itself
(each tenant seeds its own built-in roles, and a role from another tenant —
the system tenant's included — cannot be resolved there) before the write
commits — whether or not RBAC is enabled. See
[engineering.md](engineering.md#authorization-model) for the mechanism and
its tests.

The user store owns two invariants on `RoleBindings`, so they hold whatever
path the write took. Each binding's `TenantID` is stored normalized (an empty
tenant becomes `system`), because the read side matches it against the
normalized target tenant. And every user — the system tenant's included — may
hold a binding only in its own tenant; any other is refused with
`user.RoleBindingTenantError`.

### Alternatives rejected

- **Passwords.** Rejected outright — the user model is passwordless. There is no
  password field to leak, rotate or brute-force.
- **Binding to the full certificate by default.** Rejected as the default:
  `CertBindingSPKIThumbprint` is canonical precisely so renewal does not break
  the binding. `CertBindingFullCertFingerprint` exists for exact-certificate
  pinning where that is genuinely wanted, and is **not** the default.
- **Per-subsystem authorization.** Rejected — one PDP, one vocabulary.
- **Interpreting factor enrollment blobs.** Rejected: qrdb stores
  `FactorEnrollment.Data` and **never interprets it**. Verification belongs to
  the registered `authfactors.Factor`, which is go-xkms's.

## Built-in roles

Five roles are bootstrapped by `firstboot.Initialize` with deterministic UUIDv8
(SHA-256) identifiers, undeletable — see [../firstboot/](../firstboot/architecture.md) for
the permission matrix. The load-bearing one is the **Security Officer's explicit
denial of crypto operations**: governance and key use are different people, and
that is the FIPS 140-3 AS10.03 separation-of-duties control.

### Reconciliation: the one sanctioned writer

A built-in role is immutable — `UpdateRole` refuses to replace its permission
set and `DeleteRole` refuses to remove it — so that an operator cannot widen
`auditor` or re-permission `so` by hand.

That invariant assumed nothing ever needed to rewrite a built-in's grants. The
assumption expired the first time a permission was **renamed**. Roles are copied
out of `BuiltInRoles()` when a tenant is provisioned and never touched again, so
a tenant created before the rename keeps the retired verbs forever, never gains
the live ones, and every write path that could repair it is closed: update is
refused, delete-and-reseed is refused, and create-over-the-top collides with the
name index. The symptom is a 403 on a live endpoint with nothing explaining it.

`RoleServicer.ReconcileBuiltInRoles(ctx, tenantID)` is the seam. It is **not** a
relaxation of the invariant, because its target is not arbitrary: it is
`BuiltInRoles()` itself, by construction the definition the guard exists to
protect. The caller chooses only WHICH tenant to converge, never what to
converge it to, and every other caller is still refused. The guard was never
about the write; it was about who chooses the value.

Three properties make it safe to run unattended:

- **Idempotent, and measurably so.** A built-in already in agreement is skipped
  entirely — no write, no `UpdatedAt` bump, no audit record. A no-op `Put` would
  still replicate a Raft entry on every boot of every node.
- **Bounded blast radius.** Only names `IsSystemRoleName` recognises are read or
  written. A custom role — including one an operator created to work around the
  drift this repairs — survives untouched.
- **Owner is repaired but never created.** Owner drifts for the same reason (its
  grant is `permission.All()`) and is equally immutable, so it is converged when
  the tenant already holds it. It is never seeded, because Owner is
  posture-conditional: creating it would hand a strict separation-of-duties
  deployment a superuser reconciliation was never asked to create.

**Timing is part of the contract.** `BuiltInRoles()` resolves its permission
sets through the global registry at call time, and a composing layer contributes
permissions when its modules register. A composer must therefore call this
AFTER module registration, or it will converge every tenant onto a definition
missing the modules' own verbs and then report success.

Drift is also visible **before** the repair runs: `Role.RetiredPermissions` is
derived on every read from the live registry and lists the stored permissions
the build no longer defines. It is never persisted — one marshal seam clears it
— so it can only ever report the current disagreement, and it is the field an
operator reads on a listing to see which tenants need converging.

### Device onboarding and OTA updates

Device onboarding and over-the-air update control are **permissions, not
roles**, in the one RBAC model every layer above inherits. qrdb holds no device
or update records; it owns the vocabulary, so the layer that admits devices and
ships updates gates on these exact strings through the same PDP:

| Permission | Grants |
|---|---|
| `devices:onboard` | Admit a device into the caller's own tenant and bind its identity there. |
| `ota:activate` | Activate a staged update image on the tenant's own devices. |
| `ota:confirm` | Confirm an activated image, so the device no longer rolls back. |

All three are `ScopeSelf`: each tenant onboards and updates its own devices,
and a grant held in one tenant reaches no other — the system tenant's included,
since `ScopeSelf` carries no system-tenant lift. The confinement is checked
against the target tenant the handler names (`AuthorizeTenant`), never against
the caller's own.

Of the default roles, the tenant **Admin** carries all three: it does
everything in its tenant except key custody, and onboarding a device or rolling
out an update is neither. **User** (a crypto consumer) and **Auditor**
(read-only) carry none. A tenant provisioned before these grants existed gains
them through `ReconcileBuiltInRoles`; a custom role may grant any of them.

## The canonical login key

Identity in qrdb has two handles, and they answer different questions.

`Username` is the tenant-scoped human name, and for a system identity it is the
only one — `admin` is not an address. `Email` is the **canonical login key**:
the handle every layer above this one resolves a principal by, from the WebAuthn
credential name to the CLI's user identifier.

Neither is the Subject. Both can change, so the identity a login produces
names the account by its immutable user id
([The Subject is the user id](#the-subject-is-the-user-id)); the handles only
find the account.

Before this existed, each layer above kept its own answer — go-xkms named
credentials from one field, go-quicbaas held an email in a side table beside the
qrdb record — and a login could verify a factor and then fail to find anybody it
belonged to. Owning the key here, and exporting it through the SDK, is what
makes those layers agree by construction rather than by convention.

The key is tenant-scoped, not global: the same person may legitimately hold an
account in two tenants, and every login path supplies the tenant alongside the
handle, so a global index would forbid that while buying nothing.

## The login identity

The unified-login ceremony ends by turning the resolved account and the
verified factor into the identity the session bearer carries. That step is an
`IdentityHydrator`, and a composer replaces it with `node.WithIdentityHydrator`
([api.md](api.md#login-identity-nodewithidentityhydrator)). The account's
immutable user id reaches it as `Principal.ID`, so a composer whose sessions
must survive a rename can make the subject the id and not the username. The
default keeps the username.

There is one hydrator per node because there is one ceremony per node: the
embedded transport serves it. The network transports never mint a login
identity. Each request on them carries its own credential, and the
authentication chain decides who it is, so the subject of a REST, gRPC, QUIC,
MCP or Unix request is whatever the chain's authenticator produced. The
authorization layer then resolves that subject to an account by username.

## Invitation self-enrolment

An invitation is how an ORDINARY account — one certs:issue's strict-subset
history never let a tenant Admin mint a credential for directly — gets its
first certificate: the Admin creates the account `pending` and hands the
user a single-use token out of band, and the user enrols a key of their own
with it. The inviter never holds a credential for the account, which is the
property direct issuance cannot offer: whoever signs the CSR holds the
account.

`users:invite` (`ScopeSelfStrict`, granted to the built-in Admin and SO)
creates the pending account in the caller's own tenant — or re-invites one
still pending, taking no other field — and returns the token exactly once
(`pkg/service.InvitationService.InviteUser`). Only the token's SHA-256
digest is ever stored, in the tenant's own identity store
(`pkg/invite.TenantStore`), so a token for one tenant is absent from every
other's. It expires after `auth.invitation_ttl` (default `24h`, capped at
seven days — a token redeemable longer than that is a standing credential,
not an invitation). Requested role IDs go through the same
`roles:assign` gate `CreateUser` already applies, and the target account is
judged by the SAME certificate target guard `certs:issue` is
(`pkg/service.certTargetGuard`, see [Trust boundaries](#trust-boundaries)):
no Security Officer target, and an Owner target only when the caller itself
holds Owner.

**Redemption is the one verb a caller may invoke with no credential at
all.** `InvitationService.RedeemInvitation` takes the tenant, the token, and
either a CSR for the redeemer's own key (the CSR's self-signature is the
proof of possession) or a bring-your-own certificate over that key verified
against the tenant's trust anchors. Everything refusable is judged BEFORE
the token is spent — including a key already bound in the tenant, which is
how the inviter's own key is refused without disturbing the invited user's
ability to redeem — and then the token is consumed and the account is
activated with the binding in ONE conditional commit that requires it still
`pending` (`user.CertActivator.ActivateWithCertBinding`); a failure after
that point revokes the certificate it issued. An unknown, other-tenant,
expired, already-used, or ceremony-role-held token gets the identical
opaque refusal: telling an unauthenticated caller that a token exists but is
spent would confirm a real account was invited, which is exactly the
enumeration the opaque token exists to prevent.

A genuine storage or transport fault behind the lookup or the consuming
write is NOT folded into that refusal. It says nothing about any particular
token or tenant — unlike a sealed or deleted tenant, which the opaque
refusal above does cover — so reporting it as itself leaks nothing an
enumerating caller could use, and folding it in anyway would silently read
an outage as "every token presented right now is forged" for as long as it
lasts. `pkg/invite.DAOStore` tells the two apart with `dao.IsNotFound` (the
DAO layer's typed not-found on an empty read, distinct from a storage
error) and reports the fault as `*invite.StoreError`;
`InvitationService.RedeemInvitation` propagates that type unchanged instead
of collapsing it, so it classifies as a server fault
(`pkg/api/errclass`: Internal) rather than the credential refusal
(Unauthenticated) a bad token gets. Formerly tracked as the
"invite-notfound-ambiguity" open question; resolved by giving `pkg/dao` the
typed not-found every DAO consumer can check for, rather than a one-off fix
inside `pkg/invite`.

Because the caller is unauthenticated, two further controls apply only to
redemption:

- **Optional key pinning.** An invitation may carry the invited user's SPKI
  thumbprint (`invite.WithKeyPin`), sent to the inviter out of band. A
  pinned invitation redeems only with a CSR or certificate over exactly that
  key; any other key gets the same opaque refusal, and the token is NOT
  spent, so the invited user can still redeem it with the right key.
  `auth.invitation_require_key_pin` refuses every unpinned invitation at
  issuance, for a posture that must never hand out a bearer-only token.
- **Per-source and per-tenant throttling.** Redemption draws on two
  token-bucket limits (`auth.invitation_redeem_limit.{per_source,per_tenant}`,
  `pkg/ratelimit`) before anything else runs: one keyed by the caller's
  network address (an IPv6 caller is bucketed by its `/64`), one by the
  tenant the request names. A throttled redemption gets the same opaque
  answer as a bad token.

Redemption is served without authentication on REST
(`POST /invitations/redeem`, a declared public route), native QUIC
(`invitation.Redeem`, the one frame method with no registered permission at
all — `QUICMethodRegistry.RegisterPublic`), gRPC and the Unix socket
(`RedeemInvitation`, registered with
`AuthMiddleware.RegisterPublicGRPCMethods` — the gRPC twin of
`RegisterPublic` — so the unary interceptor skips both authentication and
authorization for it alone), MCP (a SEPARATE public server on
`/mcp/enroll` carrying exactly that one tool, since the main `/mcp` endpoint
answers an unauthenticated caller nothing beyond the tools an embedder
declares public) and
the embedded transport (still gated on the node's seal/boot-state, since a
sealed barrier can serve no invitation data, but with no identity or
permission check beyond that).

## Tenant-issued delegations

An embedding host (an HSM front end, a managed-service control plane) runs a
**service principal**, `qrdb-service:<name>`, for its own in-process work. Some
of that work is on a tenant's behalf: the host serves the tenant's keys out of
the tenant's own data. The platform must never be able to grant that, because
the system owner never reads tenant data. So **the tenant grants it**, and the
grant is an ordinary X.509 client certificate.

```
tenant admin ──certs:delegate──▶ tenant's own login CA (Key B)
                                   │ signs a short-lived client certificate:
                                   │   CN  qrdb-service:<name>
                                   │   OU  <tenant>   (or the tenant's service SPIFFE SAN)
                                   │   critical extension: DelegationGrant{tenant, principal, scope}
                                   ▼
service principal ──mTLS, any transport──▶ node
                                   │ chain → that tenant's login CA, nothing else
                                   │ principal = the one this node accepts
                                   │ lifetime ≤ 24h, not revoked, not the system tenant
                                   ▼
             permissions = scope ∩ the principal's own grants, audited to the tenant
```

**The one mechanism.** A delegation certificate the tenant issues is the
only way an embedder's service principal acts in a tenant's scope. There is
no tenant-scoped `ServiceContext`, no configuration key or `With*` option that
turns delegation on or off or changes its scope, and no fallback path.
`Node.ServiceContext` binds the principal to the system tenant alone, for the
host's own in-process work, and never reaches tenant data.

**Shape chosen: a certificate, not a token.** A delegation is a short-lived
client certificate the tenant's login CA issues, in the spirit of RFC 3820
proxy certificates and SPIFFE X.509-SVIDs. It adds one **critical** extension
(`DelegationGrant`, DER: version, tenant, principal, scope). Critical, so a
verifier that does not understand it refuses the certificate (RFC 5280
§4.2) rather than reading it as a user login. The existing mTLS chain
authenticates it on every transport, the CA's existing revocation revokes it,
and `crypto/x509` builds and verifies it. There is no new token format and no
signature code of its own.

- **Only the tenant issues one.** Issuance is `certs:delegate`, a
  tenant-only permission: it is confined to the caller's own tenant, the
  system tenant has none to give, and a caller that is itself delegated cannot
  issue another (a delegation that could renew itself would have no lifetime).
  The tenant's Key B may be device-held through the certificate-key seam
  (`WithCertKeyProviderFactory`); the delegation is signed wherever Key B is.
- **Only the tenant's own CA speaks for it.** The verifier requires the leaf's
  direct issuer to be the delegating tenant's registered login CA. A
  certificate signed by the cluster CA, a static client CA, a trust anchor or
  another tenant's CA is refused even when it chains.
- **The node names the one principal it accepts.** That is the principal the
  host runs as (`node.WithServicePrincipal`); there is no separate setting. A delegation naming
  any other principal is refused, and a node naming none (`qrdbd`) accepts no
  delegation at all.
- **Authority is an intersection.** A delegated request holds the
  delegation's scope intersected with the grants the host declared for its
  principal (`sdk.RegisterPermissionRoleGrant`), resolved on every request.
  The tenant can narrow the principal's authority, never widen it, and a grant
  the host withdraws stops applying at once. The identity carries no roles,
  role bindings, attributes or user account.
- **Short-lived; revocation is the exception.** One hour by default and never
  more than 24 hours. The cap is one constant, `delegation.MaxLifetime`, not a
  setting: issuance refuses a longer request and the verifier refuses a longer
  certificate whatever the issuing CA wrote. The issuer may only ask for less.
  `ca revoke --serial` refuses one before then.
- **Every use is audited to the tenant's trail.** Each authorization decision
  for a delegated request, allowed or denied, is recorded under the
  delegating tenant with the principal, serial and scope. With no audit logger,
  or a record that cannot be written, the request is refused (503), whatever
  the operator's fail-open setting: that setting covers the operator's own
  trail, not acting on a tenant's behalf unrecorded.
- **Forwarding re-verifies.** A follower forwarding a delegated request to the
  leader carries the certificate itself in the signed on-behalf assertion, and
  the leader verifies it again with its own authenticator. See
  [engineering.md](engineering.md#invariants).
- **In-process, the same certificate and the same check.** An embedder
  holding a delegation in process presents it with `Node.DelegatedContext`,
  which hands the certificate to the node's own delegation verifier, the one
  the mTLS path and forwarding use, and returns a context bound to the
  delegated identity in the delegating tenant. The embedded transport and the
  service layer honour that context exactly as they honour a delegated mTLS
  caller: the same intersection, the same tenant confinement, the same audit
  to the tenant's trail. Each refusal is typed: no service principal
  configured, a chain that does not verify, an expired or revoked
  certificate, or a principal other than the node's.

**Alternatives rejected.**

- **A platform-side switch letting the principal act in a tenant.** The
  platform would then hold tenant data access, which it must not.
- **A tenant-scoped `ServiceContext`, or a delegation on/off or scope
  option.** Each is a second way in beside the certificate, granted by the
  host rather than the tenant, unaudited to the tenant's trail and
  unrevocable by it. In process, the credential stays the tenant-issued
  certificate: `Node.DelegatedContext` takes the certificate, not a tenant
  name.
- **A second, in-process verifier.** Checking a delegation the embedder
  holds by any path but the node's own authenticator would let the two drift;
  `Node.DelegatedContext` reuses the one verifier rather than restating it.
- **A new bearer token (JWT, macaroon).** That is a second credential
  format, a second verifier and a second revocation path, beside an mTLS
  chain that already authenticates every transport.
- **A non-critical extension.** A verifier that did not know it would admit
  the certificate as a user named `qrdb-service:<name>`.

The extension is named `tp-delegation` and its OID is
`1.3.6.1.4.1.29377.101.24`. The arc `29377.101` is the company's IANA Private
Enterprise Number (29377) certificate-extension arc, shared with
go-trusted-platform and go-xkms.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| User model, lifecycle state, cert bindings, role bindings, attributes | `pkg/user` | go-qrdb |
| Roles | `pkg/role` | go-qrdb |
| Permission vocabulary and registry | `pkg/permission` | go-qrdb |
| The RBAC PDP, ABAC, middleware, authenticators | `pkg/auth` | go-qrdb |
| Certificate issuance | `pkg/cert` — see [../certificate-authority/](../certificate-authority/) | go-qrdb |
| Auth factors framework and the mTLS factor | `pkg/authfactors` — see [../auth-factors/](../auth-factors/) | go-qrdb |
| FIDO2 / PIV / PKCS#11 factor implementations | — | **go-xkms**, injected |
| External auth backends (LDAP, PAM, TACACS+, RADIUS) | — | **go-auth**, if composed |

## Trust boundaries

- **The tenant comes from the certificate, never from the request.** This is the
  root of tenant isolation across every feature.
- **No user record or credential can become the embedder's service
  principal.** `node.WithServicePrincipal` gives an embedding host an
  in-process identity, `qrdb-service:<name>` in the system tenant. Its grant
  set is the permissions registered under its name, and the RBAC PDP decides
  each of its calls as it decides any caller's. The subject namespace is
  reserved: an identity in it, or one carrying the principal marker, is
  refused as unauthenticated on every network transport, even when a user
  record of that name exists. The one way the principal reaches a request
  from the network is a delegation its tenant's own CA issued, and it then
  acts inside that tenant only. See
  [Tenant-issued delegations](#tenant-issued-delegations) and
  [../api-server/engineering.md](../api-server/engineering.md#service-principal).
- **No caller mints another tenant's identities or reads another tenant's
  audit trail — the system tenant included.** `certs:issue`, `certs:enroll`,
  `certs:list`, `certs:revoke`, `certs:read`, `certs:export` and
  `trust_anchors:add|list|remove` are `ScopeSelfStrict`, which the
  system-tenant rule does not lift: a certificate this node's CA signs — or
  one chaining to a tenant's trust anchor — authenticates as the user and
  tenant it names, so minting or enrolling one for another tenant's user is
  logging in as that user. Issuance and enrolment take no tenant at all; they
  act on the caller's own. The only exception is a new tenant's first admin,
  certified at tenant creation by the platform operator from the admin's own
  CSR or certificate (see
  [../multi-tenancy/architecture.md](../multi-tenancy/architecture.md)), with
  no private key generated on the server. An audit query is confined to the
  caller's own tenant on every transport (`tenant.ConfineAuditFilter`); the
  system tenant additionally sees the platform events that carry no tenant.
  See [../certificate-authority/architecture.md](../certificate-authority/architecture.md#which-tenant-a-certs-call-may-act-on)
  and [../audit/architecture.md](../audit/architecture.md).
- **Enrolment bindings are SPKI thumbprints**, so possession of the private key
  is what authenticates. An account holding at least one binding is refused any
  certificate that matches none of them, whatever its CN, OU or SPIFFE SAN say;
  an account holding none states no restriction under the default posture and
  is refused under `auth.mtls_binding_policy: required`. See
  [What a certificate actually claims](#what-a-certificate-actually-claims).
- **Key custody stays with the ceremony: the certificate target guard**
  (`pkg/service.certTargetGuard`) refuses `certs:issue`, `certs:enroll` and
  `users:add_cert_binding` (granted to the built-in tenant Admin) against an
  account holding the tenant's Security Officer role, and against an account
  holding the Owner role unless the CALLER itself holds Owner too — a tenant
  Admin otherwise does everything in its own tenant. There is no Custodian
  role to refuse: a custodian authenticates by holder certificate
  (`pkg/holderprofile`), never by user account, so it is never a target to
  begin with. `users:add_cert_binding` binds a thumbprint, which is binding a
  key, so it is held to the SAME guard on every transport: the auth
  middleware reads the target account's record and asks the guard the node
  built for issuance (`AuthMiddleware.AuthorizeCertBindingTarget`, wired by
  `wiring.AuthMiddlewareConfig.CertTargetGuard` from
  `lifecycle.Node.CertTargets`; without a guard the verb fails closed with
  `*middleware.CertTargetGuardRequiredError`).
- **A binding can be withdrawn, and withdrawal is the only unenrolment.**
  `users:remove_cert_binding` removes the record entry and both index entries.
  It is a distinct grant from enrolment because it is the direction that can
  widen an account, and it is distinct from `certs:revoke` because a binding
  outlives the certificates issued for its key.
- **`Lifecycle` is the only account-state field and the single source of truth
  for whether an account may authenticate** — `pending` · `active` ·
  `suspended` · `locked` · `disabled` · `deleting`. `User.CanAuthenticate()` is
  the check, and it fails closed on an absent state and on a state this build
  does not recognize.
- **`deleting` is terminal.** A deletion writes it before its first mutation,
  and from that instant no concurrent enable, and no credential ceremony
  finishing late, can make the account authenticable again. `DeleteUser` marks
  the record before its own cascade, and a terminal record accepts no mutation
  at all — not an update, not a login timestamp, and above all not a new
  certificate binding.
- **`AuthMethods` is a policy qrdb enforces for the authenticators it owns.**
  An empty policy states no restriction. Where the method is not observable to
  qrdb — a forwarded assertion, a capability module's own session token — the
  layer that ran the ceremony enforces it, through the same exported
  `CheckAuthMethod` the middleware runs and with the method spelled by a qrdb
  constant (`MethodMTLS`, `MethodPIV`, `MethodWebAuthn`).
- **Group membership is not a qrdb field.** qrdb authorization resolves
  user → role bindings → roles → permissions, and nothing in it reads a group.
  A layer that needs group membership stores it itself; the former `GroupIDs`
  field, which influenced no decision, has been removed.
- **`System: true` marks exactly one user — the cluster-wide bootstrap admin —
  and it, like the built-in roles, cannot be deleted.** No request on any
  transport can set the flag; it is populated once, in-process, by first-boot.
  A per-tenant admin created by tenant provisioning is an ordinary, deletable
  user.
- **Factor data is opaque.** qrdb never parses it, so a factor implementation
  cannot smuggle a decision past the PDP through its blob.
- **Invitation redemption is the one write any caller may make with no
  credential at all**, and every refusal before the token is spent is the
  SAME opaque answer regardless of cause. See
  [Invitation self-enrolment](#invitation-self-enrolment).

## What this feature deliberately does not do

- **No passwords.**
- **No hardware factor verification.** The `Factor` framework and the software
  mTLS factor are here; FIDO2, PIV and PKCS#11 come from go-xkms.
- **No external identity providers.** LDAP, PAM, TACACS+ and RADIUS are
  go-auth's, composed above.
- **No permission invention at runtime.** The vocabulary is a registry populated
  at `init()`; `Parse` validates against it.
- **No groups.** qrdb stores no group membership, owns no group catalog, no
  group CRUD and no group→role resolution. Roles live here because qrdb's own
  PDP evaluates them; a group is an indirection for assigning roles to many
  users, and the layer that owns the mapping stores the membership and resolves
  it.

> **Ruled change — strict tenant isolation.** The multi-tenancy design
> [Strict tenant isolation](../multi-tenancy/architecture.md#strict-tenant-isolation--design)
> (RULED 2026-10-01, not implemented) changes this feature in five places:
>
> - `ScopeSelf` loses its system-tenant lift. Cross-tenant reach exists only
>   through `ScopeCross` platform permissions.
> - The system-tenant exception in `user.NormalizeRoleBindings` is removed:
>   a role binding names its holder's own tenant, for every holder.
> - The global identity index (`tenant.IdentityIndex`) is removed. A by-id
>   operation resolves in the caller's own tenant, and an operator verb names
>   its tenant explicitly.
> - A strict tenant's users, roles, factors, bindings, certificates and CA
>   move out of the system store into the tenant's own home store, and its
>   users authenticate only with certificates that chain to the tenant's own
>   CA, with SPKI binding required.
> - A tenant's first admin comes from a CSR supplied at tenant creation. Barrier
>   init proves possession of that key, replacing trust-on-first-init by "a
>   certificate naming the tenant".
>
> Until that ships, this document describes the code.

## Companion documents

- [auth/README.md](../auth/README.md) — pluggable authentication interfaces and
  the default mTLS authenticator.
- [auth/roles.md](../auth/roles.md) — the built-in roles with their full
  permission matrix and the FIPS 140-3 AS10.03 separation-of-duties
  constraints.
- [auth/abac.md](../auth/abac.md) — attribute-based access control.
- [auth/layering.md](../auth/layering.md) — how the authorization layers compose.
- [auth/custom-authorizers.md](../auth/custom-authorizers.md) — replacing or
  extending the authorizer.
