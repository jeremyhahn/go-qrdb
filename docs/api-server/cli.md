# Unified API Server — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

**No `qrdb` verb group.** The unified server is hosting infrastructure: it is
configured on the daemon and observed through `admin`, not driven by a client
verb of its own.

## Starting it — `qrdbd`

Transport listeners are enabled per protocol in the daemon configuration.
Representative flags:

```bash
qrdbd start \
  --admin-port 63002 \
  --grpc-port 63003 \
  --enable-quic --quic-port 63004 \
  --unix-socket-path /var/run/qrdbd/qrdbd.sock \
  --enable-mcp --mcp-port 63005 --mcp-endpoint /mcp \
  --enable-mysql --mysql-port 3306 \
  --enable-pgsql --pgsql-port 5432
```

The per-database etcd listener is configured on the database, not here — see
[../etcd-compatibility/cli.md](../etcd-compatibility/cli.md).

gRPC-Web has no flag: it is the `server.grpc_web` block of the configuration
file `qrdbd start --config` reads, served on the REST listener under
`/grpc-web/` — see [user.md](user.md#calling-from-a-browser--grpc-web). It is a
browser transport; `qrdb` never uses it, so it is not a `--transport` value.

## Selecting a transport as a client

Every `qrdb` verb takes the root persistent transport flags:

| Flag | Values | Default |
|---|---|---|
| `--transport` | `rest \| grpc \| quic \| mcp \| unix \| embedded` | `unix` |
| `--server` | address; **a URL scheme overrides `--transport`** | auto-detect |

Recognised schemes: `https://`, `grpcs://`, `quics://`, `unix://`, `http://`,
`grpc://`, `quic://`, `mcp://`.

```bash
qrdb --transport grpc --server grpcs://db.example.com:63003 kv get hello
qrdb --server https://db.example.com kv get hello     # scheme implies REST
qrdb kv get hello                                     # default: local unix socket
```

`embedded` requires the desktop/GUI build.

## Observing it

```bash
qrdb admin health           # public on every transport — works without credentials
qrdb admin status
qrdb admin capabilities     # effective feature set: build tags x mode x config
curl -k https://db.example.com/metrics    # Prometheus, authenticated
```

`admin capabilities` is the first thing to check when a capability appears
missing — see [../run-modes/cli.md](../run-modes/cli.md).

## Adding a capability module

There is no CLI for this. Modules are Go code registered at composition:

```go
import sdkserver "github.com/jeremyhahn/go-qrdb/sdk/go/server"

node, err := node.NewStandaloneNode(ctx, cfg, []sdkserver.ServerOption{
	sdkserver.WithModule(myModule),
	sdkserver.WithStrategyRegistry(reg),
	sdkserver.WithPINBackend(backend),
	sdkserver.WithCeremonyCA(ca),
})
```

See [engineering.md](engineering.md).
