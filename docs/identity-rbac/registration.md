# Registration policy (design note)

Status: proposed, awaiting approval of the API shape. SPEC row:
`registration-policy` in [../SPEC.md](../SPEC.md).

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
   documented in [../12-configuration.md](../12-configuration.md). It applies to
   every tenant without its own value, the system tenant included.
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
   tenant Admin may set its own tenant (the "admin is powerful" ruling). The
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
4. Set the credential in the same way that `RedeemInvitation` activates an
   account, with a password or a certificate binding. Registering reuses the
   activation code (`users.ActivateWithCertBinding` and the password path),
   so the credential rules of both flows cannot drift apart.
5. Write an audit record: `user.registered` with the tenant, the account and
   the transport, in the tenant's trail.

The tenant's user quota (`quota.MaxUsers`) applies, so `open` cannot grow a
tenant past its quota. `Register` is a pre-auth verb. It gets the 4 MiB
pre-auth request cap, and its `publicverbs.Table` entry carries a `RateLimit`
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
| CLI | `qrdb register --tenant T --username U (--password-stdin \| --cert F)` |

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

## Out of scope (question for the owner)

The SaaS case "new customers sign up" could mean one of two things:

- a new user in an existing tenant, which this row covers;
- a new **tenant** per customer.

go-qrdb's tenant Admin cannot create tenants (ruling: tenant admin scope),
and an unauthenticated tenant-creation verb would be a much larger surface.

**Recommendation:** this row covers user accounts only. go-quicbaas keeps
creating a customer's tenant in its own onboarding, with its platform
credential. It then sets that tenant's mode and lets the customer's users
register.

## Tests (test-first)

- Unit tests:
  - mode parsing and validation;
  - effective-mode resolution (tenant value, inherit, node default);
  - `Register` refuses `invite_only`, unknown and suspended tenants with the
    same error;
  - the gate can deny and can make an account pending, but cannot override
    `invite_only`;
  - exactly the `user` role is assigned;
  - the quota is enforced;
  - the audit record is written.
- CLI end-to-end on every transport (m651, tier 1). Under `invite_only`,
  `qrdb register` is refused. `qrdb tenant registration set open` then makes
  `qrdb register` succeed and the new user able to log in with only the
  `user` role. `set inherit` returns the tenant to the node default.
  Invitation redemption works the same in both modes.
- A buildgates check that `Register` is in `publicverbs.Table` and is public
  on every transport.
