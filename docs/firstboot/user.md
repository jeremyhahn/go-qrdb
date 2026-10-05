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
(`auth.mtls_binding_policy: required`) the daemon starts regardless, but
every node serves ONLY this ceremony's own status and retry endpoints
("awaiting admin credential") until that binding commits; every node in
the cluster — not only the one `qrdb init` happened to reach — leaves that
state the moment it does, through an event-driven watch on the admin's
replicated record rather than a restart or a per-request poll. See
Troubleshooting if a node still reports awaiting after the binding should
have committed.

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
- **`--admin-generate`** — the server **generates** a key pair and returns
  the private key in the response, exactly once. **This path is for
  development, testing and evaluation only.** It does not meet FedRAMP, HIPAA
  or PCI DSS 4.0, because the server briefly held the private key in memory.
  `qrdb init` prints a loud warning when it is used. Use `--admin-csr` or
  `--admin-cert` for any deployment that must meet those controls.

One of the three flags is **required**: there is no credential-less
default. Omitting all three is refused before anything is initialized
(`*NoAdminCredentialSelectedError`), the same way supplying more than one is
(`*ConflictingAdminCredentialInputsError`).

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
up in time. This no longer aborts the boot: the node logs this message as a
warning and starts anyway, coming up in the same "awaiting admin credential"
state described below, rather than shutting down and leaving the operator
with no running node to retry against. The admin-credential step is
retriable: re-run `qrdb init` with `--admin-csr`, `--admin-cert`, or
`--admin-generate`, against the node that is current Raft leader (`qrdb
status`). The retry both issues a fresh credential AND binds it to the admin
account, in the same call — it never hands back a credential that looks
successful but is not actually bound. Nothing about this is destructive — it
does not touch the barrier, the CA, or the SO/User PINs.

If the retry cannot complete the bind it refuses rather than returning an
unbound credential: "admin-credential reissue has no CertBinder wired" means
this composition's own wiring is missing the piece that binds a certificate
(an operator/configuration gap, not something retrying fixes); "the
bootstrap admin does not exist yet to bind it to" means first boot itself
has not created the admin account on this cluster yet — retry again once it
has (or once `qrdb status` shows this node as leader, if a different node is
about to run first boot); any other bind failure names its own cause and is
retriable once that cause is resolved.

**The node logs "awaiting admin credential" and most endpoints refuse with
503 (auth.mtls_binding_policy is "required").** The bootstrap admin exists
but has no live certificate binding — typically because its one enrolled key
expired, because a fleet was moved to `required` before every account
finished migrating, or because this is a fresh node whose first `qrdb init`
never completed the bind. The node does **not** refuse to boot over this: it
starts every listener — REST, gRPC, native QUIC, MCP, the embedded transport,
the Unix socket transport — in a reduced "awaiting admin credential" state
that serves exactly two things, with no certificate or other identity
required to reach either:

- `qrdb status` / `GET /v1/ceremony/status` — so you can confirm the state
  without a credential.
- `qrdb init` / `POST /v1/ceremony/initialize` — the SAME admin-credential
  retry described above. Run it with `--admin-csr`, `--admin-cert`, or
  `--admin-generate` against the current Raft leader; the SO/User PINs in
  the request are the only credential this call needs.

Every other endpoint answers `503` /
`{"code":"AWAITING_ADMIN_CREDENTIAL"}` until that retry binds a
certificate. The transition back to normal serving is event-driven, not a
poll: the node that actually performs the retry (ordinarily the current
Raft leader, since the retry is leader-gated) is told directly the moment
the bind commits, and every OTHER node in the cluster learns of it through
a watch on the admin's replicated record and leaves "awaiting" the same
way — there is no flag to flip and nothing to restart, on any node. If you would rather not wait for a production binding right now,
`auth.mtls_binding_policy: enrolled` lifts the restriction entirely (not a
resting configuration — it does not meet FedRAMP, HIPAA or PCI DSS 4.0); see
[../identity-rbac/user.md](../identity-rbac/user.md#moving-a-fleet-to-required-certificate-enrolment)
for migrating a fleet the other direction, back to `required`.

**A node that already left "awaiting" does not re-enter it on its own if
the admin's binding later expires while the node keeps running.** Leaving
"awaiting" is permanent for that running process — a deliberate choice, so
a certificate's natural expiry years into a node's uptime cannot silently
reintroduce the lockout window this state exists to close. If the admin's
only binding expires while the node is up, the fix is the same
admin-credential retry described above (you do not need to wait for a
restart to recover): only a NODE THAT RESTARTS re-runs the boot-time check
that decides "awaiting" from scratch, and that restarted node WILL come up
in "awaiting admin credential" if the expired binding was never replaced.

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

**You lost the admin certificate.** This is the SO-PIN-gated admin-credential
reissue, reached through the same `qrdb init` command as the "awaiting"
retry above (not a separate recovery procedure): re-run it with your SO PIN
(`--so-pin`) and a fresh `--admin-csr` or `--admin-cert`. A running node —
not only one that is "awaiting" — honours this call: it mints or adopts a
fresh certificate and binds it to the admin account in the same step,
without touching the barrier, the CA, or the SO/User PINs. The SO PIN is the
only thing standing between this call and anyone who can reach the node, so
treat it with the same care as the barrier's own secret. If you have also
lost the ability to authenticate at all, you are into barrier recovery
territory.
