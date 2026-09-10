# Watch — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

## Exported surface

`sdk/go`:

```go
func (c *Client) Watch() transport.WatchClient
```

`sdk/go/transport`:

```go
type WatchClient interface {
	Watch(ctx context.Context, key string, opts WatchOptions) (<-chan WatchResponse, error)
	WatchPrefix(ctx context.Context, prefix string, opts WatchOptions) (<-chan WatchResponse, error)
}

type EventType uint8
const (
	EventPut EventType = iota
	EventDelete
)

type WatchEvent struct {
	Type   EventType
	Kv     *MVCCKeyValue
	PrevKv *MVCCKeyValue
}

type WatchOptions struct {
	StartRevision  int64
	PrevKV         bool
	ProgressNotify bool
}

type WatchResponse struct {
	WatchID      int64
	Events       []*WatchEvent
	Canceled     bool
	CancelReason string
	Revision     int64
}

func (r WatchResponse) Err() error
func ClassifyWatchError(err error) error
```

Typed errors: `WatchCanceledError`, `WatchCompactedError`,
`WatchStartRevisionUnsupportedError`.

## Invariants

- **The channel is closed exactly once**, when the stream ends. A caller ranging
  over it terminates rather than blocking forever.
- **`WatchResponse.Err()` is the only correct way to test a response for
  failure.** It returns `nil` unless `Canceled` is set, and otherwise classifies
  `CancelReason` into a typed error. Comparing the reason string directly is a
  defect — the classification substrings are unexported precisely so callers do
  not depend on their spelling.
- **Events are delivered in Raft commit order within a stream.** No ordering is
  promised across two streams.
- **Replay is seamless.** When `StartRevision` is set and supported, historical
  events are delivered before live ones with no gap and no duplicate at the
  join point. This holds for a consumer that reads the stream to its end. The
  MCP transport reads a bounded collection window instead, so a SINGLE call may
  return a partial replay — but it reports a cursor covering only what it
  delivered, so the polling sequence as a whole is still gap-free and
  duplicate-free; see *Known limitations*.
- **`PrevKV` is opt-in** because retaining the prior value costs a lookup per
  event.
- **Cancellation is in-band, never a silent close — on the five wire
  transports.** REST, gRPC, Unix, QUIC and MCP each consult `watcher.Err()` when
  the hub closes the event channel and synthesize a terminal frame from it, so
  every abnormal termination reaches the client carrying `Canceled: true` and a
  reason. **The embedded transport does not**: `embedded.Transport.Watch` and
  `WatchPrefix` range the watcher's events and close their own channel without
  reading `watcher.Err()`, and `pkg/watch` never sets `Canceled` itself. So an
  in-process consumer sees a hub-side eviction — a watcher dropped for overflow,
  or a compaction racing an in-progress replay — as an ordinary clean close.
  Corrected here to match the code; the invariant is the goal, and embedded is
  the transport that does not yet meet it.
- **Closing a watcher is synchronous with deregistration.** When `Close`
  returns, the watcher is out of the hub registry and `Hub.WatchCount` no
  longer counts it, so no further event can be dispatched to it. A caller may
  act on that immediately — assert the count, reuse the key, tear down the
  surrounding stream — without waiting for anything to catch up.

## Watcher lifecycle

`Close` cancels the watcher's context and then blocks until the hub's cleanup
has removed it from the registry. It is idempotent, and *every* caller returns
only after deregistration, not just the one that won the race to cancel.

What `Close` deliberately does **not** wait for is the event channel closing.
That step has one extra dependency: an in-flight historical replay must stop
touching the channel before anything may close it, and a replay observes
cancellation only between the events it matches, so a scan over a wide range
with few matches notices late. Waiting for the channel would hand `Close` the
duration of an arbitrary history scan — and callers close watchers from places
that must not stall, including a cancel path that holds the mutex serialising a
whole multiplexed watch stream. Deregistration costs one shard write lock and
nothing else, so that is where the boundary sits.

The channel is still closed exactly once, immediately after the replay
releases it. A caller that needs it drained ranges over `Events()`; a caller
that only needs the watcher gone calls `Close` and proceeds.

The registry count is maintained by whoever actually removes an entry.
`Hub.Close` empties the registry and zeroes the count itself, so the per-watcher
cleanup that follows finds nothing to remove and does not decrement a second
time — an unconditional decrement there wraps the unsigned counter to its
maximum instead of reaching zero.

## Error semantics

`ClassifyWatchError` and `WatchResponse.Err()` map a cancel reason to:

