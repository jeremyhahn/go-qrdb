# Identity and RBAC — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

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

### Identity mapping

| Certificate field | User field | Role |
|---|---|---|
| Subject CN | `Username` | unique identity within a tenant |
| SPIFFE tenant SAN, trust domain pinned | `TenantID` | tenant isolation boundary, when present |
| Subject OU | `TenantID` | tenant isolation boundary, when no SAN is present |
| SHA-256(SubjectPublicKeyInfo), base64url | `CertBindings[].Thumbprint` | **the authoritative mTLS identity** — `User.MatchesCert(cert)` |

**Which tenant derivation a certificate from the built-in CA uses** depends on
`auth.spiffe_trust_domain`: with a trust domain configured the CA emits the
tenant-scoped SPIFFE SAN and the pinned derivation resolves it; without one it
emits no SAN and the OU does. The CA refuses to mint a certificate whose SAN
would disagree with its OU. See
[../certificate-authority/](../certificate-authority/architecture.md#which-derivation-a-generated-certificate-uses).

**The thumbprint is computed over the public key, not the certificate.** A
renewed certificate for the same key produces the same thumbprint, so bindings
survive renewal — which is what makes certificate rotation an operation rather
than a re-enrolment.

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
  certificate is validly signed by the cluster CA, and even when its tenant
  came from a SPIFFE SAN rather than the OU fallback. **The SAN carries no
  extra weight: it is issued by the same CA as the OU.**
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

## The canonical login key

Identity in qrdb has two handles, and they answer different questions.

`Username` is the tenant-scoped human name, and for a system identity it is the
only one — `admin` is not an address. `Email` is the **canonical login key**:
the handle every layer above this one resolves a principal by, from the WebAuthn
credential name through the session subject to the CLI's user identifier.

Before this existed, each layer above kept its own answer — go-xkms named
credentials from one field, go-quicbaas held an email in a side table beside the
qrdb record — and a login could verify a factor and then fail to find anybody it
belonged to. Owning the key here, and exporting it through the SDK, is what
makes those layers agree by construction rather than by convention.

The key is tenant-scoped, not global: the same person may legitimately hold an
account in two tenants, and every login path supplies the tenant alongside the
handle, so a global index would forbid that while buying nothing.

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
answers nothing, not even `tools/list`, to an unauthenticated caller) and
the embedded transport (still gated on the node's seal/boot-state, since a
sealed barrier can serve no invitation data, but with no identity or
permission check beyond that).

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
  record of that name exists. See
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
