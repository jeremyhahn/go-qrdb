# Watch — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-02

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
  MCP transport does not — it snapshots the stream inside a bounded collection
  window — and there the invariant is currently broken; see *Known limitations*.
- **`PrevKV` is opt-in** because retaining the prior value costs a lookup per
  event.
- **Cancellation is in-band, never a silent close.** Every abnormal termination
  carries `Canceled: true` and a reason.

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

- **A replay truncated by the MCP collection window loses events silently.**
  `qrdb_watch` reports, as its `revision`, the revision of the last stream
  response it consumed — and `Hub.startReplay` delivers the `Created` response,
  whose `Revision` is the store boundary at watcher registration, *before* any
  replayed event. A collection window that expires mid-replay therefore returns
  a truncated event list next to a revision covering the events it dropped, and
  the SDK's polling client advances its cursor past them and never asks again.
  Any backlog that takes longer than one `timeout_seconds` window to drain is
  affected, which is exactly the resume-after-a-gap case the cursor exists to
  serve. A deterministic reproducer exists; the fix needs a decision on how
  replay completion becomes visible to the tool handler, since the handler
  cannot currently tell a completed replay from a truncated one.

## Observability

- A cancelled watch logs its reason at the transport boundary.
- Watch establishment and cancellation are audited through
  [../audit/](../audit/) where the audit logger is configured.
