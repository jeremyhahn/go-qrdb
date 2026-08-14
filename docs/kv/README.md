<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Key-Value Operations

The KV service provides distributed, versioned key-value storage with Raft consensus, multi-version concurrency control (MVCC), and optional point-in-time restore capabilities.

## Overview

All KV operations are replicated through Raft consensus and stored in the configured backend (Pebble, in-memory, or file-based). The service supports:

- **Simple KV**: Put, Get, Delete, List, Scan (immediate consistency)
- **MVCC operations**: As-of reads, revision metadata, compaction (requires `raft.sm_type: mvcc`)
- **Selective restore**: Logical undo of bounded key sets to an earlier revision (requires `raft.sm_type: mvcc`)

## Configuration

KV operations depend on two settings:

| Setting | Values | Effect |
|---------|--------|--------|
| `raft.sm_type` | `pebble` (default), `memory`, `mvcc` | State machine wrapper type. Only `mvcc` supports as-of reads, watch replay, and selective restore. |
| `storage.backend` | `pebble`, `file`, `memory` | Underlying storage engine. |

For point-in-time restore and as-of reads, you must configure:

```yaml
raft:
  sm_type: mvcc

mvcc:
  compaction_retention: 1000  # Minimum revisions to retain
```

If your daemon runs the default `raft.sm_type: pebble`, all restore and as-of read operations return a clear error: `service: requires raft.sm_type: mvcc` or `service: backend does not support MVCC operations`.

## As-Of Reads

As-of reads retrieve the value of a key at an earlier MVCC revision, without changing live state. They are read-only snapshots of historical data.

### Semantics

- The requested revision must be at or below the store's current committed revision (a future revision is rejected as invalid).
- The requested revision must be above the compaction floor (revisions below `current - compaction_retention` have been discarded and are not readable).
- A key that did not exist at the target revision returns not-found, same as asking for a key that was never written.
- The read is consistent: the store is not modified.

### CLI

```bash
# Read current value with full MVCC metadata (revision, create-time, mod-time)
qrdb kv get mykey --revision

# Read at a specific historical revision (no current data shown)
qrdb kv get mykey --at-revision 42

# Range scan at a historical revision
qrdb kv range start-key end-key --revision 42
```

### SDK / API

| Transport | Method | Endpoint |
|-----------|--------|----------|
| gRPC/QUIC/Unix | `GetAtRevision(key, revision)` | `/kv/get-at-revision` |
| REST | `GET /kv/{key}?revision=R` | (query parameter) |
| Embedded SDK | `mvcc.GetAtRevision(ctx, key, revision)` | N/A |

### Availability

As-of reads are supported by the MVCC state machine (`raft.sm_type: mvcc`) and require the `mvcc:get_at_revision` permission. The default Pebble state machine does not track revision history and refuses as-of reads with an error.

---

## Selective Point-in-Time Restore

Selective restore is a logical undo operation: it returns a bounded set of user keys to the value they held at an earlier MVCC revision by proposing new writes through Raft consensus. It is **not** a cluster rewind or a substitute for full backup/restore.

### Key Semantics

**Logical undo, not Raft rewind:**
- Every change the plan makes is applied as an ordinary write through the normal service write path.
- Raft consensus and the index infrastructure process each change independently (not atomic across keys).
- Committed history is never discarded — the restore moves the keyspace forward to an earlier logical state.

**Scope is bounded and required:**
- Exactly one of `--prefix` or `--keys` must be supplied. An empty scope (neither flag) or an ambiguous one (both) is refused.
- The blast radius is capped at 10,000 keys per restore. A scope that matches more is refused whole, never silently truncated.

**Internal keys are unreachable:**
- The internal namespace (`__sys:*`, `ts:*`, `idx:*`, `retention:*`) cannot be restored. Attempting to do so (either by explicit key or by a prefix that begins with a reserved prefix) is refused with a 403 Forbidden.

**Revisions must be in history:**
- The target revision must be positive, at or below the store's current revision, and above the compaction floor.
- Reading below the compaction floor fails closed with a clear error.

### Plan and Apply

Restore is a two-phase operation: the plan is computed from a point-in-time snapshot, then applied afterwards.

**Plan phase** (always computed):
- Reads the current state at the target revision using as-of reads.
- Identifies which keys to revert, recreate, or delete.
- Returns the plan (ACTION, KEY, FROM-REV, TO-REV, VALUE-SIZE) without writing anything.
- Dry-run is the plan-only variant.

**Apply phase** (unless `--dry-run`):
- By default (CAS mode): Each change is committed only while the key still matches the state the plan observed. A key that has been written since planning is left untouched and reported as a CONFLICT.
- With `--force`: Each change overwrites unconditionally, regardless of concurrent writes.
- Changes are applied as ordinary Raft proposals, so each is independently committed. Partial application is possible; retry is safe.

