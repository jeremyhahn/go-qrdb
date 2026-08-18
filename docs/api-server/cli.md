# Unified API Server — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

**No `qrdb` verb group.** The unified server is hosting infrastructure: it is
configured on the daemon and observed through `admin`, not driven by a client
verb of its own.

## Starting it — `qrdbd`

Transport listeners are enabled per protocol in the daemon configuration.
Representative flags:

```bash
qrdbd start \
  --listen 127.0.0.1:8443 \
  --grpc-port 9090 \
  --quic-port 9443 \
  --unix-socket-path /var/run/qrdbd/qrdbd.sock \
  --mcp-endpoint /mcp \
  --enable-mysql --mysql-port 3306 \
  --enable-pgsql --pgsql-port 5432
```

The per-database etcd listener is configured on the database, not here — see
[../etcd-compatibility/cli.md](../etcd-compatibility/cli.md).

## Selecting a transport as a client

Every `qrdb` verb takes the root persistent transport flags:

| Flag | Values | Default |
|---|---|---|
| `--transport` | `rest \| grpc \| quic \| mcp \| unix \| embedded` | `unix` |
| `--server` | address; **a URL scheme overrides `--transport`** | auto-detect |

Recognised schemes: `https://`, `grpcs://`, `quics://`, `unix://`, `http://`,
`grpc://`, `quic://`, `mcp://`.

```bash
qrdb --transport grpc --server grpcs://db.example.com:9090 kv get hello
qrdb --server https://db.example.com kv get hello     # scheme implies REST
qrdb kv get hello                                     # default: local unix socket
```

`embedded` requires the desktop/GUI build.

## Observing it

```bash
qrdb admin health           # public route — works without credentials
qrdb admin status
qrdb admin capabilities     # effective feature set: build tags x mode x config
curl -k https://db.example.com/metrics    # Prometheus, authenticated
```

`admin capabilities` is the first thing to check when a capability appears
missing — see [../run-modes/cli.md](../run-modes/cli.md).

## Adding a capability module

There is no CLI for this. Modules are Go code registered at composition:

```go
node, err := node.NewStandaloneNode(ctx, cfg, []server.ServerOption{
	sdk.WithModule(myModule),
	sdk.WithStrategyRegistry(reg),
	sdk.WithPINBackend(backend),
	sdk.WithCeremonyCA(ca),
})
```

See [engineering.md](engineering.md).

## Parity

Nothing in this feature has a `CommandDefinition`, because nothing here has a
`qrdb` verb. The parity suite itself is the artefact this feature exists to make
meaningful: it exercises every capability on every transport.
