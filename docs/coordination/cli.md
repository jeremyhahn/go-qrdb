<!--
Copyright 2026 Jeremy Hahn

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Coordination — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

**There is no CLI surface for this feature, and there is deliberately no plan
to add one.** There is no `qrdb coordination` command group; the surface is
reachable only by importing `github.com/jeremyhahn/go-qrdb/sdk/go` and calling
`Client.Coordination()` or `sdk.NewCoordinator(...)`.

## Why not

Coordination is a **long-lived in-process relationship**, and a command
invocation is the wrong shape for every part of it that matters:

- **A lease belongs to a process, not to a command.** `AcquireLease` returns a
  handle whose whole value is `Done()` — a channel a running program selects on
  in order to fence. A command exits, and the moment it does, the thing that was
  supposed to hear about losing the key is gone. A `qrdb coordination acquire`
  verb could only print a lease ID and leave a key held by nobody until it
  expired.
- **Renewal is a loop with a deadline, not a verb.** The contract in
  [engineering.md](engineering.md) is about when an acknowledgement arrives
  relative to a deadline the client is tracking. A shell loop calling a command
  cannot track that deadline, and a late acknowledgement — the case that returns
  `LeaseLostError` — would arrive as a successful exit status.
- **A watch's terminal reason is the point.** `WatchStream.Err()` is what
  distinguishes "you cancelled", "the server closed cleanly" and "you fell
  behind and missed changes". Collapsing those three into an exit code loses
  exactly the distinction the surface exists to preserve.

Anything a command line *can* usefully express here is already a verb on the
primitive it composes.

## Where the underlying verbs are

| To do this from a shell | Use |
|---|---|
| Grant, keep alive, inspect, revoke or list leases | `qrdb lease ...` — [../leases/cli.md](../leases/cli.md) |
| Attach a key to a lease on write | `qrdb kv put <key> <value> --lease-id <id>` — [../leases/cli.md](../leases/cli.md) |
| Stream changes for a key or prefix | `qrdb watch <key>` — [../watch/cli.md](../watch/cli.md) |
| Take a lock **with a fencing token**, extend or release it | `qrdb lock ...` — [../distributed-locks/cli.md](../distributed-locks/cli.md) |
| Run a compare-and-swap transaction | `qrdb tx execute ...` — [../transactions/cli.md](../transactions/cli.md) |

Those verbs are the *primitives*. Using them from a shell script does not
reproduce this surface: the exclusivity of `AcquireLease` is one transaction
binding the key to the grant, and the ownership deadline is client-side state
that only a running program can hold.

## The one CLI-visible thing

Before writing a consumer, check which half of the surface the target node can
serve. `AcquireLease` and `Watch` require raft mode:

```bash
qrdb admin capabilities --output json
```

Read `mode`, `lease` and `watch` from the result. A node reporting
`"mode": "standalone"` will refuse `AcquireLease` and `Watch` with a typed
error naming the remedy, and will serve `Get`, `Put` and `CompareAndSwap`
normally. See [../run-modes/cli.md](../run-modes/cli.md).

## Parity

No verbs, so no `CommandDefinition` entries in the integration parity suite. A
`qrdb coordination` verb appearing in the parity suite without appearing here
would be a defect in whichever is behind.
