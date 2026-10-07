# Identity and RBAC — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix — users

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Users()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Create | `POST /users` | `CreateUser` | `user.Create` | `qrdb_user_create` | `CreateUser` | `users:create` |
| Get by ID | `GET /users/{id}` | `GetUser` | `user.Get` | `qrdb_user_get` | `GetUser` | `users:read` |
| Get by username | `GET /users/by-username/{username}` | `GetUserByUsername` | `user.GetByUsername` | `qrdb_user_get_by_username` | `GetUserByUsername` | `users:read` |
| Get by email (canonical login key) | `POST /users/lookup` | `GetUserByEmail` | `user.GetByEmail` | `qrdb_user_get_by_email` | `GetUserByEmail` | `users:read` |
| Get by cert thumbprint | `GET /users/by-cert-thumbprint/{thumbprint}` | `GetUserByCertThumbprint` | `user.GetByCertThumbprint` | `qrdb_user_get_by_cert_thumbprint` | `GetUserByCertThumbprint` | `users:get_by_cert_thumbprint` |
| Add cert binding | `POST /users/{id}/cert-bindings` | `AddCertBinding` | `user.AddCertBinding` | `qrdb_user_add_cert_binding` | `AddCertBinding` | `users:add_cert_binding` |
| Withdraw cert binding | `DELETE /users/{id}/cert-bindings/{thumbprint}` | `RemoveCertBinding` | `user.RemoveCertBinding` | `qrdb_user_remove_cert_binding` | `RemoveCertBinding` | `users:remove_cert_binding` |
| Update | `PUT /users/{id}` | `UpdateUser` | `user.Update` | `qrdb_user_update` | `UpdateUser` | `users:update` |
| Delete | `DELETE /users/{id}` | `DeleteUser` | `user.Delete` | `qrdb_user_delete` | `DeleteUser` | `users:delete` |
| List | `GET /users/list` | `ListUsers` | `user.List` | `qrdb_user_list` | `ListUsers` | `users:list` |
| Enable | `POST /users/{id}/enable` | `EnableUser` | `user.Enable` | `qrdb_user_enable` | `EnableUser` | `users:enable` |
| Disable | `POST /users/{id}/disable` | `DisableUser` | `user.Disable` | `qrdb_user_disable` | `DisableUser` | `users:disable` |

## Capability matrix — invitation self-enrolment

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`transport.InvitationClient`) | `scp` permission |
|---|---|---|---|---|---|---|
| Invite a user | `POST /users/invite` | `InviteUser` | `user.Invite` | `qrdb_user_invite` | `InviteUser` | `users:invite` |
| Redeem an invitation | `POST /invitations/redeem` (public) | `RedeemInvitation` (public method) | `invitation.Redeem` (public frame) | `qrdb_invitation_redeem` on the separate, public `/mcp/enroll` server | `RedeemInvitation` | none — unauthenticated |

