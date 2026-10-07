# Configuration — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

How the daemon is configured: the load order, the validation contract, and the
sections that exist.

## Problem

A store with six transports, four storage backends, two run modes, a dozen
build-tag-gated features and a barrier has a large configuration surface. A
config system that silently ignores an unrecognized or impossible setting turns
every misconfiguration into a production surprise.

## Shape chosen

**Four layers, in one order, then validate everything.**

1. `DefaultConfig()` initializes all fields with production-ready defaults.
2. The YAML file (if provided via `--config`) is unmarshaled on top of the defaults. Only fields present in the YAML file override the defaults.
3. `QRDB_`-prefixed environment variables override the file. Every default-bearing key is registered with Viper before the file is merged, so an env var works whether or not the file mentions the key; the variable name is the key path upper-cased with separators as underscores (`server.host` -> `QRDB_SERVER_HOST`).
4. CLI flags that are explicitly set override everything below them.

**Only fields present in the YAML override the defaults**, and only flags
explicitly set override the layers below. A flag left at its own default does
not silently overwrite a YAML or environment value.

Layer 3 lives inside `config.Load`, which the daemon calls only when `--config`
names a file. A daemon started with no config file therefore resolves from
defaults and flags alone; environment overrides need a file, even an otherwise
empty one. Changing that would move every key, not just the addressing ones, so
it is deliberately left as it stands.

### Infrastructure values are never literals

**Owner ruling, 2026-08-26** (`software/conventions/configuration.md` §3): bind
hosts, ports and advertised/peer addresses come from the config path above. A
literal in non-test code is a defect **unless it is the defaults-layer value
that path overrides** — `config.DefaultServerHost` is such a value, and it is
the single source both `DefaultConfig()` and the `--host` flag default read.

Two shapes follow from it:

- **The resolved value reaches the consumer.** `server.host` is not merely
  defined and validated; `cmd/qrdbd` puts it in the `*config.Config` handed to
  `server.New`, and every listener joins that field with its port. The defect
  this ruling was filed against was the missing middle: the field was defined,
  validated, Viper-registered, then replaced with a literal on the only path
  that built that struct, so `server.host` and `QRDB_SERVER_HOST` were
  discarded for every listener.
- **Where a value is genuinely absent, fail closed.** `clusterenroll` returns
  `*MissingBindHostError` rather than binding the enrollment listener somewhere
  the operator did not ask for, and `*MissingAdminPortError` rather than
  completing a bootstrap URL with a guessed port. The guessed port used to be
  `8443`, which exists nowhere in the defaults layer and contradicts
  `DefaultConfig().Server.AdminPort`, so the enroll client dialed a port nothing
  in the system listens on.

Before any of that, `config.Load` decodes the file **strictly**: a key that
names no field anywhere in the `Config` schema fails the load rather than
being silently dropped. This closed a real gap — a documented-but-wrong
`databases[].protocol.etcd` key loaded cleanly and had no effect, so an
operator who believed etcd was configured was running without it, with no
error and no log line. The check walks the YAML document tree against the
same struct tags Viper's own decode step reads, so the known-key set can
never drift from what `Load` actually honors into a second, separately
maintained list.

After merging, `Config.Validate()` checks **every** field and returns the first
error as a `*ValidationError` carrying the field path and the reason:

```
config: validation failed for "raft.shard_id": must be greater than 0
```

### Three error types

| Error | Returned when |
|---|---|
| `*LoadError` | the YAML file cannot be read or parsed |
| `*UnknownFieldError` | the YAML file carries a key that names no field in the schema |
| `*ValidationError` | a field has an invalid value |

All three implement `error`; `LoadError` also implements `Unwrap()` for
`errors.Is` and `errors.As`.

### Unknown keys are refused, not dropped

**Owner ruling, 2026-10-03**: a config carrying a stale or
misspelled key must fail to load, naming the key. Precedence was already
**build tag > run mode > config** for *values* config asks for beyond what the
mode or binary allow (see "Alternatives rejected" below); this closes the
matching gap for *keys* the schema never defined in the first place, which
previously loaded and did nothing.

