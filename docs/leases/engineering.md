# Leases — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go`:

```go
func (c *Client) Lease() transport.LeaseClient
```

`sdk/go/transport`:

```go
type LeaseClient interface {
	LeaseGrant(ctx context.Context, ttl int64) (*LeaseInfo, error)
	LeaseRevoke(ctx context.Context, leaseID int64) error
	LeaseKeepAlive(ctx context.Context, leaseID int64) (*LeaseInfo, error)
	LeaseTimeToLive(ctx context.Context, leaseID int64) (*LeaseInfo, error)
	LeaseList(ctx context.Context) ([]*LeaseInfo, error)
}

type LeaseInfo struct {
	ID           int64    `json:"id"`
	TTL          int64    `json:"ttl"`
	GrantedTTL   int64    `json:"granted_ttl"`
	RemainingTTL int64    `json:"remaining_ttl"`
	Keys         [][]byte `json:"keys,omitempty"`
}
```

Response envelopes: `LeaseGrantResponse`, `LeaseKeepAliveResponse`,
`LeaseTimeToLiveResponse`, `LeaseListResponse` — each wrapping `Lease` or
`Leases`.

Attachment is on the write side, not here:

```go
// KV
Put(ctx, key, value, opts...)                        // lease via WriteOption
// MVCC
PutWithLease(ctx, key, value, leaseID, opts...) (*MVCCKeyValue, error)
```

The resulting `MVCCKeyValue` carries the `lease` field, so a reader can see
which lease a key hangs off.

## Invariants

- **Expiry is a committed Raft operation, not a local timer.** Every replica
  observes the cascade delete at the same log position. No node expires a key on
  its own clock.
- **`GrantedTTL` is immutable for the life of the lease**; `LeaseKeepAlive`
  resets `RemainingTTL` to it, it does not change the grant.
- **Attachment is many-to-one**: many keys, one lease. Deleting the lease
  deletes all of them.
- **Reading or writing a leased key does not renew the lease.** Only
  `LeaseKeepAlive` does.
- **Revoke is immediate and idempotent from the caller's view**: revoking an
  already-expired lease is not an error worth distinguishing, and both end with
  the lease gone.
- **A lease ID is meaningful only within its (tenant, database) scope.**
- **TTL granularity is seconds.**

## Error semantics

Typed errors; `service.LeaseExistsError` and its siblings are matched by type at
the transport boundary, never by string.

| Condition | Behavior | Fails closed? |
|---|---|---|
| Lease ID not found (expired, revoked, or never existed) | not-found | yes |
| Attaching a write to an unknown lease | the write is **refused** | **yes** — a key silently written without its lease would never expire |
| `ttl <= 0` on grant | invalid-argument | yes |
| Missing `lease:*` permission | permission denied | **yes** |
| Barrier sealed | refused | **yes** |
| Keep-alive against an already-expired lease | not-found; the keys are already gone | **yes** |

The refusal on attaching to an unknown lease is the important one: accepting the
write and dropping the association would produce a key that outlives its owner
with no way to notice.

## Configuration

No lease-specific configuration keys. The sweep runs on the leader; its cadence
follows the Raft tick rather than a separate setting.

## Persistence

Lease objects live in the internal namespace and are replicated like any other
state-machine record. They are **not** visible through user-facing KV
enumeration — see the internal-key guard in
[../key-value/engineering.md](../key-value/engineering.md). Attachment is stored
on the key's MVCC record (`lease` field), so a snapshot restores keys and their
lease bindings together.

## Test strategy

- **Unit**: `pkg/lease` — grant, keep-alive resetting `RemainingTTL` to
  `GrantedTTL`, revoke cascade, list, and expiry sweep. Each function has a
  happy path and an error path.
- **No `time.Sleep`.** Expiry is exercised by driving the clock/tick seam, not
  by waiting — the repo rule and the only way these tests stay deterministic on
  a loaded runner.
- **Integration**: lease parity across transports in the standalone stage.
  Multi-node expiry (leader change mid-lease) belongs to the cluster stage,
  which is **not currently wired into CI**.
- **Fault injection**: the leader seam allows a leadership change to be forced
  between grant and expiry.

## Observability

- Lease grant, keep-alive, revoke and expiry are audited through
  [../audit/](../audit/).
- Cascade deletes appear as ordinary delete events on
  [../watch/](../watch/) streams, which is how a consumer learns a session ended.
