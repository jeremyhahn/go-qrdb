# Barrier and Seal — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb barrier`. **This is the canonical barrier surface shared by
every CLI binary in the stack** (`qrdb`, `xkmsctl`, `quicbaasctl`,
`platformctl`). The verbs are `status`, `init`, `init-shamir`, `unseal`,
`seal`, `cluster-unseal`, `rekey`, `enable-encryption`, `recovery`, `officer` and `quorum`.

A Shamir barrier is unsealed, sealed, rekeyed and recovered only by a quorum
of authenticated share holders. `officer`, `quorum`, `rekey`, `init-shamir`
and `recovery` are documented in
[../shamir-custody/cli.md](../shamir-custody/cli.md).

All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb barrier status`

Show the current barrier state. Read-only. No flags.

Against a sealed node the command prints two facts: `Sealed: true` and
`Initialized:` (`initialized` in JSON), every other field empty. `Initialized:
false` is a node waiting for its first boot, which needs `qrdb init`;
`Initialized: true` is an initialized node whose barrier is sealed. Unseal
progress is printed by the unseal call itself.

Against a node that a cluster seal holds, or that the serving lease cuts off,
the command fails with `transport: barrier sealed`. That refusal is the
answer: the node is up, initialized and held.

On a serving node the output includes the node barrier's cluster seal: whether
the cluster has activated `cluster-seal`, the record's state and epoch, who
wrote it, and this node's own state (see
[api.md](api.md#the-cluster_seal-status-object)). A node that is held or cut
off refuses status, so read the record from a node that serves. `qrdb tenant
barrier-status` and `qrdb database barrier-status` print the same for their
barriers.

```bash
qrdb barrier status
qrdb --output json barrier status
```

## `qrdb barrier init`

Refused by a `qrdbd` server: its system barrier is brought up by `qrdb init`
(see [../firstboot/cli.md](../firstboot/cli.md)), and the server answers this
command with `SystemBarrierInitRefusedError`, whose message names `qrdb init`.
Only a barrier served without the first-boot ceremony, such as an embedded
standalone node's, is initialized by it. The command carries no tenant or
database scope: a tenant admin initializes a tenant barrier with
`qrdb tenant barrier-init`.

The CLI validates its flags before it calls the server, so a malformed
invocation fails locally first:

A `qrdbd` node built with the first-boot ceremony refuses it, whatever the
flags, with a typed error naming `qrdb init`, which brings that node's system
barrier up ([../firstboot/cli.md](../firstboot/cli.md)). A refused initialize
writes nothing, PINs included. A node with no first-boot ceremony, such as a
standalone embedded node, accepts it.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--barrier-strategy` | string | `passphrase` | `none \| passphrase` (`shamir` is refused — use `init-shamir`) |
| `--passphrase` | string | — | **required** for `passphrase`; **rejected** for `none` |
| `--so-pin` | string | — | Security Officer PIN; **requires `--user-pin`** |
| `--user-pin` | string | — | User PIN; **requires `--so-pin`** |

Local validation: `--barrier-strategy shamir` is refused with an
`InvalidArgError` (a Shamir barrier is initialized with `init-shamir`); a PIN
without its pair is refused with an `InvalidArgError`; any PIN with
`--barrier-strategy=none` is refused with a `PINsNotAllowedError`.

```bash
qrdb barrier init --barrier-strategy passphrase --passphrase '<passphrase>'
# against qrdbd: refused, the system barrier is brought up by "qrdb init"
```

## `qrdb barrier init-shamir`

Initialize with M-of-N Shamir secret sharing. A `qrdbd` server refuses it with
`SystemBarrierInitRefusedError`, as it refuses `init`: its system barrier is
put under Shamir custody by `qrdb init --barrier-strategy shamir`. See
[../shamir-custody/cli.md](../shamir-custody/cli.md) for the custody model.

| Flag | Type | Meaning |
|---|---|---|
| `--threshold` | int | minimum holders to unseal (M) |
| `--total` | int | N — must equal the number of enrolled holders |
| `--policy` | string | factor policy: `designated` (default) or `any-enrolled` |
| `--so-pin` / `--user-pin` | string | same dual-control rule as `init` |

There is no secret to supply: the barrier generates its keys. **The response
is share-free.** One envelope is sealed to each enrolled holder, who claims it
once — see [../shamir-custody/cli.md](../shamir-custody/cli.md).

```bash
qrdb barrier init-shamir --threshold 3 --total 5
```