A key for a feature this binary was not compiled with (e.g. `protocols.mysql`
without build tag `qrdb_mysql`) is **not** an unknown key: `pkg/config`
declares every field unconditionally regardless of build tags, so the schema
check passes and `Config.ValidateCapabilities` refuses it by name instead,
keeping the two gates — schema shape and build-tag availability — separate for
the same reason they were already separate for values.

### The separate capability gate

`Config.ValidateCapabilities` is a **deliberately separate** check, called by
the composition roots rather than by `Validate()`. It fails the boot when a
feature is enabled whose build tag is absent, **naming the tag**.

Keeping it separate is the point: pure configuration validation stays
**build-independent**, so the same config file is valid or invalid regardless of
how a particular binary was compiled. Only composition knows which tags are
present.

### Alternatives rejected

- **Ignoring unknown or impossible settings.** Rejected both ways: config
  asking for more than the mode or the binary allows is a **validation error,
  never a silent no-op** — see [../run-modes/](../run-modes/architecture.md) —
  and a key the schema never defined at all is now a load error rather than a
  no-op key.
- **Folding the capability check into `Validate()`.** Rejected on the
  build-independence argument above.
- **Reporting all validation errors at once.** Not done — `Validate()` returns
  the **first** error. That is a real ergonomic cost on a large config, and it is
  the current behaviour.

## Sections

`server`, `protocols`, `raft`, `storage`, `tls`, `logging`, `discovery`,
`mvcc`, `watch`, `lease`, `index`, `timeseries`, `auth`, `cors`,
`databases` (with per-database `mysql`, `postgresql`, `etcd` and
`sql.concurrency` — flat on the entry, no `protocol` wrapper field),
`embedding` (with `EmbeddingProviderConfig`), `rag`, `tenant`, plus `seal` and
`mode`. There is no `audit:` section — audit logging is always on and is not
configured; see [../audit/](../audit/architecture.md). See
[../etcd-compatibility/](../etcd-compatibility/architecture.md) for the
per-database `etcd` block's own validation (engine/backend/build-tag
requirements) and the CLI/API verbs (`qrdb database create --etcd` /
`update-etcd`) that set it without a restart.

Each is documented in its own feature folder; the complete field-by-field
reference with defaults is [12-configuration.md](../12-configuration.md).

## Default ports

| Service | Port | Protocol | Field |
|---|---|---|---|
| Raft | 63001 | QUIC (internal) | `raft.raft_address` |
| REST | 63002 | HTTPS | `server.admin_port` |
| gRPC | 63003 | gRPC + TLS | `server.grpc_port` |
| QUIC | 63004 | QUIC | — |
| MySQL | 3306 | MySQL wire | `server.mysql_port` |
| PostgreSQL | 5432 | PostgreSQL wire | `server.pgsql_port` |
| etcd | 2379 | gRPC + mTLS | `databases[].etcd.listen_addr` |

## Trust boundaries

- **Secret-bearing fields must reference a secret, not embed one.** Protected
  fields resolve `credential://` or `literal://` references into enclave-backed
  values at boot, and **fail closed** if they cannot — see
  [../credentials/](../credentials/architecture.md#secret-resolution).
- **TLS and auth are mandatory for the SQL and etcd protocols**; validation
  refuses the combination otherwise.
- **A multicast discovery secret has no default.** Without an operator-chosen
  PSK the method does not run — see [../clustering/](../clustering/).

## What this feature deliberately does not do

- **No runtime reconfiguration.** Configuration is read at boot; changing it is
  a restart.
- **No environment-variable configuration without a config file.** The
  `QRDB_`-prefixed overlay is part of `config.Load`, which runs only for
  `--config`. The command-scoped bindings (`QRDB_SO_PIN`, `QRDB_USER_PIN`,
  `QRDB_BARRIER_STRATEGY`, `QRDB_SHARES`, `QRDB_THRESHOLD`, `QRDB_HOSTNAME`,
  `QRDB_SANS`) are bound separately by their own commands and are unaffected.
- **No separate bind host per protocol.** All API listeners share
  `server.host`. The enrollment listener is the one exception
  (`bootstrap.noise_host`), because it serves a different trust boundary — a
  joining node reaches it before it holds any credential.
- **No config-driven database creation beyond the `databases:` block.**
