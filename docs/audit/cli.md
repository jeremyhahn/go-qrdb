# Audit — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb audit` — query and verify the compliance-grade audit
event log. All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

There are two verbs: querying (`audit:query`) and verifying the tamper-evidence
hash chain (`audit:verify`). Audit entries are written by the
subsystems that emit them and there is no CLI path to create, edit or delete
one — which is the point.

Both verbs read the **node-local** trail by default. A second, cluster-wide
replicated audit chain exists at the storage/Raft layer
([architecture.md](architecture.md#cluster-wide-replicated-trail)); both
verbs take a `--replicated` flag that reads it instead, per-tenant opt-in via
the tenant's `AllowReplicatedAudit` entitlement (off by default — see
[architecture.md](architecture.md#opt-in-not-default-on)). No verb writes
either trail.

## `--replicated`

Both `qrdb audit query` and `qrdb audit verify` accept a `--replicated` flag.
With it:

- `qrdb audit query --replicated` reads the cluster-wide replicated chain —
  Raft-replicated, so the SAME tenant's trail reads identically from any
  node in the group, unlike the node-local file. A tenant whose
  `AllowReplicatedAudit` entitlement is off sees an empty result
  (`Total: -1`), never an error. A node with no replicated audit configured
  refuses the query as not implemented.
- `qrdb audit verify --replicated [--tenant-id ID]` verifies the hash-chain
  CONTINUITY of the replicated chain for one tenant (the caller's own by
  default; `--tenant-id` names another and is refused unless it IS the
  caller's), and requires `audit:verify_replicated`. This is a different check from the node-local `verify`'s
  rotation-archive walk — see
  [architecture.md](architecture.md#chain-hash-continuity-verification) for
  what it means and what it cannot prove. A tenant not entitled to the
  replicated chain is refused outright (a request failure, not a report),
  because there is no chain to verify.

```bash
# Read the replicated chain for the caller's own tenant
qrdb audit query --replicated --from 2026-08-01T00:00:00Z

# Verify the replicated chain's hash continuity
qrdb audit verify --replicated

# Verify another tenant's replicated chain (refused unless it is yours)
qrdb audit verify --replicated --tenant-id acme
```

Default-format output for `qrdb audit verify --replicated`:

```
Verified: true
Records: 1842
Head: 1842
Low: 1
Genesis: true
```

`Genesis: false` means the chain has been pruned (retention), not tampered
with: the surviving tail still verifies, but the walk cannot prove anything
about the sequence numbers that were deleted — the replicated-chain analogue
of the node-local verify's own `Genesis: false` case.

## `qrdb audit query`

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--from` | string | — | start of the time range, RFC3339 |
| `--to` | string | — | end of the time range, RFC3339 |
| `--actor` | string | — | filter by actor identity |
| `--action` | string | — | filter by action |
| `--resource` | string | — | filter by resource |
| `--outcome` | string | — | `success`, or a `failure`/`denied` **prefix** |
| `--tenant-id` | string | — | your own tenant; any other is refused. Omitted, the query reads your own tenant |
| `--limit` | int | server default **100**, max **1000** | maximum events |
| `--cursor` | string | — | pagination cursor from a previous response |
| `--replicated` | bool | `false` | read the cluster-wide replicated chain instead of the node-local trail |

```bash
# Everything in a window
qrdb audit query --from 2026-08-01T00:00:00Z --to 2026-08-02T00:00:00Z

# What did this identity do?
qrdb audit query --actor admin --from 2026-08-01T00:00:00Z --output json

# Every denial — the query that matters most
qrdb audit query --outcome denied --from 2026-08-01T00:00:00Z --limit 1000

# Everything that touched the barrier
qrdb audit query --resource barrier --output json
```

`--outcome` matches `success` exactly, or a **prefix** of `failure` / `denied`
— outcomes carry a reason (`denied:missing permission`), so prefix matching is
how you select a class of them.

## Paging through a large result

```bash
CURSOR=""
while :; do
  OUT=$(qrdb audit query --from 2026-08-01T00:00:00Z --limit 1000 \
        ${CURSOR:+--cursor "$CURSOR"} --output json)
  echo "$OUT" | jq -c '.events[]'
  CURSOR=$(echo "$OUT" | jq -r '.next // empty')
  [ -z "$CURSOR" ] && break
done
```

The server caps `--limit` at 1000; a wider query must be paged.

## `qrdb audit verify`

Verify the audit log's tamper-evidence hash chain across the active file and
every rotation archive. Without `--replicated` the target is always the
server's own configured trail and `--tenant-id` is ignored. **Requires the
`audit:verify` permission.**

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--replicated` | bool | `false` | verify the cluster-wide replicated chain instead of the node-local trail |
| `--tenant-id` | string | the caller's own | tenant whose replicated chain to verify (`--replicated` only); any other tenant is refused |

```bash
qrdb audit verify
qrdb audit verify --output json
```

Default-format output:

```
Verified: true
Files: 3
Records: 1842
Genesis: true
Order: audit-events-2026-08-10-000.ndjson, audit-events-2026-08-11-000.ndjson, audit-events.ndjson
```

**A tamper finding exits non-zero.** `verify` prints the report either way,
but when `Verified: false` the command also returns a non-zero exit status —
useful in a monitoring script or a cron job, distinct from a genuine failure
to run the check at all (see below).

**Exit status is not the same signal as a caught error.** A run that could
not PERFORM the check — the configured logger holds no hash chain, the file
is missing, or a rotation raced the walk — reports the underlying error
directly (`transport: method not implemented: VerifyChain`, a `503`-class
message for a rotation race) and is not a report with `Verified: false`. Read
the message before treating any non-zero exit as a tamper finding; see
[user.md](user.md) for how to tell the two apart.

## What gets audited

Identity (resources `user:<id>` and `role:<id>`, actions `create`, `update`,
`delete` and the rest), certificates (`cert.issue`, `cert.enroll`,
`cert.revoke`),
every barrier (`initialize`, `unseal`, `seal`, `quorum-complete`,
`distribute` and the rest, resource `barrier`), share holders (`holder-register`, `holder-claim`, `holder-quorum-submit`,
`holder-rekey-start`, `holder-revoked`, `holder-unenrol`, resource
`share-holder`), and **every
authorization decision** (action `auth.authorize`, resource the permission,
outcome `denied` for a denial).

```bash
# Who submitted shares to the last unseal quorum?
qrdb audit query --action holder-quorum-submit
```

## Server-side configuration

**There is nothing to configure, and no `audit:` block.** Auditing is always
on. The node composes the trail unconditionally — before the user and role
stores, and regardless of `--auth-enabled` — because FIPS, FedRAMP, PCI-DSS and
HIPAA all require a trail in environments where RBAC itself is switched off.

The trail is written to `audit-events.ndjson` in the data directory: one JSON
object per line, hash-chained, for a log shipper to collect. **There is no
shipping built in.**

The one audit-related key is the failure policy, which is not part of an
`audit:` block:

```yaml
auth:
  audit_failure_policy: fail-closed   # or fail-open
```

It decides what happens when an audit record **cannot be written**, not what is
audited or where it goes. See [user.md](user.md).

## Parity

`qrdb audit query` and `qrdb audit verify` run end to end on every CLI
transport in `test/integration/audit_cli_test.go`. `verify` is Tier-1 across all six transports
(REST, gRPC, QUIC, MCP, Unix, embedded) — see
[api.md](api.md#capability-matrix). The `--replicated` form of each verb
reaches the same six transports plus the Go SDK, through the same
capability matrix's replicated rows.
