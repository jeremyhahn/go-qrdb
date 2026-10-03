# Firstboot and System Bootstrap — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-02

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
qrdb init --so-pin SOPIN --user-pin USERPIN \
          --barrier-strategy passphrase --passphrase '<passphrase>'
```

That one call brings up the system barrier (`--barrier-strategy` defaults to
`passphrase`, which --- unlike `none` --- needs the `--passphrase` above;
see [../barrier-seal/user.md](../barrier-seal/user.md) for the other
strategies), the CA and the six built-in roles. The ceremony also resolves
the bootstrap admin's credential right there (see "Bringing your own admin
credential" below) and prints it; first boot binds it to the admin account
moments later, once the account exists. By default
(`auth.mtls_binding_policy: required`) the daemon will not finish
starting until that binding has committed — see Troubleshooting if it does
not.

`qrdb init` is the ONLY way a fresh node's system barrier is ever brought
up: `qrdb barrier init` targeting it is refused with a typed error pointing
back here. Auth also now requires the seal subsystem --- an auth-enabled
node started with `seal.enabled: false` is refused at config validation ---
so a deployment that wants no encryption runs `--barrier-strategy none`
above (no `--passphrase`) rather than disabling the seal.

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
qrdb roles list           # SO, Admin, Operator, User, Auditor
qrdb users list           # the admin user
```

In M-of-N the state stays `enrolling` until every officer has claimed their
certificate and share — see [../ceremony/user.md](../ceremony/user.md).

## What you get

Five built-in roles, created with stable identifiers so they mean the same thing
on every node and after every restart:

| Role | For |
|---|---|
| Security Officer | governance — barrier, certificates, roles, users. **Crypto operations are explicitly denied**, which is the separation-of-duties control |
| Admin | day-to-day key lifecycle, tenants, backups |
| Operator | routine operations, including seal and unseal |
| User | crypto operations only |
| Auditor | read-only everywhere |

A key custodian — a Shamir share holder — is not a role: it is enrolled in the
holder roster and authenticates with its holder certificate and factors. See
[../shamir-custody/](../shamir-custody/).

Plus an admin user for the system tenant, and a data directory containing
`ca.pem`, the server certificate and key, the storage directory, the Raft WAL
and a `.qrdb.lock` preventing a second daemon from starting on the same data.
There is no per-node admin certificate file: the admin's credential travels
through the ceremony and the cluster's own replicated store, not
a file on one node's disk.

## Bringing your own admin credential

Exactly one of three paths produces the bootstrap admin's certificate, chosen
by which flag you pass to `qrdb init` (none of these are an `admin-user` CLI
flag — all three are initialize-time, because the credential is chosen once,
at the same moment as the barrier and the CA):

- **`--admin-csr <path>`** — a CSR you generated with your own key (ideally on
  a PIV card or a security key). The ceremony's CA signs it as
  `CN=admin, OU=[system, administrator]`, using only the CSR's key. Your
  private key never leaves your machine.
- **`--admin-cert <path>`** (optionally with `--admin-cert-pool <path>`) — a
  certificate you already hold. Nothing is issued: the certificate is
  validated against the given pool (or, if you omit it, against this
  deployment's own CA bundle) and adopted unchanged. Your key never leaves
  your token.
- **Neither flag (the default)** — the server **generates** a key pair and
  returns the private key in the response, exactly once. **This path is for
  development, testing and evaluation only.** It does not meet FedRAMP, HIPAA
  or PCI DSS 4.0, because the server briefly held the private key in memory.
  `qrdb init` prints a loud warning when it is used. Use `--admin-csr` or
  `--admin-cert` for any deployment that must meet those controls.

The certificate (and, for the generate path, the key) are printed by `qrdb
init` itself — capture them immediately; the key cannot be retrieved again.
First boot binds the certificate's thumbprint to the admin account once that
account exists, which may be moments after `qrdb init` returns rather than
inside the same call: see Troubleshooting below for what to do if the daemon
does not finish starting.

## Troubleshooting

**"no officers".** `--threshold >= 2` requires `--officers` entries. Without
them you would end up with a quorum arrangement nobody holds a share in, so it
fails closed.

**"no admin certificate binding and no pending admin credential record".**
First boot waited for the admin's certificate to arrive (it may have been
issued by `qrdb init` on a different node of the cluster) and nothing showed
up in time. The admin-credential step is retriable: re-run `qrdb init` with
`--admin-csr`, `--admin-cert`, or no flags to generate, against the node that
is current Raft leader (`qrdb status`), and it issues a fresh credential and
retries the binding. Nothing about this is destructive — it does not touch the
barrier, the CA, or the SO/User PINs.

**"the bootstrap admin … could not log in under it" (auth.mtls_binding_policy
is "required").** The admin exists but has no live certificate binding —
typically because its one enrolled key expired, or because a fleet was moved
to `required` before every account finished migrating. Start with
`auth.mtls_binding_policy: enrolled`, enrol the admin's certificate
(`POST /users/{id}/cert-bindings`), then set `required` again. See
[../identity-rbac/user.md](../identity-rbac/user.md#moving-a-fleet-to-required-certificate-enrolment).
Note `enrolled` does **not** meet FedRAMP, HIPAA or PCI DSS 4.0 — it is a
migration step, not a resting configuration.

**"AdminCSRPEM and AdminCertPEM are mutually exclusive" / "conflicting admin
cert inputs".** You gave both `--admin-csr` and `--admin-cert`. Signing the
CSR would leave your certificate unbound; adopting the certificate would
discard your CSR. Pick one.

**"bootstrap admin credential rejected".** The presented certificate (or the
pool you supplied) does not parse, the certificate has expired, or it does not
chain to the pool (or, with no `--admin-cert-pool`, to this deployment's own
CA).

**Re-running `init` did nothing.** Correct — bootstrap is idempotent. Existing
roles and the admin user cause an immediate return. If the admin already holds
a certificate binding, a retried admin-credential flag is refused rather than
silently replacing a working binding.

**On a cluster, a node says "not leader".** Only the leader creates; followers
wait for the writes to replicate. Nothing to do.

**A node reports something other than "not leader" during first boot.** That is
this node's own failure and it is reported as itself — read the error. It is
not a replication delay and waiting will not clear it.

**"atomic role/user creation requires an --sm state machine with transaction
support".** The shard's state machine did not answer a guarded batch. Every
registered `--sm` type answers one, so this means the daemon was built with a
state machine registered without a transaction plane. See
[cli.md](cli.md#daemon-side-mode-selection).

**The system started but the barrier is sealed.** Bootstrap does not unseal for
you. See [../barrier-seal/user.md](../barrier-seal/user.md).

**You want to change the built-in roles.** You cannot — they are undeletable and
their permissions are fixed. Create additional roles instead:
[../identity-rbac/user.md](../identity-rbac/user.md).

**You lost the admin certificate.** Re-run `qrdb init` with a fresh
`--admin-csr` or `--admin-cert` (see "no admin certificate binding" above) —
this is the same retry path, not a separate recovery procedure. If you have
also lost the ability to authenticate at all, you are into barrier recovery
territory.
