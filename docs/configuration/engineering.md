# Configuration — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

```go
// pkg/config
func DefaultConfig() *Config
func (c *Config) Validate() error                // returns the FIRST error
func (c *Config) ValidateCapabilities() error    // separate; called by composition roots
func (c *Config) EffectiveCapabilities() ...     // sealed into pkg/capabilities at boot
func (c *SealConfig) CanonicalStrategy() string  // canonicalizes the "software" alias

type LoadError struct{ /* implements error and Unwrap() */ }
type ValidationError struct{ /* field path + reason */ }
```

`sdk/go/types_config.go` re-exports the config types.

## Load order

1. `DefaultConfig()` — production-ready defaults for every field.
2. The YAML file (via `--config`) unmarshaled **on top of** the defaults; only
   fields present in the YAML override.
3. CLI flags **explicitly set** override the corresponding YAML value.

Then `Config.Validate()`.

## Invariants

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
- **`seal.strategy: software` is a deprecated alias for `passphrase`**,
  canonicalized **once at load** by `SealConfig.CanonicalStrategy()`, so every
  downstream consumer sees `passphrase`.
- **`mode` absent means `raft`** — full backward compatibility with configs
  written before run modes existed.
- **Protected fields must reference a secret.** `credential://` and `literal://`
  references resolve at boot into enclave-backed values; a protected field with
  no reference, or an unresolvable one, **fails the boot**.

## Validation rules worth knowing

| Rule | Consequence |
|---|---|
| `mode: standalone` with `mvcc.enabled` / `watch.enabled` / `lease.enabled: true`, `raft.join`, or multi-member `raft.peers` | validation error — `requires mode: raft` |
| A wire protocol enabled without its build tag | boot-time error **naming the tag** (`ValidateCapabilities`) |
| MySQL or PostgreSQL enabled with TLS or auth disabled | refused |
| etcd enabled on a database that is not `engine: kv` + `backend: raft` | refused |
| Objstore as `raft.sm_storage` | refused — snapshot/backup tier only |
| Non-pebble `sm_storage` with a pebble-native state machine (`pebble`, `mvcc`) | refused |
| Per-database barrier on a database sharing the default store | refused |
| Multicast discovery with no secret | the method does not run |

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
| `--sm`                          | `raft.sm_type`                     |
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
| `--audit-enabled`               | `audit.enabled`                    |

---

## Sections

`server`, `protocols`, `raft`, `storage`, `tls`, `logging`, `discovery`,
`mvcc`, `watch`, `lease`, `index`, `timeseries`, `auth`, `audit`, `cors`,
`databases` (with `ProtocolConfig` and `EtcdConfig`), `embedding` (with
`EmbeddingProviderConfig`), `rag`, `tenant`, `seal`, `mode`.

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
