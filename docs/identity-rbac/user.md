# Identity and RBAC — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-09
> Conflicts with this document are defects. Fix them here first.

Who someone is, and what they may do. There are no passwords: a certificate is
the identity, and roles carry the permissions.

## Prerequisites

A reachable server, client certificates, and the `users:*` / `roles:*`
permissions.

## How identity works

Your certificate carries everything:

| Certificate field | Becomes |
|---|---|
| CN | your username |
| OU | your tenant |
| the public key's SHA-256 | the binding the server matches you by |

Separately from the certificate, your account carries an **email login key**.
That is the identifier everything above the database resolves you by — the
passkey you register, the session you get, the address you type into a sign-in
form. It is unique within your tenant and compared case-insensitively, so
`Alice@acme.com` and `alice@acme.com` are the same account.

Service identities that authenticate only by certificate can be created without
one; they simply cannot be looked up by email.

Because the binding is over the **public key**, renewing your certificate with
the same key keeps you the same user. Rotate the key and you need a new
binding.

## Enrolling somebody, end to end

Run as an Admin of the tenant the user belongs to (here `acme`). Under the
default `auth.mtls_binding_policy: required`, issuing a certificate is not
enough on its own — alice cannot log in until her account also holds a
binding for that certificate's key (step 5 below). Skip it and her
certificate authenticates to nobody.

```bash
# 1. Create the user record — the email is the login key
qrdb users create --username alice --email alice@acme.com --tenant-id acme

# 2. Issue a certificate. The key is generated on this machine (or pass
#    --csr with a CSR alice made herself); the certificate is bound to
#    alice's account in the same call.
qrdb ca issue --username alice --out ./alice

# 3. Find the IDs (both assign flags take IDs, not names)
ROLE=$(qrdb roles get-by-name Operator --output json | jq -r .id)
USER=$(qrdb users list --tenant-id acme --output json \
       | jq -r '.[] | select(.username=="alice") | .id')

# 4. Give them a role
qrdb roles assign --user "$USER" --role "$ROLE"

# 5. Enrol her certificate's key -- REQUIRED before she can log in.
#    No CLI verb exists for this yet; every transport exposes AddCertBinding,
#    REST shown here (see api.md). The thumbprint is the base64url SHA-256 of
#    the certificate's SubjectPublicKeyInfo (user.ComputeSPKIThumbprint).
THUMB=$(openssl x509 -in ./alice/alice-cert.pem -noout -pubkey \
        | openssl pkey -pubin -outform DER \
        | openssl dgst -sha256 -binary | base64 | tr -d '=' | tr '/+' '_-')
curl -X POST "https://db.example.com/v1/users/$USER/cert-bindings" \
     --cert admin-cert.pem --key admin-key.pem --cacert ca.pem \
     -d "{\"thumbprint\": \"$THUMB\"}"
```

The account must exist before step 2: a certificate is only ever issued for an
existing account of the issuer's own tenant, which is also why `ca issue` takes
no tenant. A user whose certificate comes from their organisation's own CA is
enrolled with `qrdb ca enroll` instead
([../trust-anchors/user.md](../trust-anchors/user.md)).

## Inviting somebody instead

The walkthrough above has the Admin sign alice's CSR directly — the Admin's
own credential touches the act of issuing alice's certificate. An invitation
avoids that: the Admin creates alice's account `pending` and hands her a
one-time token out of band (chat, email, however you reach her); alice
redeems it herself, with a key nobody else — the Admin included — ever
holds.

```bash
# 1. Invite alice into your own tenant. --roles is optional; an invitation
#    may not target the Security Officer role, or the Owner role unless you
#    hold Owner yourself -- the same rule direct issuance (step 2 above)
#    follows. The token is returned exactly once: save it now.
qrdb users invite --username alice --email alice@acme.com --roles "$ROLE"
# => Token: a94e3f1b...   ExpiresAt: 2026-10-07T12:00:00Z   ...

# 2. Deliver the token to alice out of band. She redeems it herself, from her
#    own machine, with NO credential of her own yet -- "qrdb enroll" needs no
#    --cert-file/--key-file at all, and generates her key and CSR for her:
qrdb enroll --server https://db.example.com --tenant-id acme \
  --token a94e3f1b... --username alice --out ./alice
# => writes ./alice/key.pem and ./alice/cert.pem: her certificate, already
#    bound to her account and active. The key never left her machine.
```

