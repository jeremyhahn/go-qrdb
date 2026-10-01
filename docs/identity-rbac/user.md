# Identity and RBAC — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-23

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

```bash
# 1. Issue a certificate
qrdb ca issue --username alice --tenant-id acme --out ./alice

# 2. Create the user record — the email is the login key
qrdb users create --username alice --email alice@acme.com --tenant-id acme

# 3. Find the IDs (both assign flags take IDs, not names)
ROLE=$(qrdb roles get-by-name Operator --output json | jq -r .id)
USER=$(qrdb users list --tenant-id acme --output json \
       | jq -r '.[] | select(.username=="alice") | .id')

# 4. Give them a role
qrdb roles assign --user "$USER" --role "$ROLE"
```

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

**Turn somebody off without deleting them.**

```bash
qrdb users disable "$USER"     # authentication stops
qrdb users enable  "$USER"     # and starts again
```

Prefer this to `delete` for anyone you might want back — the record, its role
bindings and its certificate bindings all survive.

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
| Custodian | holding a Shamir share, nothing else |

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
- **Tenant-scoped** (`roleBindings`) — the role applies only inside one tenant.

They add up. This is how somebody can be tenant-admin for one customer without
being an admin of the whole system — a binding counts whenever the request
acts on the tenant it names, and nowhere else. A request that names a user or
a role acts on the tenant that user or role belongs to; a per-tenant barrier
operation acts on the tenant it names; everything else — including managing
a tenant's own record, quotas and entitlements — acts on your own. This holds
on REST, gRPC, the unix socket, QUIC and MCP.

```bash
qrdb users update "$USER" --role-binding "$ROLE:acme"
qrdb users update "$USER" --attribute department=eng --attribute region=us-east
```

Both flags **replace** the whole set each time you use them; there is no "add
one" verb. `qrdb users update "$USER" --clear-role-bindings` removes every
tenant-scoped binding, and `--clear-attributes` does the same for ABAC
attributes.

Setting `--role-binding`, `--clear-role-bindings`, `--attribute`, or
`--clear-attributes` requires the operator's identity to hold `roles:assign`
— the same permission required to assign a role directly — in addition to
the `users:update` permission the plain update needs. A `--role-binding`
naming a tenant requires `roles:assign` in THAT tenant too: granting a role
in tenant `acme` from an identity that only administers tenant `contoso`
is refused even if the operator can otherwise update the target account. The
named role must exist and belong to the binding's tenant: every tenant has
its own copy of the built-in roles (`admin`, `operator`, `user`, ...), so use that
tenant's copy. A role that does not exist, or belongs to some other tenant —
including the system tenant's built-ins — is refused before anything
is written.

Only a system-tenant user can hold a binding outside its own tenant. A user
that belongs to tenant `acme` is refused every request on any other tenant
before a binding is looked at, so a `globex` binding on it could never grant
anything; it is refused with 400 rather than stored. An empty tenant in a
binding means the system tenant and is stored as `system`.

An attribute set with `--attribute` is visible to ABAC policies as
`subject.user_attrs.<name>` (or the match key `user_attrs.<name>`), never as
a certificate or token attribute: setting `--attribute organization=acme`
cannot satisfy a policy that trusts the organization in the caller's
certificate. See [../auth/abac.md](../auth/abac.md).

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

By default an account nobody has enrolled still logs in on its certificate's CN
and OU. Once every account that logs in by certificate has an enrolled key, you
can make that the rule for the whole fleet:

```yaml
auth:
  mtls_binding_policy: required   # default: enrolled
```

1. **Pre-flight: find who would be refused.** List each tenant's users and look
   for empty `cert_bindings`:

   ```bash
   qrdb users list --tenant-id acme --output json \
     | jq -r '.users[] | select((.cert_bindings // []) | length == 0) | .username'
   ```

   Page through with `--page` until `has_more` is false. Every name printed
   would be refused under `required`. Enrol each one's certificate
   (`POST /users/{id}/cert-bindings`, or `AddCertBinding` on any other
   transport — see [api.md](api.md)). **Start with `admin` in the `system`
   tenant.**
2. **Set `required` on every node** and restart them. A node checks at boot that
   the bootstrap admin holds an unexpired binding and **refuses to start** if it
   does not, with a message naming the reason.
3. **Rolling back** is setting `enrolled` and restarting. Nothing else changes.
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
`FAILED_PRECONDITION` on gRPC and QUIC, a tool error on MCP — because a node
whose admin cannot log in refuses to boot. Enrol the replacement key first; an
expired binding does not count as one. For any other account the withdrawal is
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
