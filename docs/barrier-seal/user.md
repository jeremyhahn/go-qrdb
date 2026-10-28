# Barrier and Seal — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

The barrier is what encrypts your data at rest. Sealed, the database refuses
every read and write. Unsealed, everything works and the encryption is
invisible.

## Prerequisites

A reachable server, client certificates, and the `seal:*` permissions for what
you do. `barrier status` is read-only and is where you start when something is
refusing requests. A sealed node answers it with two facts, sealed and
whether it is initialized: `initialized: false` means the node is waiting for
its first boot and needs `qrdb init`; `initialized: true` means it is
initialized and needs unsealing. A node held by a cluster seal answers
`transport: barrier sealed` instead -- and that answer is the status: the node
is up, and held. A node cut off from the seal records answers `this node
cannot confirm the cluster seal state; retry` (`SEAL_STATE_UNCONFIRMED`): its
barrier is not sealed, and it serves again once it reads the records. Read the
seal record from a node that serves.

**A barrier secret travels only over TLS.** The SDK and the CLI refuse to send
an init secret, unseal key, passphrase, Shamir share or holder PIN on a
connection without TLS, the Unix socket included, with
`BarrierSecretRequiresTLSError`; connect with TLS.

## Choosing a strategy

You pick this at initialization and it is what protects the master key.

| Strategy | What unseals it | Use it when |
|---|---|---|
| `passphrase` (default) | one passphrase | a single operator, or an automated unseal with a secret store |
| `shamir` | M of N shares held by different people | no one person should be able to unseal alone |
| `none` | nothing — data is plaintext | development, tests, or a device already doing authenticated full-disk encryption |

Hardware strategies — TPM 2.0, PKCS#11, AWS KMS, GCP KMS, Azure Key Vault,
Vault — are available when the deployment composes go-xkms in. From your side
they are just another `--barrier-strategy` name.

## Auth requires the seal

`auth.enabled` defaults to `true`, and an auth-enabled node requires
`seal.enabled: true` too: the bootstrap admin's certificate
binding depends on `qrdb init` being able to bring the system barrier up at
all, which does not exist when the seal is off. Config validation refuses to
start an auth-enabled, seal-disabled node with a typed error naming the fix.
A deployment that does not want encryption runs the seal with strategy
`none` (below) rather than omitting it; only a node that also sets
`auth.enabled: false` may run with the seal off.

## Running with no barrier at all

`seal.enabled: false` is a step further than strategy `none`: there is no
barrier object in front of the data plane at all, so none of the barrier
commands above apply, and (per the previous section) it is only reachable
with auth also disabled. Every node started this way logs one WARN at
startup:

```
qrdb/seal: seal disabled; at-rest data is stored in plaintext
```

**There is no supported in-place path from here to encrypted.**
`barrier enable-encryption` only migrates an *existing* barrier that is
currently in a credential-free strategy — `none`, or a tenant's
`system-barrier`-wrapped root key (see "Turn on encryption for a deployment
that started with `none`" below) — by re-encoding files that are already in
qrdb's encrypted-file format. With `seal.enabled: false` there is no barrier
and the data-plane files were never written in that format at all, so
`enable-encryption` does nothing here, and simply flipping `seal.enabled` to
`true` and restarting does **not** encrypt anything either: qrdb's encrypting
filesystem passes pre-existing plaintext files through unmodified on read and
can fail outright (`encfs: invalid header`) the first time Pebble reopens one
for writing, which can wedge the boot. Nothing in qrdb rewrites those files
for you.

If you need encryption at rest for data that started this way, provision a
**new** node or tenant with `seal.enabled: true` (start it with `strategy:
none` if you want the option to migrate to a keyed strategy later without a
second re-provisioning) and move your data into it through a supported
data-path — a verified backup/restore ([../backup-restore/architecture.md](../backup-restore/architecture.md))
or an application-level re-seed — never a raw copy of the old data directory.

## First run

`qrdb init` is the only way a fresh node's system barrier is ever brought
up: it runs in the sealed window before anything is unsealed,
initializes the system barrier with the strategy and secret you give it, and
issues the bootstrap admin's certificate in the same call (see
[../firstboot/user.md](../firstboot/user.md)). `qrdb barrier init` targeting
the system barrier is refused with a typed error pointing back here — it
remains the command for a tenant or database barrier, which a tenant admin
initializes separately after the system is up.