The same two verbs work on REST, QUIC and MCP (see
[api.md](api.md#capability-matrix--invitation-self-enrolment) for the raw
wire shapes, and [cli.md](cli.md) for every flag); gRPC and the Unix socket
refuse both with a typed error. `qrdb enroll` can also submit a CSR made
elsewhere (`--csr`) or bring your own certificate over that CSR's key
(`--cert`, `--chain`) instead of generating a key and letting the tenant's CA
sign it.

An invitation expires after `auth.invitation_ttl` (24 hours by default) and
can be redeemed exactly once; re-running step 1 with just `--username` issues
a fresh token for the same still-pending account. If the operator has set
`auth.invitation_require_key_pin`, step 1 also needs alice's key fingerprint
(computed from her CSR — ask her to send it first) as `--key-pin-fingerprint`,
and only a CSR over that exact key redeems it.

## Common tasks

**See what somebody can actually do.** Resolve the effective permissions for
their roles before you assign, not after.

```bash
qrdb roles resolve-permissions "$ROLE_A" "$ROLE_B"
```

**See the whole permission vocabulary.**

```bash
qrdb roles permissions --output json
```

Anything not in that list will be refused when you create a role.

**Make a narrow role.**

```bash
qrdb roles create --name readonly --tenant-id acme \
     --permissions "kv:get,kv:list,kv:range"
```

**Let a release team activate and confirm updates on your devices.** The
`ota:activate` and `ota:confirm` permissions belong to your tenant, so a custom
role carrying them in `acme` reaches `acme`'s devices and no other tenant's.
They are separate grants. To keep the person who activates an image from also
confirming it, put them in two roles.

```bash
qrdb roles create --name ota-release --tenant-id acme \
     --permissions "ota:activate,ota:confirm"
ROLE=$(qrdb roles get-by-name ota-release --output json | jq -r .id)
qrdb roles assign --user "$USER" --role "$ROLE"
```

The built-in Admin already holds both. go-qrdb does not activate or confirm
anything itself. The layer that ships updates gates each step on these
permissions through the `Authorize` endpoint (`POST /permissions/authorize`, or
the SDK's RBAC client), so a new role applies on its next check.

**Turn somebody off without deleting them.**

```bash
qrdb users disable "$USER"     # authentication stops
qrdb users enable  "$USER"     # and starts again
```

Prefer this to `delete` for anyone you might want back — the record, its role
bindings and its certificate bindings all survive.

**Retire a role.**

```bash
qrdb roles delete "$ROLE"
```

Deleting a role takes it away from everyone who held it. You do not need to
unassign it first, and nobody is left carrying a role that no longer exists.
From the moment the delete starts, the role grants nothing. If the delete
fails part way, for example because a node is unavailable, run it again: it
picks up where it stopped.

**List and page.**

```bash
qrdb users list --tenant-id acme --page-size 50 --output json
qrdb roles list --tenant-id acme
```

## The built-in roles

Six exist from the first boot and cannot be deleted:

| Role | For |
|---|---|
| Security Officer | governance — barrier, certificates, roles, users, key escrow. **Crypto operations are explicitly denied**, on purpose |
| Admin | key lifecycle, tenants, backups, key escrow |
| Operator | routine operations, including seal and unseal |
| User | crypto operations only |
| Auditor | read-only everywhere |

A key custodian — a Shamir share holder — is not a role: it is enrolled in the
holder roster and authenticates with its holder certificate and factors. See
[../shamir-custody/](../shamir-custody/).

The Security Officer's inability to use keys is the separation-of-duties
control, not an oversight.

Every tenant gets these roles, but the node's own barrier is not any tenant's.
Initializing, unsealing, sealing, rekeying or recovering the node barrier,
registering or listing its Security Officers, submitting node unseal shares,
and running its key ceremony are done by the Security Officer, Admin, Operator
or Custodian **of the `system` tenant**. The same role in your tenant carries
those grants but is refused them; your tenant's own barrier and officers are
managed through `tenant_barrier:*` instead. See
[../auth/roles.md](../auth/roles.md#node-barrier-permissions-are-platform-operator-only).

The cluster is the platform's too: adding or removing replicas, draining the
node, requesting snapshots, starting or stopping shards, reading the topology
and rebalancing are done by the `system` tenant. See
[../auth/roles.md](../auth/roles.md#cluster-membership-permissions-are-platform-operator-only).

You cannot edit them: their permissions are fixed, and so is their membership.
What you *can* do is bring them back into line with the software you are
running, after an upgrade renamed or added a permission:

```bash
qrdb roles reconcile --tenant acme
```

Your tenant was seeded from the built-in definitions on the day it was created
and, before this command existed, never touched again — so a tenant older than
an upgrade keeps verbs the software has retired and never gains the ones it
added. Reconciling adds what is missing, removes what is retired, and leaves
your own roles alone. Running it twice is harmless: the second run reports that
everything is already current and changes nothing.

## Two ways to hold a role

- **System-scoped** (`roleIDs`) — the role applies everywhere.
- **Tenant-scoped** (`roleBindings`) — the role applies only inside one tenant,
  and that tenant must be the user's own.

They add up. A binding can name only the tenant the user belongs to, and that
includes users of the system tenant: no binding grants anything in another
tenant. To make somebody an administrator of tenant `acme`, create their
account in `acme` and give it `acme`'s Admin role; an `acme` administrator
acts only in `acme`. This holds on REST, gRPC, the unix socket, QUIC, MCP and
the embedded SDK.

One gap remains: an Admin of the system tenant can still manage a tenant's
users and roles. Closing it is planned work on strict tenant isolation.

```bash
qrdb users update "$ACME_USER" --role-binding "$ACME_ROLE:acme"
qrdb users update "$USER" --attribute department=eng --attribute region=us-east
```

Both flags **replace** the whole set each time you use them; there is no "add
one" verb. `qrdb users update "$USER" --clear-role-bindings` removes every
tenant-scoped binding, and `--clear-attributes` does the same for ABAC
attributes.

Setting `--role-binding`, `--clear-role-bindings`, `--attribute`, or
`--clear-attributes` requires the operator's identity to hold `roles:assign`
— the same permission required to assign a role directly — in addition to
the `users:update` permission the plain update needs, in the tenant the
account belongs to. The named role must exist and belong to that tenant: every tenant has
its own copy of the built-in roles (`admin`, `operator`, `user`, ...), so use that
tenant's copy. A role that does not exist, or belongs to some other tenant —
including the system tenant's built-ins — is refused before anything
is written.

A `--role-binding` naming any tenant other than the account's own is refused
with 400 and nothing is written — a `globex` binding on an `acme` user, and an
`acme` binding on a system-tenant user, alike. An empty tenant in a binding
means the system tenant and is stored as `system`.

An attribute set with `--attribute` is visible to ABAC policies as
`subject.user_attrs.<name>` (or the match key `user_attrs.<name>`), never as
a certificate or token attribute: setting `--attribute organization=acme`
cannot satisfy a policy that trusts the organization in the caller's
certificate. See [../auth/abac.md](../auth/abac.md).

## Letting an application act inside your tenant

Some applications embed qrdb and run their own **service principal** — for
example a key-management front end that serves your keys out of your tenant's
data. The platform operator cannot let that principal into your tenant; only
you can, by issuing it a **delegation**: a short-lived certificate signed by
your tenant's own CA that names the principal and the permissions you hand
it.

```bash
qrdb ca delegate --principal billing --scope kv:get,kv:put --csr billing.csr --out ./billing
```

Give `cert.pem` to the application. It presents the certificate either over
mTLS or, when it runs qrdb in its own process, with `Node.DelegatedContext`;
both are checked the same way. Then:

- **It can do no more than you delegated, and no more than the application
  itself allows its principal.** Its permissions are the overlap of the two;
  anything else is refused with a permission error.
- **It expires on its own** — after one hour unless you pass `--ttl`, and never
  later than 24 hours. Issue a fresh one before it lapses.
- **You can end it sooner** with `qrdb ca revoke --serial <serial>`, using the
  serial `ca delegate` printed. It stops working within the node's revocation
  cache window (`auth.revocation_cache_ttl`, 30 seconds by default).
- **Every request it makes is recorded in your tenant's audit trail**, with the
  principal, the certificate serial and the delegated scope. A node that cannot
  record it refuses the request instead.
- **Only a certificate from your own CA works.** One signed by another
  tenant's CA, or by the platform's CA, is refused, as is one naming a
  principal the application does not run.

Issuing a delegation needs `certs:delegate`, which your Admin role grants.
Your Admin role grants no data permissions (`kv:get` and the like) of its own,
so the application's own grants are what bound the delegation, not yours.
Full reference: [../certificate-authority/cli.md](../certificate-authority/cli.md#qrdb-ca-delegate).

## Telling whether a caller owns its tenant

An application built on the embedded SDK often needs one answer: is this
caller its tenant's administrator? The identity a login returns answers it
directly. `transport.Identity.TenantOwner` (`tenantOwner` in JSON) is `true`
exactly when the caller holds its own tenant's **Admin** role, either as a
role id or as a role binding in that tenant. Authorize on that field. Do not
compare role names, and do not look in `Metadata`, which never carries it.

```go
fin, err := client.Login().FinishLogin(ctx, req)
if err != nil {
	return err
}
if fin.Identity.TenantOwner {
	// fin.Identity.TenantID's administrator
}
```

The field always refers to `Identity.TenantID` and nothing else. The `system`
tenant's administrator has `TenantOwner` set for the `system` tenant, and that
says nothing about any other tenant. A role id from another tenant never makes
a caller the owner of its own tenant.

The answer is fixed at login. The login ceremony reads it from the role store,
and the session bearer carries it from then on, the same way it carries the
caller's roles. If you withdraw or grant Admin, the change shows at the
caller's next login. A node with no role store refuses the login rather than
return an identity that might wrongly say "not owner". The embedded login
(`FinishLogin`) and session resolution (`ResolveIdentity`) return the field.
No `qrdb` command prints a caller's identity.

Over REST, gRPC, QUIC, MCP and the unix socket nothing returns the identity,
but the node still decides ownership on every authenticated request, from the
role store and uncached, for the caller the request names. Code on the node
that reads the request's identity sees a withdrawn or granted Admin role on
the caller's very next request. If the node cannot read the role store, the
request is refused as temporarily unavailable, which a client may retry. It is
never answered as "not owner".

## Seeing whether someone has a second factor

`qrdb users get "$USER"` prints `MFAEnabled`, and `MFAMethods` when it is
true — computed at read time from the account's real enrolled factors
(never a stored field you set directly, and never something a role-
assignment write can forge). A `Factor:` line, when one is present, shows the
factor's id and type — never its credential material, which never leaves the
server on any interface. Enroll or remove a factor through
[../auth-factors/user.md](../auth-factors/user.md); this account's `MFAEnabled`
reflects that enrollment immediately, on every transport. If the factor
records cannot be read at that moment, the command fails with an
"unavailable" error rather than showing `MFAEnabled: false`; retry it.

## Moving a fleet to required certificate enrolment

`auth.mtls_binding_policy: required` is the default and the only
posture that meets FedRAMP, HIPAA and PCI DSS 4.0: every certificate login
must present a key the named account actually enrolled. A **fresh** fleet
needs no migration — first boot enrols the bootstrap admin's certificate for
you, and every other user is enrolled the ordinary way
(`POST /users/{id}/cert-bindings`) before their first login.

This section is for an **existing** fleet still running with
`auth.mtls_binding_policy: enrolled` set explicitly (the non-compliant
opt-out, where an account nobody has enrolled still logs in on its
certificate's CN and OU alone) and moving off it:

```yaml
auth:
  mtls_binding_policy: enrolled   # TEMPORARY, while migrating -- see below
```

1. **Pre-flight: find who would be refused once you remove this line.** List
   each tenant's users and look for empty `cert_bindings`:

   ```bash
   qrdb users list --tenant-id acme --output json \
     | jq -r '.users[] | select((.cert_bindings // []) | length == 0) | .username'
   ```

   Page through with `--page` until `has_more` is false. Every name printed
   would be refused under `required`. Enrol each one's certificate
   (`POST /users/{id}/cert-bindings`, or `AddCertBinding` on any other
   transport — see [api.md](api.md)). **Start with `admin` in the `system`
   tenant.**
2. **Remove the `enrolled` line (or set it to `required`) on every node** and
   restart them. A node checks at boot that the bootstrap admin holds an
   unexpired binding; if it does not, the node still starts, but serves ONLY
   the ceremony status and admin-credential-retry endpoints ("awaiting admin
   credential") until a certificate binds, with a message naming the reason.
3. **Rolling back** is setting `enrolled` again and restarting. Nothing else
   changes — but remember `enrolled` does not meet FedRAMP, HIPAA or PCI DSS
   4.0, so treat it as a temporary step back, not a resting configuration.
4. **Watch the admin's key expiry.** Every boot check exports
   `qrdb_auth_admin_cert_binding_not_after_timestamp_seconds` — when the
   admin's last unexpired binding expires, as a Unix time — and logs a warning,
   "the bootstrap admin's last live certificate binding expires soon", once that
   is within 30 days. Enrol a replacement key before then: under `required` the
   node will not boot once it has expired. Alert on the time remaining, with
   your own lead time:

   ```
   qrdb_auth_admin_cert_binding_not_after_timestamp_seconds - time() < 30 * 86400
   ```

If you embed qrdb with the Go SDK, run first boot through `Node.FirstBoot`
rather than `firstboot.Initialize` directly: it holds first boot to the node's
policy and checks the result against the node's own user store.

## From the SDK

A Go program that authorizes its own requests imports only the SDK
(`github.com/jeremyhahn/go-qrdb/sdk/go`) and gets the server's own decision:

- `sdk.Identity`, `sdk.IdentityFromContext` and `sdk.WithIdentity` carry the caller.
- `sdk.NewRBACAuthorizer(roles, opts...)` builds the authorizer over a
  `sdk.RoleServicer`. Its options are `sdk.WithRBACCacheTTL`,
  `sdk.WithRBACStrictSeparation`, `sdk.WithRBACRoleServiceFallback` and
  `sdk.WithRBACScopeResolver`.
- `Authorize(ctx, identity, resource, action)` returns nil when the call is allowed.
  `*sdk.PermissionDeniedError` means the caller was denied.
  `*sdk.RBACResolveError` means the role store could not be read, so no
  decision was made. Use `errors.As` to tell the two apart.
- The user and role management permissions are `sdk.PermissionUsers*` and
  `sdk.PermissionRoles*`.

## Troubleshooting

**"role not found" when assigning.** `assign` and `unassign` take role and user
**IDs**, not names. `qrdb roles get-by-name <name>` gives you the ID.

**Your certificate works but nothing is authorized.** Authentication and
authorization are separate steps. The certificate proves who you are; a role
grants what you may do. Check that a user record exists with a binding matching
your certificate, and that it has a role.

**You renewed a certificate and got locked out.** Renewal with the *same key*
keeps the same binding. If you generated a new key, add a binding for it
(`POST /users/{id}/cert-bindings`) or issue against the original key. Bindings
are additive, so the safe order for a key rotation is: enrol the new key first,
then retire the old certificate. Done that way there is no window where you
cannot log in.

**A private key was exposed and you need it to stop working.** Withdraw its
binding:

```
DELETE /users/{id}/cert-bindings/{thumbprint}
```

Revoking the certificate is **not** enough on its own. A binding is over the
*key*, not the certificate, so a certificate re-issued for the same key
authenticates again — which is exactly what a renewal does on purpose. Revoke
the certificate *and* withdraw the binding; the first stops that certificate
being trusted anywhere, the second stops that key being this account's
credential.

The order that never locks you out is: **enrol the replacement key first, then
withdraw the compromised one.** Withdrawing is refused with a 404 if the
thumbprint is not one this account holds — that is deliberate, so a typo cannot
look like a successful withdrawal.

**Careful: under the default `mtls_binding_policy: enrolled`, withdrawing an
account's LAST binding opens it back up.** An account with no bindings accepts
*any* certificate from your CA whose CN and OU name it — which is how every
account behaved before anyone enrolled anything. So withdrawing the only
binding on an account does not lock it down, it widens it. If your intent is
"this account should not log in", disable the account
(`POST /users/{id}/disable`); if your intent is "this key should not log in",
make sure another key is enrolled first.

**Under `mtls_binding_policy: required` it is the other way round.** An account
with no live binding cannot log in by certificate at all, so withdrawing the
last one locks it out rather than opening it. For the bootstrap `admin` in the
`system` tenant the server refuses that withdrawal outright — **409** on REST,
`FAILED_PRECONDITION` on gRPC and QUIC, a tool error on MCP — because a
restart after it would find the admin unbound and come up in "awaiting admin
credential," unable to repair itself by certificate: the recovery path out of
that state (the SO-PIN-gated admin-credential reissue) is the only way back
in, and this refusal keeps a node from reaching it by accident. Enrol the
replacement key first; an expired binding does not count as one. For any other account the withdrawal is
allowed and the account is locked out of certificate login until a key is
enrolled again.

The same goes for everything else that would lock the admin out under
`required`: the server also refuses to delete, disable, suspend or rename the
admin, or to take `mtls` out of its permitted methods, and the error names
which. If the admin's last key expires anyway, the node will not start again
under `required`; start it with `mtls_binding_policy: enrolled`, enrol a new
key for the admin, then set `required` again.

An enrolment whose expiry is already in the past is refused (400) for every
account: it could never log in, and for a key that is already enrolled it would
quietly replace the working binding with a dead one.

Note that an **expired** binding is not the same as a withdrawn one. An expired
binding stops working but still counts as an enrolment, so the account stays
closed to everything except its other keys. Withdrawing the last binding is what
empties the set.

**"auth.mtls_binding_policy is "required" and … has no enrolled certificate
binding" (401).** The account has no enrolled key, so under `required` its
certificate cannot log in. Enrol the thumbprint the message quotes. The same
message ending "names no account" means the certificate's CN and tenant match no
user at all.

**The node will not start: "the bootstrap admin … could not log in under it".**
The policy is `required` and the admin has no live binding (or does not exist,
or first boot has nothing to bind it with). Start the node with `enrolled`,
enrol the admin's certificate, then set `required` again.

**"presented certificate … is not bound to …" (401).** The account has enrolled
certificate bindings and the one you presented matches none of them. The CN and
the OU on a certificate say which account you are attempting; the key is what
proves it is yours, and this account has said which keys are its. Either you
are using a certificate issued for a different key — enrol it, with the
thumbprint the message quotes — or the certificate was issued for someone other
than its subject, which is the case this check exists for.

**Nobody can log in and there is no second administrator.** This is the
standing break-glass for total credential loss and it is unchanged: restart the
node with authentication disabled, enrol the certificate you do hold, and
restart with authentication on. An account you never enrolled is not affected
by any of this — it authenticates on its certificate's CN and OU as it always
did.

**"permission denied" on something you think you have.** Run
`qrdb roles resolve-permissions` over the user's roles and look for the exact
`resource:action` string. Effective permissions are the union of system-scoped
roles and tenant-scoped bindings.

**A built-in role is missing a verb the release notes say it has.** Your tenant
predates the change. `qrdb roles get <id>` shows a `Retired:` line if the role
also carries verbs the build has dropped — either way,
`qrdb roles reconcile --tenant <id>` converges it. This is the one supported way
to change a built-in role's permissions; `roles update` refuses.

**Creating a role failed on a permission string.** The vocabulary is a fixed
registry — you cannot invent `kv:frobnicate`. `qrdb roles permissions` lists
what exists.

**You cannot delete a user or a role.** The bootstrap admin — the one built-in
user, created the first time the cluster ever comes up — and the six built-in
roles are undeletable, and a built-in role cannot be re-permissioned either.
Disable the user instead; for a built-in role's permissions, use
`qrdb roles reconcile`, which is the only writer the guard admits. Every other
user, including an additional tenant's own admin account, is an ordinary
account and can be deleted like any other.

**You are looking for the password field.** There isn't one. The user model is
passwordless throughout; the credential is the certificate. For a second factor
— FIDO2, PIV — see [../auth-factors/user.md](../auth-factors/user.md).

**You want LDAP or RADIUS.** Those are external auth backends from go-auth,
composed above this layer, not part of qrdb's own identity model.
