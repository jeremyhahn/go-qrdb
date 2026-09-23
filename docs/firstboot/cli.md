# Firstboot and System Bootstrap — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

One client verb, `qrdb init`, plus the daemon flags that shape what it does.

## `qrdb init`

Bootstrap a fresh qrdb system in one call. It delegates to the ceremony service
and creates the **barrier, the CA, the initial admin certificate and the
default role set in a single transactional ceremony**.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--so-pin` | string | — | **required** — Security Officer PIN (FIPS 140-3 / PCI-DSS v4 SO credential) |
| `--user-pin` | string | — | **required** — User PIN (FIPS 140-3 / PCI-DSS v4 user credential) |
| `--barrier-strategy` | string | `passphrase` | `passphrase \| shamir \| pkcs11 \| tpm2 \| awskms \| gcpkms \| azurekv \| vault` |
| `--shares` | int | — | total Shamir shares — required when `--barrier-strategy=shamir` |
| `--threshold` | int | — | Shamir M-of-N threshold — required when `--barrier-strategy=shamir` |
| `--officers` | stringArray | — | `username=path/to/csr.pem`, repeatable — **required when `--barrier-strategy=shamir` with `--threshold >= 2`** |
| `--hostname` | string | — | FQDN written into the admin/server certificate CN |
| `--sans` | string | — | comma-separated additional DNS / IP SANs |

The hardware strategy names (`pkcs11`, `tpm2`, `awskms`, `gcpkms`, `azurekv`,
`vault`) are accepted here, but the strategies themselves ship in go-xkms and
must have been registered into this server's strategy registry at composition
time — see [../barrier-seal/](../barrier-seal/architecture.md).

### Environment variables

All Viper-bound, and equivalent to the flags:

| Variable | Flag |
|---|---|
| `QRDB_SO_PIN` | `--so-pin` |
| `QRDB_USER_PIN` | `--user-pin` |
| `QRDB_BARRIER_STRATEGY` | `--barrier-strategy` |
| `QRDB_SHARES` | `--shares` |
| `QRDB_THRESHOLD` | `--threshold` |
| `QRDB_HOSTNAME` | `--hostname` |
| `QRDB_SANS` | `--sans` |

### Examples

```bash
# Single admin
qrdb init --so-pin SOPIN --user-pin USERPIN

# M-of-N, 3 of 5, officers named up front
qrdb init --barrier-strategy shamir --shares 5 --threshold 3 \
          --so-pin SOPIN --user-pin USERPIN \
          --officers alice=alice.csr --officers bob=bob.csr \
          --officers carol=carol.csr

# From the environment
QRDB_SO_PIN=... QRDB_USER_PIN=... qrdb init
```

**A `--threshold >= 2` request with no `--officers` fails closed** with the
ceremony's typed `ErrNoOfficers`. Each `--officers` entry points at a
PEM-encoded PKCS#10 CSR the officer generated themselves.

## Daemon-side mode selection

```bash
qrdbd start --threshold 1              # single-admin: barrier initializes, unseals, operational
qrdbd start --threshold 2 --total 3    # M-of-N: enters "enrolling" until every officer claims
```

In M-of-N the daemon stays in `enrolling` after start; officers claim their
certificates and shares through the ceremony verbs before the system becomes
`operational`. See [../ceremony/cli.md](../ceremony/cli.md).

**Only `--sm pebble` (the default), `--sm mvcc` and `--sm mvcc-fast` can
start.** Every other state machine type — `memory`, `standard`, `concurrent`,
`fast`, `disk`, `highperf`, `regular` — serves KV and batch only, with no
MVCC/transaction subsystem, and role and user creation commits atomically
through one. `qrdbd start` asks the running shard on **every start**, not
only the first — every role and user created later takes the same path —
right after leader election and before any role or user is created, and
exits with:

```text
cli: auth bootstrap failed at "atomic role/user creation requires an --sm state machine with transaction support": kvstore: store does not support atomic conditional writes
```

A fresh deployment wrote nothing, so restart it with one of the three
supported types. **An existing deployment on one of the other types cannot
start on this release**: its data belongs to that state machine, so changing
`--sm` is not a way out. Stay on the previous release until that state
machine gains transaction support.

On a follower the check cannot be asked — only the leader proposes — so a
follower defers to the leader's answer; every replica of a shard runs the same
state machine. A probe that fails for any other reason after the leader wait
also stops the start, at step `"guarded batch capability probe"`.

## Checking the result

```bash
qrdb ceremony status              # awaiting_init | enrolling | operational
qrdb roles list                   # the six built-in roles
qrdb users list                   # the admin user
qrdb barrier status
```

## Files produced

After a successful bootstrap the data directory holds `ca.pem` (readable by
operators), the server TLS key and certificate, the admin client certificate,
the storage directory, the Raft WAL, and a `.qrdb.lock` file preventing
concurrent daemon instances. Sensitive keys are stored **inside the barrier**.

## Notes

- **`qrdb ceremony init` has been removed** — this verb replaced it.
- `qrdb init` is idempotent, like the bootstrap it drives: re-running it on an
  initialized system creates nothing.

## Parity

`qrdb init` has a `CommandDefinition` in the integration parity suite.
