# etcd Compatibility — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

Test reference: `make integration-test-etcd-compatibility`; coverage row: `test/integration/COVERAGE.md` `[etcd-compatibility](../../docs/etcd-compatibility/)`.

Point `etcdctl`, a `clientv3` program, or `kube-apiserver` at a qrdb database
and it works as an etcd v3 endpoint.

## Prerequisites

- A daemon built with `WITH_QRDB_ETCD=1` (the Makefile default). Check with
  `qrdb admin capabilities` — `etcd` must be listed.
- TLS and auth enabled on the daemon; the etcd listener refuses to start
  otherwise.
- Client certificates from the same CA. There is **no username or password** —
  the CN is your username, the OU is your tenant.

## First run

Create a database with etcd enabled, on a node running in mode `raft`; any
backend serves it.

```bash
qrdb database create k8s --etcd --etcd-listen-addr :2379
```

Then talk to it as etcd:

```bash
export ETCDCTL_API=3
etcdctl --endpoints=https://db.example.com:2379 \
        --cacert=ca.pem --cert=client.pem --key=client-key.pem \
        put /foo bar

etcdctl --endpoints=https://db.example.com:2379 \
        --cacert=ca.pem --cert=client.pem --key=client-key.pem \
        get /foo
```

## Common tasks

**Turn etcd on or off for an existing database.**

```bash
qrdb database update-etcd k8s --enabled --listen-addr :2379
qrdb database update-etcd k8s --disabled
```

**Use it as the Kubernetes backing store.**

```bash
kube-apiserver \
  --etcd-servers=https://node1.example.com:2379,https://node2.example.com:2379 \
  --etcd-cafile=/etc/kubernetes/pki/etcd/ca.pem \
  --etcd-certfile=/etc/kubernetes/pki/etcd/client.pem \
  --etcd-keyfile=/etc/kubernetes/pki/etcd/client-key.pem
```

The `kube-apiserver` client certificate's role needs `kv:range`, `kv:put`,
`kv:delete`, `transaction:execute`, `watch:watch_prefix`, `admin:status` and the
`lease:*` permissions, and must belong to the tenant that owns the database. The daemon's `raft.sm_type` must be `mvcc` — Kubernetes depends on
revisions, watches, leases and transactions. On a state machine without MVCC
revision history (the default `pebble`) the etcd server refuses to start
(`etcd.RevisionHistoryRequiredError`), and a put that reached one would be
refused with `FAILED_PRECONDITION` rather than answered with revision 0.

**Run more than one etcd cluster.** Create a second database with etcd enabled.
Each listener serves only its own database's keys, watches and leases, so two
Kubernetes clusters can share a qrdb deployment without seeing each other. On the
shared shard revisions are store-global; give each cluster a raft-isolated
tenant if it needs a revision counter of its own.

## Troubleshooting

**The etcd listener never came up.** The listener refuses to start rather than
fail later. Check that the node runs in mode `raft`, that TLS and auth are both
enabled, that `raft.sm_type` is `mvcc`, and that the database is not sealed.

**`etcdctl user` or `etcdctl role` returns "permission denied".** That is
permanent. Authentication is mTLS and authorization is qrdb RBAC; etcd's own
auth RPCs are not served. Use `qrdb roles` to grant permissions.

**The etcd listener refuses to start with "refusing to serve database ...
without authentication".** The node has no TLS or no authentication configured.
The listener never serves an unauthenticated port.

**Every RPC fails with "unauthenticated" or "etcd: permission denied".** Present
a client certificate, issued to a user of the tenant that owns the database.

**Operations fail with permission errors partway through a workload.**
Authorization is per RPC, mapped from the etcd operation to a qrdb permission.
Your certificate's role is missing one of them — the mapping table is in
[api.md](api.md#rpc-coverage).

**`kube-apiserver` reports compacted revisions.** `kube-apiserver` calls
`Compact` periodically; that is normal, and it discards this database's
revision history by design -- never another database's. If a watcher fell
behind further than the retained window, it must re-list rather than resume.

**`kube-apiserver` logs `permission denied` from its compactor.** Its
certificate's role lacks `mvcc:compact`. Grant it to the role in the tenant
that owns the database; a role in any other tenant cannot compact it.

**`etcdctl defrag` is refused.** `Maintenance.Defragment` is not served; the
storage engine compacts on its own.

**A watch on one database saw nothing from another.** Correct — watch streams do
not cross databases. Each listener serves only its own database's keys, watches
and leases.
