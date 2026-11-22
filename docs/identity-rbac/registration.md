# Registration policy (design note)

> **Status: PROPOSED — awaiting owner ruling** · Scope: go-qrdb
> Last verified: 2026-11-22 at main 952b4057f2
> SPEC row `registration-policy` (future) in [../SPEC.md](../SPEC.md). Nothing here is built. Each open question lists its options, a recommendation and the reason. The owner rules, and the Ruling section stays empty until then.

## Problem

Whether a stranger may create an account is decided today in two products
above go-qrdb, each with its own setting: go-xkms
`webauthn.allow_public_registration` (default invite-only) and go-quicbaas
IAM `self_registration_disabled`. go-qrdb has invitations (`InviteUser`,
`RedeemInvitation`, `auth.invitation_ttl`) but no registration mode, and no
way for an unauthenticated caller to create an account. Every product that
builds on go-qrdb should use one implementation: a hosted SaaS lets new
customers sign up, while an internal deployment stays invite-only.

## Accounts today

Every account goes through `user.Service.CreateUser`
(`pkg/user/routing.go`, `pkg/user/store.go` `kvUserStore.CreateUser`). The
callers are:

| Path | Authorization | Public |
|---|---|---|
| Admin create (`POST /users`, gRPC `CreateUser`, QUIC `user.Create`, MCP `qrdb_user_create`) | `users:create` | no |
| `InvitationService.InviteUser`: pending account plus a token | `users:invite` | no |
| `RedeemInvitation`: activates the pending account, creates none | the token | yes (`publicverbs.VerbInvitationRedeem`) |
| firstboot admin, tenant provisioning, pending tenant-admin completion | internal | n/a |

### Dependencies

No lower-project API is needed: go-quicraft and go-objstore are not touched.
The design reuses `publicverbs.Table`, the guarded tenant update,
`user.Service.CreateUser`, `users.ActivateWithCertBinding`,
`certenroll.IssueFromCSR` and the tenant user quota. It adds no Raft opcode.
go-xkms and go-quicbaas each remove their own setting in their own
repository after this row ships, and go-qrdb does not depend on them.

## Design

### The policy

A registration mode has two values:

- `invite_only`, the default. No unauthenticated account creation. Accounts
  come from admin create and from invitations, as today.
- `open`. An unauthenticated caller may create an account for themselves in
  the tenant with the new `Register` verb.

The invitation flow is the same in both modes. `open` adds a path. It
removes none.

### Where it is set

1. **Node default:** a new config key `auth.registration_mode` (`invite_only`
   | `open`, empty means `invite_only`), a field on `config.AuthConfig` beside
   `InvitationTTL`. It is parsed and validated by `config.ParseRegistrationMode`
   (a `ValidationError` on `auth.registration_mode` for any other value), and
   documented in [../12-configuration.md](../12-configuration.md) when the row
   is built. It applies to every tenant without its own value, the system
   tenant included (see Q5).
2. **Per tenant:** `tenant.TenantInfo.RegistrationMode` (empty means inherit
   the node default) and `TenantPatch.RegistrationMode`. The value lives in
   the replicated tenant record, written through the existing guarded tenant
   update. It adds no Raft opcode and no node-local state.
3. **API:**
   - `SetTenantRegistrationMode(tenant, mode)`, where `mode` is `invite_only`,
     `open` or `inherit`.
   - `GetTenantRegistrationMode(tenant)`, which returns the stored value and
     the effective mode, so a reader can tell "inherited" from "set".

   Both exist on every transport (REST `PUT|GET /v1/tenants/{id}/registration`,
   gRPC, QUIC `tenant.SetRegistration|GetRegistration`, unix, MCP, embedded)
   and in the CLI as `qrdb tenant registration set|get`.
4. **Permission:** a new `tenants:set_registration`. It is self-scoped, so a
   tenant Admin may set its own tenant (owner ruling of 2026-10-04: a tenant Admin may do everything in its own tenant except SO and custodian duties). The
   cross-tenant form `tenants:set_registration:cross` is for platform
   operators. It is a separate permission rather than `tenants:update`, so
   ABAC policy and audit can name the act of opening sign-up. Reads use
   `tenants:read`.

### Enforcement

`RegistrationService.Register` in `pkg/service` is the only place a mode is
read for account creation. The transports call it and decide nothing
themselves. In order:

