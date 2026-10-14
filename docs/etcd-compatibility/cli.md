# etcd Compatibility — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

etcd compatibility has **no `qrdb etcd` command group.** It is a per-database
protocol, so it is configured through the `qrdb database` verbs — see
[../databases/cli.md](../databases/cli.md) for that group in full. The verbs
that matter here:

```bash
# Create a database with etcd protocol enabled
qrdb database create --name k8s --etcd --etcd-listen-addr :2379

# Enable etcd on an existing database
qrdb database update-etcd --name k8s --enabled --listen-addr :2379

# Disable etcd on a database
qrdb database update-etcd --name k8s --disabled
```

| Flag | Default | Description |
|------|---------|-------------|
| `--etcd` | `false` | Enable etcd protocol on database creation |
| `--etcd-listen-addr` | `localhost:2379` | etcd gRPC listen address |
| `--etcd-advertise-client-urls` | (none) | Advertised client URLs for etcd endpoint discovery |


The other `qrdb database` flags apply as normal: any `--backend`, the node
must run in mode `raft`, and for Kubernetes the daemon's `sm_type` must be
`mvcc`. *(Corrected: the database no longer needs to be `--engine kv` --
Lane 1C deleted the `engine` concept entirely, OD-12/OD-13, so etcd now
serves any database regardless of what it stores.)*

**All three etcd flags, and `database update-etcd` itself, exist only on a
`qrdb` built with `WITH_QRDB_ETCD=1` (the default).** A build with
`WITH_QRDB_ETCD=0` has no `database update-etcd` subcommand at all — cobra
reports `unknown command` — and `database create` has no `--etcd`,
`--etcd-listen-addr` or `--etcd-advertise-urls` flag. The service layer
mirrors this on every transport, CLI included: enabling etcd on any database
— through `create`, `update-etcd`, or a YAML `databases[].etcd` block in
`qrdbd`'s own configuration file — is refused before anything is persisted,
with a typed error whose message, exactly as printed, is `service: database:
etcd wire protocol not compiled in: requires build tag qrdb_etcd`, rather
than accepting the setting and never actually starting a listener.

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
password, and `etcdctl user`/`role` commands are refused with "permission denied".

## Parity

The `qrdb database` verbs that configure etcd have `CommandDefinition`s in the
integration parity suite. The etcd protocol itself is exercised by
`make test-etcd` and by real `etcdctl` / `clientv3` flows in the integration
suite, not by CLI parity — it has no `qrdb` verb of its own.
