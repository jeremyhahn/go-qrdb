# Audit — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-11

Who did what, when, and whether it worked. Auditing is on by default.

## Prerequisites

- The `audit:query` permission — the Auditor built-in role has it.
- A node with an audit sink wired. The daemon always builds one: a hash-chained
  NDJSON file beside the data directory, an in-memory ring buffer, and a mirror
  to the operational log stream.

The only audit-related setting is the failure posture, and it lives under
`auth:` because that is where it is enforced:

```yaml
auth:
  audit_failure_policy: "fail-closed"   # the default; see "When the audit disk fills"
```

> **Correction.** Earlier revisions of this guide described an `audit:` block
> with `enabled`, `kv_backend`, `file_backend` and `file_path`, and a KV-backed
> store queried by the CLI. **No such configuration or KV backend exists.**
> References to `kv_backend` further down are stale for the same reason and are
> being corrected document-by-document.

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

**The audit log is enormous.** The **file** sink rotates at its size bound and
deletes archives older than its retention window (90 days by default) when it
opens — so the active directory is bounded, but nothing moves those archives
off the node before they are deleted. The **KV** backend has no retention at
all: nothing prunes `__sys:audit/`. Archiving off the node is your operational
responsibility for both.

**Audit entries do not appear in `kv list`.** They live under `__sys:audit/`,
inside the internal namespace, deliberately invisible to user enumeration.

**One sink is broken and you are worried the others stopped too.** They did not.
The fan-out attempts every sink: the first error is reported, and every other
sink is still written to.

**Every request suddenly returns 503 saying "audit log failed".** The node
cannot write its audit trail, and it is refusing to authorize anything it
cannot record. That is the default posture and it is deliberate — see *When the
audit disk fills* below.

**Clients cannot even connect, not just "some requests fail".** Expect this and
do not diagnose it as a network fault. The SDK's gRPC, QUIC and Unix transports
perform an authenticated health call at connect time, and that call is an
authorization decision like any other — so under the default posture it is
refused too, and `Connect` fails outright. A node with a full audit disk is
unreachable to those clients, not degraded. The REST `/metrics` endpoint is
**not** affected (it is served before authorization runs), which is why the
counters below remain your way in.

**You want alerting.** Nothing here alerts on the trail's *contents* — ship the
log and alert on it downstream. Two Prometheus counters are the exception, and
you should alert on both, because they are the only signals that survive the
audit plane itself being down:

```
qrdb_auth_audit_write_failures_total{outcome="denied"|"permitted"}
    an authorization decision could not be recorded

qrdb_audit_write_failures_total{component="user"|"role"|"custodian"|...}
    some other subsystem's record could not be written; the label says
    whose trail is now incomplete

qrdb_audit_rotation_failures_total
    the audit file could not be rotated. NOTHING IS LOST -- records keep
    landing -- but the file has stopped honouring its size limit and will
    grow until the disk does not allow it. Check permissions on the audit
    directory and free space. Requests are NOT refused for this.
```

## When the audit disk fills

By default **qrdb refuses an authorization decision it cannot record.** An
unrecorded grant is precisely the event audit exists to prevent, so a node
whose audit sink has failed answers `503 Service Unavailable` (gRPC
`UNAVAILABLE`) with `audit log failed, refusing request: ...` naming the file
that failed.

**Fix the sink.** That is the intended resolution and usually the fast one:

```bash
df -h <data-dir>                  # the audit file lives beside the data dir
ls -l <data-dir>/audit-events.ndjson
# archive rotated files, free space, correct permissions, then restart
```

Watch the counters to confirm:

```
qrdb_auth_audit_write_failures_total{outcome="denied"}   # refused authorization decisions
qrdb_audit_gate_failures_total{component,outcome}        # refused SO-PIN verifications
qrdb_audit_write_failures_total                          # everything else
```

**If you cannot fix it right now**, there is one documented lever. It is a
deliberate, stated choice, not an automatic degradation:

