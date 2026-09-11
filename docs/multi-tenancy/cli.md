# Multi-Tenancy — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Command group: `qrdb tenant` — create, delete and list tenants, manage their
quotas and entitlements, and drive their dedicated barriers. All verbs accept
the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

Note the flag inconsistency in this group: some verbs take `--id`, some take
`--tenant-id`, and some take the tenant as a positional argument. The tables
below give the real spelling per verb.

## Tenant lifecycle

### `qrdb tenant create`

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--id` | string | — | tenant ID, must be unique |
| `--name` | string | — | human-readable name |
| `--isolation-type` | string | `prefix` | `prefix \| barrier \| volume \| raft` |
| `--data-root` | string | pool allocation | for `volume`/`raft`: pin the dedicated `DataRoot` to an **absolute path** instead of allocating from the pool; must name an empty or not-yet-existing directory |
| `--barrier-auto-unseal` | bool | false | for a dedicated-isolation tenant, use **system-managed** auto-unseal instead of the secure self-managed default — **entitlement-gated** |

```bash
qrdb tenant create --id acme --name "Acme Corp"
qrdb tenant create --id secure-co --name "Secure Co" --isolation-type volume
qrdb tenant create --id big-co --name "Big Co" --isolation-type raft \
     --data-root /mnt/tenant-disks/big-co
```

`--barrier-auto-unseal` trades custody for convenience: the tenant's key is
wrapped under the *system* barrier so the node can unseal it without the tenant
present. It is off by default and entitlement-gated for that reason.

### `qrdb tenant get <id>` / `list` / `delete <id>`

Positional tenant ID; no flags.

**`delete` is permanent in both directions.** It reclaims the tenant's data
AND retires the id: `qrdb tenant create` with the same id afterwards is
refused, for good. There is no release, no retention window and no force flag,
because "is this the same customer as the one that held the name before?" is
not a question the server can answer about a name you chose.

It refuses reserved ids outright — `system`, `default`, anything starting with
`_`, anything containing `:`. The platform's own tenant is not yours to
delete, and nothing was committed when it says so.

It refuses while the tenant still owns databases and tells you which:

```
$ qrdb tenant delete acme
Error: tenant: acme still owns 2 database(s) (ledger, reports); drop them before deleting the tenant

$ qrdb database drop ledger
$ qrdb database drop reports
$ qrdb tenant delete acme
```

If a delete fails part-way, **re-run the same command**. The teardown is
idempotent and resumes where it stopped; the id is already retired by then, so
nothing can attach to whatever is left.

### `qrdb tenant update <id>`

| Flag | Type | Meaning |
|---|---|---|
| `--name` | string | new human-readable name |
| `--isolation-type` | string | **`barrier \| prefix` only** |

Note the restriction: `update` accepts only `barrier` and `prefix`, and any
change is refused once the tenant holds data (`ErrTenantHasData`).

## Governance

### `qrdb tenant set-entitlement <id>`

Which tiers and features the tenant's **plan** permits. Checked **before**
quotas.

| Flag | Type | Meaning |
|---|---|---|
| `--isolation-modes` | strings | permitted isolation modes, e.g. `prefix,volume,raft` |
| `--allow-dedicated-raft` | bool | permit dedicated Raft-backed databases |
| `--allow-volume-storage` | bool | permit databases on a dedicated volume |
| `--allow-system-managed-barrier` | bool | permit opting into system-managed auto-unseal |
| `--allowed-barrier-modes` | strings | sealing strategies the tenant may select, e.g. `none,passphrase,shamir` or an xkms hardware id; **empty = software defaults, hardware fail-closed** |

```bash
qrdb tenant set-entitlement acme \
     --isolation-modes prefix,volume,raft \
     --allow-dedicated-raft --allow-volume-storage \
     --allowed-barrier-modes passphrase,shamir
```

The built-in defaults deny everything above `prefix`. Nothing is granted
implicitly.

### `qrdb tenant update-quotas <id>`

How much of each resource the tenant may hold. **`-1` means unlimited; `0` means
use the system default.**

| Flag | Caps |
|---|---|
| `--max-databases` | databases (`-1` = unlimited) |
| `--max-raft-groups` | dedicated Raft groups |
| `--max-replicas-per-group` | replication factor of one dedicated-raft database (`-1` = unlimited) |
| `--max-total-replicas` | aggregate replicas across the tenant's raft groups (`-1` = unlimited) |
| `--max-storage-bytes` | total on-disk bytes |
| `--max-database-size-bytes` | on-disk size of any single database |
| `--max-collections`, `--max-keys`, `--max-users`, `--max-roles`, `--max-certificates`, `--max-backups`, `--max-vector-dims`, `--max-ts-metrics` | the remaining caps |
| `--system-barrier-quota` | bytes an isolated tenant may consume via the **shared system barrier** (`0` = option unavailable, `-1` = unlimited) |

```bash
qrdb tenant update-quotas acme --max-databases 20 \
     --max-storage-bytes 107374182400 --max-total-replicas 9
```

## Per-tenant barrier

Applies only to tenants with a **dedicated** isolation type
(`barrier`/`volume`/`raft`). A tenant on the shared system barrier has no
barrier of its own.

### `qrdb tenant barrier-init`

| Flag | Type | Meaning |
|---|---|---|
| `--id` | string | tenant ID |
| `--strategy` | string | `none \| passphrase \| shamir \| <registered hardware id>`; empty = `passphrase` |
| `--passphrase` | string | required for the passphrase strategy; ignored by none/hardware |
| `--threshold` | int | M, shamir only (`0` = default) |
| `--shares` | int | N, shamir only (`0` = default) |

**Share material is never returned here** — officers claim their shares
individually.

```bash
qrdb tenant barrier-init --id secure-co --strategy passphrase --passphrase "$PP"
qrdb tenant barrier-init --id secure-co --strategy shamir --threshold 3 --shares 5
```

### `qrdb tenant barrier-status` / `barrier-seal`

| Flag | Type |
|---|---|
| `--id` | string |

### `qrdb tenant barrier-unseal`

| Flag | Type | Meaning |
|---|---|---|
| `--id` | string | tenant ID |
| `--key` | string | base64-encoded master key |
| `--share` | string | base64-encoded Shamir share |

### `qrdb tenant barrier-shamir-seal-vote`

| Flag | Type |
|---|---|
| `--tenant-id` | string |
| `--share` | string |

Shamir-strategy tenant barriers only. When the quorum is met all submitted
shares are wiped and the barrier seals.

## Tenant Security Officers

`qrdb tenant barrier-officer` mirrors `qrdb barrier officer`
([../shamir-custody/cli.md](../shamir-custody/cli.md)) scoped to one tenant via
`--tenant-id`. Verbs: `register`, `list`, `claim-status`, `claim-begin`,
`claim-complete`, with the same flags plus `--tenant-id`.

```bash
qrdb tenant barrier-officer register --tenant-id secure-co \
     --username alice --csr-file alice.csr --authenticator-type piv
qrdb tenant barrier-officer claim-status --tenant-id secure-co
```

Applies only to dedicated-isolation tenants.

## Using a tenant

The tenant is taken from **your certificate's OU**, not from a flag. To operate
as a tenant, use that tenant's certificate:

```bash
qrdb ca issue --username alice --tenant-id acme --out ./alice
qrdb --cert-file alice/cert.pem --key-file alice/key.pem kv list app/
```

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite.
