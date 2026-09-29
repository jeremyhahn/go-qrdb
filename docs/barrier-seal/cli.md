# Barrier and Seal — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-29

Command group: `qrdb barrier`. **This is the canonical barrier surface shared by
every CLI binary in the stack** (`qrdb`, `xkmsctl`, `quicbaasctl`,
`platformctl`). The verbs are `status`, `init`, `init-shamir`, `unseal`,
`seal`, `rekey`, `enable-encryption`, `recovery`, `officer` and `quorum`.

A Shamir barrier is unsealed, sealed, rekeyed and recovered only by a quorum
of authenticated share holders. `officer`, `quorum`, `rekey`, `init-shamir`
and `recovery` are documented in
[../shamir-custody/cli.md](../shamir-custody/cli.md).

All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb barrier status`

Show the current barrier state. Read-only and **safe to call against a sealed
barrier**. No flags.

```bash
qrdb barrier status
qrdb --output json barrier status
```

## `qrdb barrier init`

Initialize the cryptographic barrier.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--barrier-strategy` | string | `passphrase` | `none \| passphrase \| shamir` |
| `--passphrase` | string | — | **required** for `passphrase` and `shamir`; **rejected** for `none` |
| `--so-pin` | string | — | Security Officer PIN; **requires `--user-pin`** |
| `--user-pin` | string | — | User PIN; **requires `--so-pin`** |

Strategies:

- `none` — identity passthrough; data stored in cleartext. Development only.
- `passphrase` — single passphrase-derived master key. The default.
- `shamir` — single-secret Shamir seed. For M-of-N use `init-shamir`.

PINs are optional but follow the **dual-control rule**: supplying one requires
the other, so the pair satisfies FIPS 140-3 and PCI-DSS v4. Any PIN with
`--barrier-strategy=none` is refused with a typed `PINsNotAllowedError`.

```bash
qrdb barrier init --barrier-strategy passphrase --passphrase '<passphrase>'
qrdb barrier init --barrier-strategy none
qrdb barrier init --barrier-strategy passphrase --passphrase '<pp>' \
                  --so-pin SO123 --user-pin USR123
```

## `qrdb barrier init-shamir`

Initialize with M-of-N Shamir secret sharing. See
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

| Flag | Type | Required |
|---|---|---|
| `--passphrase` | string | yes |

```bash
qrdb barrier unseal --passphrase '<passphrase>'
```

## `qrdb barrier seal`

Seal the barrier. **Once sealed, the data path returns `ErrBarrierSealed` for
every read and write** until unsealed.

| Flag | Type | Meaning |
|---|---|---|
| `--so-pin` | string | **required** once an SO PIN is provisioned; refused with no PIN backend |
| `-y`, `--assume-yes` | bool | skip the interactive confirmation |

A Shamir barrier refuses a single-caller seal: M holders seal it with
`qrdb barrier quorum submit --op seal`.

Prompts for confirmation on a TTY; pass `-y` from scripts.

```bash
qrdb barrier seal -y
qrdb barrier seal --so-pin SO123 --assume-yes
```

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
*started*. **Poll `barrier status`** until the strategy changes from `none`.

A tenant on the shared system barrier cannot change its encryption; the server
rejects it. A migration to `shamir` splits the new unseal key to the holders
already enrolled on the barrier, `--shamir-total` of them.

```bash
qrdb barrier enable-encryption --strategy passphrase --passphrase 'hunter2'
qrdb barrier enable-encryption --tenant acme --strategy passphrase --passphrase 'pw'
qrdb barrier enable-encryption --strategy shamir \
     --shamir-threshold 2 --shamir-total 3 --passphrase 'pw'
```

## Per-database barriers

A non-raft database with its own `--data-dir`, created by the system tenant,
can have its own barrier; every other shape is refused because the barrier
would not be on its data path. Until per-database storage bindings land, a
non-system tenant cannot create one — see
[../databases/cli.md](../databases/cli.md):

```bash
qrdb database barrier-status secure
qrdb database seal secure
qrdb database unseal secure --secret "$SECRET"
```

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite. Note
that the cluster-scope encryption migration test lives in the cluster stage,
which is not currently wired into CI.
