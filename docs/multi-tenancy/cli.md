# Multi-Tenancy — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb tenant` — create, delete and list tenants, manage their
quotas and entitlements, and drive their dedicated barriers. All verbs accept
the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

Note the flag inconsistency in this group: some verbs take `--id`, some take
`--tenant-id`, and some take the tenant as a positional argument. The tables
below give the real spelling per verb.

## Tenant lifecycle

### `qrdb tenant create`

Creates a tenant **together with its first admin**, or not at all.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--id` | string | — | required; tenant ID, must be unique and a SPIFFE trust domain (lowercase letters, digits, `.`, `-`, `_`) other than `auth.cluster_trust_domain` |
| `--name` | string | — | required; human-readable name |
| `--isolation-type` | string | `prefix` | `prefix \| barrier \| volume \| raft` |
| `--pool` | string | the tenant's default pool | for a `volume`/`raft` tenant, place it on this named volume pool instead of its default; the tenant must be entitled to the pool |
| `--barrier-auto-unseal` | bool | false | for a dedicated-isolation tenant, use **system-managed** auto-unseal instead of the secure self-managed default — **entitlement-gated** |
| `--admin-username` | string | — | required; the tenant's first admin |
| `--admin-csr` | string | — | the first admin's PEM certificate signing request: a `prefix` tenant's own CA signs it; for a `barrier`, `volume` or `raft` tenant the cluster CA signs it. A node with no cluster CA (bring-your-own TLS) signs a `--barrier-auto-unseal` tenant's with the tenant's CA and refuses a self-managed one's before creating anything; use `--admin-cert` with `--trust-anchors` there |
| `--admin-cert` | string | — | the first admin's own PEM certificate, leaf first; must chain to `--trust-anchors` |
| `--admin-chain` | string | — | optional PEM intermediates for `--admin-cert` |
| `--trust-anchors` | string | — | a PEM bundle of CA certificates to register as the tenant's trust anchors |
| `--out` | string | — | also write the admin's certificate to this directory as `admin-cert.pem` |

Exactly one of `--admin-csr` and `--admin-cert` is required. The admin keeps
its own key either way: it makes a CSR with `qrdb ca csr`
([../certificate-authority/cli.md](../certificate-authority/cli.md)) and hands
the operator only `csr.pem`.

```bash
# the admin, on the admin's machine:
qrdb ca csr --username alice --out ./alice

# the platform operator:
qrdb tenant create --id acme --name "Acme Corp" \
     --admin-username alice --admin-csr alice/csr.pem --out ./acme
qrdb tenant create --id secure-co --name "Secure Co" --isolation-type volume \
     --admin-username alice --admin-csr alice/csr.pem
qrdb tenant create --id big-co --name "Big Co" --isolation-type raft \
     --admin-username ops --admin-cert ops.pem --trust-anchors corp-root.pem
```

There is no flag that pins a directory: a tenant's location is a named volume
pool resolved on each node, never a path.

On a cluster, a `barrier`, `volume` or `raft` create, and a
`qrdb tenant update --isolation-type barrier`, fail as unavailable
(503 / `UNAVAILABLE`,
`cluster feature "per-node-barrier-keys" is not active yet`) until every node
runs a release that initialises its own tenant barrier key; they write
nothing, and a retry after the rollout succeeds. A single-member cluster
activates the feature at its first check, a standalone node never refuses, and
a `prefix` tenant is never refused for it.

A `volume` or `raft` tenant is allocated a directory from the node's volume
pool. The replicated record names the pool and the slot within it, never a
path, and each node resolves that slot to its own directory, `{pool path}/{tenant}`.

The command prints the tenant, the admin's username, the certificate's SPKI
thumbprint and serial (none for `--admin-cert`), and the certificate. Nothing
it prints or writes is a private key.

**Validation comes first.** The CSR's signature and key, the trust anchors, and
an `--admin-cert`'s chain and identity (its CN must be `--admin-username` and
it must name the new tenant) are all checked before anything is written, so a
refused credential leaves no tenant behind and the id stays usable. A failure
after that point undoes what was written — but undoing a tenant deletes it,
and a deleted id is retired (below), so that id cannot be used again.

**When the admin's account exists.** For a `prefix` tenant the account is
created at once, with the tenant's Admin role and the certificate bound. A
`barrier`, `volume` or `raft` tenant keeps its accounts behind its own barrier,
which does not exist yet, so the command reports that the admin's account is
created when the admin runs `qrdb tenant barrier-init` with this certificate —
and only this certificate may run it.

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

It refuses reserved ids outright — `system`, `default`, `custodian`, `security-officer`, anything starting with
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

### `qrdb tenant list-deleted`

Lists retired tenant ids — every id that was deleted and can never be used
again — with when each was retired, in ascending id order, one page at a time.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--page-size` | int32 | `0` (server default, 100) | records per page; above the server maximum (1000) is served at the maximum |
| `--page-token` | string | — | the next page token the previous page printed |

