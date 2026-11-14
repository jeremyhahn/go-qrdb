# Watch — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-11-14
> Conflicts with this document are defects. Fix them here first.

## Problem

Polling a key to notice it changed is wasteful at low frequency and lossy at
high frequency: between two polls a key can change twice and the caller sees
one. Anything built on "react when this changes" — service discovery, config
reload, distributed locks, leader election — needs a change *stream*, not a
sampling loop.

## Shape chosen

A server-pushed event stream, scoped to a single key or a prefix, optionally
replayed from a caller-named revision.

- The stream carries `WatchEvent`s (`EventPut` or `EventDelete`) with the resulting
  `MVCCKeyValue`, and optionally the previous one.
- **Replay from a revision is opt-in** (`StartRevision`) and requires the MVCC
  state machine, because only it retains the history to replay from.
- **Cancellation is in-band.** A cancelled stream delivers a final response with
  `Canceled: true` and a reason, rather than closing silently.

### Entity watch

The same stream, projected onto entities. `WatchServicer.WatchEntity` and
`WatchEntityType` watch one entity (`{type}/{id}`) or every entity of a type
(`{type}/`), and transform raw key-value events into `WatchEntityEvent`s
carrying the entity's type, ID, payload and revision. It is an in-process
service-layer surface — no transport exposes it — and the namespacing wrapper
that scopes a watch to one (tenant, database) pair is its only consumer.

**Cancellation is in-band here too**, for the same reason and in the same
shape: a `WatchEntityEvent` with `Canceled` set and a `CancelReason` is the
last thing an entity stream delivers when it ended for a reason, and a stream
that ended cleanly delivers no such event. The reason is the watcher error's
message verbatim, so a termination classifies the same way it would on the
key-value stream.

The terminal event names no entity, which makes it the one event the
namespacing wrapper forwards unfiltered: a type-level watch spans the store and
the wrapper keeps only the IDs under its own namespace, so filtering an event
that carries no ID would discard the only thing telling the consumer the stream
died rather than ended.

### Alternatives rejected

- **Long-polling.** Rejected: the same lost-update problem as polling, with more
  connection churn.
- **A durable, per-consumer subscription cursor.** Rejected at this layer: it
  turns the store into a message broker with retention and acknowledgement
  semantics to own. The replay window is the compaction window, and that is a
  deliberate ceiling — a consumer that needs guaranteed delivery beyond it wants
  a queue, not a watch.
- **Silent stream close on error.** Rejected: an EOF is ambiguous between "the
  server went away", "your revision was compacted", and "this store cannot
  replay". Each of those needs a different response from the caller, so the
  reason travels in the final message.
- **A stream handle carrying an `Err()` for the entity watch**, in place of an
  in-band terminal event. Rejected: a handle whose error is read after the
  channel drains is the SDK layer's idiom, and the entity watch is a
  service-layer surface, where an in-band terminal response is already how a
  watch says why it ended. Taking both would leave one layer with two
  mechanisms answering one question, and two mechanisms drift.
- **A fully asynchronous `Close`** — cancel the context and let a goroutine
  deregister whenever it is scheduled. Rejected: it gives the caller no moment
  at which the watcher is known to be gone, so every caller that cares has to
  invent one, and the only thing available to invent it from is elapsed time.
  A close that waits for deregistration turns that into a fact.
- **A `Close` that also waits for the event channel to close.** Rejected: the
  channel cannot close until an in-flight replay stops writing to it, and a
  replay notices cancellation only between the events it matches. That would
  make the cost of closing a watcher depend on the width of a history scan
  nobody is reading. The two guarantees are separated instead: `Close` means
  deregistered, and ranging over `Events()` means drained.

## Run modes

Watch is served identically in both run modes. In both, the engine that applies a write is the one
publisher into the node's `watch.Hub`, and the watch service subscribes to that
hub through the same per-(tenant, db) registry, so prefix watches, revision
resume and cancellation behave the same and a tenant sees only its own
namespace. Revision resume needs the store's revision index. A standalone node composes
the MVCC engine on every backend (`lifecycle.NewLocalEngine`), so it keeps one;
a raft node keeps one when `raft.sm_type` is `mvcc`. A raft node on another
state machine answers a start revision with an error the SDK classifies as
`WatchStartRevisionUnsupportedError`. MCP's polling watch needs replay after its
first event, so on such a node an MCP watch ends with that typed error rather
than dropping changes between polls.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Event fan-out (`Hub`) | `pkg/watch` | go-qrdb |
| Watch service (`WatchServicer`, `WatchService`) | `pkg/service` | go-qrdb |
| Revision history that replay reads from | `pkg/mvcc` | go-qrdb |
| Streaming transport (gRPC server-streaming, QUIC stream, SSE-style REST) | `pkg/api/*` | go-qrdb |
| Raft commit ordering that gives events their order | — | **go-quicraft** |
| SDK surface | `sdk/go` (`Client.Watch()`) | go-qrdb |

