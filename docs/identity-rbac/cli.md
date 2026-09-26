# Identity and RBAC — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Two command groups: `qrdb users` and `qrdb roles`. Authentication factors are
[../auth-factors/cli.md](../auth-factors/cli.md); certificate issuance is
[../certificate-authority/cli.md](../certificate-authority/cli.md).

All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

**Accounts are passwordless.** There is no password flag anywhere in this
group — identity is a certificate.

## Users

### `qrdb users create`

| Flag | Type | Meaning |
|---|---|---|
| `--username` | string | username for the new user (maps to certificate CN) |
| `--email` | string | **canonical login key**, unique within the tenant. Required unless `--without-email-login-key` |
| `--name` | string | display name (presentation only; never a lookup key) |
| `--without-email-login-key` | bool | create an identity with **no** email login key — certificate-only or bootstrap. It cannot be resolved by email |
| `--tenant-id` | string | tenant (maps to certificate OU) |
| `--roles` | string | comma-separated **role IDs** to assign |

```bash
# An ordinary user: the email is the login key.
qrdb users create --username alice --email alice@acme.com --name "Alice" \
  --tenant-id acme --roles "$OPERATOR_ROLE_ID"

# A certificate-only identity: the omission is DECLARED, never defaulted.
qrdb users create --username svc-backup --without-email-login-key --tenant-id acme
```

Creating a user with no email and no `--without-email-login-key` is refused. The
flag exists so an identity that cannot be resolved by its login key is always
something somebody asked for.

### `qrdb users get <id>` / `delete <id>` / `enable <id>` / `disable <id>`

All take the user **ID**, no flags.

```bash
qrdb users get 1f2e...
qrdb users disable 1f2e...     # blocks authentication, keeps the record
qrdb users enable 1f2e...
qrdb users delete 1f2e...      # refused for built-in (System) users
```

`disable` is the reversible option; prefer it over `delete` for an account you
may want back.

`enable` and `disable` are **lifecycle transitions**, to `active` and `disabled`
respectively — not writes to a boolean. Repeating either is a no-op, and both
are **refused on an account whose deletion has begun** (`lifecycle: deleting`),
because that state is terminal: nothing moves a record out of it. `get` shows
the state (`Lifecycle:`; the `LIFECYCLE` column in `list`), which distinguishes
an account awaiting activation (`pending`), an administrative hold
(`suspended`), an automated lockout (`locked`) and a deactivated record
(`disabled`).

> **Breaking change.** `users get` and `users list` no longer print an
> `Enabled` line or `ENABLED` column, and `--output json|yaml` no longer carries
> an `enabled` key: `lifecycle` is the only account-state field.

### `qrdb users list`

`--output json` carries each account's `cert_bindings`. Under
`auth.mtls_binding_policy: required` an account whose list is empty cannot log
in by certificate, so this is the pre-flight before switching — see
[user.md](user.md#moving-a-fleet-to-required-certificate-enrolment).

| Flag | Type | Meaning |
|---|---|---|
| `--tenant-id` | string | filter by tenant |
| `--page` | int | page number |
| `--page-size` | int | results per page |

```bash
qrdb users list --tenant-id acme --page-size 50 --output json
```

### `qrdb users update <id>`

| Flag | Type | Meaning |
|---|---|---|
| `--username` | string | new username |
| `--email` | string | new canonical login key; uniqueness is re-validated within the tenant. Left unchanged when empty |
| `--name` | string | new display name. Left unchanged when empty |
| `--roles` | string | comma-separated role IDs to assign |

Changing the email moves the login index atomically: the new key is claimed
before the old one is released, so a failed update never leaves the account
reachable by neither.

## Roles

### `qrdb roles create`

| Flag | Type | Meaning |
|---|---|---|
| `--name` | string | role name |
| `--tenant-id` | string | tenant |
| `--permissions` | string | comma-separated permissions, `resource:action` |

```bash
qrdb roles create --name readonly --tenant-id acme \
     --permissions "kv:get,kv:list,kv:range"
```

Permission strings are validated against the registry; an unregistered one is
refused.

### `qrdb roles get <id>` / `get-by-name <name>` / `delete <id>`

```bash
qrdb roles get 7a1b...
qrdb roles get-by-name Operator --tenant-id system
qrdb roles delete 7a1b...        # refused for the six built-in roles
```

`get-by-name` takes `--tenant-id`, defaulting to the system tenant. **It is how
you turn a role name into the ID the other verbs want.**

### `qrdb roles list`

Flags: `--tenant-id`, `--page`, `--page-size`.

### `qrdb roles update <id>`

| Flag | Type |
|---|---|
| `--name` | string |
| `--permissions` | string — comma-separated |

### `qrdb roles assign` / `unassign`

| Flag | Type |
|---|---|
| `--user` | string — user **ID** |
| `--role` | string — role **ID** |

```bash
ROLE=$(qrdb roles get-by-name Operator --output json | jq -r .id)
qrdb roles assign --user 1f2e... --role "$ROLE"
qrdb roles unassign --user 1f2e... --role "$ROLE"
```

**Both flags take IDs, not names.** This is the most common mistake in this
group.

### `qrdb roles permissions`

List every registered permission. No flags. This is the authoritative
vocabulary — anything not in it will be refused by `roles create`.

```bash
qrdb roles permissions --output json
```

### `qrdb roles resolve-permissions <role-id> [role-id...]`

Resolve the effective permission set for a combination of roles. No flags.

```bash
qrdb roles resolve-permissions "$ROLE_A" "$ROLE_B"
```

Use this to answer "what would this user actually be able to do" before
assigning.

### `qrdb roles reconcile`

Converge a tenant's **built-in** roles onto this build's definitions: add the
permissions the build now grants, remove the ones it no longer grants, leave
custom roles alone.

| Flag | Meaning |
|---|---|
| `--tenant` | Tenant to reconcile. Defaults to the caller's own tenant. |

```bash
qrdb roles reconcile --tenant acme
```

```
Tenant acme:
  admin (5aee453e-…) updated
    + sdlc:bind
    - sdlc:register
  operator (…) created
    + orchestrator:task_block
```

A converged tenant prints `built-in roles already current` and writes nothing —
the command is idempotent and safe to re-run.

This is the **only** way to change a built-in role's permissions: `roles update`
refuses a system role by design, and so does `roles delete`. Run it after a
build that renamed or added a permission, otherwise every tenant created before
that build keeps the old set and its administrators get a 403 on the new verb
with nothing explaining it.

Requires `roles:create` — the same grant as seeding, because reconciliation is
seeding applied to an existing tenant.

`qrdb roles get` prints a `Retired:` line when a role carries permissions this
build no longer defines. That is the drift this command repairs; seeing it is
how you know to run it.

## The built-in roles

Six roles exist from bootstrap and **cannot be deleted**: Security Officer,
Admin, Operator, User, Auditor, Custodian. Their identifiers are deterministic,
so they are the same on every node. Their permissions are fixed too — the one
supported way to change them is `qrdb roles reconcile`, which converges them
onto the build's own definitions rather than onto anything a caller supplies.
See [../firstboot/cli.md](../firstboot/cli.md).

## A complete enrolment

```bash
qrdb ca issue --username alice --tenant-id acme --out ./alice
qrdb users create --username alice --tenant-id acme
ROLE=$(qrdb roles get-by-name Operator --output json | jq -r .id)
USER=$(qrdb users list --tenant-id acme --output json | jq -r '.[] | select(.username=="alice") | .id')
qrdb roles assign --user "$USER" --role "$ROLE"
```

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite.
