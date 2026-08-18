# Barrier and Seal — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

The barrier is what encrypts your data at rest. Sealed, the database refuses
every read and write. Unsealed, everything works and the encryption is
invisible.

## Prerequisites

A reachable server, client certificates, and the `seal:*` permissions for what
you do. `barrier status` is read-only and safe to call against a sealed
barrier — it is where you start when something is refusing requests.

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

## First run

```bash
qrdb barrier init --barrier-strategy passphrase --passphrase '<passphrase>'
qrdb barrier status
```

After a restart:

```bash
qrdb barrier unseal --passphrase '<passphrase>'
```

## Common tasks

**Check state before anything else.**

```bash
qrdb barrier status --output json
```

**Seal deliberately** — before maintenance, or in response to an incident.

```bash
qrdb barrier seal -y
```

Everything stops. That is the point.

**Set up M-of-N unsealing.** Enrol the officers first, then initialize; the
shares are sealed to them server side and each claims theirs once. The full
custody workflow is [../shamir-custody/user.md](../shamir-custody/user.md).

```bash
qrdb barrier init-shamir --passphrase '<seed>' --threshold 3 --total 5
```

**Unseal with shares.**

```bash
qrdb barrier unseal-shares --share aaa --share bbb --share ccc
qrdb barrier unseal-shares --shares-file /run/shamir.txt
```

Fewer than the threshold is not an error — the shares accumulate and the
response tells you how far along you are.

**Rotate the shares** after an officer leaves, or on a schedule. This revokes
every share previously handed out.

```bash
qrdb barrier rekey --so-pin SO123
qrdb barrier rekey --threshold 3 --total 5     # change M/N at the same time
```

**Turn on encryption for a deployment that started with `none`.**

```bash
qrdb barrier enable-encryption --strategy passphrase --passphrase 'hunter2'

# then poll until it finishes
watch -n5 'qrdb barrier status --output json | jq -r .strategy'
```

This re-encrypts your data in place and runs asynchronously — the command
returns as soon as the migration starts, not when it finishes.

**Use PINs for dual control.** Supplying one requires the other; the pair is
what satisfies FIPS 140-3 and PCI-DSS v4.

```bash
qrdb barrier init --barrier-strategy passphrase --passphrase '<pp>' \
                  --so-pin SO123 --user-pin USR123
```

**Give one database its own key.** See
[../databases/user.md](../databases/user.md):

```bash
qrdb database barrier-status secure
qrdb database seal secure
qrdb database unseal secure --secret "$SECRET"
```

## Troubleshooting

**Everything returns "unavailable" or "barrier sealed".** The barrier is
sealed. `qrdb barrier status` works even then — it will tell you the strategy,
and therefore how to unseal.

**"no strategy specified".** More than one strategy is registered (typically
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
dedicated barrier first.

**`enable-encryption` returned immediately and nothing looks different.** It is
asynchronous. Poll `barrier status` until the strategy stops saying `none`.

**Certificate operations fail after sealing.** Expected: the CA private key is
destroyed in memory when the barrier seals, and CA calls return
`CAKeyNotAvailableError` until you unseal.

**A compliance gate refuses to start against your server.** You are running
`strategy: none`. Every barrier operation is audited with `strategy: none`
precisely so this is visible, and the go-xkms `--require-encryption` gate
refuses such a deployment.

**One node in the cluster will not unseal.** Each node holds its own key —
there is no shared cluster DEK — so unsealing is per node. For automatic
cluster unseal, nodes exchange shares between themselves; that is a deployment
configuration, not something you drive by hand.

**You lost the passphrase or too many shares.** Recovery keys are a separate,
independent split maintained for exactly this — see
[../shamir-custody/user.md](../shamir-custody/user.md). If there are no recovery
keys, the data is not recoverable. That is what at-rest encryption means.
