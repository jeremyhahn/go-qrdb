# Watch — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-01

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
- **Cancelling a watch releases every goroutine behind it.** A consumer ends a
  watch by cancelling the context it opened it with, and *each* goroutine
  forwarding responses along the chain selects on that context for *every*
  channel operation, including the send. It has to: a consumer that has stopped
  reading is the normal way a watch ends, so a bare send on a handover channel
  parks permanently the first time a cancel races a delivery — one goroutine per
  cancelled watch, silently, in proportion to watch churn. Buffering the channel
  does not change this; it moves the block to the send after the buffer fills.

  **The chain, not just its last link.** A namespaced (tenant, database) watch
  runs through a wrapper between the service layer and the transport, and it has
  a forwarding goroutine of its own for the key/prefix stream and another for
  the entity stream. A select in the service relay and a select in the transport
  do not cover a hop between them, so the guarantee is stated over the whole
  chain and each hop is measured the same way: churn abandoned watches with a
  delivery in flight at the cancel and assert the live goroutine count returns
  to its baseline. A leak reproduces only when the reader is *already gone while
  a send is in flight* — a cancel with nothing pending lets even an unguarded
  loop see the inner channel close and exit, which is why a plain
  cancel-and-assert-closed test passes against the defect.
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
- **Cancellation is in-band, never a silent close.** `pkg/watch` never sets
  `Canceled` itself: the hub ends a watch it evicts by recording the reason on
  the watcher and closing the event channel, and it is the transport that turns
  that into a frame. All six do. REST, gRPC, Unix, QUIC and MCP consult
  `watcher.Err()` in their server-side handler and synthesize the terminal
  frame there; the embedded transport, which is its own server, does the same in
  the goroutine that forwards the watcher to the caller's channel. So an
  abnormal termination — a watcher dropped for overflow, a compaction racing an
  in-progress replay — reaches every consumer carrying `Canceled: true` and a
  reason, and a watch that ended cleanly carries no frame at all, which is what
  keeps the two apart.

  The reason text is the watcher error's own message on every transport, which
  is what lets `WatchResponse.Err()` classify the same hub-side condition into
  the same typed error everywhere. The embedded transport was the one exception
  to this invariant until it was fixed; the qualification that used to stand
  here is gone because the exception is.
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

`ClassifyWatchError` and `WatchResponse.Err()` map a reason to:

| Typed error | Raised when | Caller's move |
|---|---|---|
| `WatchCompactedError` | the requested start revision has been compacted away | re-read current state, then watch live |
| `WatchStartRevisionUnsupportedError` | the store keeps no revision history (`raft.sm_type` is not `mvcc`) | drop `StartRevision` and watch live |
| `WatchCanceledError` | any other cancellation, reason carried verbatim | inspect the reason |

**A refused start revision is validated at open**, before any watcher exists, so
it reaches the caller as the error `Watch`/`WatchPrefix` returns rather than as
a terminal response — there is no stream yet for one to travel on. The two
helpers are the two arrival paths for the same condition, and which one a
transport uses is a property of the transport, not of the condition:

| Arrival | Transports | Classified by |
|---|---|---|
| the error returned by the open call | embedded, REST, gRPC, Unix | `ClassifyWatchError` |
| a terminal response on the stream | QUIC, MCP; and on every transport when a compaction outruns an in-progress replay | `WatchResponse.Err()` |

Both produce the same typed error for the same hub-side condition, so
`errors.As` is written once and is correct on all six. That is an invariant, not
a coincidence: every transport that reports the refusal as an open error routes
it through `ClassifyWatchError`, and a transport that translated it as an
ordinary service error would hand back a generic `RequestError` and break
`errors.As` for that caller alone.

| Condition | Behavior | Fails closed? |
|---|---|---|
| Missing `watch:watch` / `watch:watch_prefix` | stream refused at open | **yes** |
| Barrier sealed | stream refused | **yes** |
| Start revision above current | accepted at open; delivers once a change reaches that revision | n/a — nothing to fail; the revision has simply not happened yet |
| Start revision at or below the watched database's compaction floor | `WatchCompactedError` | **yes** — never silently starts live instead. The floor is the one covering the watched key or range, so another database's compaction never cancels this watch |
| Caller context cancelled | stream ends, channel closed | n/a |

The compacted case fails rather than quietly downgrading to a live watch,
because a silent downgrade loses events the caller believes it received. The
hub's refusal, and the error of a watch whose replay compaction overtook, is
`watch.CompactedError`: it matches `watch.ErrCompacted`, reads as it, and
carries the floor that caused it (`CompactRevision`), which the etcd listener
returns as etcd's `CompactRevision`. A
start revision above current is the opposite of that case rather than a
variant of it: it does not name a revision the store refuses to serve, it
names one the store has not reached yet, so `Hub.prepareReplay` only checks it
against the compaction floor covering the watched range
(`HistoryReplayer.CompactedRevision(key, rangeEnd)`, `pkg/watch/hub.go`) and
never against
`CurrentRevision()`. The watch opens, replays nothing (there is nothing yet to
replay), and the deliver floor — initialised to `startRevision-1` — holds every
live event below the requested revision until one arrives at or above it, so
no event the caller did not ask for leaks through and none after the requested
revision is missed. This mirrors etcd's Watch, which accepts a start revision
beyond the current one.

## Configuration

No watch-specific keys. Replay availability is governed by `raft.sm_type`
(`mvcc` required) and the replay window by the watched database's compaction
floor -- per database, moved only by that database's owner. See
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

## Integration testing

`make integration-test-watch` runs this feature's self-spawned integration tests: `watch_cli_test.go`. The per-transport accounting, including every gap, is the `watch` row of [test/integration/COVERAGE.md](../../test/integration/COVERAGE.md).