1. Resolve the tenant named in the request. An unknown tenant, a tenant that
   is not `active`, and a tenant whose effective mode is `invite_only` all
   return the same `ErrRegistrationClosed`. A caller learns nothing about
   which tenants exist. It maps to `PermissionDenied` on gRPC, QUIC, MCP,
   unix and embedded, and to `403` on REST.
2. Call the composer's `RegistrationGate`, if one is set (see Seam). A denial
   is returned as-is, as `PermissionDenied`.
3. Create the account through `user.Service.CreateUser` in the tenant, with
   exactly one role: the tenant's built-in `user` role
   (`role.SystemRoleID(tenant, role.RoleNameUser)`). The request carries no
   roles, permissions or tenant-admin flags, so self sign-up cannot assign
   privilege.
4. Set the credential exactly as `RedeemInvitation` sets one. The request
   carries a CSR (required; it proves the caller holds the key) and,
   optionally, a bring-your-own certificate and chain over that key.
   `Register` issues through `certenroll.IssueFromCSR` or verifies the BYO
   certificate, then binds it with `users.ActivateWithCertBinding`. It shares
   that code with redemption, so the credential rules of both flows cannot
   drift apart, and it returns the issued certificate as redemption does.
   There is no other credential path.
5. Write an audit record: `user.registered` with the tenant, the account and
   the transport, in the tenant's trail.

The tenant's user quota (`quota.MaxUsers`) applies, so `open` cannot grow a
tenant past its quota. `Register` is a pre-auth verb. It gets the pre-auth
message cap (`transport.max_message_bytes`, default 4 MiB), and its `publicverbs.Table` entry carries a `RateLimit`
dimension, as redemption's does.

### The new public verb

`Register` is added to `publicverbs.Table` (`pkg/api/publicverbs`) beside
`VerbHealth` and `VerbInvitationRedeem`. That table is the single allowlist
every transport reads, so the verb is public on all transports at once and
nowhere else:

| Transport | Name |
|---|---|
| REST | `POST /v1/registrations` |
| gRPC | `Register` (`identity:public`) |
| QUIC | `user.Register` |
| MCP | `qrdb_register` |
| unix, embedded | `Register` |
| CLI | `qrdb register --tenant-id T --username U [--csr F [--cert F [--chain F]]] [--out DIR]` |

The CLI flags have the same meanings as `qrdb enroll`: without `--csr`, a
key is generated locally and written only to `--out`.

### Seam

```go
// RegistrationGate is consulted by Register before an account is created.
// A composer uses it for checks go-qrdb does not own: email or phone
// verification, CAPTCHA, billing. A nil gate admits every request that the
// tenant's mode allows.
type RegistrationGate interface {
    AdmitRegistration(ctx context.Context, req RegistrationRequest) (RegistrationDecision, error)
}

type RegistrationDecision struct {
    // Pending creates the account pending. It is activated through the
    // existing activation path once the composer's check completes.
    Pending bool
}
```

It is installed with `WithRegistrationGate` on the server and the embedded
SDK, like the other `With*` seams. The gate can only make sign-up stricter.
It cannot open a tenant whose mode is `invite_only`, because the mode is
checked first.

### Upstream mapping

| Product setting | go-qrdb |
|---|---|
| go-xkms `webauthn.allow_public_registration: false` (default) | mode `invite_only` (default) |
| go-xkms `webauthn.allow_public_registration: true` | mode `open` for that tenant |
| go-quicbaas `self_registration_disabled: true` | mode `invite_only` |
| go-quicbaas self-registration on | mode `open`, with its verification as the `RegistrationGate` |

Each product's own setting is then removed in its repository, with no
compatibility layer. go-xkms keeps WebAuthn credential registration, which
is about factors and is a separate thing.

## Open questions

### Q1. Does "new customers sign up" mean a user in an existing tenant, or a new tenant?

- **A (recommended). User accounts only.** This row covers a new user in an
  existing tenant. go-quicbaas creates a customer's tenant in its own
  onboarding, with its platform credential, then sets that tenant's mode and
  lets the customer's users register. Dependency: none.
- **B. A public tenant-creation verb.** An unauthenticated caller creates a
  new tenant per customer. Dependency: none in a lower project, but a new
  public verb that provisions a tenant.