## Data flow

1. The caller opens a watch on a key or prefix, optionally naming a
   `StartRevision`.
2. Tenant and database are resolved; `watch:watch` is checked.
3. If `StartRevision` is set, the server replays committed history from that
   revision forward, then switches to live delivery with no gap.
4. Every subsequent commit in scope produces a `WatchEvent` on the stream.
5. If the caller's context ends, or the server must stop, the stream terminates
   with `Canceled: true` and a `CancelReason`.

Events are delivered in Raft commit order. Ordering across two separate watches
is not defined beyond each stream's own revision sequence.

## WatchPrefix: behaviour on main

What a prefix watch does today, claim by claim, each with the function it
comes from. A change to any of this is a SPEC row, not a doc edit.

### Matching

- **Byte prefix, half-open range.** The prefix becomes the range
  `[prefix, prefixEnd(prefix))`: `prefixEnd` copies the prefix, increments its
  last byte below `0xff` and truncates after it
  (`pkg/watch/hub.go:prefixEnd`). A key matches when `key >= prefix` and, if
  the end is non-empty, `key < end`, compared bytewise with no Unicode or path
  semantics (`pkg/watch/hub.go:matchesRange`, `pkg/watch/hub.go:watcher.matches`).
  `"a/"` therefore matches `"a/"` itself and `"a/b"`, but not `"a"` or `"a0"`.
- **Empty prefix watches everything in scope.** An empty prefix, and a prefix
  of only `0xff` bytes, yield an empty end, which `matchesRange` treats as
  unbounded (`pkg/watch/hub.go:prefixEnd`, `pkg/watch/hub.go:matchesRange`).
  Under a tenant scope "everything" is the scope's namespace (see Scoping).
- **Every write is checked against every prefix watcher.** Prefix watchers are registered in
  the shard of the prefix itself (`pkg/watch/hub.go:Hub.WatchPrefix`), and each
  write scans the prefix watchers of all shards for a range match, after the
  exact-key watchers of the key's own shard (`pkg/watch/hub.go:Hub.notify`).
- **Put/delete filters** (`WatchFilterNoPut`, `WatchFilterNoDelete`) drop the
  matching event type on both the live path (`pkg/watch/hub.go:Hub.sendEvent`)
  and the replay path (`pkg/watch/hub.go:Hub.startReplay`). The previous value
  is attached only when `prev_kv` was asked for (same two functions).

### Start revision, catch-up and replay

- **`StartRevision <= 0` is a live-only watch**: no replay, no history needed
  (`pkg/watch/hub.go:Hub.prepareReplay`).
- **Validated before anything is registered.** A positive start revision on a
  hub with no history replayer fails with `ErrStartRevisionUnsupported`; one at
  or below the range's compaction floor fails with `CompactedError` carrying
  that floor. Both return from `WatchPrefix` with no watcher created
  (`pkg/watch/hub.go:Hub.prepareReplay`, called first in
  `pkg/watch/hub.go:Hub.WatchPrefix`).
- **Replay boundary.** The watcher is registered and the replay boundary
  revision captured under the same shard lock, so every commit after the
  boundary reaches the watcher live (`pkg/watch/hub.go:Hub.WatchPrefix`,
  `pkg/watch/hub.go:Hub.captureReplayBoundary`).
- **Replay order.** A replaying watcher first receives a `Created` response
  whose `Revision` is the boundary, then one response per historical event from
  the start revision through the boundary, then the live events that arrived
  meanwhile (`pkg/watch/hub.go:Hub.startReplay`). Live events are held in
  staging while replay runs (`pkg/watch/watcher.go:watcher.send`) and flushed by
  `finishReplay`; a live event for a revision replay already delivered is
  dropped (`pkg/watch/watcher.go:watcher.dedupDrop`), so the hand-off has no gap
  and no duplicate. A live-only watch gets no `Created` response from the hub.
- **Start revision beyond the boundary** runs no historical scan; the watcher
  withholds live events below the start revision through its deliver floor
  (`pkg/watch/hub.go:Hub.startReplay`, `pkg/watch/watcher.go:watcher.advanceDeliverFloor`).
