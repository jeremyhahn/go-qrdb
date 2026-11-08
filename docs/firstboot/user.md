# Firstboot and System Bootstrap — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-22
> Conflicts with this document are defects. Fix them here first.

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
          --barrier-strategy passphrase --passphrase '<passphrase>' \
          --admin-csr admin.csr
```

That one call brings up the system barrier (`--barrier-strategy` defaults to
`passphrase`, which --- unlike `none` --- needs the `--passphrase` above;
see [../barrier-seal/user.md](../barrier-seal/user.md) for the other
strategies), the CA and the built-in roles. The ceremony also resolves
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
back here. Auth requires the seal subsystem --- an auth-enabled
node started with `seal.enabled: false` is refused at config validation ---
so a deployment that wants no encryption runs `--barrier-strategy none`
above (no `--passphrase`) rather than disabling the seal.

For a production M-of-N setup, name the officers up front:

```bash
qrdb init --barrier-strategy shamir --shares 5 --threshold 3 \
          --so-pin SOPIN --user-pin USERPIN --admin-csr admin.csr \
          --officers alice=alice.csr --officers bob=bob.csr \
          --officers carol=carol.csr
```

You can supply the PINs, strategy, shares and names through the environment
instead — `QRDB_SO_PIN`, `QRDB_USER_PIN`, `QRDB_BARRIER_STRATEGY`,
`QRDB_PASSPHRASE`, `QRDB_SHARES`,
`QRDB_THRESHOLD`, `QRDB_HOSTNAME`, `QRDB_SANS`.

## Check it worked

```bash
qrdb ceremony status      # operational (single-admin) or enrolling (M-of-N)
qrdb barrier status
qrdb roles list           # so, admin, operator, user, auditor (+ owner)
qrdb users list           # the admin user
```

In M-of-N the state stays `enrolling` until every officer has claimed their
certificate and share — see [../ceremony/user.md](../ceremony/user.md).

## What you get

Five built-in roles, created with stable identifiers so they mean the same thing
on every node and after every restart, plus an all-permission `owner`
convenience role unless separation of duties is enforced:

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
`ca.pem`, the server certificate and key, the storage directory and the Raft
WAL.
There is no per-node admin certificate file: the admin's credential travels
through the ceremony and the cluster's own replicated store, not
a file on one node's disk.

## Bringing your own admin credential

Exactly one of three `qrdb init` flags produces the bootstrap admin's
certificate. The credential is chosen once, with the barrier and the CA:

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
default. Omitting all three, or passing more than one, is refused before
anything is initialized.

The certificate (and, for the generate path, the key) are printed by `qrdb
init` itself — capture them immediately; the key cannot be retrieved again.
First boot binds the certificate's thumbprint to the admin account once that
account exists, which may be moments after `qrdb init` returns rather than
inside the same call: see Troubleshooting below for what the daemon does
when that binding has not arrived by the time it finishes starting.

## Naming a system Security Officer

Under strict separation of duties (`auth.separation_of_duties`, always on in
FIPS 140-3 mode), only a Security Officer separate from the admin can create
roles or run the built-in role reconciles. The system admin can never hold the
`so` role. Name that SO in the same `qrdb init` call:

```bash
qrdb init --so-pin SOPIN --user-pin USERPIN --barrier-strategy none \
          --admin-csr admin.csr --so-username secofficer --so-csr so.csr
