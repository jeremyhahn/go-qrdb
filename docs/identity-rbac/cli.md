# Identity and RBAC — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26 at main 851fd06fc3
> Conflicts with this document are defects. Fix them here first.

Three command groups: `qrdb users`, `qrdb roles` and `qrdb abac policy`.
Authentication factors are
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

### `qrdb users invite`

| Flag | Type | Meaning |
|---|---|---|
| `--username` | string | the account to invite |
| `--email` | string | canonical login key for a new account |
| `--name` | string | display name for a new account (presentation only) |
| `--roles` | string | comma-separated role IDs for a new account |
| `--key-pin-fingerprint` | string | pin the invitation to one key -- the base64url SHA-256 SPKI thumbprint of a CSR the invited user sends out of band. Only that key can redeem it |

Creates a pending account in your own tenant -- or, given only `--username`,
re-invites one still pending -- and returns a single-use enrolment token
**exactly once**. Deliver it to the invited user out of band; they redeem it
themselves with `qrdb enroll`. You never hold a credential for their
account. A re-invite supersedes every token issued for the account before
it: only the newest token redeems, and an earlier one is refused exactly as a
spent token is. If two re-invites of one account race, one of them fails with
an aborted error; run it again.

```bash
qrdb users invite --username alice --email alice@acme.com --roles "$OPERATOR_ROLE_ID"
# => prints Token: <one-time-token> and the account's ID and TenantID

# Re-invite the same still-pending account with a fresh token:
qrdb users invite --username alice
```

An invitation expires after `auth.invitation_ttl` (24 hours by default); there
is no per-invite TTL override -- it is a server-wide posture, not a per-call
choice. `--roles` is validated the same `roles:assign` gate `users create`
applies, and the account is judged by the same target guard `ca issue` is: no
Security Officer target, and an Owner target only when you hold Owner
yourself.

