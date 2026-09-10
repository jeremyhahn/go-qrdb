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

- **The sweeper is started by the composer, and every clustered composition
  must start it.** `Lessor.RunExpiry` is a loop, not a background service that
  begins on its own; a composition that builds a `Lessor` and hands it to the
  state machine has done half the wiring. All three clustered compositions run
  it — `cmd/qrdbd`, `pkg/server`'s tenant host, and `sdk/go/node` — and a new
  one must too. It takes a NODE-LIFETIME context, never a boot request's ctx,
  which is cancelled as soon as `New`/`Unseal` returns.
- **`lease.mu` guards `expiry` as well as `keys`, and every reader takes it.**
  `KeepAlive` writes `expiry` from the Raft apply goroutine while the sweeper
  reads it from its own; an unlocked read there is a real data race, measured
  under `-race`. The `*Locked` helpers carry the
  caller-holds-the-lock contract and are deliberately NOT wrapped by the
  locking variants: `toLease` already holds `l.mu` when it needs the remaining
  TTL, and `sync.RWMutex` is not reentrant, so a nested `RLock` deadlocks the
  moment a writer queues between the two acquisitions. Lock ordering is
  `Lessor.mu` then `lease.mu`, never the reverse.
- **Assert the reap by reading the STORE, not `Lessor.Leases()`.** That getter
  filters expired leases, so a test asserting on it passes whether or not
  anything was reaped — which is how the embedded gap stayed invisible.
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

`sdk/go/transport.LeaseNotFoundError` / `LeaseExpiredError` cross `embedded`,
`grpc`, `unix`, `quic` and `mcp` as the same two types — `grpc`/`unix` classify
their shared `codes.NotFound` by message text at the lease call sites, `quic`
does the same against its `wire.StatusError` payload, and `mcp` against its
tool-result text, since none of those wires carries a status distinct enough to
tell the two conditions apart on its own.

**REST is the exception, and it is a partial one.** Only `LeaseTimeToLive` maps
a typed lease error (`transport/rest/transport_lease.go` turns a 404 into
`LeaseNotFoundError`). `LeaseRevoke` and `LeaseKeepAlive` go through
`postJSON`/`postJSONDecode`, whose 404 lands in `decodeErrorResponse` and comes
back as a generic `RequestError`. And REST emits **no `LeaseExpiredError` at
all**: `mapHTTPError` sends both the not-found and the expired case to
`http.StatusNotFound`, so the wire cannot tell them apart — a client would have
to parse the body text, which is what #141 exists to eliminate.

Closing that gap means giving REST a status or body field that distinguishes
the two, which is a wire-format change and not a client-side mapping; it is not
done here. `sdk/go`'s
lease monitor (`coordination.go`) is the reason this matters: it detects
external revocation at the revoke rather than at the lease's own expiry only
when the absence arrives typed.

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

### Reading a failure from the sub-TTL renewal test

`sdk/go`'s `TestRaft_LeaseRenewedOnSubTTLCadenceKeepsDoneOpen` can fail for two
different reasons and **says which**. Read the message before assuming a lease
defect.

| Message says | Meaning | What to do |
|---|---|---|
| `... within the 2s TTL, so the lease should still have been held` | The deadline had **not** passed and renewal failed anyway. A **real defect**. | Investigate the lease path. |
| `THE ENVIRONMENT COULD NOT VALIDATE the sub-TTL renewal property` | Every attempt exceeded the TTL before a renewal landed. The assertion **never ran**. | Re-run on a quiet host. See below — this is **not** automatically the host's fault. |

**The second message is not a clean bill of health.** Two causes produce it and
this test cannot tell them apart:

1. **host contention** — the machine could not schedule a 1.5 s cadence inside a
   2 s deadline, five times running; or
2. **a renewal-path regression** in which keepalive acknowledgements consistently
   miss the local deadline.

Both present as "every window overran". If a quiet host still cannot get a clean
window, suspect the second. This is the sharpest reason the path must **fail**
rather than skip: under a skip, cause 2 — a genuine regression — would fire the
skip on every run and CI would report success. **This test is the only end-to-end
coverage of the property**, so a non-failing silence there is the whole signal
lost.

**And skipping is forbidden regardless.** `CLAUDE.md` and `AGENTS.md` both say
*"NEVER use skip guards or -short flags"*, and a measured precondition is not an
exemption from a rule written as *never*.

**Which interval decides it.** Not the sleep. The lease deadline is dated from
the instant the **previous** confirmation was *sent* (`heldUntil = sent.Add(ttl)`
in `sdk/go/coordination.go`), so the quantity that decides expiry is
`previous renew's round trip + the sleep + this renew's round trip`. A
discriminator built on the sleep omits the previous round trip — which is exactly
what consumes the 500 ms of slack between a 1.5 s cadence and a 2 s TTL — and
will report a slow pair of raft proposals as a lease-renewal defect.

**Why the timing is not widened.** A 2 s TTL renewed at 1.5 s is the exact
cadence that produces the ~0.999 s remainder the whole-second `RemainingTTL`
truncation rounds to zero, which is the defect the test exists to catch. A longer
TTL would make it pass more often and stop testing anything. The durable fix is
the injectable clock seam, which this test is a named consumer of.

## Observability

- Lease grant, keep-alive, revoke and expiry are audited through
  [../audit/](../audit/).
- Cascade deletes appear as ordinary delete events on
  [../watch/](../watch/) streams, which is how a consumer learns a session ended.
