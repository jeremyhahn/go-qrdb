# Configuration — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-23

## Exported surface

```go
// pkg/config
func DefaultConfig() *Config
func (c *Config) Validate() error                // returns the FIRST error
func (c *Config) ValidateCapabilities() error    // separate; called by composition roots
func (c *Config) EffectiveCapabilities() ...     // sealed into pkg/capabilities at boot

const DefaultServerHost = "0.0.0.0"              // the defaults-layer bind host
const DefaultSecureMemoryBudgetMiB int64 = 64    // ceiling of the derived secure-memory budget

type LoadError struct{ /* implements error and Unwrap() */ }
type UnknownFieldError struct{ /* File, Path, Line -- schema has no such key */ }
type ValidationError struct{ /* field path + reason */ }
```

`sdk/go/types_config.go` re-exports the config types.

## Load order

1. `DefaultConfig()` — production-ready defaults for every field.
2. The YAML file (via `--config`) unmarshaled **on top of** the defaults; only
   fields present in the YAML override.
3. `QRDB_`-prefixed environment variables, applied by Viper's `AutomaticEnv`
   over every default-bearing key (`server.host` → `QRDB_SERVER_HOST`).
4. CLI flags **explicitly set** override everything below them.

Then `Config.Validate()`.

Layers 2 and 3 both live in `config.Load(path)`, which the daemon calls only
for `--config`. Without a config file the daemon resolves from defaults and
flags alone.

## Invariants

- **An unrecognized key fails the load.** `config.Load` decodes the file
  strictly, before any merge with defaults or environment: a key that names no
  field anywhere in the `Config` schema returns `*UnknownFieldError`, naming
  the key's full path (`databases[2].protocol`), the file and, where known,
  the line. The known-key set is read from the same `yaml` struct tags Viper's
  own decode step resolves against, so it cannot drift from what `Load`
  actually honors into a second, separately maintained list. A key for a
  feature not compiled into this binary (`protocols.mysql` without
  `qrdb_mysql`) is still a *known* field — `pkg/config` declares every field
  unconditionally — and is refused by `ValidateCapabilities` instead, naming
  the missing tag.
- **A YAML file that omits a field leaves the default in place.** Absence is not
  zero.
- **A flag left at its own default does not overwrite a YAML value.** Only
  explicitly-set flags win.
- **`Validate()` returns the first error**, as a `*ValidationError` naming the
  field path and the reason. It does not accumulate.
- **`ValidateCapabilities()` is separate and is called by the composition
  roots**, not by `Validate()`. This keeps pure config validation
  **build-independent**: the same file is valid or invalid regardless of which
  build tags a binary carries.
- **Precedence is build tag > run mode > config.** Config narrows only; asking
  for more is a validation error, never a silent no-op.
- **`mode` absent means `raft`.**
- **No infrastructure literal on a configuration path.** Bind hosts, ports and
  advertised addresses come from the load order above. The one permitted
  literal is the defaults-layer value that path overrides — `DefaultServerHost`,
  read by both `DefaultConfig()` and the `--host` flag default, so there is one
  value and one place to change it.
