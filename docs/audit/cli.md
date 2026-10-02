# Audit — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Command group: `qrdb audit` — query the compliance-grade audit event log.
**Requires the `audit:query` permission.** All verbs accept the root persistent
flags documented in [../key-value/cli.md](../key-value/cli.md#global-flags).

There is exactly one verb: querying. Audit entries are written by the
subsystems that emit them and there is no CLI path to create, edit or delete
one — which is the point.

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
  CURSOR=$(echo "$OUT" | jq -r '.next_cursor // empty')
  [ -z "$CURSOR" ] && break
done
```

The server caps `--limit` at 1000; a wider query must be paged.

## What gets audited

Identity (`user.*`, `role.*`), certificates (`cert.generate`, `cert.revoke`),
the barrier (`seal.initialize`, `unseal`, `seal`, `rotate`, `rekey`), cluster
administration (`admin.drain`, `admin.snapshot`, `admin.member.add/remove`), and
**every permission denial** (`auth.denied`).

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

> An earlier version of this page documented an `audit:` block with `enabled`,
> `kv_backend`, `file_backend` and `file_path`, and said that `kv_backend: true`
> makes entries queryable. **None of those keys has ever existed**, and setting
> them does nothing. The claim was also backwards in a way that mattered:
> `audit query` works today, against the file and the in-memory ring, with no
> KV backend anywhere.

## Parity

`qrdb audit query` has a `CommandDefinition` in the integration parity suite.
