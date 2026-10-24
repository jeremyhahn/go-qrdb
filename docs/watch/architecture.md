# Watch — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
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
| Watch service and event fan-out | `pkg/watch` | go-qrdb |
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
