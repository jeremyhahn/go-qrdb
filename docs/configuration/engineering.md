# Configuration — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

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
- **`mode` absent means `raft`** — full backward compatibility with configs
  written before run modes existed.
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
- **Protected fields must reference a secret.** `credential://` and `literal://`
  references resolve at boot into enclave-backed values; a protected field with
  no reference, or an unresolvable one, **fails the boot**.

## Validation rules worth knowing

| Rule | Consequence |
|---|---|
| `mode: standalone` with `raft.join` or multi-member `raft.peers` | validation error — `requires mode: raft` (`mvcc`/`watch`/`lease.enabled` are valid in both modes since 2026-10-01) |
| A wire protocol enabled without its build tag | boot-time error **naming the tag** (`ValidateCapabilities`) |
| MySQL or PostgreSQL enabled with TLS or auth disabled | refused |
| etcd enabled on a database | any backend serves etcd; there is no longer an `engine` constraint (Lane 1C deleted `database.EtcdEngineUnsupportedError` along with the `engine` concept, OD-12/OD-13) |
| Objstore as `raft.sm_storage` | refused — snapshot/backup tier only |
| Non-pebble `sm_storage` with a state machine that carries its own storage (anything but `disk`, `fast`) | refused |
| Seal barrier enabled with `disk`/`fast` over `file` `sm_storage` | refused at start: file storage cannot be encrypted at rest (`pebble` storage is encrypted and allowed) |
| Per-database barrier on a database sharing the default store | refused |
| Multicast discovery with no secret | the method does not run |
| `raft.shard_id` at or above `shard.UserMin` (1000) in raft mode | validation error -- the node's own group sits below the user range, whose IDs the cluster issues to databases |

## Environment variables

Viper-bound, equivalent to their flags:

| Variable | Equivalent |
|---|---|
| `QRDB_SO_PIN` | `--so-pin` |
| `QRDB_USER_PIN` | `--user-pin` |
| `QRDB_BARRIER_STRATEGY` | `--barrier-strategy` |
| `QRDB_SHARES` | `--shares` |
| `QRDB_THRESHOLD` | `--threshold` |
| `QRDB_HOSTNAME` | `--hostname` |
| `QRDB_SANS` | `--sans` |
| `QRDB_SEAL_SHARE_TTL` | `seal.share_ttl` |

## CLI flag to YAML mapping

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

go-quicraft's `TransportConfig` carries other resource knobs (snapshot
receive rate/memory, connection caps, the inbound message-receive budget);
`buildHostConfig` sets only `MTLSConfig` on it, so those run at go-quicraft's
defaults and have no entry in this table. See `../configuration/cli.md` for
the 0.18.0 message-receive-budget default change
(`DefaultMaxMessageReceiveMemory` removed; now derived from
`MessageReceiveBudgetFrames`).
| `--sm`                          | `raft.sm_type` (`pebble` default; every registered type starts — see `../run-modes/architecture.md`) |
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

---

## Sections

`server`, `protocols`, `raft`, `storage`, `tls`, `logging`, `discovery`,
`mvcc`, `watch`, `lease`, `index`, `timeseries`, `auth`, `cors`,
`databases` (with per-database `mysql`, `postgresql` and `sql.concurrency`),
`embedding` (with `EmbeddingProviderConfig`), `rag`, `tenant`, `seal`,
`transport`, `import_session`, `mode`.
There is no `audit:` section (audit logging is always on) and no per-database
etcd key (etcd is enabled through `qrdb database create --etcd`, not YAML).

No compaction policy is configuration. A database's scheduled compaction and
the platform's node-keyspace policy are replicated records set through the
API, so they apply on every node and survive a node's loss; see
[../key-value/engineering.md](../key-value/engineering.md) "Scheduled
compaction" and "Node-keyspace compaction". *(Corrected: `mvcc.node_compaction`
and `databases[].compaction` were configuration keys. Neither is read any
more. The loader used to ignore an unknown key, so a file that still carried
one loaded and the key had no effect; it is now refused at load with
`*UnknownFieldError` instead.)*

**The complete field-by-field reference with every default lives in
[12-configuration.md](../12-configuration.md)**, which also carries worked
example configurations (minimal single node, 3-node cluster with static
discovery, high-throughput MVCC with watches and leases). That file is the
reference; this folder is the contract around it.

Per-section detail also appears in the owning feature's folder — `seal` in
[../barrier-seal/](../barrier-seal/), `tenant` in
[../multi-tenancy/](../multi-tenancy/), `embedding` and `rag` in
[../embeddings/](../embeddings/) and [../rag/](../rag/), `audit` in
[../audit/](../audit/), `discovery` in [../clustering/](../clustering/),
`storage` and `raft.sm_storage` in [../storage-engines/](../storage-engines/).

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
- **The `software` → `passphrase` canonicalization is its own test**, since a
  consumer seeing the alias would be a live bug.
- **Error tests** for `*LoadError` (unreadable and unparseable YAML) including
  `errors.Is`/`errors.As` through its `Unwrap()`.

## Observability

`qrdb admin capabilities` reports the **effective** profile after sealing —
build tags ∩ run mode ∩ config — including the `mode` field. It is the
authoritative answer to "did my configuration take effect", and it is more
reliable than reading the file back.
