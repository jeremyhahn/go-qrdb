# etcd Compatibility — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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

Create a database with etcd enabled. It must be a `kv` engine on a `raft`
backend.

```bash
qrdb database create --name k8s --engine kv --backend raft \
     --etcd --etcd-listen-addr :2379
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
qrdb database update-etcd --name k8s --enabled --listen-addr :2379
qrdb database update-etcd --name k8s --disabled
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
`kv:delete`, `kv:transaction`, `kv:watch`, `kv:compact` and the `lease:*`
permissions. The daemon's `sm_type` must be `mvcc` — Kubernetes depends on
revisions, watches, leases and transactions.

**Run more than one etcd cluster.** Create a second database with etcd enabled.
It is a genuinely separate cluster: its own revision counter starting at 1, its
own watches, its own leases, its own encryption key. Two Kubernetes clusters can
share a qrdb deployment without seeing each other.

## Troubleshooting

**The etcd listener never came up.** Configuration validation refuses the
combination at startup rather than failing later. Check that the database is
`engine: kv` on `backend: raft`, and that TLS and auth are both enabled.

**`etcdctl user` or `etcdctl role` returns "Unimplemented".** That is permanent.
Authentication is mTLS and authorization is qrdb RBAC; etcd's own auth RPCs are
a deliberate stub. Use `qrdb roles` to grant permissions.

**Operations fail with permission errors partway through a workload.**
Authorization is per RPC, mapped from the etcd operation to a qrdb permission.
Your certificate's role is missing one of them — the mapping table is in
[api.md](api.md).

**Revisions start at 1 on a database you expected to have history.** Each
etcd-enabled database has its own revision counter, starting at 1 at database
creation. You are looking at a different database.

**`kube-apiserver` reports compacted revisions.** `kube-apiserver` calls
`Compact` periodically; that is normal, and it discards revision history by
design. If a watcher fell behind further than the retained window, it must
re-list rather than resume.

**`etcdctl defrag` did something you did not expect.** `Maintenance.Defragment`
is mapped to `Compact`. It is not a space-reclamation no-op; it discards
revision history.

**A watch on one database saw nothing from another.** Correct — watch streams do
not cross databases. Each is an independent etcd cluster.
