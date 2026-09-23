# Firstboot and System Bootstrap — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Getting a brand-new server from nothing to something you can log into.

## Prerequisites

- A daemon that has never been initialized. Check without credentials:

```bash
curl -k https://db.example.com/v1/config/initialized
```

- The SO and User PINs you intend to set. Both are required.
- For M-of-N: a CSR from each officer, generated with their own key.

## First run

Simplest case, one administrator:

```bash
qrdb init --so-pin SOPIN --user-pin USERPIN
```

That one call creates the barrier, the CA, the admin certificate and the six
built-in roles, transactionally.

For a production M-of-N setup, name the officers up front:

```bash
qrdb init --barrier-strategy shamir --shares 5 --threshold 3 \
          --so-pin SOPIN --user-pin USERPIN \
          --officers alice=alice.csr --officers bob=bob.csr \
          --officers carol=carol.csr
```

You can supply everything through the environment instead —
`QRDB_SO_PIN`, `QRDB_USER_PIN`, `QRDB_BARRIER_STRATEGY`, `QRDB_SHARES`,
`QRDB_THRESHOLD`, `QRDB_HOSTNAME`, `QRDB_SANS`.

## Check it worked

```bash
qrdb ceremony status      # operational (single-admin) or enrolling (M-of-N)
qrdb barrier status
qrdb roles list           # SO, Admin, Operator, User, Auditor, Custodian
qrdb users list           # the admin user
```

In M-of-N the state stays `enrolling` until every officer has claimed their
certificate and share — see [../ceremony/user.md](../ceremony/user.md).

## What you get

Six built-in roles, created with stable identifiers so they mean the same thing
on every node and after every restart:

| Role | For |
|---|---|
| Security Officer | governance — barrier, certificates, roles, users. **Crypto operations are explicitly denied**, which is the separation-of-duties control |
| Admin | day-to-day key lifecycle, tenants, backups |
| Operator | routine operations, including seal and unseal |
| User | crypto operations only |
| Auditor | read-only everywhere |
| Custodian | holding a Shamir share, nothing else |

Plus an admin user for the system tenant, and a data directory containing
`ca.pem`, the server certificate and key, the admin client certificate, the
storage directory, the Raft WAL and a `.qrdb.lock` preventing a second daemon
from starting on the same data.

## Bringing your own admin credential

Three ways the admin identity can be established, and you choose by what you
give the daemon:

- **Present a certificate you already hold** — a PIV card, a corporate PKI
  credential. Nothing is issued; your existing certificate is validated against
  a pool *you* supply and adopted. Your key never leaves your token.
- **Submit a CSR** — the server signs it. Your key never leaves your machine.
- **Let the CA generate one** — you receive a key and certificate bundle.

The presented-certificate path is checked **first**. If you present a
certificate, that is the identity that gets bound — the server will not quietly
mint a second one from its CA and bind that instead.

## Troubleshooting

**"no officers".** `--threshold >= 2` requires `--officers` entries. Without
them you would end up with a quorum arrangement nobody holds a share in, so it
fails closed.

**"conflicting admin cert inputs".** You gave both a CSR and a certificate.
Signing the CSR would leave your certificate unbound; adopting the certificate
would discard your CSR. Pick one.

**"missing admin cert pool".** You presented a certificate but no pool to
validate it against. An absent pool is refused rather than treated as "trust
anything". Note the pool is **not** the platform CA — your certificate came
from somewhere else, so validating it against the CA this deployment is about
to create would reject every legitimate input.

**"admin cert rejected".** It does not parse, it has expired, or it does not
chain to the pool you supplied.

**Re-running `init` did nothing.** Correct — bootstrap is idempotent. Existing
roles and the admin user cause an immediate return.

**On a cluster, a node says "not leader".** Only the leader creates; followers
wait for the writes to replicate. Nothing to do.

**A node reports something other than "not leader" during first boot.** That is
this node's own failure and it is reported as itself — read the error. It is
not a replication delay and waiting will not clear it.

**"atomic role/user creation requires an --sm state machine with transaction
support".** The daemon was started with a state machine type that has no
MVCC/transaction subsystem — anything other than `pebble` (the default),
`mvcc` or `mvcc-fast` — and role and user creation needs one. The check runs
on every start, not only the first. On a fresh deployment nothing was created,
so restart with `--sm pebble`, `--sm mvcc` or `--sm mvcc-fast`. An existing
deployment on another type cannot switch — its data belongs to that state
machine — and has to stay on the previous release for now. See
[cli.md](cli.md#daemon-side-mode-selection).

**The system started but the barrier is sealed.** Bootstrap does not unseal for
you. See [../barrier-seal/user.md](../barrier-seal/user.md).

**You want to change the built-in roles.** You cannot — they are undeletable and
their permissions are fixed. Create additional roles instead:
[../identity-rbac/user.md](../identity-rbac/user.md).

**You lost the admin certificate.** Issue a new one with
`qrdb ca issue --username admin`, then bind it — see
[../certificate-authority/user.md](../certificate-authority/user.md) and
[../identity-rbac/user.md](../identity-rbac/user.md). If you have also lost the
ability to authenticate at all, you are into barrier recovery territory.
