# Audit — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Who did what, when, and whether it worked. Auditing is on by default.

## Prerequisites

- The `audit:query` permission — the Auditor built-in role has it.
- **The KV backend enabled** if you want to query from the CLI. The default
  configuration writes to a *file*, which is for a log shipper, not for
  `audit query`.

```yaml
audit:
  enabled: true
  kv_backend: true      # needed for `qrdb audit query`
  file_backend: true
  file_path: ""         # default "audit.log" under the data dir
```

## First run

```bash
qrdb audit query --from 2026-08-01T00:00:00Z --limit 100 --output json
```

Times are RFC3339. Without a range you get the most recent events up to the
limit.

## Common tasks

**Find every denial.** This is the query worth running on a schedule.

```bash
qrdb audit query --outcome denied --from 2026-08-01T00:00:00Z --limit 1000
```

`--outcome` matches `success` exactly, and matches `failure` or `denied` by
prefix — outcomes carry a reason, so the prefix is how you select a class.

**Follow one identity.**

```bash
qrdb audit query --actor admin --from 2026-08-01T00:00:00Z --output json
```

**Everything that touched the barrier.**

```bash
qrdb audit query --resource barrier --output json
```

**One tenant's activity.**

```bash
qrdb audit query --tenant-id acme --from 2026-08-01T00:00:00Z
```

**Page through a big window.** The server caps `--limit` at 1000.

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

## What is recorded

Identity changes, role changes, certificate issuance and revocation, every
barrier operation, cluster administration, and **every permission denial**.
Each entry names the actor, the actor's **role at that moment**, the tenant, the
resource, the action, the outcome, the client IP and a request ID.

Entries are chained by hash, so an edit or a deletion is detectable.

## Shipping the log

The file backend writes one JSON object per line at mode 0600. Point your
collector at it:

```bash
tail -F /var/lib/qrdb/audit.log | your-shipper
```

**There is no built-in shipping**, no syslog output and no remote sink.

## Troubleshooting

**`audit query` returns nothing and you know things happened.** The KV backend
is off — the default is file-only. Entries are being written to
`audit.log`, not into the store, so there is nothing to query. Turn on
`kv_backend`.

**`--outcome failure` returned nothing but there were failures.** Outcomes
include a reason, like `failure:leader unavailable`. Prefix matching handles
this; check you are not passing the full string.

**You expect more than 1000 results.** That is the server's hard cap per page.
Follow `next_cursor`.

**The audit log is enormous.** There is **no retention policy** — nothing prunes
it. Rotating and archiving is your operational responsibility, for both the file
and the KV backend.

**Audit entries do not appear in `kv list`.** They live under `__sys:audit/`,
inside the internal namespace, deliberately invisible to user enumeration.

**One backend is broken and you are worried the other stopped too.** It did not.
The multi-backend fan-out is best-effort: an error from one is recorded, and
every other backend is still written to.

**You want alerting.** Nothing here alerts. Ship the log and alert on it
downstream.

**You need to be sure nobody edited the trail.** Entries carry the hash of the
previous entry, so a gap or an edit breaks the chain. Note the source describes
hash-chain computation and verification as a phased feature — confirm it is
live in your build before relying on it as a control.