- **`server.secure_memory_budget_mib` defaults to `0`, meaning "derive".**
  `DefaultConfig()` and the `--secure-memory-budget-mib` flag default both
  leave it at zero so a configured value is always distinguishable from the
  default; `qrdbd start` derives the budget from `RLIMIT_MEMLOCK`, capped at
  `DefaultSecureMemoryBudgetMiB`. `Validate()` refuses a negative value or one
  that overflows converting to bytes. See [cli.md](cli.md#secure-memory-budget)
  and [../deployment/engineering.md](../deployment/engineering.md).
- **A resolved value must reach its consumer.** `server.host` is carried into
  the `*Config` the daemon hands `server.New`, and every listener joins that
  field with its port; a field that is defined, validated and then replaced
  downstream is the same defect as no field at all.
- **An absent infrastructure value fails closed.** `clusterenroll` returns
  `*MissingBindHostError` rather than choosing a bind host, and
  `*MissingAdminPortError` rather than completing a bootstrap URL with a
  guessed port. Both are re-exported from `sdk/go/clusterenroll`.
- **A bind decision holds on every transport that serves the same handler.**
  The REST server's HTTP/3 sibling binds the host the TCP listener actually
  got; `Start` returns `*rest.ListenerHostMismatchError` if they land on
  different bind hosts, and `*rest.HTTP3StartError` if the QUIC socket cannot
  bind at all.
- **An unresolved protected field fails the boot.** `pkg/config` declares no
  `SecretValue` field, so `qrdbd` resolves no secret reference. An embedder
  runs `secret.ConfigProcessor` over its own configuration; a protected field
  whose `credential://` or `literal://` reference does not resolve fails
  `Process` with `ErrUnresolvedSecretURI`. See
  [../credentials/engineering.md](../credentials/engineering.md).

## Validation rules worth knowing

| Rule | Consequence |
|---|---|
| `mode: standalone` with `raft.join` or multi-member `raft.peers` | validation error — `requires mode: raft` (`mvcc`/`watch`/`lease.enabled` are valid in both modes) |
| A wire protocol enabled without its build tag | boot-time error **naming the tag** (`ValidateCapabilities`) |
| MySQL or PostgreSQL enabled with TLS or auth disabled | refused |
| etcd enabled on a database | any backend serves etcd |
| Objstore as `raft.sm_storage` | refused — snapshot/backup tier only |
| Non-pebble `sm_storage` with a state machine that carries its own storage (anything but `disk`, `fast`) | refused |
| Per-database barrier on a database sharing the default store | refused |
| Multicast discovery with no secret | the method does not run |
| `raft.shard_id` at or above `shard.UserMin` (1000) in raft mode | validation error -- the node's own group sits below the user range, whose IDs the cluster issues to databases |
| `observability.slow_sync_threshold` not in (0, 1m] or malformed (default `1s`) | `*ValidationError` on that field, from `Validate` and again from `ApplySlowSyncThreshold` on both boot paths. It is an observation threshold for the stall signal only, NOT a durability knob: quicraft keeps per-entry fsync with no tunable |

## Environment variables

Viper-bound, equivalent to their flags:

| Variable | Equivalent |
|---|---|
| `QRDB_SO_PIN` | `--so-pin` |
| `QRDB_USER_PIN` | `--user-pin` |
| `QRDB_BARRIER_STRATEGY` | `--barrier-strategy` |
| `QRDB_PASSPHRASE` | `--passphrase` |
| `QRDB_SHARES` | `--shares` |
| `QRDB_THRESHOLD` | `--threshold` |
| `QRDB_HOSTNAME` | `--hostname` |
| `QRDB_SANS` | `--sans` |
| `QRDB_SEAL_SHARE_TTL` | `seal.share_ttl` |
| `QRDB_OBSERVABILITY_SLOW_SYNC_THRESHOLD` | `observability.slow_sync_threshold` |

## CLI flag to YAML mapping

| Flag                            | YAML key                           |
|---------------------------------|------------------------------------|
| `--config`                      | N/A (specifies the YAML file path) |
| `--admin-port`                  | `server.admin_port`                |
| `--grpc-port`                   | `server.grpc_port`                 |
| `--enable-rest`                 | `protocols.rest`                   |
| `--enable-grpc`                 | `protocols.grpc`                   |
| `--data-dir`                    | `raft.data_dir`                    |
| `--deployment-id`               | `raft.deployment_id`               |
| `--shard-id`                    | `raft.shard_id`                    |
| `--replica-id`                  | `raft.replica_id`                  |
| `--raft-address`                | `raft.raft_address`                |
| `--listen`                      | `raft.listen_address`              |
| `--peers`                       | `raft.peers`                       |
| `--join`                        | `raft.join`                        |
| `--snapshot-entries`            | `raft.snapshot_entries`            |
| `--compaction-overhead`         | `raft.compaction_overhead`         |
| `--max-in-mem-log-size`         | `raft.max_in_mem_log_size`         |
| `--max-total-in-mem-log-size`   | `raft.max_total_in_mem_log_size`   |
| `--member-identities`           | `raft.member_identities`           |
| `--sm`                          | `raft.sm_type` (`pebble` default; every registered type starts — see `../run-modes/architecture.md`) |
| `--sm-backend`                  | `raft.sm_backend`                  |
| `--sm-storage`                  | `raft.sm_storage`                  |
| `--lease-read`                  | `raft.lease_read`                  |
| `--check-quorum`                | `raft.check_quorum`                |
| `--tls-ca-file`                 | `tls.ca_file`                      |
| `--tls-cert-file`               | `tls.cert_file`                    |
| `--tls-key-file`                | `tls.key_file`                     |
| `--log-level`                   | `logging.level`                    |
| `--log-format`                  | `logging.format`                   |
| `--discovery`                   | `discovery.method`                 |
| `--multicast-secret`            | `discovery.multicast_secret`       |
| `--dns-domain`                  | `discovery.dns_domain`             |
| `--debug-raft`                  | N/A (forces `logging.level: debug`)|
| `--auth-enabled`                | `auth.enabled`                     |

Each Raft flag's type, default and bounds are in [cli.md](cli.md#raft-flags).

go-quicraft's `TransportConfig` carries other resource knobs (snapshot
receive rate/memory, connection caps, the inbound message-receive budget);
`buildHostConfig` sets only `MTLSConfig` on it, so those run at go-quicraft's
defaults and have no entry in this table.

