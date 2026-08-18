# Configuration — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

How the daemon is configured: the load order, the validation contract, and the
sections that exist.

## Problem

A store with six transports, four storage backends, two run modes, a dozen
build-tag-gated features and a barrier has a large configuration surface. A
config system that silently ignores an unrecognized or impossible setting turns
every misconfiguration into a production surprise.

## Shape chosen

**Three layers, in one order, then validate everything.**

1. `DefaultConfig()` initializes all fields with production-ready defaults.
2. The YAML file (if provided via `--config`) is unmarshaled on top of the defaults. Only fields present in the YAML file override the defaults.
3. CLI flags that are explicitly set override the corresponding YAML values.


**Only fields present in the YAML override the defaults**, and only flags
explicitly set override the YAML. A flag left at its own default does not
silently overwrite a YAML value.

After merging, `Config.Validate()` checks **every** field and returns the first
error as a `*ValidationError` carrying the field path and the reason:

```
config: validation failed for "raft.shard_id": must be greater than 0
```

### Two error types

| Error | Returned when |
|---|---|
| `*LoadError` | the YAML file cannot be read or parsed |
| `*ValidationError` | a field has an invalid value |

Both implement `error`; `LoadError` also implements `Unwrap()` for `errors.Is`
and `errors.As`.

### The separate capability gate

`Config.ValidateCapabilities` is a **deliberately separate** check, called by
the composition roots rather than by `Validate()`. It fails the boot when a
feature is enabled whose build tag is absent, **naming the tag**.

Keeping it separate is the point: pure configuration validation stays
**build-independent**, so the same config file is valid or invalid regardless of
how a particular binary was compiled. Only composition knows which tags are
present.

### Alternatives rejected

- **Ignoring unknown or impossible settings.** Rejected: precedence is
  **build tag > run mode > config**, and config asking for more than the mode or
  the binary allows is a **validation error, never a silent no-op** — see
  [../run-modes/](../run-modes/architecture.md).
- **Folding the capability check into `Validate()`.** Rejected on the
  build-independence argument above.
- **Reporting all validation errors at once.** Not done — `Validate()` returns
  the **first** error. That is a real ergonomic cost on a large config, and it is
  the current behaviour.

## Sections

`server`, `protocols`, `raft`, `storage`, `tls`, `logging`, `discovery`,
`mvcc`, `watch`, `lease`, `index`, `timeseries`, `auth`, `audit`, `cors`,
`databases` (with nested `ProtocolConfig` and `EtcdConfig`), `embedding` (with
`EmbeddingProviderConfig`), `rag`, `tenant`, plus `seal` and `mode`.

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
| etcd | 2379 | gRPC + mTLS | `databases[].protocol.etcd.listen_addr` |

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
- **No environment-variable-only configuration** beyond the documented bindings
  (`QRDB_SO_PIN`, `QRDB_USER_PIN`, `QRDB_BARRIER_STRATEGY`, `QRDB_SHARES`,
  `QRDB_THRESHOLD`, `QRDB_HOSTNAME`, `QRDB_SANS`, `QRDB_SEAL_SHARE_TTL`).
- **No config-driven database creation beyond the `databases:` block.**
