# Watch — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
  join point.
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

## Observability

- A cancelled watch logs its reason at the transport boundary.
- Watch establishment and cancellation are audited through
  [../audit/](../audit/) where the audit logger is configured.
