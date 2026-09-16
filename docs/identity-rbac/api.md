# Identity and RBAC — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
| List | `GET /users` | `ListUsers` | `user.List` | `qrdb_user_list` | `ListUsers` | `users:list` |
| Enable | `POST /users/{id}/enable` | `EnableUser` | `user.Enable` | `qrdb_user_enable` | `EnableUser` | `users:enable` |
| Disable | `POST /users/{id}/disable` | `DisableUser` | `user.Disable` | `qrdb_user_disable` | `DisableUser` | `users:disable` |

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

**Withdrawing an account's LAST binding WIDENS it.** An empty binding set
states no restriction, so the account returns to admitting any certificate
whose CN and OU name it — the state every account was in before enrolment. That
is a real posture change and it is audited as `remove_cert_binding` with
`last_binding_removed: true`. **Enrol the replacement first, then withdraw.**

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
  "tenantID": "acme", "roleIDs": ["…"], "groupIDs": ["platform"],
  "lifecycle": "active", "enabled": true, "system": false,
  "authMethods": ["webauthn", "piv"], "lastAuth": "…",
  "roleBindings": [ { "roleID": "…", "tenantID": "acme" } ],
  "attributes": { "department": "eng" },
  "certBindings": [ { "thumbprint": "…", "kind": "spki_thumbprint", "notAfter": "…" } ],
  "factors": [ { "…": "opaque" } ],
  "createdAt": "…", "updatedAt": "…" }
```

`certBindings` lists the account's enrolled mTLS keys on **every transport**
(`cert_bindings` in the REST/SDK spelling), in enrolment order and **in full —
an expired binding is listed with its `notAfter`** rather than hidden. It is
listed because it still holds the account in the enforcing posture: an account
with any binding admits only a matching certificate, so a reader who could not
see the expired one could not explain why the account refuses a certificate.
`kind` is `spki_thumbprint` (canonical), `full_cert_fingerprint` or
`piv_attested`; `notAfter` is absent for a binding that does not expire on its
own. The thumbprint is a hash of public material — listing it discloses
nothing — and is read back byte-identical to the one enrolled, so it can be
passed straight to the withdrawal verb. It is part of the record, so reading
it needs what reading the record needs: `users:read` on a single user,
`users:list` on a listing.

`lifecycle` is the **canonical account state**: `pending` · `active` ·
`suspended` · `locked` · `disabled` · `deleting`. `enabled` is a **derived
projection** of it — exactly `lifecycle == "active"` — kept because it is the
field existing clients read. It never lies, but it collapses every non-active
state into one `false`, so a client that needs to tell an operator hold from an
automated lockout reads `lifecycle`. There is no request field that writes
`enabled`: a second writable source of truth is how a record ends up claiming to
be locked and active at once.

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

`groupIDs` are **opaque membership identifiers** — qrdb stores the membership
edge and does not own a Group entity, exactly as `roleIDs` holds identifiers
without the user record owning the roles. Trimmed and de-duplicated,
**case-preserving**, because a group id is a minted token.

`authMethods` is a **policy** statement of what the account may present
(`webauthn`, `piv`, …), distinct from `factors`, which records what it has
actually enrolled. Trimmed, de-duplicated and **lowercased** — these are
protocol names from a closed vocabulary. Empty means no restriction.

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

`groupIDs` and `authMethods` distinguish three states on an update, and every
transport carries the distinction in its own idiom:

| Intent | REST / QUIC / MCP-response JSON | gRPC | MCP tool arguments |
|---|---|---|---|
| Leave alone | key absent, or `null` | `has_group_ids` unset | argument absent |
| Clear | `[]` | `has_group_ids: true` with no values | `clear_group_ids: true` |
| Replace | `["a","b"]` | `has_group_ids: true` + values | `group_ids: "[\"a\",\"b\"]"` |

The flags and the clear-argument exist because proto3 cannot distinguish an
absent repeated field from an empty one, and a flat tool argument cannot either.
Without them a membership could be granted but never **revoked** — an empty list
would read as "no change" and the revocation would report success while doing
nothing.

The MCP list arguments are **JSON arrays**, never comma-separated. A group id is
an opaque minted token, so a comma is a character it may legitimately contain: a
CSV encoding splits such an id into two memberships and drops blank elements the
store would have refused, writing a different authorization graph than the
caller asked for while reporting success. A malformed list is an error, not a
silent "leave alone".

`lastAuth` is RFC 3339 on the JSON transports and MCP, and
`last_auth_unix` (seconds since the epoch, UTC) on gRPC, where `0` means the
account has never authenticated. On the JSON transports "never" is the **absent
key**, never a zero timestamp — `1970` is not a login.

`certBindings[].kind`: `0` SPKI thumbprint (**canonical default**) ·
`1` full-certificate fingerprint (exact pinning) · `2` PIV-attested.

`roleBindings` are **additive** to `roleIDs`; effective permissions are the
union. `attributes` are the ABAC inputs. `factors` are **opaque** — qrdb stores
the data and never interprets it.

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
| Disabled user | 401 | `UNAUTHENTICATED` |
| Missing permission | 403 | `PERMISSION_DENIED` |

## Pagination

`ListUsers` and `ListRoles` are **page-based** (`page`, `page_size`).
`ListPermissions` returns the whole registry and is not paginated.

## Versioning

Identity REST paths are unversioned (`/users/...`, `/roles/...`,
`/permissions/...`). Nothing here is deprecated.

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