```bash
qrdb init --so-pin <so-pin> --user-pin <user-pin> \
  --barrier-strategy passphrase --passphrase '<passphrase>'
qrdb barrier status
```

After a restart:

```bash
qrdb barrier unseal --passphrase '<passphrase>'
```

Unsealing a barrier that was never initialized is refused with a typed
"barrier not initialized" error (`seal.ErrNotInitialized`): run `qrdb init`
first.

### Embedding qrdb: a passphrase-sealed system barrier

An application that embeds a node through `sdk/go/node` brings its system
barrier up without the sealed-window listener. Set `seal.enabled: true` and
`seal.strategy: passphrase` in the node's configuration, then either:

- pass the passphrase at construction, and the node initializes the barrier
  on first boot and unseals it on every later one:

  ```go
  n, err := node.NewStandaloneNode(ctx, cfg, nil, node.WithSealPassphrase(passphrase))
  ```

- or construct the node without it. The node starts sealed, and
  `Node.Unseal` initializes the barrier the first time and unseals it after
  that:

  ```go
  n, err := node.NewStandaloneNode(ctx, cfg, nil)
  // ...
  err = n.Unseal(ctx, seal.MustCredentials(ctx, passphrase))
  ```

On a node that runs the first-boot ceremony, `BarrierInitialize` over any
transport is refused on the system barrier with
`service.SystemBarrierInitRefusedError`: the ceremony (`qrdb init`,
`Node.FirstBoot`) owns that barrier. Without a ceremony, the node's own
constructor or `Node.Unseal` is the path; do not mix the two. A wrong passphrase on a
later unseal returns an `*node.UnsealError` wrapping
`seal.ErrDecryptionFailed`.

## Common tasks

**Check state before anything else.**

```bash
qrdb barrier status --output json
```

**Seal deliberately** — before maintenance, or in response to an incident.

```bash
qrdb barrier seal -y
```

Everything stops — on every node of the cluster, once the cluster has
activated cluster sealing. That is the point. See *Sealing in a cluster*
below.

Sealing the node barrier seals **every tenant and every encrypted database on
the node** with it: each tenant and database barrier is sealed and its keys
are wiped from memory, and every open watch and SQL connection ends. When you
unseal the node:

- tenants whose key the node manages (*system-managed*, the auto-unseal
  default) come back on their own -- unless they were sealed for their own
  reasons (by their admin, or cluster-wide), which still holds;
- tenants that manage their own key (*self-managed*) stay sealed until their
  own admins unseal them -- tell them before you seal;
- encrypted databases stay sealed until their owners unseal them -- with the
  database passphrase, or, for a Shamir database, by its holders' quorum --
  so tell their owners too.

A node that loses touch with the cluster is different: it stops serving but
keeps its keys (see below).

**Change the passphrase** of a passphrase-sealed barrier. The barrier must be
unsealed; the current passphrase must open it, and a wrong one is refused
with a typed credential mismatch and changes nothing. The root key is not
rotated: it is re-wrapped under the new passphrase, and the stored blob is
replaced in one guarded swap, so the next unseal takes the new passphrase and
the old one is refused. Data is not re-encrypted. It needs `seal:rekey` and
the Security Officer PIN, checked before the barrier is touched exactly as for
a seal or a rekey: a missing or wrong PIN is refused and changes nothing, and
a node with no PIN backend configured refuses the change outright.

```bash
qrdb barrier change-passphrase --current '<current>' --next '<next>' --so-pin "$SO_PIN"
```

An embedder calls `Node.ChangePassphrase(ctx, current, next)`, which returns
`seal.ErrCredentialMismatch` for a wrong current passphrase and
`node.ErrSealNotEnabled` on a node without a seal barrier.

**Set up M-of-N custody.** Enrol the share holders first, then initialize;
one envelope is sealed to each holder and each claims theirs once. The full
custody workflow is [../shamir-custody/user.md](../shamir-custody/user.md).

```bash
qrdb barrier init-shamir --threshold 3 --total 5
```

**Unseal, seal and rotate with the holders.** A Shamir barrier opens only when
M holders each submit their share, authenticated; it seals the same way, and
it is rotated — after a holder leaves, or on a schedule — by a rekey that M
holders complete. Every earlier share stops working.

