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
| `--barrier-strategy` | string | `passphrase` | `none \| passphrase \| shamir \| pkcs11 \| tpm2 \| awskms \| gcpkms \| azurekv \| vault` |
| `--passphrase` | string | — | the system barrier's secret — **required when `--barrier-strategy=passphrase`**, rejected otherwise. `qrdb init` is the only way a fresh node's system barrier is ever brought up — `qrdb barrier init` targeting it is refused. |
| `--shares` | int | — | total Shamir shares — required when `--barrier-strategy=shamir` |
| `--threshold` | int | — | Shamir M-of-N threshold — required when `--barrier-strategy=shamir` |
| `--officers` | stringArray | — | `username=path/to/csr.pem`, repeatable — **required when `--barrier-strategy=shamir` with `--threshold >= 2`** |
| `--hostname` | string | — | FQDN written into the admin/server certificate CN |
| `--sans` | string | — | comma-separated additional DNS / IP SANs |
| `--admin-csr` | string | — | path to a PEM CSR; selects the CSR-sign admin-credential path (mutually exclusive with `--admin-cert`) |
| `--admin-cert` | string | — | path to a PEM certificate the admin already holds; selects bring-your-own-certificate (mutually exclusive with `--admin-csr`) |
| `--admin-cert-pool` | string | — | PEM trust-anchor bundle `--admin-cert` must chain to (optional; defaults to the ceremony's own CA bundle) |

Neither `--admin-csr` nor `--admin-cert` selects the dev/test-only GENERATE
path: the server mints the admin's key pair and returns the private key in
the response exactly once. `runInit` warns loudly on stderr when this path
is taken; it does not meet FedRAMP, HIPAA or PCI DSS.

The hardware strategy names (`pkcs11`, `tpm2`, `awskms`, `gcpkms`, `azurekv`,
`vault`) are accepted here, but the strategies themselves ship in go-xkms and
must have been registered into this server's strategy registry at composition
time — see [../barrier-seal/](../barrier-seal/architecture.md).

**`--so-pin` and `--user-pin` are both required on every call, not an
optional pair** -- this is the system barrier's dual control: `qrdb init`
is the only way a fresh node's system barrier is ever brought up, and that
same call demands both PINs, so one party holding only the SO PIN or only
the user PIN can never bring it up alone. (A tenant's own barrier has no
equivalent dual-control pair today; see
[../barrier-seal/user.md](../barrier-seal/user.md#common-tasks).)

### Environment variables

All Viper-bound, and equivalent to the flags:

| Variable | Flag |
|---|---|
| `QRDB_SO_PIN` | `--so-pin` |
| `QRDB_USER_PIN` | `--user-pin` |
| `QRDB_BARRIER_STRATEGY` | `--barrier-strategy` |
| `QRDB_PASSPHRASE` | `--passphrase` |
| `QRDB_SHARES` | `--shares` |
| `QRDB_THRESHOLD` | `--threshold` |
| `QRDB_HOSTNAME` | `--hostname` |
| `QRDB_SANS` | `--sans` |

### Examples

```bash
# Single admin, passphrase-strategy system barrier
qrdb init --so-pin SOPIN --user-pin USERPIN \
          --barrier-strategy passphrase --passphrase '<passphrase>'

# Single admin, no encryption (--passphrase omitted; rejected if given)
qrdb init --so-pin SOPIN --user-pin USERPIN --barrier-strategy none

# M-of-N, 3 of 5, officers named up front -- the barrier stays sealed
# until officers reconstruct it from their shares, so no --passphrase here
qrdb init --barrier-strategy shamir --shares 5 --threshold 3 \
          --so-pin SOPIN --user-pin USERPIN \
          --officers alice=alice.csr --officers bob=bob.csr \
          --officers carol=carol.csr

# From the environment
QRDB_SO_PIN=... QRDB_USER_PIN=... QRDB_PASSPHRASE=... qrdb init --barrier-strategy passphrase
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

**Every `--sm` state machine type can run first boot.** Role and user
creation commits each identity claim and its records as one guarded batch, and
every type — `pebble` (the default), `mvcc`, `memory`, `standard`,
`concurrent`, `fast`, `disk`, `highperf`, `regular` — serves guarded batches.
`qrdbd start` still confirms it on **every start**, right after leader election
and before any role or user is created, by asking the running shard for a
real no-op guarded batch. A state machine that could not answer it would stop
the start with:

```text
cli: auth bootstrap failed at "atomic role/user creation requires an --sm state machine with transaction support": kvstore: store does not support atomic conditional writes
```

No registered type does. On a follower the check cannot be asked — only the
leader proposes — so a follower defers to the leader's answer; every replica of
a shard runs the same state machine. A probe that fails for any other reason
after the leader wait also stops the start, at step
`"guarded batch capability probe"`.

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