---

## Sections

The sections are listed in [architecture.md](architecture.md#sections).

No compaction policy is configuration. A database's scheduled compaction and
the platform's node-keyspace policy are replicated records set through the
API, so they apply on every node and survive a node's loss; see
[../key-value/engineering.md](../key-value/engineering.md) "Scheduled
compaction" and "Node-keyspace compaction".

**The complete field-by-field reference with every default lives in
[12-configuration.md](../12-configuration.md)**, which also carries worked
example configurations (minimal single node, 3-node cluster with static
discovery, high-throughput MVCC with watches and leases). That file is the
reference; this folder is the contract around it.

Per-section detail also appears in the owning feature's folder — `seal` in
[../barrier-seal/](../barrier-seal/), `tenant` in
[../multi-tenancy/](../multi-tenancy/), `embedding` and `rag` in
[../embeddings/](../embeddings/) and [../rag/](../rag/), `discovery` in
[../clustering/](../clustering/),
`storage` and `raft.sm_storage` in [../storage-engines/](../storage-engines/),
`observability` in [../api-server/](../api-server/).

## Test strategy

- **Unit**: the three-layer merge (a YAML omission keeps the default; an unset
  flag does not overwrite YAML), `Validate()` returning the first error with the
  correct field path, and each refusal in the table above.
- **Strict decoding**: an unknown top-level key, an unknown nested key (path
  threaded through the error), a misspelled key, and a key under a sequence
  element (`databases[N].x`) are each refused with `*UnknownFieldError`; a
  build-tag-gated key (`protocols.mysql`) is accepted as known regardless of
  tags, with `ValidateCapabilities` the only thing that can refuse it; every
  shipped and documented example config loads strictly.
- **`ValidateCapabilities` is tested independently of build tags** — that
  separation is the property it exists for.
- **Error tests** for `*LoadError` (unreadable and unparseable YAML) including
  `errors.Is`/`errors.As` through its `Unwrap()`.

## Observability

`qrdb admin capabilities` reports the **effective** profile after sealing —
build tags ∩ run mode ∩ config — including the `mode` field. It is the
authoritative answer to "did my configuration take effect", and it is more
reliable than reading the file back.