```

`--so-csr` is a CSR made with the SO's own key, which must not be the admin's
key. `--so-cert` (with `--so-chain` for intermediates) adopts a certificate the
SO already holds. First boot creates the SO's account in the system tenant
with only the `so` role, binds its certificate and records `--so-pin` as its
PIN. Re-running first boot never creates a second SO. In the standard posture
the SO is optional.

## Troubleshooting

**"strict separation of duties requires a system SO".** The server runs
strict separation of duties and was initialized without `--so-username`.
First boot refuses before any write (`*firstboot.SystemSORequiredError`),
because no principal could ever manage roles. Name the system SO with the
flags above when you run `qrdb init`.

**"ceremony: system SO rejected".** The SO flags named the admin's username
or key, no SO PIN, a CSR that does not parse, or a certificate that does not
verify under the system tenant. Fix the named input and re-run `qrdb init`.

**"M-of-N requires at least one officer".** `--barrier-strategy shamir`
needs its share holders named: `--officers`, `--custodians` or
`--officers-file`. Without them you would end up with a quorum arrangement nobody holds a share in, so it
fails closed.

**"no admin certificate binding and no pending admin credential record".**
First boot waited for the admin's certificate to arrive (it may have been
issued by `qrdb init` on a different node of the cluster) and nothing showed
up in time. This does not abort the boot: the node logs this message as a
warning and starts anyway, coming up in the same "awaiting admin credential"
state described below, rather than shutting down and leaving the operator
with no running node to retry against. The admin-credential step is
retriable: re-run `qrdb init` with `--admin-csr`, `--admin-cert`, or
`--admin-generate`, against ANY node over REST — a follower forwards the
retry to the current Raft leader in one hop, like every other system-shard
write, instead of refusing it, so there is no need to find the leader by
hand with `qrdb admin status` first. The
retry both issues a fresh credential AND binds it to the admin account, in
the same call — it never hands back a credential that looks successful but
is not actually bound. Nothing about this is destructive — it does not
touch the barrier, the CA, or the SO/User PINs.

If the retry cannot complete the bind it refuses rather than returning an
unbound credential: "admin-credential reissue has no CertBinder wired" means
this composition's own wiring is missing the piece that binds a certificate
(an operator/configuration gap, not something retrying fixes); "the
bootstrap admin does not exist yet to bind it to" means first boot itself
has not created the admin account on this cluster yet — retry again once it
has (or once `qrdb admin status` shows this node as leader, if a different node is
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

- `qrdb ceremony status` / `GET /v1/ceremony/status` — so you can confirm the state
  without a credential.
- `qrdb init` / `POST /v1/ceremony/initialize` — the SAME admin-credential
  retry described above. Run it with `--admin-csr`, `--admin-cert`, or
  `--admin-generate` against ANY node; a follower forwards it to the
  current Raft leader rather than refusing it, so there is no need to
  target the leader directly. The SO/User PINs in the request are the only
  credential this call needs.

Every other endpoint answers `503` /
`{"code":"AWAITING_ADMIN_CREDENTIAL"}` until that retry binds a
certificate. The transition back to normal serving is event-driven, not a
poll: the node that performs the retry (the current Raft leader) is told
directly the moment the bind commits, and every OTHER node in the cluster learns of it through
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
only binding expires while the node is up, the same admin-credential retry
described above still works (you do not need to wait for a restart), but
it is reached differently on a node that is not "awaiting": `ceremony:initialize`
is an SO-exclusive, RBAC-gated permission like any other, and the
awaiting-mode bypass that otherwise admits the call unauthenticated no
longer applies once a node has left that state. The retry request itself
must therefore authenticate as an SO — present an SO holder certificate
over mTLS (`--cert-file`/`--key-file`, the credential issued when the
ceremony enrolled that officer, never the admin's own certificate) — as
well as the SO PIN. Only a
NODE THAT RESTARTS re-runs the boot-time check that decides "awaiting"
from scratch, and that restarted node WILL come up in "awaiting admin
credential" if the expired binding was never replaced — at which point the
retry goes back to being reachable with the SO PIN alone, no SO identity
required.

**"mutually exclusive: supply exactly one admin credential path".** You gave
more than one of `--admin-csr`, `--admin-cert` and `--admin-generate`. Only
one credential can be bound to the admin. Pick one.

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
[cli.md](cli.md#state-machine-check).

**The system started but the barrier is sealed.** Bootstrap does not unseal for
you. See [../barrier-seal/user.md](../barrier-seal/user.md).

**You want to change the built-in roles.** You cannot — they are undeletable and
their permissions are fixed. Create additional roles instead:
[../identity-rbac/user.md](../identity-rbac/user.md).

**You lost the admin certificate.** If another account in the `system`
tenant already holds a role permitted to issue certificates (`certs:issue`),
it can mint you a new one directly: `qrdb ca issue --username admin --out
./admin` (the key is generated on their machine), or with `--csr` for a CSR
you made with `qrdb ca csr`; the certificate is bound to the account in the
same call — see
[../certificate-authority/user.md](../certificate-authority/user.md) and
[../identity-rbac/user.md](../identity-rbac/user.md).

When no other such account exists, whether THIS call can help you depends
on whether the admin account's EXISTING binding is still live (unexpired),
not merely on whether you still hold the private key:

- **The existing binding is still live.** The reissue refuses with
  "the bootstrap admin already holds a certificate binding" — this is not a bug to work around; a working
  binding is deliberately never silently replaced, precisely because
  "the operator lost the key file" and "the account's credential was
  compromised and someone else has it" look identical from the server's
  side, and the second case must never self-service a replacement. There
  is no SO-PIN override for this refusal. Recovery becomes available once
  that binding's own expiry (`NotAfter`) passes — the account is not
  stuck forever, only until then.
- **The existing binding has expired (or never bound at all).** This is
  the SO-PIN-gated admin-credential reissue, reached through the same
  `qrdb init` command as the "awaiting" retry above (not a separate
  recovery procedure): re-run it with your SO PIN (`--so-pin`) and a
  fresh `--admin-csr` or `--admin-cert`. It mints or adopts a fresh
  certificate and binds it to the admin account in the same step, without
  touching the barrier, the CA, or the SO/User PINs. On a node that is
  "awaiting", the SO PIN is the only thing standing between this call and
  anyone who can reach the node. On a node that is NOT "awaiting" (the
  common case — see "does not re-enter it on its own" above), the call is
  additionally gated like any other SO-exclusive permission: it must
  authenticate as an SO over mTLS (an SO holder certificate, never the
  admin's own) as well as present the SO PIN. Treat the SO PIN with the
  same care as the barrier's own secret either way.

If you have also lost the ability to authenticate at all — no admin
credential, no SO identity, and the binding is still live — you are into
barrier recovery territory.
