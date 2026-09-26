# Firstboot and System Bootstrap — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Firstboot is the **admin bootstrap**: system tenant, built-in roles, the
platform admin user and the admin client certificate. It runs *after* the
[ceremony](../ceremony/), which is barrier custody only.

## Problem

A system that has just initialized its barrier still has nobody who can log in,
no roles to authorize against, and no tenant to put anything in. Creating those
in the wrong order — or creating an admin whose credential the server has never
heard of — leaves an operator locked out of the system they just installed.

## Shape chosen

Two bootstraps, run in order, each idempotent:

| Concern | Package | Function |
|---|---|---|
| Barrier custody (M-of-N split, custodian enrolment, share lifecycle) | `pkg/ceremony` | `ceremony.Initialize` |
| System tenant + built-in roles + platform admin user + admin client cert | `pkg/firstboot` | `firstboot.Initialize` |

Orchestration entry points live in `pkg/bootstrap`:

System-level admin enrollment is cert-based (CSR-sign or generate-and-distribute -> SPKI-thumbprint binding); there are no invite tokens at the system level. The control-plane (system) and data-plane (tenant) scopes are orchestrated through the `pkg/bootstrap` entry points:

- **System bootstrap** — system barrier + system tenant + system admin + admin-mTLS.
- **Provision tenant** — minimal control-plane tenant record + tenant-admin enrollment per `AdminEnrollmentMode` (no tenant barrier key held by the system admin): `cert` (default) enrolls the tenant-admin cert during provisioning; `deferred` creates the tenant admin PENDING with no credential bound, returning the user + role ids so the composing host (go-quicbaas) can attach a single-use invitation (`pkg/invite` `TenantInviteStore`) whose redemption binds the admin's first auth factor and activates the account (owner ruling 2026-09-01, company doc 08 §"Admin enrollment").
- **Initialize tenant barrier** — tenant-admin self-service (authenticated): initialize THIS tenant's barrier and scope.


**System-level admin enrollment is certificate-based** — CSR-sign or
generate-and-distribute, binding an SPKI thumbprint. **There are no
system-level invite tokens**; the deferred tenant-admin mode above is the one
sanctioned invitation seam, and it applies to tenant admins only.

## Admin credential paths

`firstboot.Initialize` binds the admin's SPKI thumbprint by one of three
mutually-exclusive paths, selected by which `Dependencies` inputs it receives:

| Path | Input | What happens | Key custody |
|------|-------|--------------|-------------|
| **A** generate-and-distribute | `CA` | `CertProvider.GenerateAdminCert` issues a key + cert bundle | CA emits the bundle |
| **B** CSR-sign | `AdminCSRPEM` + `Signer` | `CertSigner.SignAdminCSR` signs the admin's CSR | Key never leaves the admin |
| **C** adopt-presented | `AdminCertPEM` + `AdminCertPool` | The certificate the admin **already holds** (PIV card, security key) is validated and adopted; nothing is issued | Key never leaves the operator's token |

Path C is checked **first**, because it is the only path that issues nothing: a deployment that presents a certificate has already decided which identity the admin authenticates as, so falling through to a configured CA would mint a second, unrelated credential and bind that instead — leaving the operator holding a certificate the server has never heard of.

**Path C is checked first**, because it is the only path that issues nothing.
A deployment presenting a certificate has already decided which identity the
admin authenticates as; falling through to a configured CA would mint a second,
unrelated credential and bind *that* instead — leaving the operator holding a
certificate the server has never heard of.

Refusals:

- `AdminCSRPEM` **and** `AdminCertPEM` together → `ConflictingAdminCertInputsError`. Signing the CSR would leave the presented certificate unbound; adopting the certificate would silently discard the CSR. Neither is choosable on the operator's behalf.
- `AdminCertPEM` without `AdminCertPool` → `MissingAdminCertPoolError`. An absent pool is refused rather than treated as "trust anything".
- Parse failure, expiry, or no chain to the pool → `AdminCertRejectedError`.

`AdminCertPool` is deliberately **not** the platform CA: a certificate the operator already holds was issued by something else (corporate PKI, card vendor), so validating against the CA this deployment is about to create would reject every legitimate input.

`AdminCertPool` is deliberately **not** the platform CA: a certificate the
operator already holds was issued by something else (corporate PKI, a card
vendor), so validating it against the CA this deployment is about to create
would reject every legitimate input.

This makes admin enrolment the mirror of `EnrollExternalOfficer` — the admin and
every security officer enrol by the same rule, and an operator who can bring
their own credential to one role can bring it to the other.

### Under `auth.mtls_binding_policy: required`

