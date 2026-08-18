# Watch — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Problem

Polling a key to notice it changed is wasteful at low frequency and lossy at
high frequency: between two polls a key can change twice and the caller sees
one. Anything built on "react when this changes" — service discovery, config
reload, distributed locks, leader election — needs a change *stream*, not a
sampling loop.

## Shape chosen

A server-pushed event stream, scoped to a single key or a prefix, optionally
replayed from a caller-named revision.

- The stream carries `WatchEvent`s (`Put` or `Delete`) with the resulting
  `MVCCKeyValue`, and optionally the previous one.
- **Replay from a revision is opt-in** (`StartRevision`) and requires the MVCC
  state machine, because only it retains the history to replay from.
- **Cancellation is in-band.** A cancelled stream delivers a final response with
  `Canceled: true` and a reason, rather than closing silently.

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
2. Tenant and database are resolved; `watch:watch` or `watch:watch_prefix` is
   checked.
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
- Prefix watches carry their own permission (`watch:watch_prefix`) distinct from
  single-key watches, because a prefix watch is a far broader read grant.
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