### Actions

| Action | Scenario | Semantics |
|--------|----------|-----------|
| `revert` | Key exists both now and at target rev, with a different value | Write the historical value back under the key. |
| `recreate` | Key existed at target rev, is deleted now | Put the historical value back under the key. |
| `delete` | Key exists now, did not exist at target rev | Remove the key (it was added after the target revision). |

### Prefix Restore

A prefix-scoped restore finds every key in the scoped range and recovers deleted keys using the MVCC revision index. The index records deletes as first-class changes, so a key deleted since the target revision is restorable (the range scan alone cannot see it).

**Backend requirement:** Prefix restore requires a durable revision index. The MVCC state machine maintains this automatically. A backend (future variant) without the index returns a clear refusal: `service: prefix-scoped restore requires the MVCC revision index`.

### Authorization

Selective restore requires the `mvcc:restore` permission (a privileged bulk mutation, separate from read permissions). It is granted only to **Admin** and **Operator** roles, never to User, Auditor, or Security Officer (who holds no data-plane verb at all). It respects the caller's tenant and database namespace: a restore always operates within the caller's scoped keyspace.

### Exit Code Semantics

When one or more keys conflict and `--force` was not set, the command exits non-zero even though the request itself succeeded. This is deliberate: a partially applied restore that exits 0 reads as a complete undo, which could mislead automation. Re-run with `--force` or re-run again once the conflicting writers have quiesced.

---

## Usage Examples

### As-Of Read: Query Historical Data

```bash
# Current value
$ qrdb kv get app:config --revision
Key: app:config
Value: {"version": 2}
Revision: 1042

# What was it at revision 1000?
$ qrdb kv get app:config --at-revision 1000
Key: app:config
Value: {"version": 1}
Revision: 987
```

### Restore: Recover from Bulk Delete

Accidentally deleted all keys matching `session:*`?

```bash
# First, check what the restore plan would be
$ qrdb kv restore --revision 950 --prefix session: --dry-run
ACTION    KEY                FROM-REV  TO-REV  VALUE-SIZE
recreate  session:abc123     0         1002    128
recreate  session:def456     0         998     256
recreate  session:ghi789     0         1005    64

# No conflicts shown; safe to apply
$ qrdb kv restore --revision 950 --prefix session:
Applied: 3, Skipped: 0, Conflicts: 0
```

### Restore: Revert a Key Modified Since Planning

Live writers are updating keys. The plan sees version A, but by the time the restore applies, a writer has committed version B.

```bash
# Plan computed against current state
$ qrdb kv restore --revision 900 --keys user:1 --dry-run
ACTION   KEY     FROM-REV  TO-REV  VALUE-SIZE
revert   user:1  2150      1899    512

# Apply with CAS (default): compare-and-swap
$ qrdb kv restore --revision 900 --keys user:1
Applied: 0, Skipped: 0, Conflicts: 1
KEY     FROM-REV-PLAN  FROM-REV-ACTUAL
user:1  2150           2151

# The key moved on. Override and rewrite it
$ qrdb kv restore --revision 900 --keys user:1 --force
Applied: 1, Skipped: 0, Conflicts: 0
```

### API: Restore via REST

```bash
curl -X POST https://localhost:63002/kv/restore \
  -H 'Content-Type: application/json' \
  -d '{
    "revision": 950,
    "prefix": "session:",
    "dry_run": true
  }'
```

### API: Restore via the Go SDK

The SDK exposes restore on every transport (gRPC, REST, QUIC, unix, MCP, embedded);
the call is identical regardless of which one the client was built with. `Prefix`
and `Keys` are `[]byte`/`[][]byte` so keys stay binary-safe on the wire.

```go
result, err := client.MVCC().RestoreToRevision(ctx, &transport.RestoreRevisionRequest{
  Revision: 950,
  Prefix:   []byte("session:"),
  DryRun:   true,
})
if err != nil {
  return err
}
fmt.Printf("plan: %d changes, %d skipped, %d conflicted\n",
  len(result.Changes), result.Skipped, result.Conflicted)
```

Apply the plan by setting `DryRun: false`. Keys modified since the plan was
computed come back in `result.Conflicts` and are not written; set `Force: true`
to overwrite them.

---

## Web UI Restore Workflow

The KVExplorer page in the web UI offers a guided restore workflow:

1. **Form**: Enter the target revision and choose a scope (prefix or explicit keys).
2. **Preview**: Review the computed plan (action, key, from-revision, to-revision, value size). This is the plan that will be applied; you must confirm it.
3. **Result**: Apply the plan and see the outcome (applied count, conflicts, skipped). If conflicts occurred and `--force` was not used, a "Retry with Force" button is offered.

