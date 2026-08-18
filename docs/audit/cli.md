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
| `--tenant-id` | string | — | filter by tenant |
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

Auditing is **on by default**, writing JSON to a file:

```yaml
audit:
  enabled: true         # master switch
  kv_backend: false     # also store entries in the KVStore
  file_backend: true    # write JSON audit log to a file
  file_path: ""         # default "audit.log", relative to the data dir
```

With `kv_backend: true` the entries become queryable through `audit query`. The
file backend produces line-delimited JSON at mode 0600 for a log shipper to
collect — **there is no shipping built in**.

## Parity

`qrdb audit query` has a `CommandDefinition` in the integration parity suite.
