# Identity and RBAC — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
| Security Officer | governance — barrier, certificates, roles, users. **Crypto operations are explicitly denied**, on purpose |
| Admin | key lifecycle, tenants, backups |
| Operator | routine operations, including seal and unseal |
| User | crypto operations only |
| Auditor | read-only everywhere |
| Custodian | holding a Shamir share, nothing else |

The Security Officer's inability to use keys is the separation-of-duties
control, not an oversight.

## Two ways to hold a role

- **System-scoped** (`roleIDs`) — the role applies everywhere.
- **Tenant-scoped** (`roleBindings`) — the role applies only inside one tenant.

They add up. This is how somebody can be tenant-admin for one customer without
being an admin of the whole system.

## Troubleshooting

**"role not found" when assigning.** `assign` and `unassign` take role and user
**IDs**, not names. `qrdb roles get-by-name <name>` gives you the ID.

**Your certificate works but nothing is authorized.** Authentication and
authorization are separate steps. The certificate proves who you are; a role
grants what you may do. Check that a user record exists with a binding matching
your certificate, and that it has a role.

**You renewed a certificate and got locked out.** Renewal with the *same key*
keeps the same binding. If you generated a new key, add a binding for it
(`POST /users/{id}/cert-bindings`) or issue against the original key.

**"permission denied" on something you think you have.** Run
`qrdb roles resolve-permissions` over the user's roles and look for the exact
`resource:action` string. Effective permissions are the union of system-scoped
roles and tenant-scoped bindings.

**Creating a role failed on a permission string.** The vocabulary is a fixed
registry — you cannot invent `kv:frobnicate`. `qrdb roles permissions` lists
what exists.

**You cannot delete a user or a role.** Built-in users and the six built-in
roles are undeletable. Disable the user instead.

**You are looking for the password field.** There isn't one. The user model is
passwordless throughout; the credential is the certificate. For a second factor
— FIDO2, PIV — see [../auth-factors/user.md](../auth-factors/user.md).

**You want LDAP or RADIUS.** Those are external auth backends from go-auth,
composed above this layer, not part of qrdb's own identity model.