The workflow always previews before applying, so accidental whole-keyspace restore (the unscoped case) is structurally impossible.

---

## Limits and Guards

| Aspect | Limit | Behavior |
|--------|-------|----------|
| Keys per restore | 10,000 | Scope exceeding this is refused; never silently truncated. |
| Reachable revisions | `compaction_retention` | Revisions below `current - compaction_retention` fail closed. |
| Scope | Exactly one of prefix or keys | Neither or both is refused. |
| Internal keys | None | Explicit internal keys and prefixes are refused. |

---

## Compaction

MVCC compaction discards old versions beyond the retention window, freeing storage and improving read performance. The `mvcc.compaction_retention` setting controls the minimum number of revisions to keep.

```yaml
mvcc:
  compaction_retention: 1000  # Default: keep at least the last 1000 revisions
```

### Implications

- As-of reads targeting a revision below `current - compaction_retention` fail with `RevisionCompactedError`.
- Restores targeting a revision below the compaction floor also fail with `RevisionCompactedError`.
- To extend the restore window, increase `compaction_retention` and restart the daemon; it will retain more history on the next compaction cycle.

---

## Permissions

| Operation | Permission | Granted to |
|-----------|-----------|------------|
| Simple KV (put, get, delete, list, range) | `kv:put`, `kv:get`, `kv:delete`, `kv:list`, `kv:range` | User, Operator, Admin |
| As-of reads | `mvcc:get_at_revision` | User, Operator, Admin |
| Compaction | `mvcc:compact` | Operator, Admin (system-scoped; global operation) |
| Selective restore | `mvcc:restore` | Operator, Admin (tenant-scoped; per-tenant keyspace only) |

---

## Error Types

### Request Validation Errors (4xx/InvalidArgument)

| Error | Condition |
|-------|-----------|
| `RestoreNilRequestError` | Request is nil. |
| `RestoreRevisionInvalidError` | Revision is not positive. |
| `RestoreRevisionFutureError` | Revision is ahead of the store's current revision. |
| `RestoreScopeEmptyError` | Neither prefix nor keys supplied. |
| `RestoreScopeAmbiguousError` | Both prefix and keys supplied. |
| `RestoreEmptyKeyError` | Explicit key list contains an empty string. |
| `RestoreScopeTooLargeError` | Scope matches more than 10,000 keys. |
| `RestoreUnboundedPrefixError` | Prefix has no finite upper bound (all-0xFF bytes). |

### Authorization Errors (403/PermissionDenied)

| Error | Condition |
|-------|-----------|
| `RestoreInternalKeyError` | Scope includes the internal namespace (`__sys:*`, etc.). |
| Permission denied | Caller lacks `mvcc:restore` permission. |

### Runtime Errors (5xx/Internal)

| Error | Condition |
|-------|-----------|
| `RestoreChangeIndexUnavailableError` | Prefix restore attempted on a backend without revision index (prefix-only; explicit keys still work). |
| `RestoreApplyError` | Write failed partway through the plan. Includes the number of changes already committed; safe to retry. |
| `RevisionCompactedError` | Target revision is below the compaction floor. |

---

## FAQ

**Q: Can I restore the entire keyspace?**  
A: No. An unscoped restore (neither prefix nor keys) is refused, by design. The "obvious" default — the whole keyspace — is the one scope no operator should reach by omission. Specify a prefix or explicit key list.

**Q: What if restore fails halfway through?**  
A: A `RestoreApplyError` is returned with the number of changes already committed. Those changes are permanent (committed through Raft consensus). You can safely re-run the identical restore request; it will recompute the plan against the then-current state and skip the already-applied changes.

**Q: Can I restore the barrier configuration or user roles?**  
A: No. The internal key namespace (`__sys:roles`, `__sys:users`, etc.) is off-limits. Restoring a revoked permission or a deleted administrator would be a privilege escalation via the data plane.

**Q: Does restore block writes?**  
A: No. Restore is online and non-blocking. Live writes can proceed concurrently. A key modified between planning and applying is left untouched (reported as a conflict) in CAS mode, or overwritten in force mode.

**Q: Why do I need `raft.sm_type: mvcc` for restore?**  
A: The default `pebble` state machine discards version history for performance. Only the MVCC state machine retains enough history to reconstruct a key's value at an earlier revision. Restore without history is impossible.

---

## See Also

- [Configuration](../12-configuration.md) — `raft.sm_type` and MVCC settings
- [Permissions](../permissions/README.md) — Permission system and `mvcc:restore` details
- [Roles](../auth/roles.md) — Admin and Operator role definitions
- [CLI](../cli/README.md) — `qrdb kv restore` command reference