Served on every transport (see
[api.md](api.md#capability-matrix--invitation-self-enrolment)).

### `qrdb enroll`

The redeemer's half of `qrdb users invite`. It is a **top-level** command, not
under `users` or `ca`: unlike every other command, it runs with **no
credential at all** -- `--cert-file`/`--key-file` are not just optional, they
are not even read.

| Flag | Type | Meaning |
|---|---|---|
| `--tenant-id` | string | the tenant the invitation was issued in |
| `--token` | string | the one-time invitation token |
| `--username` | string | common name for a freshly generated key's CSR (cosmetic only -- your account comes from the invitation, never from the CSR) |
| `--csr` | string | submit an existing CSR instead of generating a key here |
| `--cert` | string | bring your own certificate over the CSR's key, leaf first, instead of letting the tenant's CA sign it |
| `--chain` | string | optional PEM intermediates for `--cert` |
| `--out` | string | output directory for `cert.pem` (and `key.pem` when the key is generated here) |

```bash
# Generate a key and CSR here; the server never sees the private key.
qrdb enroll --server https://db.example.com --tenant-id acme \
  --token "$TOKEN" --username alice --out ./alice

# Submit a CSR made elsewhere instead:
qrdb enroll --tenant-id acme --token "$TOKEN" --csr ./alice.csr
```

`--server`/`--transport` select where to redeem, the same global flags every
other command uses, but this is the one command that dials without
connecting first: on `mcp`, redemption is served by a **separate** public
server (`/mcp/enroll`) because the main endpoint answers an unauthenticated
caller nothing beyond the tools an embedder declares public. Served on every
transport.

A token is single-use: a second `qrdb enroll` with the same token gets the
same opaque refusal an unknown or expired token gets, by design (see
[user.md](user.md#inviting-somebody-instead)).

### `qrdb users cert-binding add|list|remove`

Enrols, lists and withdraws the mTLS certificate bindings of a user — the
CLI form of `POST /users/{id}/cert-bindings` and
`DELETE /users/{id}/cert-bindings/{thumbprint}`
([api.md](api.md#withdrawing-a-binding)), on every CLI transport. Under the
default `auth.mtls_binding_policy: required` a certificate authenticates as an
account only once its SPKI thumbprint is enrolled on it, so `add` is how an
operator admits a new certificate.

| Verb | Arguments | Flags | Meaning |
|---|---|---|---|
| `add` | `<user-id> [thumbprint]` | `--cert <pem>`, `--not-after <rfc3339>` | enrol a key, by its base64url SHA-256 SPKI thumbprint or by the PEM certificate given to `--cert` (exactly one of the two). With `--cert` the expiry defaults to the certificate's; `--not-after` overrides it. Without either the binding never expires. An expiry at or before the Unix epoch, from either, is refused rather than sent, since the wire reads a non-positive expiry as "never expires". Re-enrolling an identical binding is a no-op |
| `list` | `<user-id>` | — | the user's bindings, expired ones included |
| `remove` | `<user-id> <thumbprint>` | — | withdraw one binding; every certificate for that key stops authenticating as the account. A thumbprint the user does not hold is refused |

Each verb prints the user's bindings as they now stand (`THUMBPRINT`, `KIND`,
`NOT_AFTER`; a JSON or YAML array under `--output`).

A thumbprint is base64url (RFC 8705 `x5t#S256`), so about one in 64 begins
with `-`, which the CLI would otherwise read as a flag. Put every flag first
and the thumbprint after `--`, which ends option parsing (POSIX Utility Syntax
Guideline 10); doing so always is safe, and scripts should.

```bash
qrdb users cert-binding add 1f2e... --cert alice.pem
qrdb users cert-binding add 1f2e... --not-after 2030-01-02T03:04:05Z -- -mfgDIAA4OnmhPXEgQ0d46rNYBLntyNnLUbId3NJ_eg
qrdb users cert-binding list 1f2e...
qrdb users cert-binding remove 1f2e... -- 3q2-7w...   # enrol the replacement first
```

### `qrdb users get <id>` / `delete <id>` / `enable <id>` / `disable <id>` / `suspend <id>` / `unsuspend <id>`

All take the user **ID**, no flags.

```bash
qrdb users get 1f2e...
qrdb users disable 1f2e...     # blocks authentication, keeps the record
qrdb users enable 1f2e...
qrdb users suspend 1f2e...     # administrative hold, refuses even existing tokens
qrdb users unsuspend 1f2e...
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

`suspend` and `unsuspend` move only between `active` and `suspended`. Repeating
either is a no-op; from any other state (a `disabled`, `locked` or `pending`
account) both are refused with a `LifecycleTransitionError`, so `unsuspend`
never brings back an account that `enable` owns. A suspended account's requests
are refused, including those carrying a token issued before the suspension.
`lifecycle` is the only account-state field.

The default (table) output of `users get` also prints, when present: a
`CertBinding: <thumbprint> (<kind>[, expires <time>])` line per enrolled mTLS
key, a `RoleBinding: <role_id>@<tenant_id>` line per tenant-scoped role binding, an
`Attribute: <key>=<value>` line per ABAC attribute, `MFAEnabled: true|false`
(derived from enrolled factors, never stored), `MFAMethods: <comma-separated>`
when at least one factor is enrolled, and a `Factor: <id> (<factor_name>)`
line per enrolled factor — the factor's opaque `data` is never printed,
because it never reaches the client on any transport. `--output json|yaml`
carry the same information under `cert_bindings`, `role_bindings`, `attributes`,
`mfa_enabled`, `mfa_methods`, `mfa_enrolled_at` and `factors`.

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
| `--role-binding` | string, repeatable | role binding to set, as `role_id:tenant_id`; `tenant_id` must be the user's own tenant, any other is refused. Replaces the current set. Left unchanged when absent |
| `--clear-role-bindings` | bool | remove every tenant-scoped role binding |
| `--attribute` | string, repeatable | ABAC attribute to set, as `key=value`. Replaces the current set. Left unchanged when absent |
| `--clear-attributes` | bool | remove every ABAC attribute |

Changing the email moves the login index atomically: the new key is claimed
before the old one is released, so a failed update never leaves the account
reachable by neither.

`--role-binding` and `--attribute` REPLACE the whole set when given; there is
no way to add or remove a single entry from the command line — read the
current set with `qrdb users get <id> --output json` first if only one entry
needs to change. Given with its set flag (`--role-binding`, `--attribute`), a
clear flag is ignored: the set flag wins.

`system`, `factors[].data`, `mfa_enabled`, `mfa_methods` and `mfa_enrolled_at`
have **no update flag**. `system` is set exactly once, by first-boot, for the
single cluster-wide bootstrap admin, and no CLI command can create a second
one. The MFA fields and the auth-factor metadata are derived read-only
projections; enrolling a factor is a separate verb this command group does not
cover.

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

**A grantor may grant only permissions it holds.** `roles create` and
`roles update` are refused with a permission denial when the role would carry
any permission the caller does not hold in the role's tenant, a wildcard
counting for what it covers. The tenant Admin holds no data-plane or
key-custody permission, so it creates roles only within its own holdings:
`--permissions "kv:get"` or `"keys:sign"` from a tenant Admin is refused. The
example above is run by a principal that holds `kv:get`, `kv:list` and
`kv:range`. A tenant Admin may write a role carrying its own tenant's database
lifecycle (`databases:create`, `databases:read`, `databases:list`,
`databases:update`, `databases:delete`, `databases:create_raft`,
`databases:create_volume`, `databases:scale`, `databases:barrier_status`,
`databases:seal`, `databases:unseal`) or `tenant_barrier:enable_encryption`,
which it holds; for a role that also carries `kv:*`, it first gives a second
account in its tenant the built-in `admin` and `operator` roles, and that
account runs `roles create`. See
[architecture.md](architecture.md#the-grant-rule).

Requires `roles:create`. Under strict separation of duties
(`auth.separation_of_duties`, always on in FIPS 140-3 mode) it additionally
runs only as a separate Security Officer of the tenant: the tenant Admin is
refused with 403 `SO role required`, and an account holding both `so` and
`admin` with 403 `separation of duties: ...`. Over every transport.

### `qrdb roles get <id>` / `get-by-name <name>` / `delete <id>`

```bash
qrdb roles get 7a1b...
qrdb roles get-by-name operator --tenant-id system
qrdb roles delete 7a1b...        # refused for the five built-in roles
```

`get-by-name` takes `--tenant-id`, defaulting to the system tenant. **It is how
you turn a role name into the ID the other verbs want.**

`delete` strips the role from every account that holds it before it removes
the role, so after it prints `OK` no `users get` lists the role's ID. Once it
starts, the role grants nothing and can be neither assigned nor updated. If it
fails part way, the role is left in place in that state; run the same `delete`
again to finish it.

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
ROLE=$(qrdb roles get-by-name operator --output json | jq -r .id)
qrdb roles assign --user 1f2e... --role "$ROLE"
qrdb roles unassign --user 1f2e... --role "$ROLE"
```

**Both flags take IDs, not names.** This is the most common mistake in this
group.

Assigning a custom role is refused unless the assigner holds every permission
the role carries. A built-in role is assigned to another account under its
usual rules, which is how the tenant Admin gives the built-in `user` role, and
its crypto-use duty, to the accounts that do the signing. No caller may assign
itself a role, built-in or custom, that carries a permission it does not hold:
`roles assign --user <your own id> --role <user>` from a tenant Admin is
refused.

Under strict separation of duties only the tenant's own Security Officer
grants or revokes the `so` role, and no account may hold `so` together with
its tenant's `admin` role: an `assign` that would leave a user holding both is
refused with 403 `separation of duties: ...`.

### Granting device onboarding alone

`devices:onboard` is a permission, and the tenant Admin already holds it. To
give it to someone without the rest of Admin, define a custom role in your own
tenant that carries it alone, then assign that role:

```bash
qrdb roles create --name device-onboarder --tenant-id acme \
     --permissions "devices:onboard"
ROLE=$(qrdb roles get-by-name device-onboarder --tenant-id acme --output json | jq -r .id)
qrdb roles assign --user "$USER" --role "$ROLE"
```

The holder may onboard acme's devices and no other tenant's. `qrdb roles
own-permissions`, run as the holder, lists `devices:onboard` with scope
`self`. See [architecture.md](architecture.md#granting-device-onboarding-through-a-custom-role).

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

### `qrdb roles own-permissions`

List the caller's own effective permissions: its roles resolved, wildcards
expanded, one row per concrete permission with its scope (`NAME`,
`RESOURCE`, `ACTION`, `SCOPE`). No flags and no arguments; it answers only
for the authenticated caller, so it needs no grant. Works over every
transport.

```bash
qrdb roles own-permissions --output json
```

### `qrdb roles reconcile`

Converge a tenant's **built-in** roles onto this build's definitions: add the
permissions the build now grants, remove the ones it no longer grants, leave
custom roles alone.

| Flag | Meaning |
|---|---|
| `--tenant` | Tenant to reconcile. Defaults to the caller's own tenant. |
| `--all-tenants` | Reconcile every live tenant in one pass, the system tenant first. Platform operator only; cannot be combined with `--tenant`. |

```bash
qrdb roles reconcile --tenant acme
```

```
Tenant acme:
  admin (5aee453e-…) updated
    + devices:onboard
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
seeding applied to an existing tenant. In the standard posture a tenant's
built-in Admin holds it, so the tenant's own Admin reconciles the tenant
without taking another role. Under strict separation of duties the reconcile,
like `roles create`, runs only as a separate Security Officer of the tenant:
the tenant Admin is refused with 403 `SO role required`, and the tenant's SO,
which holds `roles:create` and not `admin`, reconciles it.

`qrdb roles get` prints a `Retired:` line when a role carries permissions this
build no longer defines. That is the drift this command repairs; seeing it is
how you know to run it.

#### `--all-tenants`

After an upgrade that renamed or added a permission, the platform operator
converges every tenant at once instead of asking each tenant's Admin:

```bash
qrdb roles reconcile --all-tenants
```

```
Tenant system: built-in roles already current
Tenant acme:
  admin (5aee453e-…) updated
    + certs:issue
    - certs:generate
Tenant vault: skipped (sealed): tenant barrier is sealed
Tenant broken: FAILED: role store unavailable
```

Each tenant is reconciled independently. A tenant whose barrier is sealed, or
that refuses the call, is listed as skipped and left for a later run. A tenant
that fails is listed with its error and does not stop the pass; the command
then exits non-zero naming every failed tenant. `--output json` and
`--output yaml` print the same report as `tenants`, `skipped` and `failed`
lists.

Requires `roles:reconcile_all`, a cross-tenant permission held only by the
system tenant's built-in Admin role, in both separation postures. A tenant's
own Admin, the system SO, or any other tenant principal, is refused even if it
holds `roles:create`. Under strict separation of duties
(`auth.separation_of_duties`) the single-tenant reconcile is an SO's alone and
the system Admin is refused it; `--all-tenants` stays the system Admin's. First
boot can enrol the system SO (`qrdb init --so-username` with `--so-csr` or
`--so-cert`).

A node upgraded from a build that predates `roles:reconcile_all` has a stored
system Admin role without it. Run `qrdb roles reconcile` as the system Admin
(the system SO under strict separation) first; that converges the system tenant and grants the permission. Then run
`--all-tenants`.

## ABAC policies

`qrdb abac policy` manages the stored ABAC policy documents of the caller's
own tenant (the document format is [the ABAC reference](../auth/abac.md)).
Every verb acts on that tenant and works on every transport the root
`--transport` flag selects. A stored policy does **not** feed the ABAC
evaluator yet: these verbs store and return documents, and an authorization
decision does not change when one is created, updated or deleted.

One flag is shared by every verb:

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--tenant` | string | the caller's tenant | the tenant addressed. It may only name the caller's own tenant; any other is refused |

Each verb prints a table (`NAME`, `TENANT`, `ENABLED`, `PRIORITY`, `RULES`,
`UPDATED`) by default, and the stored document with `--output json` or
`--output yaml`. A refusal exits non-zero with the server's error.

### `qrdb abac policy create`

Creates a policy from one YAML or JSON document. A name the tenant already
holds is refused.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--file`, `-f` | string | — (required) | path to the policy document; the file must hold exactly one document |

If the document carries no `metadata.tenant` and `--tenant` is given, the
document takes that tenant; a document naming a tenant other than `--tenant`
is refused before any request is sent.

```bash
qrdb abac policy create --file ./deny-after-hours.yaml
qrdb --output json abac policy create -f ./deny-after-hours.yaml
```

### `qrdb abac policy get <name>`

Shows one policy. The document is returned exactly as it was written:
`spec.enabled` appears only if the document set it.

```bash
qrdb --output yaml abac policy get deny-after-hours
```

### `qrdb abac policy list`

Lists the tenant's policies, sorted by name. An empty tenant prints an empty
table, or `[]` with `--output json`.

```bash
qrdb abac policy list
```

### `qrdb abac policy update`

Replaces a policy with one YAML or JSON document; `metadata.name` selects the
policy. It takes the same `--file` / `-f` flag as `create`. The policy must
exist; its creation time is kept and its update time set. An update that
loses to a concurrent write is refused and is safe to retry after a fresh
`get`.

```bash
# Switch a policy off without deleting it: set spec.enabled to false.
qrdb abac policy update -f ./deny-after-hours.yaml
```

### `qrdb abac policy delete <name>`

Deletes one policy and prints `Deleted policy <name>`.

```bash
qrdb abac policy delete deny-after-hours
```

Each verb needs its `abac_policies:*` permission (`create`, `read`, `list`,
`update`, `delete`); the built-in `admin` role holds all five.

## The built-in roles

Five roles exist from bootstrap and **cannot be deleted**: `so` (Security
Officer), `admin`, `operator`, `user` and `auditor`; `get-by-name` takes these
names. Their identifiers are deterministic,
so they are the same on every node. Their permissions are fixed too — the one
supported way to change them is `qrdb roles reconcile`, which converges them
onto the build's own definitions rather than onto anything a caller supplies.

A tenant's Admin creates and updates its tenant's custom roles (`roles create`,
`roles update`) within its own holdings; deleting a role stays with the
Security Officer. Both verbs
act in the caller's own tenant only: `--tenant-id` naming another tenant, or
`system`, is refused, and another tenant's role id is not found. A built-in
role's name is reserved, so `roles create --name admin` is refused, and
`roles update` refuses a built-in role.
See [../firstboot/cli.md](../firstboot/cli.md).

## Delegating to an application's service principal

There is no user or role verb for a service principal: it is not an account.
A tenant lets an embedding application's principal act inside it by issuing a
delegation certificate with `qrdb ca delegate` — see
[../certificate-authority/cli.md](../certificate-authority/cli.md#qrdb-ca-delegate).
What that principal can then do is the delegated scope intersected with the
grants the application gives it, so no `roles assign` is involved.

For work the application runs on its own, write the grant down once with
`qrdb ca delegate policy put --principal <p> --scope ... --ttl ...`: the
delegation then renews itself within that policy, and
`qrdb ca delegate policy delete <p>` revokes it (the next mint is refused) — see
[../certificate-authority/cli.md](../certificate-authority/cli.md#qrdb-ca-delegate-policy).
`qrdb ca delegate policy list` shows your tenant's policies, never another
tenant's.

## A complete enrolment

The whole flow, create the account, issue its certificate and assign a role,
is [user.md](user.md#enrolling-somebody-end-to-end), "Enrolling somebody, end
to end".

## Parity

The `qrdb users`, `qrdb enroll`, `qrdb roles` and `qrdb abac policy`
commands are driven through the CLI binary on every transport of one node by the
`_PerProtocol` suites `make integration-test-identity-rbac` runs
(`roles_cli_test.go`, `users_cli_test.go`, `invitation_cli_test.go`,
`abac_policy_cli_test.go`). Per-transport accounting, including every gap: the
`identity-rbac` row of
[`test/integration/COVERAGE.md`](../../test/integration/COVERAGE.md).