When the fleet requires every certificate login to present an enrolled key
(see [../identity-rbac/](../identity-rbac/architecture.md#the-fleet-wide-posture-authmtls_binding_policy)),
an admin without a binding is an admin nobody can log in as — and the way back
in needs a login. `Initialize` therefore takes the policy
(`Dependencies.MTLSBindingPolicy`, forwarded by `bootstrap.SystemConfig`) and,
under `required`, refuses **before its first write**:

- an admin it would **create** with no `Binder`, or with no path above to issue
  or adopt a certificate — there would be nothing bound;
- an admin that **already exists** with no unexpired binding — a re-run issues
  nothing, so nothing would change.

Verifying an existing admin needs a read, so `Users` must also implement
`AdminLookup`; one that cannot is refused, because the posture cannot be shown
to hold. After its last step it reads the admin back and checks the outcome
too, so a component that reported success without producing a binding is
caught rather than trusted. The SDK node's `Node.FirstBoot` runs this under the
node's own policy and verifies against the node's store. A composer that skips `Initialize` once the admin exists — the daemon
does — runs the same predicate at every boot with `VerifyAdminCertBinding` and
refuses to start on its error. Every refusal is
`AdminCertBindingRequiredError`, naming the reason and the way back (start with
`enrolled`, enrol the admin, set `required`).

### Alternatives rejected

- **Invite tokens for system-level identities.** Rejected: a bearer token that
  grants system admin (or a Shamir custodian) is a bearer token that can be
  replayed against barrier material. System-level enrolment is
  certificate-based, always. The initial *tenant* admin is the ruled
  exception (2026-09-01): `ProvisionTenant`'s deferred mode defers the
  credential to a single-use invitation redeemed through the ordinary
  `authfactors.Factor.Enroll` seam — the same machinery as ordinary-user
  invitations, guarding no barrier material at redemption time.
- **Falling back to the CA when a certificate is presented.** Rejected — that
  is the reason path C is evaluated first.
- **Treating a missing `AdminCertPool` as "trust anything".** Rejected:
  `MissingAdminCertPoolError`.
- **Choosing between a CSR and a presented certificate on the operator's
  behalf.** Rejected: `ConflictingAdminCertInputsError`. Signing the CSR would
  leave the presented certificate unbound; adopting the certificate would
  silently discard the CSR. Neither is choosable for them.
- **Non-deterministic role IDs.** Rejected: each built-in role gets a fixed
  UUIDv8 (SHA-256) derived from its name, so role identity is stable across
  clusters.

## Built-in roles

Created deterministically on first init, undeletable, persisted at
`__sys:role/<id>`, each with a fixed `uuid5(namespace, "go-qrdb/role/<Name>")`:

| Admin | `uuid5(namespace, "go-qrdb/role/Admin")` | `keys:*` (except delete infrastructure), `certificate:*`, `tenant:*`, `backup:*`, `audit:read` | Day-to-day key lifecycle |
| Operator | `uuid5(namespace, "go-qrdb/role/Operator")` | `keys:sign`, `keys:encrypt`, `keys:decrypt`, `keys:verify`, `barrier:seal`, `barrier:unseal`, `audit:read` | Routine operations |
| User | `uuid5(namespace, "go-qrdb/role/User")` | `keys:sign`, `keys:verify`, `keys:encrypt`, `keys:decrypt` | Crypto ops only |
| Auditor | `uuid5(namespace, "go-qrdb/role/Auditor")` | `audit:read`, `system:read`, `*:list`, `*:get-metadata` | Read-only everywhere |
| Custodian | `uuid5(namespace, "go-qrdb/role/Custodian")` | `barrier:unseal-share` | Shamir share holder only |

An admin user for the system tenant is also created:


The full permission matrix and the FIPS 140-3 AS10.03 separation-of-duties
constraints are in [../identity-rbac/](../identity-rbac/) and
[auth/roles.md](../auth/roles.md).

An `admin` user for the system tenant is created alongside, bound to the
`admin` role, once and idempotently.

## Startup sequence

1. Configuration loading
2. TLS certificate generation
3. Storage initialization
4. Raft initialization
5. Barrier initialization (if needed) — [../barrier-seal/](../barrier-seal/)
6. Role and user bootstrap — this feature
7. Multi-protocol servers start — [../api-server/](../api-server/)

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Admin/tenant/role bootstrap | `pkg/firstboot` | go-qrdb |
| System / tenant orchestration entry points | `pkg/bootstrap` | go-qrdb |
| Barrier custody bootstrap | `pkg/ceremony` — see [../ceremony/](../ceremony/) | go-qrdb |
| Roles, users, permissions | `pkg/role`, `pkg/user`, `pkg/permission` — see [../identity-rbac/](../identity-rbac/) | go-qrdb |
| Certificate issuance | `pkg/cert` — see [../certificate-authority/](../certificate-authority/) | go-qrdb |

## Trust boundaries

- **The admin is bound by SPKI thumbprint**, not by a password or a token.
- **A presented certificate is validated against an operator-supplied pool**,
  never against the platform CA.
- **Only the leader may create.** In multi-node clusters followers detect
  `NotLeader` and wait for the leader's writes to replicate. That wait is
  entered for a `NotLeader` condition and nothing else — a first-boot failure
  of any other kind is returned with its cause rather than being retried as
  though a leader elsewhere were about to fix it.
- **A boot-time role or user create is ONE atomic proposal, not several.**
  The identity claim, the primary record, and every secondary index used to
  commit as separate Raft proposals — up to four for a role, more for a
  user with an email login key — so a leadership change between any two of
  them could leave a claim with no record behind it, or a record missing
  one of its indexes, permanently: nothing could complete or roll back the
  remainder without the very leadership that was just lost, and every
  future create of that same role or user then found the same partial
  state and refused identically, forever. `role.WithAtomicIdentity` /
  `user.WithAtomicIdentity` close this by committing the claim and every
  write as ONE guarded batch, using the multi-guard
  `kvstore.MultiGuardedBatchKVStore.PutAllIfAllUnchanged` for BOTH: a role
  fences its generated id claim and its name together (an id-only guard
  cannot see two concurrent creates that use different ids but the same
  name), and a user fences its generated id, its username, and an optional
  email together (a single guard cannot express all three at once). A
  failure anywhere in the sequence now leaves either everything or nothing.
  Every boot-time composer (`cmd/qrdbd`, and through
  `pkg/server/lifecycle`, `sdk/go/node`) wires its role and user stores
  with a guarded adapter, and construction refuses outright when no
  transaction plane backs it — a compile-time-shaped capability that would
  otherwise be present in the type and silently absent at run time.
  That construction-time refusal is necessary but not sufficient: the
  adapter's type says a transaction plane is wired, not that the state
  machine behind it evaluates `PutAllIfUnchanged`/`PutAllIfAllUnchanged`.
  Every registered `--sm` type does — each runs transactions against its own
  keyspace — but `cmd/qrdbd` asks rather than assumes, with a genuine
  runtime probe —
  `kvstore.ProbeGuardedBatch`, a real no-op guarded proposal against the
  shard the daemon just started, not a static capability query — immediately
  after leader election on **every start**, so an unsupported `--sm` value
  refuses at startup with a typed error rather than an operator discovering
  it at the first role or user create. After the leader wait an inconclusive
  probe also stops the start, with one exception: a follower's probe always
  answers `NotLeader`, because only the leader proposes, so a follower defers
  to the leader's probe — every replica of a shard runs the same state
  machine.
- **The routed role/user services `pkg/server/lifecycle`/`sdk/go/node`
  expose are ALSO atomic for the one tenant where it is safe: the system
  tenant.** `role.NewRoutingService` / `user.NewRoutingService` resolve a
  fresh, per-tenant store on every call and build one `kvRoleStore`/
  `kvUserStore` per distinct store they see — WithAtomicIdentity's
  same-physical-store precondition holds for exactly one of those resolved
  stores, because `tenant.NewResolvedIdentityIndex` always backs the
  identity claim with `resolver.Resolve(ctx, tenant.SystemTenantID)`, so
  only a role or user create ROUTED to the system tenant shares a physical
  store with the claim it makes. The router offers the option there, and
  ONLY there, and only when that resolved store is ALSO
  `kvstore.MultiGuardedBatchKVStore`-capable. Every other tenant — a
  barrier-isolated tenant's own store, or a prefix tenant sharing the
  non-system portion of the shared store — keeps the unconditional
  sequential path, because its own store is a DIFFERENT physical object
  than the one the claim would be checked against.
  This is a deliberate, narrower exception to "fail closed, no silent
  fallback" than `cmd/qrdbd`'s single fixed state machine gets: the router
  serves potentially many heterogeneous per-tenant backends from one
  process, so one tenant's backend lacking the capability keeps that ONE
  tenant on the sequential path rather than refusing role/user creation for
  every OTHER tenant too.
  "Capable" is asked of the state machine, not read off the store's type:
  `pkg/server/lifecycle` builds its guarded adapter whenever a transaction
  plane exists, which it always does, including over state machines that
  refuse a transaction at apply time. The router therefore runs the same
  genuine no-op guarded proposal `cmd/qrdbd` runs at startup
  (`kvstore.ProbeGuardedBatch`) the first time a role or user is CREATED in
  the system tenant, and caches only a DEFINITIVE answer. Only the create
  path probes: reads, lookups and permission resolution — the authentication
  path — use a store built without the option and never touch consensus for
  this, so they keep working while no leader can answer. The guarded adapters
  report a state machine without the subsystem as
  `kvstore.GuardedBatchUnsupportedError` (which matches
  `kvstore.ErrAtomicWriteUnsupported`); that answer keeps the system tenant
  on the sequential path for the life of the process. A probe that cannot
  answer — no leader yet, a cancelled request — fails that one role or user
  operation with a typed `StoreError` and is asked again on the next one. It
  is never read as "incapable": caching that reading would put the system
  tenant on the sequential path for the life of the process and reopen the
  multi-node first-boot race this whole section exists to close.
- **Known limit: prefix tenants take the sequential create path.** A prefix
  tenant's own store is a confined view of the shared store, not the system
  tenant's store the identity claim lives in, so its role and user creates
  keep the claim-then-write sequence. A leadership change between those
  proposals can still leave a claim without its record for a prefix-tenant
  create; the atomic guarantee above holds for the system tenant, which is
  where first boot creates everything.
- **A losing create's error is typed even when this node reads stale.** A
  refused guard means the winner's keys were committed, but a node reading
  its own applied state (`cmd/qrdbd`'s boot-time `StaleRead`) may not have
  applied them yet. The diagnosis that turns the refusal into
  `RoleIDExistsError`/`RoleNameExistsError` or
  `UserIDExistsError`/`UsernameExistsError`/`EmailExistsError` re-reads
  within the same bounded window reconciliation uses, so the losing first-boot
  node still gets the typed error `firstboot` resolves idempotently rather
  than a generic guard failure.
- **A concurrent, identical built-in role's create is verified, not
  assumed — on EITHER collision `CreateRole` can report.** Two nodes'
  first-boot attempts can both reach `CreateRole` for the SAME built-in role
  at once; exactly one wins the atomic batch, and the other is refused with
  the ordinary id-collision OR name-collision error. `firstboot` resolves
  either refusal by reading the persisted record back and comparing it
  against the canonical built-in definition — tenant, canonical name, the
  `System` immutability flag, and permission set always; the id as well for
  an id collision, but NOT for a name collision, since a cluster seeded
  before `role.SystemRoleID`'s hash algorithm changed (FIPS 140-3's move off
  SHA-1) legitimately keeps its roles under a different, older id — before
  treating either refusal as its own idempotent re-attempt. That check lives
  in `firstboot`, not in `CreateRole` itself: a general create endpoint
  accepting "same name and a guessed id means treat it as mine" would let
  any caller holding `roles:create` adopt another actor's in-flight or
  malformed role.
- **The admin is verified the same way, and an id collision with anyone else
  is retried.** A losing admin create reports `UsernameExistsError` or
  `UserIDExistsError`; either is accepted as idempotent only when the record
  under the admin username is the account first boot creates: the admin's
  tenant and username, the tenant-scoped Admin role, `System` false, and a
  defined, non-terminal lifecycle (an operator may since have suspended or
  disabled it; `deleting` or an unknown state is refused). An id collision
  with no such admin behind it means the generated id repeated some other
  user's, so the create is retried with a fresh id, a bounded number of
  times. A username held by anything but the admin is a squatter and is
  refused.
- **Sensitive keys live in the barrier.** After unseal they are available in
  memory through `seal.EncryptionBarrier.Decrypt()`.

## Idempotency

The bootstrap sequence is **idempotent** and safe to re-run:

- **Certificates** — If certs already exist, they are reused
- **Admin User** — If admin exists, creation is skipped
- **Barrier** — If sealed, unseal must be done manually; if unsealed, state remains
- **Raft** — If a node restarts, it re-joins the cluster using the stored log and snapshots

A fresh start vs. a restart of an existing node follows the same sequence; only the first run performs initialization, subsequent runs skip redundant operations.


A fresh start and a restart follow the same sequence; only the first run
performs initialization.

## What this feature deliberately does not do

- **No barrier custody.** That is the ceremony.
- **No invite tokens or password-based enrolment for system-level
  identities.** The deferred tenant-admin mode of `ProvisionTenant` is the
  only invitation seam, and the invitation itself (token issuance and
  redemption) is composed by the host, not by firstboot.
- **No unsealing.** If the barrier is sealed, unsealing is a separate,
  deliberate operator action.
- **No role customization at bootstrap.** The six built-in roles are fixed;
  additional roles are created afterwards through
  [../identity-rbac/](../identity-rbac/).