Invite is `ScopeSelfStrict`: it acts on the caller's own tenant, same as
every certificate-enrolment verb. Redeem is the one capability in this
document reachable with no credential at all; see
[../identity-rbac/architecture.md#invitation-self-enrolment](architecture.md#invitation-self-enrolment)
for why, and for the per-source/per-tenant throttling and optional key
pinning that compensate for having no identity to rate-limit by.

Both requests take the same fields regardless of transport: `username`,
optional `email`, `name`, `roleIDs` (or `role_ids`) and
`keyPinFingerprint`/`key_pin_fingerprint` for invite; `tenantID`/`tenant_id`,
`token`, `csrPEM`/`csr_pem` (required — the proof of possession) and
optional `certPEM`/`cert_pem` plus `chainPEM`/`chain_pem` for a
bring-your-own certificate, for redeem. A successful invite returns the
token exactly once:

```json
{
  "id": "3",
  "token": "a94e3f1b...",
  "expiresAt": "2026-10-07T12:00:00Z",
  "userID": "9",
  "username": "bob",
  "tenantID": "acme",
  "created": true,
  "keyPinFingerprint": ""
}
```

A successful redemption returns the same `enrolledCertResponse` shape
`POST /certs/issue` and `POST /certs/enroll` do. Every refusal of a redeem
request, whatever the cause, is `401 Unauthorized` with the same message:
see [Status and error mapping](#status-and-error-mapping).

## Capability matrix — roles and permissions

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Roles()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Create role | `POST /roles` | `CreateRole` | `role.Create` | `qrdb_role_create` | `CreateRole` | `roles:create` |
| Get role | `GET /roles/{id}` | `GetRole` | `role.Get` | `qrdb_role_get` | `GetRole` | `roles:read` |
| Get role by name | `GET /roles/by-name?tenant_id=&name=` | `GetRoleByName` | `role.GetByName` | `qrdb_role_get_by_name` | `GetRoleByName` | `roles:read` |
| Update role | `PUT /roles/{id}` | `UpdateRole` | `role.Update` | `qrdb_role_update` | `UpdateRole` | `roles:update` |
| Delete role | `DELETE /roles/{id}` | `DeleteRole` | `role.Delete` | `qrdb_role_delete` | `DeleteRole` | `roles:delete` |
| List roles | `GET /roles/list` | `ListRoles` | `role.List` | `qrdb_role_list` | `ListRoles` | `roles:list` |
| Assign role | `POST /roles/assign` | `AssignRole` | `role.Assign` | `qrdb_role_assign` | `AssignRole` | `roles:assign` |
| Unassign role | `POST /roles/unassign` | `UnassignRole` | `role.Unassign` | `qrdb_role_unassign` | `UnassignRole` | `roles:unassign` |
| List permissions | `GET /permissions` | `ListPermissions` | `role.ListPermissions` | `qrdb_permissions_list` | `Client.Permissions()` | `permissions:list` |
| Resolve role permissions | `POST /roles/resolve-permissions` | `ResolveRolePermissions` | `role.ResolvePermissions` | `qrdb_permissions_resolve` | `ResolvePermissions` | `permissions:resolve` |
| Reconcile built-in roles | `POST /roles/reconcile` | `ReconcileBuiltInRoles` | `role.ReconcileBuiltIn` | `qrdb_role_reconcile_builtin` | `ReconcileBuiltInRoles` | `roles:create` |

The four route, method and tool names above the reconcile row were corrected
against the code in the same change; the document had drifted.

Reconcile carries **`roles:create`**, not a permission of its own. It IS the
seeding operation applied to an existing tenant, and a dedicated verb would be
absent from every role seeded before that verb existed — so nobody would hold
the grant needed to repair that exact absence.

`Client.Permissions()` and `Client.RBAC()` return **nil** on a transport that
does not implement the corresponding provider. Nil-check them; the nil is not
an error value.

## Authentication

**mTLS, passwordless, on every transport.** There is no login endpoint that
takes a password, and no password field anywhere in the user model.

| Certificate field | Maps to |
|---|---|
| Subject CN | `Username` |
| Subject OU | `TenantID` |
| base64url SHA-256 of `SubjectPublicKeyInfo` | `CertBindings[].Thumbprint` |

**The tenant comes from the certificate, never from a request field.** That is
the root of tenant isolation in every other feature.

`GET /users/by-cert-thumbprint/{thumbprint}` is the lookup that turns a
presented certificate into a user. Because the thumbprint is over the public
key, a renewed certificate for the same key resolves to the same user.

**The lookup searches the context tenant only.** There is no cross-tenant
thumbprint index. A composing host that resolves a verified certificate before
any tenant is bound (a public route, where the middleware authenticates
nothing) derives the tenant with the same rule the mTLS authenticator uses,
binds it, and looks the thumbprint up within it:

```go
tenantID, _, err := sdk.TenantFromClientCert(cert, trustDomain)
if err != nil {
	// foreign trust domain, malformed SPIFFE SAN, nil certificate: refuse
}
ctx = sdk.ContextWithTenant(ctx, sdk.NormalizeTenantID(tenantID))
u, err := client.Users().GetUserByCertThumbprint(ctx, sdk.ComputeSPKIThumbprint(cert))
```

`sdk.TenantFromClientCert` is `auth.TenantFromClientCert`, the one
implementation `MTLSAuthenticator.TenantFromCert` delegates to: the SPIFFE
tenant SAN under the pinned trust domain, else the first Subject OU, else no
tenant (which `NormalizeTenantID` folds onto the system tenant, as the
middleware does). Every URI SAN is examined, not only the first. The
certificate is refused, and never falls back to the OU, when:

- any SPIFFE SAN is under a trust domain other than the pinned one;
- a SPIFFE SAN is malformed;
- its tenant SANs name more than one distinct tenant
  (`sdk.SpiffeAmbiguousTenantError`).

Every refusal is a `*sdk.ClientCertificateTenantError` that matches
`sdk.ErrInvalidClientCertificate` and carries the cause. The mTLS
authenticator applies the same rule, so such a certificate cannot log in
either.

**Pin the trust domain.** `trustDomain == ""` accepts a SPIFFE SAN from any
trust domain and exists for tests. A composer passes the trust domain its CA
issues under, the same value it configures the node with.

**Once an account has at least one binding, only a certificate matching one of
them authenticates as it** — the CN and OU select the account, the thumbprint
proves the holder. A certificate naming an enrolled account that matches none
of its bindings is refused with **401**, and the message carries the presented
thumbprint so an operator can enrol it if the refusal was a missed enrolment.
An account with NO bindings is unchanged: it authenticates on the CN and OU
alone. See [architecture.md](architecture.md#what-a-certificate-actually-claims).

`POST /users/{id}/cert-bindings` is therefore an authentication-affecting
write, not bookkeeping. It is **additive** — enrol a new key alongside the old
one and both work, which is how a key rotation happens without an interruption.

A thumbprint already bound to a **different** user in the same tenant is
refused with 409 / `ALREADY_EXISTS`: a thumbprint resolves to exactly one
identity, and silently rebinding it would let the caller take over that user's
mTLS identity. The message names the user that holds the thumbprint, which is
the fact an operator investigating a failed enrolment needs; QUIC and MCP
answer with their `AlreadyExists` equivalents. Enrolling a thumbprint the
**same** account already holds is not a conflict.

### Permitted authentication methods

A non-empty `authMethods` policy is enforced at the authentication gate on
every transport: an account that authenticated by a method outside it is
refused with **403** / `PERMISSION_DENIED` (`AuthMethodNotPermittedError`). The
tokens are `mtls`, `piv` and `webauthn`, exported as `sdk.MethodMTLS`,
`sdk.MethodPIV` and `sdk.MethodWebAuthn` (`sdk.BuiltinAuthMethods()` lists
them). A node's vocabulary also holds the name of every authenticator in ITS
chain, so a node that plugs in, say, an `oauth` authenticator can write
`["mtls","oauth"]` — and a second node in the same process that does not,
cannot. A create or update naming a token outside that live set is
refused with **400** / `INVALID_ARGUMENT` (`UnknownAuthMethodError`, a
validation error). A policy stored before that rule reads back and keeps
working unchanged, and an update may keep an entry it already holds.

A composing host that runs its own login ceremony enforces the same policy
with `sdk.CheckAuthMethod(user, method)`, which returns nil or
`*sdk.AuthMethodNotPermittedError`. It is the function the gate itself runs, so
the host cannot be more or less permissive than the transports.

### Withdrawing a binding

`DELETE /users/{id}/cert-bindings/{thumbprint}` withdraws one binding. It
removes the record entry and both index entries, so the thumbprint resolves
nothing and a certificate issued for that key no longer authenticates as the
account. It returns the user as it now stands.

**Withdrawal is not revocation, and neither replaces the other.** They act on
different objects and the two are deliberately NOT coupled:

| | Acts on | Effect |
|---|---|---|
| `certs:revoke` | one **certificate**, by serial | that certificate is refused |
| `users:remove_cert_binding` | one **key**, by SPKI thumbprint | every certificate for that key stops authenticating as this account |

A binding is over the `SubjectPublicKeyInfo`, so it outlives every certificate
issued for that key — a re-issue for the same key authenticates again until the
binding is withdrawn. Conversely, revoking a certificate during a routine
rotation must not silently unenrol the key being rotated, which is why revoking
does not drop a matching binding. **Withdrawing the binding is the remediation
for a compromised key; revoking the certificate is not.**

**Withdrawing an account's LAST binding WIDENS it** under the default
`auth.mtls_binding_policy: enrolled`. An empty binding set states no
restriction, so the account returns to admitting any certificate whose CN and
OU name it — the state every account was in before enrolment. That is a real
posture change and it is audited as `remove_cert_binding` with
`last_binding_removed: true`. **Enrol the replacement first, then withdraw.**

Under `required` the store refuses **every change that would leave the
bootstrap admin (`admin` in the `system` tenant) unable to log in by
certificate**, with `user.BootstrapAdminLockoutError` naming the `Reason`:

| Change | `Reason` |
|---|---|
| Withdrawing its last live binding — through `DELETE …/cert-bindings/{thumbprint}`, or a `PUT /users/{id}` that replaces its bindings with none live | `no_live_binding` |
| `DELETE /users/{id}` | `delete` |
| `POST /users/{id}/disable`, or a lifecycle change to `suspended`, `locked`, `disabled` or `deleting` | `lifecycle` |
| A permitted-method list that omits `mtls` | `mtls_not_permitted` |
| A new `username` | `rename` |

It is **409** on REST, `FAILED_PRECONDITION` on gRPC (and the Unix socket) and
QUIC, `CodeFailedPrecondition` on MCP, and the typed error itself on the
embedded transport. Each is refused only when it would break a requirement the
admin currently meets, so a change that leaves an already-broken one broken —
or repairs it — goes through. The check runs inside the guarded write, so two
concurrent changes that are each safe alone (withdrawing the admin's last two
keys, one each) cannot both succeed. If the admin is locked out anyway — its
last key expired — the way back is operational: set
`auth.mtls_binding_policy: enrolled`, restart, enrol a key, then return to
`required`.

`POST /users/{id}/cert-bindings` refuses a `not_after_unix` that is not in the
future with `user.CertBindingExpiredError` — **400** on REST,
`INVALID_ARGUMENT` on gRPC and QUIC, `CodeInvalidArgument` on MCP — for every
account and under either policy. Such a binding could never authenticate, and
because enrolment replaces a binding with the same thumbprint, accepting one
for a key already bound would silently withdraw that key.

A withdrawal naming a thumbprint the account does not hold is **404**, not a
vacuous success: an operator who mistypes a thumbprint must not be told a live
key was withdrawn. A 404 from this endpoint means the user or the binding is
absent; the message says which.

The verb is narrow — one thumbprint, named in the path — rather than a
whole-set replacement, because a replacement carries the caller's snapshot of
the rest of the set. Committing that snapshot is how a concurrent enrolment
resurrects a binding that was just withdrawn. `add` and `withdraw` each say what
to do to the set without depending on what the caller last saw it contain, so
the server can re-apply them against whatever committed first.

## Schemas

`Role`:

```json
{ "id": "…", "name": "admin", "tenant_id": "acme",
  "permissions": ["keys:create", "sdlc:bind"],
  "system": true,
  "retired_permissions": ["sdlc:register"],
  "created_at": "…", "updated_at": "…" }
```

`retired_permissions` is **derived on read** from the server's live permission
registry and is never stored: it lists the members of `permissions` that this
build no longer defines. It is drift between what the tenant was seeded with and
what the server now grants — visible on an ordinary listing, **before**
`POST /roles/reconcile` is run to repair it. It is absent when there is none,
and a whole-segment wildcard (`keys:*`) never appears in it, because the
authorizer expands wildcards at authorization time and there is no registry
entry for one to lose.

`ReconcileRolesResponse` — the body of `POST /roles/reconcile`:

```json
{ "tenant_id": "acme",
  "roles": [
    { "role_id": "…", "name": "admin",  "created": false,
      "added": ["sdlc:bind"], "removed": ["sdlc:register"] },
    { "role_id": "…", "name": "operator", "created": true,
      "added": ["orchestrator:task_block"] }
  ] }
```

Only **changed** roles are reported, so `"roles"` absent or empty means the
tenant already agreed with the build and **nothing was written**. Reconciliation
is idempotent: a second call reports no roles and performs zero store writes.
A `created` entry is a built-in the tenant did not hold at all; `added` then
holds its whole permission set. Roles whose names are not built-in names are
never read and never written — a custom role survives untouched.

The request body is `{"tenant_id": "…"}`, and an **empty body** means the
caller's own effective tenant. A normal tenant caller may converge only its own
tenant; naming another returns the cross-tenant refusal.

`User`:

```json
{ "id": "…", "username": "alice", "email": "alice@acme.example",
  "tenantID": "acme", "roleIDs": ["…"],
  "lifecycle": "active", "system": false,
  "authMethods": ["webauthn", "piv"], "lastAuth": "…",
  "roleBindings": [ { "roleID": "…", "tenantID": "acme" } ],
  "attributes": { "department": "eng" },
  "certBindings": [ { "thumbprint": "…", "kind": "spki_thumbprint", "notAfter": "…" } ],
  "factors": [ { "id": "…", "factorName": "fido2", "createdAt": "…" } ],
  "mfaEnabled": true, "mfaMethods": ["fido2"], "mfaEnrolledAt": "…",
  "createdAt": "…", "updatedAt": "…" }
```

(Field names above are REST/QUIC/MCP camelCase; gRPC and the SDK spell the
same fields snake_case — `tenant_id`, `role_ids`, `auth_methods`,
`last_auth`/`last_auth_unix`, `role_bindings`, `cert_bindings`, `factor_name`,
`created_at`, `mfa_enabled`, `mfa_methods`, `mfa_enrolled_at`.)

`system` marks the **single, cluster-wide bootstrap admin** created by
first-boot — the one account a cluster can never be locked out of by an
accidental delete. `DeleteUser` refuses it with `SystemUserImmutableError`
(403 / `PERMISSION_DENIED`). **No request on any transport can set this
field**: `CreateUserRequest` and `UpdateUserProtoRequest`/the REST and QUIC
wire requests carry no `system` input at all, on either create or update —
only the internal first-boot orchestration produces one, in-process, never
over a transport. A per-tenant admin created by tenant provisioning does
**not** carry this flag and remains deletable when its tenant is
deprovisioned.

`factors` lists the account's enrolled auth-factor **metadata only** — `id`,
`factorName` and `createdAt`. The opaque `data` blob a registered
`pkg/authfactors.Factor` interprets during verification **never crosses any
transport, on read or write**; enrollment and rotation stay their own
factor-specific verbs (`POST /v1/users/factors`, `AuthFactorService`, …),
never `CreateUser` or `UpdateUser`. `factors` is **read-only**: no create or
update request on any transport can set it.

`mfaEnabled` / `mfaMethods` / `mfaEnrolledAt` are **DERIVED from `factors`,
never stored**: `mfaEnabled` is true iff the account has at least one
enrolled factor, `mfaMethods` lists the distinct factor names in
first-enrollment order, and `mfaEnrolledAt` is the earliest enrollment among
them. An enrolled factor is a fact about what the account **has**, not a
policy statement that MFA is **required** for it — a requirement bit, if one
is ever needed, belongs in policy, not on this record. These three fields are
also read-only.

`certBindings` lists the account's enrolled mTLS keys on **every transport**
(`cert_bindings` in the REST/SDK spelling), in enrolment order and **in full —
an expired binding is listed with its `notAfter`** rather than hidden. It is
listed because it still holds the account in the enforcing posture: an account
with any binding admits only a matching certificate, so a reader who could not
see the expired one could not explain why the account refuses a certificate.

> **Breaking change (MCP only).** MCP previously `json.Marshal`ed the domain
> `*user.User` record directly, so `certBindings[].kind` arrived as
> pkg/user's internal ordinal (`0`/`1`/`2`) and a non-expiring binding's
> `notAfter` was the zero `time.Time` (`0001-01-01T00:00:00Z`) rather than
> omitted. That was the same defect a prior fix had already corrected on
> every other transport; MCP now builds the identical string-labeled, omit-when-absent
> projection REST, gRPC, QUIC, unix and embedded already use. An MCP client
> written against the old, leaked shape — decoding `kind` as a number — will
> fail to decode a user with certificate bindings against this server version,
> and the reverse during a rolling upgrade. There is no compatibility shim: the
> old shape was an accident of how the handler was written, not a documented
> contract, and this repository ships no wire-compatibility guarantee
> pre-release.

`kind` is `spki_thumbprint` (canonical), `full_cert_fingerprint` or
`piv_attested`; `notAfter` is absent for a binding that does not expire on its
own. The thumbprint is a hash of public material — listing it discloses
nothing — and is read back byte-identical to the one enrolled, so it can be
passed straight to the withdrawal verb. It is part of the record, so reading
it needs what reading the record needs: `users:read` on a single user,
`users:list` on a listing.

`lifecycle` is the **canonical and only account state**: `pending` · `active` ·
`suspended` · `locked` · `disabled` · `deleting`. Only `active` authenticates;
an absent or unrecognized state fails closed.

> **Breaking change.** The `enabled` boolean has been removed from the `User`
> schema on every transport (REST, gRPC, QUIC, MCP, Unix socket and the
> embedded SDK). It was a derived projection of `lifecycle`
> (`lifecycle == "active"`); read `lifecycle` instead. In the gRPC
> `UserResponse`, field number 5 and the name `enabled` are reserved.

Transitions are checked against a fixed table. `pending → active` is the only
activation; `disabled` and `deleting` are reachable from every non-terminal
state; **nothing transitions out of `deleting` to another state**. Re-asserting
the state a record already holds is a no-op, not an error, so a retried request
— including a retried `deleting` on a partially completed deletion — is safe.
A refused move returns a `LifecycleTransitionError`; an undefined or
not-permitted-here state returns an `InvalidLifecycleError` naming what is
allowed. Only `pending`, `active` and `disabled` may be created directly.

`EnableUser` and `DisableUser` are the transitions to `active` and `disabled`;
they are refused on an account already marked `deleting`.

> **Breaking change.** `groupIDs` (`group_ids` on REST, gRPC and MCP) has been
> removed from the `User` schema and from the create and update requests on
> every transport, together with gRPC's `has_group_ids` flag and MCP's
> `clear_group_ids` argument. No qrdb authorization path read it: access
> resolves user → role bindings → roles → permissions. A layer that needs group
> membership stores it itself. In gRPC the removed field numbers and names are
> reserved (`CreateUserRequest` 7; `UpdateUserProtoRequest` 6 and 7;
> `UserResponse` 12). On REST and QUIC an unknown key in a request body is
> ignored, so a client still sending `group_ids` is not refused — the value is
> simply not stored.

`authMethods` is a **policy** statement of what the account may present
(`webauthn`, `piv`, …), distinct from `factors`, which records what it has
actually enrolled. Trimmed, de-duplicated and **lowercased** — these are
protocol names from the method vocabulary (the built-in `mtls`, `piv`,
`webauthn`, plus the name of every authenticator in the configured chain).
Empty means no restriction.

It is **enforced**, not merely stored: an authentication that succeeds by a
method the policy does not list is refused with **403** (gRPC
`PermissionDenied`) — the credential is valid and retrying the same method will
never work, so 401 would be the wrong instruction. The check applies only where
the method is observable to qrdb (its own authenticator chain); a forwarded
on-behalf assertion and a capability module's session token are enforced by the
layer that ran that ceremony.

`lastAuth` is a **high-water mark**: a value at or before the one already
recorded is ignored rather than written, so two logins that overlap in flight
cannot move an account's last-seen time backwards. A request that sets **only**
`lastAuth` takes a merge path that RETRIES a lost conditional commit instead of
returning a conflict — the merge is commutative, so there is nothing stale for
the caller to reconcile. Combined with any other field it is an ordinary update
again.

### Setting the identity fields

`authMethods` distinguishes three states on an update, and every transport
carries the distinction in its own idiom:

| Intent | REST / QUIC / MCP-response JSON | gRPC | MCP tool arguments |
|---|---|---|---|
| Leave alone | key absent, or `null` | `has_auth_methods` unset | argument absent |
| Clear | `[]` | `has_auth_methods: true` with no values | `clear_auth_methods: true` |
| Replace | `["a","b"]` | `has_auth_methods: true` + values | `auth_methods: "[\"a\",\"b\"]"` |

The flag and the clear-argument exist because proto3 cannot distinguish an
absent repeated field from an empty one, and a flat tool argument cannot either.
Without them a restriction could be set but never **lifted** — an empty list
would read as "no change" and the clear would report success while doing
nothing.

The MCP list argument is a **JSON array**, never comma-separated: a CSV
encoding splits a value containing a comma in two and drops blank elements the
store would have refused, writing something other than what the caller asked
for while reporting success. A malformed list is an error, not a silent "leave
alone".

`lastAuth` is RFC 3339 on the JSON transports and MCP, and
`last_auth_unix` (seconds since the epoch, UTC) on gRPC, where `0` means the
account has never authenticated. On the JSON transports "never" is the **absent
key**, never a zero timestamp — `1970` is not a login.

`certBindings[].kind`: `0` SPKI thumbprint (**canonical default**) ·
`1` full-certificate fingerprint (exact pinning) · `2` PIV-attested (gRPC
carries the ordinal; every other transport carries the string label shown in
the schema above).

`roleBindings` and `attributes` follow the identical leave-alone / clear /
replace contract `authMethods` does, on `UpdateUser` only — neither is
settable on `CreateUser`, matching the domain request shape:

| Intent | REST / QUIC JSON | gRPC | MCP tool arguments |
|---|---|---|---|
| Leave alone | key absent, or `null` | `has_role_bindings`/`has_attributes` unset | argument absent |
| Clear | `[]` / `{}` | flag `true` with no entries | `clear_role_bindings`/`clear_attributes: true` |
| Replace | `[{...}]` / `{"k":"v"}` | flag `true` + entries | `role_bindings`/`attributes: "<json>"` |

`roleBindings` are **additive** to `roleIDs`; effective permissions are the
union. A binding's `tenantID` must be the user's own tenant — for a
system-tenant user too — and a binding naming any other tenant is refused.
A tenant's own administrators act only in their tenant; a system-tenant
Admin can still administer a tenant's users and roles until strict tenant
isolation lands (see
[architecture.md](architecture.md#three-authorization-inputs-on-one-user)). `attributes` are ABAC inputs in their own
namespace — `subject.user_attrs.<key>` in a policy condition, `user_attrs.<key>`
as a match key — so an attribute can never satisfy a policy written against a
certificate- or token-derived value of the same name (see
[../auth/abac.md](../auth/abac.md#tenant-scoping)).

Writing `roleIDs` (on `CreateUser` or `UpdateUser`), or `roleBindings`/
`attributes` (on `UpdateUser` only — neither is settable at create time)
requires `roles:assign`, the same permission the RBAC model already defines
for granting a role directly, IN ADDITION to the route's own
`users:create`/`users:update` permission, in the tenant the user belongs to.
Every referenced role — in `roleIDs` or in a binding — must exist and belong
to that tenant itself. Each tenant
has its own copy of the built-in roles, so assign that tenant's copy: the
system tenant's built-ins are not valid in other tenants. A role that does
not exist, or belongs to another tenant, is refused with a client error before anything
is written. The role check runs whether or not RBAC is enabled; only the
`roles:assign` check depends on it. See
[engineering.md](engineering.md#authorization-model) for the full mechanism.

`factors` is **read-only on every transport, on both `CreateUser` and
`UpdateUser`**: no wire request — REST, gRPC, QUIC, MCP or the SDK's
`transport.CreateUserRequest`/`UpdateUserRequest` — declares a `factors`
input. Enrolling or rotating a factor is its own verb (the `AuthFactorService`
/ `/v1/users/factors` surface), never a field on the user record's own
create/update request. The `data` blob is never readable either; only `id`,
`factorName` and `createdAt` are ever returned.

Role creation takes a name, a tenant and a permission list of `resource:action`
strings, each validated against the registry.

`ListPermissions` returns the registry, grouped into categories with display
names for UI rendering.

The embedded SDK's `RBAC().ListPermissions(identity)` is the per-identity
view: the union of what the identity's roles grant, each entry carrying its
declared scope (`self`, `cross` or `tenant-only`) so a UI can filter to what
the caller can do where it is. A whole-segment wildcard grant (`objstore:*`,
`*:read`, `*:*`) is expanded to the registered permissions it matches, each
with its own scope, and the result is deduplicated and sorted by name — a
wildcard is how the authorizer matches a name, not a permission with a scope
of its own, and the listing never contains one.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| User or role not found | 404 | `NOT_FOUND` |
| Duplicate username within a tenant | 409 | `ALREADY_EXISTS` |
| Duplicate email within a tenant | 409 | `ALREADY_EXISTS` |
| Thumbprint already bound to another user in the tenant (the message names that user) | 409 | `ALREADY_EXISTS` |
| Withdrawing a binding the account does not hold | 404 | `NOT_FOUND` |
| Empty email without `without_email_login_key` | 400 | `INVALID_ARGUMENT` |
| Lost a concurrent update (re-read and retry) | 409 | `ABORTED` |
| Commit outcome unknown (reconcile, then retry) | 503 | `UNAVAILABLE` |
| Unregistered permission string | 400 | `INVALID_ARGUMENT` |
| Deleting a `system` user or a built-in role | 403 | `PERMISSION_DENIED` |
| Certificate with no matching binding | 401 | `UNAUTHENTICATED` |
| A request authenticated with a holder (officer or custodian) certificate must be forwarded to the leader, and this node cannot sign the on-behalf assertion (`HolderForwardUnsignedError`) | 502 | — (REST leader forwarding only) |
| Disabled user | 401 | `UNAUTHENTICATED` |
| Missing permission | 403 | `PERMISSION_DENIED` |
| Role-assignment write names a role that does not exist, or belongs to another tenant | 400 | `INVALID_ARGUMENT` |
| `roleBindings` entry naming a tenant other than the user's own, system-tenant users included (`user.RoleBindingTenantError`) | 400 | `INVALID_ARGUMENT` |
| Role-assignment write names a role but the node has no role service | 501 | `UNIMPLEMENTED` |
| The user's auth factors could not be listed while building the response | 503 | `UNAVAILABLE` |
| A user/role request whose body (REST) or payload (QUIC) does not decode | 400 | `INVALID_ARGUMENT` |
| A REST create, lookup or role-assignment body larger than 1 MiB | 413 | — |
| A create, list or name lookup by a caller outside the system tenant naming a tenant other than its own (`tenant.CrossTenantDeniedError`) | 403 | `PERMISSION_DENIED` |
| An invitation token cannot be redeemed — unknown, wrong tenant, expired, already used, or for an account no longer pending — whatever the specific cause | 401 | `UNAUTHENTICATED` |
| Invite or redeem request missing a required field | 400 | `INVALID_ARGUMENT` |
| Redeemed key already bound to an account of the tenant | 409 | `ALREADY_EXISTS` |
| Re-inviting an account that already holds a credential or is not pending | 412 | `FAILED_PRECONDITION` |

## Pagination

`ListUsers` and `ListRoles` are **page-based** (`page`, `page_size`).
`ListPermissions` returns the whole registry and is not paginated.

## Versioning

Identity REST paths are unversioned (`/users/...`, `/roles/...`,
`/permissions/...`). Nothing here is deprecated; removed fields are removed
outright rather than deprecated (see the breaking-change notes under
[Schemas](#schemas)).

## The email login key

`Email` is the **canonical login key**: unique within a tenant, and what every
layer above qrdb resolves a principal by — the WebAuthn credential handle, the
session subject, the CLI's user identifier.

- **Tenant-scoped, not global.** The same person may hold an account in two
  tenants, and every login path supplies the tenant alongside the handle.
- **Normalized** by `sdk.NormalizeEmail` (lowercase, trimmed) on both the stored
  record and its index key. Comparison is case-insensitive, so `Op@x.com` and
  `op@x.com` are one account rather than two.
- **Required on create** unless the caller sets `without_email_login_key`, which
  creates a certificate-only or bootstrap identity that cannot be resolved by
  email. The omission must be declared; it is never a default.

### Why the lookup is a POST

`POST /users/lookup` takes `{"email": "...", "tenant_id": "..."}` in the body
rather than `GET /users/by-email/{email}`. Email is the canonical login
identifier, and a path parameter is captured by reverse-proxy access logs, load
balancers, tracing middleware and browser history on every successful lookup.
The gRPC, QUIC and MCP surfaces carry it in the payload; REST does too.

## Index consistency

Secondary indexes (username, email, certificate thumbprint) are maintained with
atomic claims rather than read-then-write:

- A handle is **claimed** with `PutIfAbsent` and **released** with
  `CompareAndDelete` against the owning id, so a claim cannot be taken twice and
  a release cannot evict an entry another account has since taken.
- `Update`, `Enable`, `Disable` and `Delete` **commit conditionally** against the
  record version they read. Losing yields `ConcurrentUpdateError` — nothing is
  wrong; re-read and retry.
- A store error from a conditional commit is **ambiguous**, not a failure: the
  record is read back to decide. Only an inconclusive read yields
  `CommitOutcomeUnknownError`, which asks the caller to reconcile before
  retrying rather than assuming either outcome.
- Compensation undoes only what its own invocation did, and a losing update
  restores exactly the handles the winning record still relies on.

A store that cannot claim atomically is refused (`ErrStoreNotAtomic`) rather
than silently degraded to a check-then-write, which would be a uniqueness
guarantee that is not one.
