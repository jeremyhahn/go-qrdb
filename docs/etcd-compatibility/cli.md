# etcd Compatibility — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-23
> Conflicts with this document are defects. Fix them here first.

etcd compatibility has **no `qrdb etcd` command group.** It is a per-database
protocol, so it is configured through the `qrdb database` verbs — see
[../databases/cli.md](../databases/cli.md) for that group in full. The verbs
that matter here:

```bash
# Create a database with etcd protocol enabled
qrdb database create k8s --etcd --etcd-listen-addr :2379

# Enable etcd on an existing database
qrdb database update-etcd k8s --enabled --listen-addr :2379

# Disable etcd on a database
qrdb database update-etcd k8s --disabled
```

`database create <name>` flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--etcd` | `false` | Enable etcd protocol on database creation |
| `--etcd-listen-addr` | `localhost:2379` | etcd gRPC listen address (loopback by default; set `:2379` or a routable address for remote clients) |
| `--etcd-advertise-urls` | (none) | Advertised client URLs for etcd endpoint discovery |

`database update-etcd <name>` flags (`--enabled` and `--disabled` are mutually
exclusive):

| Flag | Default | Description |
|------|---------|-------------|
| `--enabled` | `false` | Enable the etcd listener |
| `--disabled` | `false` | Disable the etcd listener |
| `--listen-addr` | (none) | etcd gRPC listen address |
| `--advertise-urls` | (none) | Advertised client URLs |

The other `qrdb database` flags apply as normal: any `--backend` serves etcd.
The node must run in mode `raft` and the daemon's `raft.sm_type` must be
`mvcc`, or the listener refuses to start.

**All three etcd flags, and `database update-etcd` itself, exist only on a
`qrdb` built with `WITH_QRDB_ETCD=1` (the default).** A build with
`WITH_QRDB_ETCD=0` has no `database update-etcd` subcommand at all — cobra
reports `unknown command` — and `database create` has no `--etcd`,
`--etcd-listen-addr` or `--etcd-advertise-urls` flag. The service layer
mirrors this on every transport, CLI included: enabling etcd on any database
— through `create`, `update-etcd`, or a YAML `databases[].etcd` block in
`qrdbd`'s own configuration file — is refused before anything is persisted,
with a typed error whose message, exactly as printed, is `service: database:
etcd wire protocol not compiled in: requires build tag qrdb_etcd`.

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

There is no username or password: `etcdctl user` and `etcdctl role` commands
are refused with `PERMISSION_DENIED`.

## Parity

`qrdb database update-etcd` is driven on every transport by the Docker-based
`test/integration/api` suite. The etcd protocol itself is covered by
`make test-etcd` (unit) and by the `clientv3` flows in
`test/integration/etcd_test.go`; it has no `qrdb` verb of its own.
