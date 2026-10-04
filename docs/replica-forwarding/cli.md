# Replica Forwarding — CLI

> **Status: PROPOSED — contract for implementation.** · Scope: go-qrdb ·
> Last verified: 2026-10-05

No CLI verbs. Forwarding is server behaviour: every `qrdb` verb that addresses
a `raft`-isolated database works through any node, on every `--transport`,
and gets the replica leader's answer.

Daemon configuration (`qrdbd`), documented with the rest in
[../configuration/cli.md](../configuration/cli.md):

| Flag | Config key | Default | Meaning |
|---|---|---|---|
| `--replica-forwarding` *(new)* | `cluster.replica_forwarding` | `true` in raft mode | turn forwarding off on this node; requests for a database it holds no replica of are then refused with the replica set |
| `--replica-forwarding-timeout` *(new)* | `cluster.replica_forwarding_timeout` | `10s` | bound on one forward, connection included |

```bash
qrdbd start --replica-forwarding=false   # refuse instead of forwarding
```

A watch on a database through a node that holds no replica fails with the
replica set, so the client can reconnect there:

```bash
qrdb --server https://db3.example.com:63002 --database orders kv watch orders/
# Error: placement: node ... is not a replica of acme/orders ...; its replicas are on ...
```
