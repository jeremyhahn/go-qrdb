# etcd Compatibility — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

etcd compatibility has **no `qrdb etcd` command group.** It is a per-database
protocol, so it is configured through the `qrdb database` verbs — see
[../databases/cli.md](../databases/cli.md) for that group in full. The verbs
that matter here:

```bash
# Create a database with etcd protocol enabled
qrdb database create --name k8s --engine kv --backend raft --etcd --etcd-listen-addr :2379

# Enable etcd on an existing database
qrdb database update-etcd --name k8s --enabled --listen-addr :2379

# Disable etcd on a database
qrdb database update-etcd --name k8s --disabled
```

| Flag | Default | Description |
|------|---------|-------------|
| `--etcd` | `false` | Enable etcd protocol on database creation |
| `--etcd-listen-addr` | `:2379` | etcd gRPC listen address |
| `--etcd-advertise-client-urls` | (none) | Advertised client URLs for etcd endpoint discovery |


The other `qrdb database` flags apply as normal: the database must be created
with `--engine kv` and `--backend raft`, and for Kubernetes the daemon's
`sm_type` must be `mvcc`.

## Driving it with etcd's own tools

Once a database has etcd enabled, its endpoint is an etcd v3 endpoint. Use
`etcdctl`:

```bash
# Set environment for mTLS
export ETCDCTL_API=3
export ETCDCTL_ENDPOINTS=https://localhost:2379
export ETCDCTL_CACERT=/etc/qrdb/tls/ca.pem
export ETCDCTL_CERT=/etc/qrdb/tls/client.pem
export ETCDCTL_KEY=/etc/qrdb/tls/client-key.pem

# Basic KV operations
etcdctl put mykey "hello world"
etcdctl get mykey
etcdctl del mykey

# Range queries
etcdctl get --prefix /config/
etcdctl get --prefix /config/ --keys-only

# Watch
etcdctl watch --prefix /events/

# Leases
etcdctl lease grant 60
etcdctl put mykey "value" --lease=694d7b0c6b5ae90a
etcdctl lease keep-alive 694d7b0c6b5ae90a
etcdctl lease revoke 694d7b0c6b5ae90a

# Transactions
etcdctl txn <<EOF
compares:
  mod("mykey") = "0"

success requests:
  put mykey "initial"

failure requests:
  get mykey
EOF

# Cluster info
etcdctl member list
etcdctl endpoint status
```


Set the client environment for mTLS as `etcdctl` normally expects
(`ETCDCTL_API=3` plus `--cacert`, `--cert`, `--key`); there is no username or
password, and `etcdctl user`/`role` commands return `Unimplemented`.

## Parity

The `qrdb database` verbs that configure etcd have `CommandDefinition`s in the
integration parity suite. The etcd protocol itself is exercised by
`make test-etcd` and by real `etcdctl` / `clientv3` flows in the integration
suite, not by CLI parity — it has no `qrdb` verb of its own.