## `qrdb barrier unseal`

Unseal using the master passphrase supplied at `init`. A Shamir barrier refuses
it: M holders unseal it with `qrdb barrier quorum submit --op unseal`.

A `none` barrier holds no secret. Its node unseals it at every start, so it
needs no unseal after a restart; one sealed by hand with `barrier seal` is
unsealed with no `--passphrase`. A passphrase barrier refuses an unseal without
one.

| Flag | Type | Required |
|---|---|---|
| `--passphrase` | string | for a passphrase barrier; omitted for `none` |

```bash
qrdb barrier unseal --passphrase '<passphrase>'   # passphrase barrier
qrdb barrier unseal                               # none barrier
```

An uninitialized barrier refuses it with `barrier not initialized`.

## `qrdb barrier change-passphrase`

Re-wrap the root key of an unsealed, passphrase-sealed barrier under a new
passphrase. The root key and the data are unchanged; the next unseal takes
`--next` and refuses `--current`. A `--current` that does not open the barrier
is refused with `supplied credentials do not match the barrier's current key`
and writes nothing. Requires `seal:rekey` and the Security Officer PIN, which
every transport verifies before the barrier is reached, as for `barrier seal`
and `barrier rekey`: once an SO PIN is provisioned, `--so-pin` is required, and
a missing or wrong one is refused with nothing written. A node with no PIN
backend refuses the change outright (`pin: no PIN backend configured`).

| Flag | Type | Required |
|---|---|---|
| `--current` | string | yes |
| `--next` | string | yes |
| `--so-pin` | string | once an SO PIN is provisioned |

```bash
qrdb barrier change-passphrase --current '<current>' --next '<next>' --so-pin "$SO_PIN"
```

`TestBarrierCLI_ChangePassphrase_PerProtocol` drives it on REST, gRPC, QUIC,
the Unix socket and MCP.

## `qrdb barrier seal`

Seal the barrier. **Once sealed, the data path returns `ErrBarrierSealed` for
every read and write** until unsealed.