```bash
qrdb barrier quorum submit --op unseal --username alice --envelope alice.env --key alice.key
qrdb barrier rekey --threshold 3 --so-pin "$SO_PIN"
```

**Turn on encryption for a deployment that started with `none`.**

```bash
qrdb barrier enable-encryption --strategy passphrase --passphrase 'hunter2'

# then poll until it finishes
watch -n5 'qrdb barrier status --output json | jq -r .strategy'
```

This re-encrypts your data in place and runs asynchronously — the command
returns as soon as the migration starts, not when it finishes.

In a cluster, the passphrase you send reaches only the node that accepts the
command; it is never stored in the cluster or sent to another node. Give
every other node its own copy, on that node, before or after the command:

```bash
# on each other node; seal.migration_credentials_dir points at this directory
install -d -m 0700 <install-dir>/migration-credentials
install -m 0600 /dev/null <install-dir>/migration-credentials/system
printf '%s\n' 'hunter2' > <install-dir>/migration-credentials/system
# a tenant barrier: <install-dir>/migration-credentials/tenant-<tenant-id>
```

The directory and the file must be owned by the user the daemon runs as; no
one else may write the directory or read the file. A node without its file waits, in phase `awaiting-credential`, and
carries on as soon as the file is there. A Shamir target needs no file.

Once every node runs a release with the `rolling-migration` cluster feature,
the nodes take their turn one at a time, the node that accepted the command
last: a node starts only after the one before it has come back and reported
the migration complete. A system-barrier migration brings each node back
sealed, so unseal each one with the new passphrase as it comes back; the next
node waits until you do. The cluster keeps its quorum, and keeps taking
writes, throughout. A node that never comes back holds the rest at its turn
rather than taking a second node away.

`qrdb barrier status`, against any node that serves, shows where the rollout
is: every owner by its Raft address in turn order, its step and its own
report.

```text
Migration:       to passphrase, generation 1, 1 of 3 owners done
  192.0.2.11:5000 serving    complete
  192.0.2.12:5000 recycling  awaiting-credential: clustermig: this node's migration credential is not available yet
  192.0.2.13:5000 pending    not reported
```