| Typed error | Raised when | Caller's move |
|---|---|---|
| `WatchCompactedError` | the requested start revision has been compacted away | re-read current state, then watch live |
| `WatchStartRevisionUnsupportedError` | the store keeps no revision history (`raft.sm_type` is not `mvcc`) | drop `StartRevision` and watch live |
| `WatchCanceledError` | any other cancellation, reason carried verbatim | inspect the reason |

| Condition | Behavior | Fails closed? |
|---|---|---|
| Missing `watch:watch` / `watch:watch_prefix` | stream refused at open | **yes** |
| Barrier sealed | stream refused | **yes** |
| Start revision above current | invalid-argument at open | **yes** |
| Start revision below the compaction floor | `WatchCompactedError` | **yes** — never silently starts live instead |
| Caller context cancelled | stream ends, channel closed | n/a |

The compacted case fails rather than quietly downgrading to a live watch,
because a silent downgrade loses events the caller believes it received.

## Configuration

No watch-specific keys. Replay availability is governed by `raft.sm_type`
(`mvcc` required) and the replay window by `mvcc.compaction_retention` — see
[../key-value/engineering.md](../key-value/engineering.md).

## Persistence

Watches hold no persistent state. A watch is server-side stream state only;
nothing survives a restart, and a consumer reconnecting must re-establish and
(if it needs the gap) replay from a revision it recorded.

## Test strategy

- **Unit**: `pkg/watch` event fan-out, per-`EventType` delivery, `PrevKV`
  population, `classifyWatchReason` for each substring and for an unrecognized
  reason.
- **Error tests**: compacted revision, unsupported-replay store, permission
  denial, context cancellation.
- **Integration**: watch parity across transports in the standalone stage;
  multi-node delivery ordering belongs to the cluster stage, which is **not
  currently wired into CI**.
- **Fault injection**: the store interface is injected, so a compaction racing a
  replay is provoked directly rather than timed.
- **No `time.Sleep` in tests** — synchronization uses channels and waitgroups,
  per the repo rule.
- **The MCP poll loop is driven, not timed.** MCP is request/response, so
  `sdk/go/transport/mcp` implements `Watch`/`WatchPrefix` by polling the
  `qrdb_watch` tool. Every delay that loop waits on goes through a package-level
  clock seam, so a test substitutes a clock whose `After` it fires by hand and
  owns the poll schedule outright. A change pushed while the loop is parked
  between polls is then in an inter-poll gap **by construction** — the window in
  which no server-side watcher exists at all — and no-loss is asserted against
  that schedule rather than against elapsed time.

  This matters beyond tidiness. The earlier version of that test arranged gaps
  statistically, by giving each poll a collection window a thousandth of its
  cycle, and asserted a fixed delivery count before a wall-clock deadline. It
  could not tell a poller that genuinely lost an update from a host that was
  merely busy, so it failed on three unrelated changes and was re-run away each
  time — and a gate that only passes on a quiet machine is not a gate. Measured
  on a 2-core cpuset with 16 concurrent `-race` processes: the timed version
  fails 6 times in 320 runs, the driven version 0 in 320, while a poller mutated
  to skip one revision fails the driven version every run.

## Known limitations

- **A replay truncated by the MCP collection window can under-report, never
  over-report, its cursor.** `qrdb_watch` computes the `revision` it returns
  only from responses that prove delivery — an event batch's own revision, or
  a post-replay progress watermark emitted once every matching event through
  the replay's boundary has actually reached the watcher's channel. It never
  credits the `Created` response's revision, because that value is the
  replay's *intended* upper bound, captured before a single historical event
  has been replayed; treating it as delivered would let a collection window
  that expires mid-replay hand a caller a cursor covering events it never
  received. The consequence of under-reporting is bounded and
  self-correcting: the SDK's polling client re-requests the same
  `start_revision` on its next poll rather than skipping ahead, so a replay
  backlog that outlives one `timeout_seconds` window costs an extra round
  trip, not a lost event.

## Watch responses (internal)

`pkg/watch.WatchResponse` carries two synthetic, event-free markers a
start-revision watch may see, and they answer different questions:

- **`Created`** — the single response emitted when the watch is established,
  before any historical event is replayed. Its `Revision` is the replay's
  *target*, not a delivery guarantee.
- **`Progress`** — opt-in via `WithProgressNotify`, emitted only once a
  start-revision replay has fully drained, including any live event staged
  during it. Its `Revision` is safe to treat as "everything through this
  revision has been delivered." The `qrdb_watch` tool handler is the
  consumer that opts in, and only for a start-revision watch — a plain
  live-only watch never replays, so the distinction does not apply to it.

## Observability

- A cancelled watch logs its reason at the transport boundary.
- Watch establishment and cancellation are audited through
  [../audit/](../audit/) where the audit logger is configured.