**In a cluster that has activated `cluster-seal`, this seals the node barrier on
every node**, and each node wipes its own key. Before activation it seals only
the node you are connected to, and `barrier status` reports
`cluster_seal.active: false`. `qrdb tenant barrier-seal` and `qrdb database
seal` behave the same way for their barriers. See
[architecture.md](architecture.md#sealing-in-a-cluster).

| Flag | Type | Meaning |
|---|---|---|
| `--so-pin` | string | **required** once an SO PIN is provisioned; refused with no PIN backend |
| `-y`, `--assume-yes` | bool | skip the interactive confirmation |

A Shamir barrier refuses a single-caller seal: M holders seal it with
`qrdb barrier quorum submit --op seal`.

**The seal waits for in-flight store work first.** Before the key leaves
memory, the node refuses new writes, flushes and compactions on its encrypted
stores and waits up to `seal.drain_timeout` (default `10s`) for the ones
already running. If they outlast it the barrier **still seals**, and the
command exits non-zero with
`qrdb/seal/quiesce: in-flight work did not drain before the barrier sealed:
after <timeout>, stores still in flight: <dir>, ...`. That error never means
the barrier is unsealed: `qrdb barrier status` shows it sealed.

Prompts for confirmation on a TTY; pass `-y` from scripts.

```bash
qrdb barrier seal -y
qrdb barrier seal --so-pin SO123 --assume-yes
```

## `qrdb barrier cluster-unseal`

End a cluster-wide seal of the node barrier. Run it, as the platform operator,
on a node you have already unsealed — after a cluster seal, every node is
unsealed, in place or at boot after a restart, and comes back **held** until
this command writes the clear record. It unseals nothing by itself: every node still
unseals itself with its own credential. No flags.

```bash
qrdb barrier unseal --passphrase '<passphrase>'   # this node
qrdb barrier cluster-unseal                       # once: clears the record
qrdb barrier status --output json                 # cluster_seal.local: serving
```

Refused with `FAILED_PRECONDITION` before the cluster has activated
`cluster-seal`, and with `UNAVAILABLE` on a node that has not unsealed itself.
Tenant and database barriers have no such verb: unsealing them on a node
clears their record.

## `qrdb barrier rekey`

Open a rekey round on a Shamir barrier. It completes only when M current
holders submit to `--op rekey`, then generates a fresh unseal key, re-wraps the
root key and seals new envelopes to every holder then enrolled. **All
previously distributed shares are cryptographically revoked.** Flags and the
revocation form are in [../shamir-custody/cli.md](../shamir-custody/cli.md).

```bash
qrdb barrier rekey --threshold 3 --so-pin "$SO_PIN"
```

## `qrdb barrier enable-encryption`

Migrate a barrier initialized without encryption (strategy `none`) to a keyed
strategy, **re-encrypting all at-rest data in place**.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--strategy` | string | — | **required** — `passphrase` or `shamir` |
| `--passphrase` | string | — | unseal passphrase for the keyed barrier |
| `--shamir-threshold` | int | — | M, when `--strategy shamir` |
| `--shamir-total` | int | — | N, when `--strategy shamir` |
| `--tenant` | string | system barrier | scope to one tenant's dedicated barrier |
| `--mechanism` | string | `swap` | `swap \| in-place` |
| `--availability` | string | `quiesced` | `quiesced \| online` |
| `-y`, `--assume-yes` | bool | false | skip the confirmation prompt |

The migration runs **asynchronously**: a system-scope migration recycles the
node's data plane in process, so the command returns once the migration has
*started*. **Poll `barrier status`** until the strategy changes from `none`,
or until it fails with `transport: barrier sealed` when the migration brings
the node back sealed.

A system-scope migration re-encrypts every store the node barrier covers: the
`pebble` or `file` data store, the vector store, and in raft mode the WAL and
the state machine's storage, including `sm_storage: file`. A `file` store is
copied record by record under either `--mechanism`. See
[engineering.md](engineering.md#what-a-system-scope-migration-re-encrypts).

Without `--tenant` the caller needs `seal:enable_encryption` in the `system`
tenant. With `--tenant` the caller needs `tenant_barrier:enable_encryption`
for that tenant, so a tenant admin can migrate their own tenant's barrier and
no other.

A tenant on the shared system barrier cannot change its encryption; the server
rejects it. A migration to `shamir` splits the new unseal key to the holders
already enrolled on the barrier, `--shamir-total` of them. The node comes back
sealed; each holder claims its envelope from the sealed node and M of them
unseal it through the quorum (the in-process restart carries the ceremony
service, and with it the envelopes, across).

```bash
qrdb barrier enable-encryption --strategy passphrase --passphrase 'hunter2'
qrdb barrier enable-encryption --tenant acme --strategy passphrase --passphrase 'pw'
qrdb barrier enable-encryption --strategy shamir \
     --shamir-threshold 2 --shamir-total 3 --passphrase 'pw'
```

## Per-database barriers

A non-raft database with its own `--data-dir`, created by the system tenant,
can have its own barrier; every other shape is refused because the barrier
would not be on its data path. A non-system tenant cannot create one — see
[../databases/cli.md](../databases/cli.md):

```bash
qrdb database barrier-status secure
qrdb database seal secure
qrdb database unseal secure --secret "$SECRET"
```

A database seal, like a tenant seal, reaches every node once `cluster-seal` is
activated; only the tenant that owns the database may seal or unseal it.

## Per-node barrier keys

`qrdb tenant barrier-init` signs the barrier's epoch-1 init statement
([architecture.md](architecture.md#per-node-barrier-keys)) with the key in the
global `--key-file` — the first admin's, the same key that signs the init
proof — and sends it with the init. No flag controls it. Without
`--key-file` no statement is sent, and the init of a `raft` tenant's barrier
is refused (`needs the barrier owner's signed init statement`); a `barrier` or `volume` tenant's init
proceeds without one. The node that takes the init records the statement, and
every other node serving the barrier initialises its own key from it; no
command shows the record. On a cluster that has not activated
`per-node-barrier-keys`, `qrdb tenant create --isolation-type
barrier|volume|raft` fails as unavailable (exit status non-zero,
`cluster feature "per-node-barrier-keys" is not active yet`) and creates
nothing; a standalone node never refuses it. See
[../multi-tenancy/cli.md](../multi-tenancy/cli.md#qrdb-tenant-create).

## Parity

The verbs are exercised end to end by `test/integration/barrier_cli_test.go`
and `test/integration/barrier_permutations_cli_test.go`. The cluster-scope
encryption migration test (`test/integration/cluster_enable_encryption_test.go`)
runs under `make integration-test-barrier-seal`. CI runs them in the
`integration-features` shard whose feature list includes `barrier-seal`
(`.github/workflows/ci.yml`), required by `ci-gate`.