`awaiting-credential` there means that node's credential file is missing or
refused; put it in place and the node carries on. With `--output json` the
same data is the `cluster_migration` object
([api.md](api.md#the-cluster_migration-status-object)). Once the last owner is
done, the cluster removes the record and the line reads
`Migration:       none in progress`. `unconfirmed` means the node could not
read the record just now, for example during an election; ask again. A node
that is itself sealed shows no migration: ask another node. Only the node
barrier's status carries the line; `qrdb tenant barrier-status` and the
database barrier status do not.

**Dual control on the system barrier is in `qrdb init`.** `barrier init`
refuses the system barrier, PINs included (`POST /v1/barrier/initialize`
carries no tenant scope). `qrdb init` requires both `--so-pin` and
`--user-pin` on every call, so the system barrier's bring-up satisfies
FIPS 140-3 / PCI-DSS v4 dual control: one party supplying the SO PIN alone, or
the user PIN alone, is never enough. `tenant barrier-init` accepts no PIN
pair, so a tenant's own barrier has no dual-control path.

**Give one database its own key.** Only a non-raft database with its own
`--data-dir`, created by an operator as the system tenant, accepts one;
see [../databases/user.md](../databases/user.md) for why the other shapes are
refused:

```bash
qrdb database barrier-status secure
qrdb database seal secure
qrdb database unseal secure --secret "$SECRET"
```

## Sealing in a cluster

**A seal reaches every node.** Once every node runs a release with cluster
sealing (the `cluster-seal` feature is then activated automatically), sealing
the node barrier, a tenant's barrier or a database's barrier through any node
writes a small replicated record, and every node seals itself: it refuses new
requests, ends open watches and lease streams, and wipes its own key. Nothing
secret is in the record, and no key moves between nodes.

```bash
qrdb barrier seal -y                      # platform operator: every node
qrdb tenant barrier-seal --id acme        # acme's own admins: acme on every node
qrdb database seal secure                 # the owning tenant: that database everywhere
```

Before the cluster has activated the feature — during a rolling upgrade, say —
a seal still acts only on the node you are connected to, and `barrier status`
shows `cluster_seal.active: false`.

**Activation does not interrupt service.** Every node reads the seal records
on every poll (`seal.cluster_poll_interval`) even before the feature is
active, without acting on them, so when the last node is upgraded and
`cluster-seal` turns on, each node already holds a fresh read and keeps
serving. No seal record can exist before activation, so a cluster that has
never been sealed has no refusal window at all, and no MySQL or PostgreSQL
connection is closed by the activation.

A node refuses requests (outside the barrier and health endpoints) only when
it cannot confirm the seal state: from the moment it starts until its first
read of the records -- made as soon as its monitor starts, before it has
anything to serve on a healthy cluster -- and whenever it cannot read them
for `seal.cluster_lease`. Clients see the ordinary sealed/unavailable answer
and should retry; open SQL connections are closed, since the refusal closes
the node's seal probe, and reconnect once the node serves again. A sealed
record is not a window: the barrier it names stays refused until it is
unsealed.

**Unsealing is per node.** Every node unseals with its own credential. Take
the nodes at whatever pace you need: a node unsealed ahead of the others logs
`waiting for a Raft leader` and waits, serving nothing, until enough of its
peers are unsealed to form a quorum.

- *A tenant or database:* unseal it on each node that serves it. The first
  unseal also clears the cluster record.
- *The node barrier:* unseal each node where it runs -- or restart it and
  unseal it at boot as usual. A sealed node keeps its Raft shards loaded but
  paused, and its unseal resumes them. Each comes back **held** — keys loaded,
  Raft running, requests refused as sealed — once it has read the seal
  records; in the moment before that it refuses them as
  `SEAL_STATE_UNCONFIRMED`, which a client retries. It stays held until you
  clear the record once, from any unsealed node:

  ```bash
  qrdb barrier cluster-unseal
  ```

**SQL connections close on a seal.** A MySQL or PostgreSQL connection
belonging to a sealed tenant, or to any tenant on a sealed or cut-off node, is
closed by the server: its open transaction is rolled back and an open cursor
ends. Clients reconnect once the barrier is unsealed, as after a restart.

**Auto-unseal volume tenants attach on first access.** With cluster sealing on,
a node starts no tenant at boot -- not even a system-managed (auto-unseal)
volume tenant, whose key it already holds -- because it has not yet read the
seal records. Each such tenant starts on its first request once the node has
read them, or when the placement reconciler reaches it. Expect that first
request to take longer than the ones after it.

**A node that loses touch with the cluster stops serving.** If a node cannot
read the seal records for about 15 seconds (`seal.cluster_lease`), it refuses
requests, ends its streams and closes every SQL connection, keeping its keys.
When it can read them again it resumes on its own — unless something was
sealed in the meantime, in which case it seals first. SQL clients reconnect.

**Every node gets its own key.** An isolated tenant's barrier has one key per
node, each initialised on its node from a signed init statement; no key is
carried between nodes. A Shamir key is the exception (below).

- A `raft` tenant whose key auto-unseals has one key per node: the
  platform signs its statement and each node initialises its own key, so
  barrier status on two nodes shows different key fingerprints. A node newly
  placed in the tenant's group reports the barrier sealed and uninitialised
  until it has done so. The platform signs with the node's cluster
  certificate, which a node embedded through the Go SDK carries just as a
  `qrdbd` node does: a single-node embedded cluster given no certificate
  issues its own on first boot, as `qrdbd` does with no TLS flags. An
  embedded node given the cluster CA but no node certificate cannot sign,
  and refuses such a tenant.
- A tenant whose own admin initialises a `passphrase` or `none` barrier
  has one key per node, and no key is carried between them. Run the
  init once, through any node: under `passphrase` it reaches every node
  serving the tenant at once, over the transport you connected with, and
  prints one result per node; under `none` the other nodes initialise from
  the first one's record. `qrdb tenant barrier-status --id acme` lists the
  nodes serving the tenant. A node that refused or could not be reached is
  named in the result; the others are initialised regardless.

  ```bash
  qrdb tenant barrier-init --id acme --strategy passphrase --passphrase "$PASS"
  ```

- A tenant whose own admin holds a Shamir key has it delivered, sealed,
  to each node that serves it.
- On a cluster, creating a `barrier`, `volume` or `raft` tenant is refused as
  unavailable until every node runs a release with the
  `per-node-barrier-keys` feature, which activates on its own once they all
  do; retry after the rollout. A standalone node is not affected.
- Whether a tenant's key auto-unseals stays the tenant's choice. A `prefix`
  tenant has no key of its own: its data is under the platform's barrier and
  readable by the platform; choose an isolated type to keep data from the
  platform.

## Troubleshooting

**Everything returns "unavailable" or "barrier sealed".** The barrier is
sealed, or the node is held by a cluster seal or cut off from the seal
records. A sealed node serves only the unseal call and `barrier status`, which
says sealed and whether the node is initialized: health and `ceremony status`
answer `barrier sealed` too, and a held or cut-off node refuses
`barrier status` as well. Unseal with the
strategy you initialized with: `qrdb barrier unseal --passphrase` for a
passphrase barrier, `qrdb barrier unseal` with no passphrase for a `none` one
sealed by hand (a `none` node unseals itself at every start), or
`qrdb barrier quorum submit` from M holders for a Shamir one (a passphrase
unseal of a Shamir barrier is refused with an error that says so). On a node held by a cluster seal, `qrdb barrier cluster-unseal`
clears the record.

**"no sealing strategy specified".** More than one strategy is registered (typically
because hardware strategies were composed in) and the server will not guess.
Name one with `--barrier-strategy`.

**Your PIN was rejected.** Two rules: PINs come in pairs (`--so-pin` requires
`--user-pin`), and no PIN is accepted at all with `--barrier-strategy=none` —
there is nothing to protect.

**You cannot change from `passphrase` to `shamir`.** The strategy is fixed at
initialization. `barrier enable-encryption` migrates **off `none`** (and off
`system-barrier`); it is not a general strategy switch. Changing between keyed
strategies means a new instance and a data migration.

**`enable-encryption` was rejected for a tenant.** A tenant sitting on the
shared system barrier has no encryption of its own to change. It needs a
dedicated barrier first. A permission refusal means the caller lacks the
permission for the barrier it named. The node barrier needs
`seal:enable_encryption` in the `system` tenant. A tenant's barrier needs
`tenant_barrier:enable_encryption` for that tenant, which that tenant's own
admin holds.

**`enable-encryption` returned immediately and nothing looks different.** It is
asynchronous. Poll `barrier status` until the strategy stops saying `none`.

**In a cluster, one node migrated and the others did not.** They are waiting
for their own credential file. Each node's migration status says
`awaiting-credential`, with the reason -- the file is missing, or it was
refused for its mode or owner. Fix the file; the node continues on its own.

**Certificate operations fail after sealing.** Expected: the CA private key is
destroyed in memory when the barrier seals, and CA calls return
`CAKeyNotAvailableError` until you unseal.

**A compliance gate refuses to start against your server.** You are running
`strategy: none`. Every barrier operation is audited with `strategy: none`
precisely so this is visible, and the go-xkms `--require-encryption` gate
refuses such a deployment.

**One node in the cluster will not unseal.** Each node's node barrier holds
its own key — there is no shared cluster DEK — so unsealing is per node: connect
to that node and unseal it there. Nothing unseals a node from its peers.

**Every request on a node says "barrier sealed", but it is unsealed.** The
cluster seal holds it, and it refuses `barrier status` too. Run
`qrdb barrier status --output json` against a node that serves: `cluster_seal`
shows the seal still recorded. Clear it with `qrdb barrier cluster-unseal`.

**Every request on a node says "cannot confirm the cluster seal state".** The
node is cut off from the seal records (`SEAL_STATE_UNCONFIRMED`), for example
while the system shard has no leader. Its barrier is not sealed and it keeps its
key; it refuses everything but the unseal call, `barrier status` included.
Check its connectivity to the rest of the cluster; it resumes by itself.

**You sealed a tenant and it is still answering on one node.** That node has
not activated cluster sealing yet (`cluster_seal.active: false`): it is running
an older release. Finish the upgrade, or seal the tenant on that node too.

**You lost the passphrase or too many shares.** Recovery keys are a separate,
independent split maintained for exactly this — see
[../shamir-custody/user.md](../shamir-custody/user.md). If there are no recovery
keys, the data is not recoverable. That is what at-rest encryption means.