**Reason:** go-qrdb's tenant Admin cannot create tenants (owner ruling of
2026-09-29; see [Trust boundaries](../multi-tenancy/architecture.md#trust-boundaries),
"A tenant's plan belongs to the platform operator"), and an unauthenticated
tenant-creation verb would be a much larger surface. A keeps tenant creation
with the platform operator, where it is today.

### Q2. Which permission sets the mode?

- **A (recommended). A new `tenants:set_registration`** (self-scoped), plus
  `tenants:set_registration:cross` for platform operators. Dependency: none.
- **B. Reuse `tenants:update`.** Dependency: none.

**Reason:** a separate permission lets ABAC policy and audit name the act of
opening sign-up, rather than folding it into every tenant update.

### Q3. How is a refusal reported?

- **A (recommended). One `ErrRegistrationClosed`** (`PermissionDenied`, REST
  `403`) for an unknown, an inactive and an `invite_only` tenant alike.
  Dependency: none.
- **B. Distinct errors:** `NotFound` for an unknown tenant and
  `FailedPrecondition` for a closed one. Dependency: none.

**Reason:** the caller is unauthenticated. B tells a stranger which tenants
exist; A tells them nothing, as `RedeemInvitation` already does with its one
opaque refusal.

### Q4. How does a gate-pending account become active?

- **A (recommended). Through the existing invitation path.** The account is
  created pending, with no credential bound. Once the composer's check
  completes, it calls `InvitationService.InviteUser` for that username:
  `InviteUser` finds the existing account and, because it is still
  `LifecyclePending` with no certificate binding, re-invites it
  (`InvitationService.reinvite`) with a fresh single-use token. The user then
  activates with `RedeemInvitation`, which binds the credential. Dependency:
  none; both APIs exist today.
- **B. The gate decides synchronously.** `AdmitRegistration` returns only
  admit or deny, and `Pending` is dropped from `RegistrationDecision`.
  Dependency: none.

**Reason:** A supports checks that complete later (email or phone
verification, billing) with no new activation code: the pending account and
its activation reuse what invitations already prove. B is simpler but leaves
a composer with an asynchronous check no way to use the gate.

### Q5. May the system tenant be `open`?

- **A. The node default applies to it as written** (see "Where it is set",
  item 1). Dependency: none.
- **B (recommended). The system tenant is always `invite_only`.**
  `SetTenantRegistrationMode` refuses `open` on it, and the node default never
  applies to it. Dependency: none.

**Reason:** open sign-up into the operator tenant hands strangers accounts
beside the platform operators, even if only with the `user` role. A node
default of `open` set for customer tenants should not reach it by accident.

## Recommendation summary

| Question | Recommended | Reason |
|---|---|---|
| Q1. Users or tenants | A, user accounts only | tenant creation stays with the platform operator |
| Q2. Permission | A, `tenants:set_registration` (+ `:cross`) | ABAC and audit can name opening sign-up |
| Q3. Refusal | A, one `ErrRegistrationClosed` | a stranger learns nothing about which tenants exist |
| Q4. Pending activation | A, re-invite then `RedeemInvitation` | asynchronous checks with no new activation code |
| Q5. System tenant | B, always `invite_only` | no stranger accounts beside the platform operators |

## Tests that would prove it

- Unit tests:
  - mode parsing and validation;
  - effective-mode resolution (tenant value, inherit, node default);
  - `Register` refuses `invite_only`, unknown and suspended tenants with the
    same error;
  - the gate can deny and can make an account pending, but cannot override
    `invite_only`;
  - exactly the `user` role is assigned;
  - the quota is enforced;
  - the audit record is written;
  - if Q5 is ruled B, `SetTenantRegistrationMode` refuses `open` on the
    system tenant, and a node default of `open` leaves it `invite_only`.
- CLI end-to-end on every transport (m651, tier 1). Under `invite_only`,
  `qrdb register` is refused. `qrdb tenant registration set open` then makes
  `qrdb register` succeed, and the new user logs in with the issued
  certificate and holds only the `user` role. `set inherit` returns the tenant to the node default.
  Invitation redemption works the same in both modes.
- A buildgates check that `Register` is in `publicverbs.Table` and is public
  on every transport.

## Ruling

None yet. The owner rules on Q1 to Q5. The Status line and this section
record the ruling, and `docs/identity-rbac/` absorbs the ruled design when
the row is built.
