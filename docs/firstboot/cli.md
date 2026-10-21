# Firstboot and System Bootstrap — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

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
| `--officers` | stringArray | — | `username=path/to/csr.pem`, repeatable — **required when `--barrier-strategy=shamir` with `--threshold >= 2`**; mutually exclusive with `--officers-file` |
| `--custodians` | stringArray | — | key custodian share holder, `username=path/to/csr.pem`, repeatable; mutually exclusive with `--officers-file` |
| `--officers-file` | string | — | JSON file naming every initial share holder with the full holder shape `--officers`/`--custodians` cannot express ([../ceremony/cli.md](../ceremony/cli.md)); mutually exclusive with `--officers`/`--custodians` |
| `--policy` | string | `designated` | factor policy of the initial share set: `designated` or `any-enrolled` |
| `--hostname` | string | — | FQDN written into the admin/server certificate CN |
| `--sans` | string | — | comma-separated additional DNS / IP SANs |
| `--admin-csr` | string | — | path to a PEM CSR; selects the CSR-sign admin-credential path (mutually exclusive with `--admin-cert`/`--admin-generate`) |
| `--admin-cert` | string | — | path to a PEM certificate the admin already holds; selects bring-your-own-certificate (mutually exclusive with `--admin-csr`/`--admin-generate`) |
| `--admin-cert-pool` | string | — | PEM trust-anchor bundle `--admin-cert` must chain to; only valid with `--admin-cert`, and required unless the certificate was issued by this deployment's own CA |
| `--admin-generate` | bool | `false` | selects the dev/test-only GENERATE path explicitly (mutually exclusive with `--admin-csr`/`--admin-cert`) |

Exactly one of `--admin-csr`, `--admin-cert` or `--admin-generate` is
**required** -- there is no credential-less default.
`--admin-generate` makes the server mint the admin's key pair and return the
private key in the response exactly once. `runInit` warns loudly on stderr
when this path is taken; it does not meet FedRAMP, HIPAA or PCI DSS.

The hardware strategy names (`pkcs11`, `tpm2`, `awskms`, `gcpkms`, `azurekv`,
`vault`) are accepted here, but the strategies themselves ship in go-xkms and
must have been registered into this server's strategy registry at composition
time — see [../barrier-seal/](../barrier-seal/architecture.md).

**`--so-pin` and `--user-pin` are both required on every call, not an
optional pair** -- this is the system barrier's dual control: `qrdb init`
is the only way a fresh node's system barrier is ever brought up, and that
same call demands both PINs, so one party holding only the SO PIN or only
the user PIN can never bring it up alone. (A tenant's own barrier has no
equivalent dual-control pair; see
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
# Single admin, passphrase-strategy system barrier, admin key kept local
qrdb init --so-pin SOPIN --user-pin USERPIN \
          --barrier-strategy passphrase --passphrase '<passphrase>' \
          --admin-csr admin.csr

# Single admin, no encryption (--passphrase omitted; rejected if given)
qrdb init --so-pin SOPIN --user-pin USERPIN --barrier-strategy none \
          --admin-cert admin.pem --admin-cert-pool corp-ca.pem

# M-of-N, 3 of 5, officers named up front -- the barrier stays sealed
# until officers reconstruct it from their shares, so no --passphrase here
qrdb init --barrier-strategy shamir --shares 5 --threshold 3 \
          --so-pin SOPIN --user-pin USERPIN --admin-csr admin.csr \
          --officers alice=alice.csr --officers bob=bob.csr \
          --officers carol=carol.csr

# From the environment
QRDB_SO_PIN=... QRDB_USER_PIN=... QRDB_PASSPHRASE=... \
  qrdb init --barrier-strategy passphrase --admin-csr admin.csr
```

**A `--threshold >= 2` request with no `--officers` fails closed** with the
ceremony's typed `ErrNoOfficers`. Each `--officers` entry points at a
PEM-encoded PKCS#10 CSR the officer generated themselves.

## M-of-N enrollment

After an M-of-N `qrdb init` (`--barrier-strategy shamir`, `--threshold >= 2`)
the system stays in `enrolling`; officers claim their
certificates and shares through the ceremony verbs before the system becomes
`operational`. See [../ceremony/cli.md](../ceremony/cli.md).

## State machine check

**Every `qrdbd start --sm` state machine type can run first boot.** Role and user
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
qrdb roles list                   # the built-in roles
qrdb users list                   # the admin user
qrdb barrier status
```

## Files produced

After a successful bootstrap the data directory holds `ca.pem` (readable by
operators), the node's TLS key and certificate (`node-key.pem`,
`node-cert.pem`), the storage directory and the Raft WAL.

## Notes

- `qrdb init` is idempotent, like the bootstrap it drives: re-running it on an
  initialized system creates nothing.

## Parity

`test/integration/init_cli_test.go` drives `qrdb init` end to end over REST,
gRPC, QUIC, MCP and the Unix socket.
