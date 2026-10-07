# Audit — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-05

Who did what, when, and whether it worked. Auditing is on by default.

## Prerequisites

- The `audit:query` permission — the Auditor built-in role has it.
- To verify the tamper-evidence hash chain (`qrdb audit verify` and its
  transport equivalents below), the `audit:verify` permission instead. It is
  a system-tenant-only, cross-scope permission — the hash chain covers the
  whole node's audit trail, not a tenant-filtered slice of it — so a
  tenant-scoped Auditor cannot verify even their own tenant's events; only a
  system-tenant principal holding `audit:verify` (the Security Officer and
  Auditor built-in roles have it) can.
- A node with an audit sink wired. The daemon always builds one: a hash-chained
  NDJSON file beside the data directory, an in-memory ring buffer, and a mirror
  to the operational log stream.

Everything in this guide reads the **node-local** trail above. A separate,
cluster-wide replicated audit chain exists internally
([architecture.md](architecture.md#cluster-wide-replicated-trail)) but has no
user-facing surface yet — nothing proposes writes to it, and there is no
`qrdb audit` command or API that reads it.

The only audit-related setting is the failure posture, and it lives under
`auth:` because that is where it is enforced:

```yaml
auth:
  audit_failure_policy: "fail-closed"   # the default; see "When the audit disk fills"
```

> **Correction (now applied throughout).** Earlier revisions of this guide
> described an `audit:` block with `enabled`, `kv_backend`, `file_backend` and
> `file_path`, and a KV-backed store queried by the CLI. **No such
> configuration or KV backend has ever existed.** `audit_failure_policy` above
> is the only audit-related key. The stale `kv_backend` references further down
> have been corrected rather than flagged.

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

**Your tenant's activity.** Every query reads only your own tenant's events —
`--tenant-id` can name your own tenant and nothing else. An Admin of `acme`
sees `acme`'s trail; an operator in the `system` tenant sees the `system`
tenant's events and the platform events that belong to no tenant, and not a
customer tenant's.

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

**Read the cluster-wide replicated chain.** Every network node answers the
SAME tenant's replicated trail identically, unlike the node-local file above
-- useful when you want one tenant's trail without caring which node you
happen to be talking to. Your tenant must be entitled to it
(`AllowReplicatedAudit`, off by default; ask your operator) or the query
answers empty rather than an error.

```bash
qrdb audit query --replicated --from 2026-08-01T00:00:00Z
```

**Verify the replicated chain's hash continuity for your tenant.**

```bash
qrdb audit verify --replicated
```

A `Genesis: false` result means the chain has been pruned (retention), not
tampered with -- the same reading [Troubleshooting](#troubleshooting) below
gives for the node-local verify's own `Genesis: false`. See
[architecture.md](architecture.md#chain-hash-continuity-verification) for
what this check does and does not prove.

## What is recorded

Identity changes, role changes, certificate issuance and revocation, every
barrier operation, cluster administration, **every share-holder event** --
enrolments, envelope claims, quorum submissions, rekeys and revocations, in
every scope, including the unseal quorum a sealed node gathers before it
starts -- and **every permission denial**.
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

**`audit query --tenant-id <t>` is refused with a permission error.** `<t>` is
not the tenant your credential is in. Nobody reads another tenant's audit
events — the `system` tenant included. Drop `--tenant-id` to read your own, or
ask that tenant's own auditor.

**`audit query` returns nothing and you know things happened.** Querying needs
no configuration and is never "off", so the cause is the filter rather than the
storage. Check the time window first: `--from`/`--to` bound the scan, and a
window that predates the active `audit-events.ndjson` finds nothing even though
the events happened — the file rotates at its size bound, and `Query` scans the
ACTIVE file, not the archives. Then check the other filters, remembering that
`--actor`, `--resource` and `--action` must match the recorded values exactly.

**`--outcome failure` returned nothing but there were failures.** Outcomes
include a reason, like `failure:leader unavailable`. Prefix matching handles
this; check you are not passing the full string.

**You expect more than 1000 results.** That is the server's hard cap per page.
Follow `next_cursor`.

**The audit log is enormous.** The file sink rotates at its size bound and
deletes archives older than its retention window (90 days by default) when it
opens, so the active directory is bounded — but nothing moves those archives
off the node before they are deleted. **Archiving off the node is your
operational responsibility**, and the retention window is a deletion deadline,
not a backup.

**Audit entries do not appear in `kv list`.** They are not in the key-value
store at all. The trail is a file, `audit-events.ndjson` in the data directory,
and the in-memory ring behind `audit query`.

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

**You want to verify the chain.** Run `qrdb audit verify`. It walks the active
file and every rotation archive as one sequence and reports whether the chain
checked out — see the note under "You need to be sure nobody edited the
trail" below, and [cli.md](cli.md) for the full command reference. It is also
reachable from every transport (REST `GET /v1/audit/verify`, gRPC/Unix
`AuditService.VerifyChain`, QUIC `audit.VerifyChain`, the MCP tool
`qrdb_audit_verify`, and the embedded SDK's `Client.Audit().VerifyChain`), so
a compliance harness can verify remotely rather than only from a shell on the
node.

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
recomputing the chain. Run `qrdb audit verify` — it recomputes the chain
across the whole rotation set for you and reports which file and record index
broke, if any.

**A verification failure is a tamper finding, not a request failure.** A
response with `"verified": false` (or, from the CLI, a non-zero exit) means
the chain did not check out; the report also says whether the walk reached
the chain's origin (`"genesis": true`) and, when not, that is what pruned
retention looks like too — see "One clean result is not a complete one"
above. A verification call that instead returns an ERROR (not a result with
`verified: false`) means the check could not be PERFORMED at all — the
configured logger holds no hash chain (`RingBuffer`, `NoopLogger`, or
`SlogSink` alone, without a `ChainFileLogger`), the file is missing, or the
sink was mid-rotation when the walk ran (retry). Do not read that error as a
clean trail.