```
$ qrdb tenant list-deleted --page-size 2
ID      NAME          ISOLATION  DELETED_AT
acme    Acme Corp     volume     2026-09-20T14:31:07Z
globex  Globex        prefix     2026-09-21T09:02:44Z

Next page token: Z2xvYmV4
$ qrdb tenant list-deleted --page-size 2 --page-token Z2xvYmV4
```

`DELETED_AT` reads `unknown` for a record that could not be decoded; the id is
retired either way. Requires `tenants:list_deleted`, a system-tenant
permission.

### `qrdb tenant reconcile-deleted`

Removes, **from the node the client is connected to**, the directories of
retired tenant ids — what a replica keeps when it was down, or not hosting the
tenant, when the tenant was deleted. No flags.

```
$ qrdb --server node-2.db.example.com tenant reconcile-deleted
Inspected: 3 retired tenant ids
ACTION     TENANT  PATH                          REASON
reclaimed  acme    <install-dir>/pool/acme
skipped    old     <install-dir>/disks/shared    overlaps-live-tenant
```

It never removes a directory a live tenant claims; such a path is reported as
`skipped` with the reason. A directory that is a mount point is emptied and
reported as `reclaimed`; the mount stays. If some tenant ids fail, the rest
are still reclaimed: the report is printed with a `failed` row per failed id
(its reason is the step and the node's message), and the command then exits
non-zero. It acts on one node, so run it against each node of
a cluster. Every node also runs it on its own at boot, so it is needed only to
reclaim disk without a restart. Requires `tenants:reconcile_deleted`, a
system-tenant permission.

### `qrdb tenant update <id>`

| Flag | Type | Meaning |
|---|---|---|
| `--name` | string | new human-readable name |
| `--isolation-type` | string | **`barrier \| prefix` only** |
| `--description` | string | new description; `--description ""` clears it |
| `--metadata` | `key=value`, repeatable | **replaces the whole metadata map** with the given entries |
| `--clear-metadata` | bool | removes every metadata entry; not with `--metadata` |
| `--status` | string | **`active \| suspended \| disabled`**; needs `tenants:set_status` (system-tenant Admin only) |

A flag that is not given leaves its field unchanged. `--metadata` splits each
entry at the first `=`, so a value may contain `=`; an entry with no `=` or an
empty key is refused before anything is sent. The server's limits: a
description of at most 1024 bytes of UTF-8; at most 64 metadata entries; keys
1 to 63 bytes of lowercase letters, digits, `.`, `_` and `-` that begin and end
with a letter or digit (`env`, `cost-center`); values at most 256 bytes of
UTF-8. Over a limit, the update is
refused and nothing changes (`invalid description: exceeds 1024 bytes`).

```
$ qrdb tenant update acme --description "Acme production" \
    --metadata env=prod --metadata team=storage
$ qrdb tenant get acme
ID:         acme
Name:       Acme Corp
...
Description: Acme production
Metadata:   env=prod,team=storage
$ qrdb tenant update acme --description "" --clear-metadata
```

`tenant get` prints the two lines only when set; `tenant list` shows them as
the `DESCRIPTION` and `METADATA` columns, metadata as sorted `key=value`
pairs.

`--status` moves the tenant between `active`, `suspended` and `disabled`; a
disabled tenant cannot move straight to suspended (`409`, reactivate it first),
and any other value is refused (`400`). While a tenant is not active, every
principal of it is refused on every transport (`403` / `PERMISSION_DENIED`,
`tenant is not active`); `--status active` restores access on the next
request. `tenant get` prints `Status:` and `tenant list` a `STATUS` column.

```
$ qrdb tenant update acme --status suspended
$ qrdb tenant get acme
ID:         acme
Name:       Acme Corp
Status:     suspended
...
$ qrdb tenant update acme --status active
```

Note the restriction: `update` accepts only `barrier` and `prefix`, and any
change is refused once the tenant holds data (`ErrTenantHasData`). Changing
`--isolation-type` also needs `tenants:set_isolation`, which only a
`system`-tenant identity can exercise; a tenant's Admin can change `--name`,
`--description` and `--metadata` on its own tenant only.

## Governance

Everything in this section is the tenant's **plan**, and only the platform
operator sets it: `set-entitlement` needs `tenants:set_entitlements` and
`update-quotas` needs `tenants:set_quotas`, both cross-scope, so the caller's
certificate must be a `system`-tenant identity. So does `tenant create`
(`tenants:create`). A tenant's Admin is refused 403 even on its own tenant. See
[api.md](api.md#who-may-call-what).

### `qrdb tenant set-entitlement <id>`

Which tiers and features the tenant's **plan** permits. Checked **before**
quotas.

| Flag | Type | Meaning |
|---|---|---|
| `--isolation-modes` | strings | permitted isolation modes, e.g. `prefix,volume,raft` |
| `--allow-dedicated-raft` | bool | permit dedicated Raft-backed databases |
| `--allow-volume-storage` | bool | permit databases on a dedicated volume |
| `--allow-system-managed-barrier` | bool | permit opting into system-managed auto-unseal |
| `--allowed-volume-pools` | strings | named volume pools the tenant may be placed on; the first is its default; **empty = no pool, fail-closed** |
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

On a tenant created with its first admin, only that admin's certificate may
initialize the barrier, and the admin's account is created as soon as the
barrier is usable. Under `auth.mtls_binding_policy: required` a tenant with no
onboarded admin cannot have its barrier initialized at all.

Over a network transport the command also **proves possession** of the first
admin's key: it asks the node for a nonce (`init/begin`) and signs it, bound
to the connection's TLS 1.3 exporter, with the key in the global `--key-file`
(`init/complete`), all on one connection. Run it with the first admin's own
`--cert-file`/`--key-file`. Without `--key-file` the node's request for a
proof is refused locally (`InitSignerRequiredError`); a key file that cannot
be read is refused before anything is sent. A signature by any other key is
refused by the node with `FirstAdminProofError`. The key is used only to sign
and is never sent. An embedded connection is in process and asks no proof.

A barrier served by several nodes holds its own key on each. A `passphrase`
init reads the serving nodes from `--server`'s `barrier-status` and is sent to
every one of them, over `--server`'s transport at the endpoint each node
publishes, with one result printed per node. Under any other strategy the init
goes to `--server`'s node and the others initialize their own keys from it.

| Flag | Type | Meaning |
|---|---|---|
| `--id` | string | tenant ID |
| `--strategy` | string | `none \| passphrase \| shamir \| <registered hardware id>`; empty = `passphrase` |
| `--passphrase` | string | required for the passphrase strategy; ignored by none/hardware |
| `--threshold` | int | M, shamir only (`0` = default) |
| `--shares` | int | N, shamir only (`0` = default) |
| `--officers` | string, repeatable | Security Officer share holder: `username=path/to/csr.pem` (shamir only) |
| `--custodians` | string, repeatable | key custodian share holder: `username=path/to/csr.pem` (shamir only) |
| `--policy` | string | factor policy for `--officers`/`--custodians`: `designated` (default) or `any-enrolled` |

**Share material is never returned here.** A Shamir tenant barrier is split
to the tenant's enrolled share holders (`--shares` must equal their number),
and each claims their own envelope.

A dedicated tenant's own IAM is encrypted behind the very barrier its share
holders exist to help initialize, so standalone `tenant barrier-officer
register`/`list` (below) cannot reach it beforehand. `--officers`/
`--custodians` name the initial roster **inline**, mirroring `qrdb init`'s
own inline roster for the system barrier: every officer is validated as one
batch, then registered atomically with initialization — a later officer
failing validation refuses the whole call before any of them is registered,
and a failure partway through registration (or in the barrier's own key
split) rolls back every officer already registered. Their combined count
must equal `--shares`. Once initialized, the tenant's IAM is readable, so
later holder changes go through ordinary RBAC via `tenant barrier-officer
register`/`list`, exactly like the system barrier works post-init.
`--officers`/`--custodians` are refused on any strategy but `shamir`.

```bash
qrdb tenant barrier-init --id secure-co --strategy passphrase --passphrase "$PP"
qrdb tenant barrier-init --id secure-co --strategy shamir --threshold 2 --shares 2 \
     --officers alice=alice.csr --custodians bob=bob.csr
```

### `qrdb tenant barrier-status` / `barrier-seal`

| Flag | Type |
|---|---|
| `--id` | string |

### `qrdb tenant barrier-unseal`

The credential unseal. A Shamir tenant barrier refuses it: M of its share
holders unseal it with `qrdb tenant barrier-quorum submit --op unseal`.

| Flag | Type | Meaning |
|---|---|---|
| `--id` | string | tenant ID |
| `--key` | string | base64-encoded barrier credential |

## Tenant share holders

`qrdb tenant barrier-officer`, `barrier-quorum` and `barrier-rekey` mirror the
system barrier's `officer`, `quorum` and `rekey` verbs
([../shamir-custody/cli.md](../shamir-custody/cli.md)) for one tenant, via
`--tenant-id`. Officer verbs: `register`, `list`, `claim`, `claim-begin`,
`claim-complete`, `revoke`. Quorum verbs: `begin`, `submit`.

```bash
qrdb tenant barrier-officer register --tenant-id secure-co \
     --username alice --role so --csr-file alice.csr
qrdb tenant barrier-officer list --tenant-id secure-co
qrdb tenant barrier-quorum submit --tenant-id secure-co --op unseal \
     --username alice --envelope alice.env --key alice.key
```

Applies only to dedicated-isolation tenants.

## Using a tenant

The tenant is taken from **your certificate's OU**, not from a flag. To operate
as a tenant, use that tenant's certificate:

```bash
qrdb --cert-file alice/cert.pem --key-file alice/key.pem kv list app/
```

That certificate is the one `tenant create` returned for the first admin, or
one the tenant's Admin issued later with `qrdb ca issue` — which always issues
into the caller's own tenant. Trust anchors, for a tenant whose users bring
certificates from their own CA, are managed with `qrdb tenant trust-anchors`:
[../trust-anchors/cli.md](../trust-anchors/cli.md).

## Strict isolation

No CLI verb reads or sets an isolation policy. A FIPS 140-3 build raises the
cluster floor to `strict` when the node starts. Design:
[architecture.md](architecture.md#strict-tenant-isolation--design).