- **Progress after replay.** With `ProgressNotify`, one `Progress` response
  carrying the boundary follows the flushed replay, a cursor safe to resume
  from (`pkg/watch/hub.go:Hub.startReplay`).

### Compaction during replay

If a replay that actually scanned history returns an error, or the range's
compaction floor has reached the start revision by the time it ends, the
watcher's error becomes `CompactedError{CompactRevision: floor}` and the
watcher is cancelled; staged live events are flushed first
(`pkg/watch/hub.go:Hub.startReplay`). A live-only watch is never ended by
compaction: it reads no history.

### Ordering and delivery

- Each watcher receives events in the order the engine publishes them into
  the hub (`pkg/watch/hub.go:Hub.notify`), one event per response, with the
  response `Revision` set to the event's `ModRevision`
  (`pkg/watch/hub.go:Hub.sendEvent`).
- **A slow consumer is cut off, never waited for.** Each watcher has a bounded
  event buffer, 4096 responses unless `WithEventBufferSize` says otherwise
  (`pkg/watch/watcher.go:defaultEventBufferSize`). Delivery never blocks the
  writer: when the buffer is full the watcher's error becomes
  `ErrWatcherOverflow` and the watcher is cancelled
  (`pkg/watch/watcher.go:watcher.deliver`). The consumer must re-read state
  and open a new watch.
- A node-wide watcher cap, when configured, is taken before registration; a
  refused slot fails `WatchPrefix` (`pkg/watch/hub.go:Hub.WatchPrefix`, via
  `limit.acquireFor`). A closed hub fails it with `HubClosedError`.

### Cancellation

Cancelling the caller's context ends the watch: a cleanup goroutine started at
registration deregisters the watcher and releases its cap slot
(`pkg/watch/hub.go:Hub.WatchPrefix`, `pkg/watch/hub.go:Hub.cleanupOnCancel`).
Overflow and compaction end it the same way, with the reason in the watcher's
`Err()` (`pkg/watch/watcher.go:watcher.deliver`, `pkg/watch/hub.go:Hub.startReplay`);
`pkg/watch` itself sends no terminal response; turning `Err()` into the
in-band terminal response of "Shape chosen" is each transport's job.

### Scoping

The service layer passes the prefix through unchanged
(`pkg/service/watch_service.go:WatchService.WatchPrefix`). The namespacing
wrapper prepends the (tenant, database) namespace to the caller's prefix
before it reaches the hub and strips it from every event key on the way out
(`pkg/server/namespaced_watch.go:namespacedWatchService.WatchPrefix`,
`pkg/server/namespaced_watch.go:namespacedWatcher.pump`). An empty prefix
under a namespace therefore watches that whole namespace and nothing outside
it.

### Per transport

| Transport | Selects a prefix watch when | Function |
|---|---|---|
| REST | query `prefix=true`; `key` is the prefix | `pkg/api/rest/handlers_watch.go:handleWatch` |
| gRPC / Unix | `range_end` is non-empty; `key` is the prefix | `pkg/api/grpc/service.go:Watch` |
| etcd v3 API | a create request's `range_end` is non-empty | `pkg/api/etcd/watch.go:watchServer.Watch` |
| QUIC | the `watch.WatchPrefix` method, or a non-empty `range_end` | `pkg/api/quic/handlers_watch.go:handleWatchStream` |
| MCP | tool argument `prefix: true` | `pkg/api/mcp/tools_watch.go:watchTools` |

On gRPC, Unix, etcd and QUIC the **value** of `range_end` is not read: any
non-empty `range_end` makes `key` a prefix and the range is always
`[key, prefixEnd(key))` (`Hub.WatchPrefix` takes no end). A request for an
arbitrary range `[key, range_end)` gets the prefix range of `key` instead.

## Trust boundaries

- A watch is scoped to one (tenant, database) pair, resolved from the
  authenticated identity — a prefix watch cannot escape its tenant.
- Key and prefix watches check the same permission, `watch:watch`, on every
  transport.
- Internal namespaces are not observable through a user watch, on the same
  reasoning as the enumeration guard in
  [../key-value/](../key-value/architecture.md).

## What this feature deliberately does not do

- **No delivery guarantee beyond the compaction window.** A consumer that
  disconnects for longer than the retained history cannot replay the gap; it
  gets `WatchCompactedError` and must re-read state.
- **No acknowledgement, redelivery, or dead-lettering.** This is a change
  stream, not a queue.
- **No filtering beyond key or prefix.** Value-predicate filtering is the
  consumer's job.
- **No replay at all on a non-MVCC state machine.** Live watching works; naming
  a `StartRevision` does not.