```yaml
auth:
  audit_failure_policy: "fail-open"
```

The node then **serves requests it cannot record**. Understand exactly what
that buys and costs:

- Requests succeed again.
- **Your audit trail acquires a gap** for the whole period, and
  `qrdb_auth_audit_write_failures_total{outcome="permitted"}` is the exact
  number of authorization decisions missing from it.
- **The same setting governs the Security Officer's second factor.** It is one
  posture, not two: with `"fail-open"` an SO PIN can verify and a rekey,
  custodian enrolment or CSR signing can proceed with no record that the PIN was
  ever presented.
  `qrdb_audit_gate_failures_total{component="so-auth",outcome="permitted"}` and
  `{component="so-dual-factor",outcome="permitted"}` size that gap.
- Every startup logs a `WARN` naming the property you gave up. Do not suppress
  it — it is the only standing reminder.
- **For FedRAMP / PCI DSS / HIPAA this is a reportable condition**, not a
  configuration preference. Record when it was set and when it was reverted.

Revert it as soon as the sink is healthy. There is no automatic revert, by
design: the node does exactly what the setting says for as long as it says it.

**An SO request answered `503` is not a bad PIN.** With the default posture, a
node that cannot write its trail answers every SO-gated route `503` — including
the ones you would use to recover it — rather than `401 SO PIN verification
failed`. If an officer reports that their PIN "stopped working", read the status
code first: a `503` whose body says `audit log failed` means the audit sink is
down and the PIN is fine.

A typo in the value **fails the boot** rather than picking a posture for you.
`"fail_open"`, `"true"` and `"permissive"` are all rejected; the accepted
values are `"fail-closed"` (the default) and `"fail-open"`.

**Nothing at all is being audited and requests still succeed.** The node has no
audit sink wired. With no sink there is nothing that could fail, so the
fail-closed posture is inert — it protects a trail you are keeping, and cannot
conjure one you are not.

**You want to verify the chain.** There is **no command for it.** `qrdb audit`
has exactly one subcommand, `query`; the chain-verification routine exists in
the sink but nothing in the daemon or the CLI calls it, so verification is not
an operator-facing control in this build. Treat the `PrevHash` field as
*evidence you can check with your own tooling*, not as a feature you can run.
See the note under "You need to be sure nobody edited the trail" below.

**The chain runs through the archives, not per file.** The first record of a
file written after a rotation carries the digest the previous file's
`chain-anchor` entry recorded, so your tooling verifies the directory as ONE
sequence: check each file's records against each other, then check that each
file's first `prev_hash` really is the `prev_hash` its predecessor's anchor
states. A break at the first record of an archive used to be an artefact of the
verifier and was documented here as expected — **it is not expected now**, and
it means the file before it is missing or was rewritten.

If a verification reports a break, check first whether the node logged
`audit: fsync failed` or `audit: write failed` near that point, and whether
`qrdb_audit_write_failures_total` moved. A record that reached the file while
its fsync failed is a genuine record, and the sink keeps the chain consistent
with the bytes actually on disk across exactly that fault — so a break there
means something else.

**One clean result is not a complete one.** If retention has deleted the oldest
archives, the surviving chain no longer reaches its origin. That is normal, and
it is also what deleting the oldest archives would look like — the two cannot be
told apart from the files alone. If that distinction matters to you, record the
newest `prev_hash` you have seen somewhere off the node, or ship the trail.

**You need to be sure nobody edited the trail.** Entries carry the hash of the
previous entry, so an edit or a deletion *within one file* is detectable by
recomputing the chain.

**Do not rely on this as a live control in this build.** The chain is
*written*, and it is written correctly across write faults, fsync faults,
rotations and restarts — but nothing verifies it for you: there is no command,
no startup check and no alert. Verifying it is your own tooling's job, and that
tooling must walk the archives as one sequence as described above. Chain
verification is a documented gap, not a shipped control.
