<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# `qrdb barrier` — Canonical Barrier CLI Surface

`qrdb barrier` is the **gold-standard** verb tree for cryptographic
barrier administration. The same nine verbs, the same flags, and the
same validation rules are reproduced byte-for-byte in
[`xkmsctl barrier`](../../../go-xkms/docs/cli/barrier.md),
[`quicbaas barrier`](../../../go-quicbaas/docs/cli/barrier.md), and
[`platformctl barrier`](../../../go-trusted-platform/docs/cli/barrier.md).
Operators learn the surface once and apply it across the entire stack.

Implementation: [`cmd/qrdb/barrier.go`](../../cmd/qrdb/barrier.go).
Wire types: [`sdk/go/transport/barrier_types.go`](../../sdk/go/transport/barrier_types.go).

---

## 1. The locked 9-verb tree

```
barrier status
barrier init           --barrier-strategy <none|passphrase|shamir>
                       [--passphrase <pp>]           (required for passphrase/shamir; rejected for none)
                       [--so-pin <pin>]              (optional; requires --user-pin; rejected for none)
                       [--user-pin <pin>]            (optional; requires --so-pin; rejected for none)
barrier init-shamir    --passphrase <pp>
                       --threshold N
                       --total M
                       [--so-pin <pin>]              (optional; requires --user-pin)
                       [--user-pin <pin>]            (optional; requires --so-pin)
barrier enable-encryption  --strategy <passphrase|shamir>
                           [--tenant <id>]           (system barrier by default)
                           [--passphrase <pp>]       (required for passphrase; Shamir unseal at quorum)
                           [--shamir-threshold N]    (Shamir: threshold)
                           [--shamir-total M]        (Shamir: total shares)
                           [--mechanism swap|in-place]    (default: swap)
                           [--availability quiesced|online] (default: quiesced)
                           [-y | --assume-yes]
barrier unseal         --passphrase <pp>             (passphrase / single-secret only)
barrier unseal-shares  --share <s1> [--share <s2> ...]   (repeatable)
                       | --shares-file <path>        ('-' = stdin)
barrier seal           [--so-pin <pin>]
                       [-y | --assume-yes]
barrier rekey          [--so-pin <pin>]              (NO --user-pin on rekey)
                       [--threshold N --total M]     (paired; Shamir share regeneration;
                                                      no secret material on the wire)
barrier recovery generate
                       --threshold N
                       --total M
                       [--so-pin <pin>]
barrier recovery recover
                       --share <s1> [--share <s2> ...]   (repeatable; at least one)
```

### Root persistent flag

| Flag | Default | Allowed values |
|------|---------|----------------|
| `--transport` | `unix` | `rest`, `grpc`, `quic`, `mcp`, `unix`, `embedded` |

`unix` is the default across **all four** CLIs (`qrdb`, `xkmsctl`,
`quicbaas`, `platformctl`); local operator workflows talk to the
co-located daemon over the gRPC-over-Unix socket without any extra
flags. Override via the root flag or, where supported, env vars
(`PLATFORMCTL_TRANSPORT`, etc.).

---

## 2. Architectural concept: `--passphrase` is **not** a PIN

This is the single most important distinction in the barrier surface.
Two completely separate credential classes share the verb tree and
they must never be conflated.

| Concern | Flag | Purpose | Backend |
|---------|------|---------|---------|
| **Strategy wrapping passphrase** | `--passphrase` | Material the barrier strategy uses to derive (passphrase → Argon2id) or seed (shamir) the master key. The `none` strategy ignores it entirely. | `pkg/seal` strategies |
| **Compliance-gating PIN** | `--so-pin`, `--user-pin` | Two-PIN administrative-authorisation primitive aligned with FIPS 140-3 IG / PCI-DSS v4. Gates rekey, seal, and recovery operations. | `pkg/pin.PINBackend` |

The two concerns are orthogonal:

* The `passphrase` strategy with `--passphrase=<pp>` and no PIN backend
  is a valid (dev / single-operator) configuration.
* The `passphrase` strategy with `--passphrase=<pp>` **and** `--so-pin`
  + `--user-pin` is the FIPS-aligned production posture.
* The `none` strategy rejects `--passphrase` because the barrier is a
  passthrough — there is nothing to wrap. It also rejects both PIN
  flags with the typed `PINsNotAllowedError` because no PIN store
  exists for a passthrough barrier.

The dual-control rule on the PIN pair (`--so-pin` requires
`--user-pin`, and vice versa) is enforced **at the CLI** before any RPC
is dispatched. The single exception is `barrier rekey`, which carries
only `--so-pin` because the wire request
([`BarrierRekeyRequest`](../../sdk/go/transport/barrier_types.go))
has no `UserPin` field — user-PIN rotation is a separate ceremony.

---

## 3. Verbs in detail

### 3.1 `barrier status`

Read-only; safe against a sealed barrier. Honours `--output`
(`table` | `json` | `yaml`).

```bash
qrdb barrier status
qrdb --output json barrier status
```

Returns
[`BarrierStatusResponse`](../../sdk/go/transport/barrier_types.go):
`Sealed`, `Strategy`, `HardwareBacked`, `InitializedAt`.

### 3.2 `barrier init`

Initialises only the barrier subsystem. Distinct from the root `qrdb
init` ceremony, which provisions the barrier *plus* the CA, the admin
client cert, and IAM bootstrap roles.

```bash
qrdb barrier init --barrier-strategy passphrase --passphrase '<passphrase>'
qrdb barrier init --barrier-strategy none
qrdb barrier init --barrier-strategy passphrase --passphrase '<pp>' \
                  --so-pin SO12345 --user-pin USR12345
```

Validation (see `validateBarrierInitFlags` in
[`cmd/qrdb/barrier.go`](../../cmd/qrdb/barrier.go)):

| Rule | Error |
|------|-------|
| `--barrier-strategy` unknown | `InvalidArgError` (must be `none`, `passphrase`, or `shamir`) |
| `--passphrase` empty for `passphrase`/`shamir` | `MissingArgError` |
| `--passphrase` supplied with `--barrier-strategy=none` | `InvalidArgError` |
| `--so-pin` or `--user-pin` with `--barrier-strategy=none` | `PINsNotAllowedError` (no PIN store for the passthrough strategy) |
| `--user-pin` without `--so-pin` | `InvalidArgError` |
| `--so-pin` without `--user-pin` | `InvalidArgError` (FIPS 140-3 / PCI-DSS v4 dual-control) |

### 3.3 `barrier init-shamir`

Splits the master secret into `--total` shares with `--threshold`
required to unseal. The response is **share-free** (INV-1): shares are
sealed server-side to the enrolled Security Officers' enclaves, and
each officer retrieves theirs exactly once via the
`barrier officer claim-begin` / `claim-complete` mTLS ceremony.

```bash
qrdb barrier init-shamir --passphrase '<seed>' --threshold 3 --total 5
```

| Rule | Error |
|------|-------|
| `--threshold < 2` | `InvalidArgError` |
| `--total < --threshold` | `InvalidArgError` |
| `--user-pin` without `--so-pin` | `InvalidArgError` |
| `--so-pin` without `--user-pin` | `InvalidArgError` (FIPS 140-3 / PCI-DSS v4 dual-control) |

### 3.4 `barrier unseal`

Replay the strategy passphrase for the **passphrase / single-secret**
case. Shamir unseal lives on the next verb.

```bash
qrdb barrier unseal --passphrase '<passphrase>'
```

### 3.5 `barrier unseal-shares`

Submit one or more Shamir shares. Two mutually exclusive input modes:

```bash
qrdb barrier unseal-shares --share aaa --share bbb --share ccc
qrdb barrier unseal-shares --shares-file /etc/qrdb/shares.txt
cat shares.txt | qrdb barrier unseal-shares --shares-file -
```

`--shares-file` accepts blank lines and `#` comments. Submitting
`< threshold` shares accumulates progress; submitting `>= threshold`
completes the unseal in one round trip.

### 3.6 `barrier seal`

Destructive. Reads stop returning until the next unseal. Prompts on a
TTY; pass `-y` / `--assume-yes` from runbooks.

```bash
qrdb barrier seal -y
qrdb barrier seal --so-pin SO12345 --assume-yes
```

`--so-pin` is verified against the
[`pkg/pin.PINBackend`](../pin/README.md) when one is configured. Older
deployments without a PIN backend ignore the field.

### 3.7 `barrier rekey`

Generate a fresh unseal key and M-of-N Shamir share set. The root key
itself is **not** rotated — the server generates a new random unseal
key, AEAD-re-wraps the root key under it, and splits the new unseal key
to the officers, **cryptographically revoking every previously
distributed share** (see [../seal/shamir.md](../seal/shamir.md#rekey)).
The server requires the barrier to be unsealed and the Shamir strategy
to be configured; otherwise it returns `ErrSealed` /
`ErrShamirNotConfigured`. New shares are sealed to the enrolled
Security Officers' enclaves (share-free response); claim history resets
so each officer claims again.

```bash
qrdb barrier rekey --so-pin SO12345
qrdb barrier rekey --threshold 3 --total 5
```

The Shamir flags are **paired**: supplying one without the other is an
error; when both are omitted the configured M/N is reused. Rekey
carries **no secret material** on the wire — the server derives
everything itself (the former `new_secret` wire field was dead and has
been removed; proto field 4 is reserved). `barrier rekey` accepts only
`--so-pin`; user-PIN rotation is a separate ceremony because the wire
request has no `UserPin` field.

### 3.8 `barrier enable-encryption`

Migrate a previously-unencrypted barrier (initialized with `--barrier-strategy
none`) to a keyed sealing strategy in place, re-encrypting all at-rest data
without downtime. Supports both system-scope (default) and per-tenant
migrations via `--tenant <id>`.

```bash
qrdb barrier enable-encryption --strategy passphrase --passphrase 'new-passphrase'
qrdb barrier enable-encryption --tenant acme --strategy shamir \
  --shamir-threshold 2 --shamir-total 3 --passphrase 'root-seed'
```

The migration runs asynchronously on the server: the barrier strategy is
updated and data re-encryption begins immediately. The command returns once
the migration has started; poll `qrdb barrier status` (or `qrdb barrier
status --tenant <id>` for per-tenant) until the reported strategy changes
from `"none"` to the target strategy to confirm completion.

When `--tenant` is supplied, the request migrates that tenant's dedicated
barrier. Tenants using the shared system barrier (the default `prefix`
isolation) cannot change encryption — the server rejects the request with
`ErrInvalidTenantIsolation` because a tenant on the shared barrier holds no
independent encryption key.

Flags:

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--strategy` | Yes | | Target strategy: `passphrase` or `shamir` |
| `--passphrase` | Yes for `passphrase` | | Unseal passphrase for the keyed barrier |
| `--shamir-threshold` | For `shamir` | (config default) | M value for M-of-N threshold |
| `--shamir-total` | For `shamir` | (config default) | N value for M-of-N shares |
| `--tenant` | No | (system barrier) | Scope the migration to a tenant's dedicated barrier |
| `--mechanism` | No | `swap` | Re-encryption method: `swap` or `in-place` |
| `--availability` | No | `quiesced` | Availability mode: `quiesced` or `online` |
| `-y`, `--assume-yes` | No | | Skip the confirmation prompt |

### 3.9 `barrier recovery generate`

Generate a fresh M-of-N recovery key set. Recovery keys live in a
parallel KMS slot — they are **not** the same as Shamir unseal shares.

```bash
qrdb barrier recovery generate --threshold 3 --total 5 --so-pin SO12345
```

### 3.10 `barrier recovery recover`

Restore the barrier by submitting M-of-N recovery keys.

```bash
qrdb barrier recovery recover --share key-a --share key-b --share key-c
```

---

## 3.11 Security Officer verbs (`barrier officer`, `barrier shamir-seal-vote`, `barrier recovery officer`)

These verbs implement the **Security Officer share-distribution model**
(see [../seal/shamir.md](../seal/shamir.md) and
[../seal/so-share-distribution-design.md](../seal/so-share-distribution-design.md)):
Shamir shares are never returned from init/rekey responses — each share
is sealed server-side to an enrolled officer, who claims it exactly
once over mTLS. Because this claim ceremony is the **only** way an
officer ever holds a share under the INV-1 share-free contract, these
verbs are part of the **canonical four-CLI surface** (promoted 2026-06;
see `08-bootstrap-auth-architecture.md` §6). qrdb is the reference
implementation; the xkmsctl / quicbaas / platformctl mirrors are
pending.

```
barrier officer register        --username <u> --csr-file <path>
                                [--authenticator-type <t>] [--authenticator-ref <r>]
barrier officer list
barrier officer claim-status
barrier officer claim-begin     --username <u>
barrier officer claim-complete  --username <u> --nonce-hex <hex> --sig-file <path>
barrier shamir-seal-vote        --share <s>
barrier recovery officer register        --username <u> --csr-file <path>
                                         [--authenticator-type <t>] [--authenticator-ref <r>]
barrier recovery officer claim-begin     --username <u>
barrier recovery officer claim-complete  --username <u> --nonce-hex <hex> --sig-file <path>
```

* **`officer register`** — submits the officer's PEM CSR; the ceremony
  CA signs it and binds the officer to the certificate thumbprint. The
  response carries the signed certificate and the officer's share
  index — never share bytes.
* **`officer claim-begin` / `claim-complete`** — the two-step
  mTLS-authenticated claim: `claim-begin` returns a challenge nonce;
  the officer signs it with their registered private key (raw signature
  bytes in `--sig-file`) and `claim-complete` returns the share bytes
  exactly once, after which the server-side enclave is destroyed.
* **`shamir-seal-vote`** — a Shamir barrier cannot be sealed by a
  single administrator (`barrier seal` returns
  `ErrShamirBarrierRequiresQuorum`); M officers each submit their share
  as a seal vote, and the barrier seals when the quorum completes.
* **`recovery officer …`** — the same register/claim ceremony for the
  independent recovery-share officer registry populated by
  `barrier recovery generate`.

```bash
qrdb barrier officer register --username alice --csr-file alice.csr
qrdb barrier officer claim-begin --username alice
qrdb barrier officer claim-complete --username alice --nonce-hex <nonce> --sig-file alice.sig
qrdb barrier shamir-seal-vote --share '<share>'
```

The verb tree and flag set are pinned by `TestOfficerBarrierVerbTree`
([`cmd/qrdb/barrier_officer_cmd_test.go`](../../cmd/qrdb/barrier_officer_cmd_test.go)).

---

## 4. PIN flow through the stack

```mermaid
flowchart LR
  Operator(["Operator<br/>--so-pin / --user-pin"])
  CLI["qrdb barrier &lt;verb&gt;"]
  SDK["qrdb SDK transport<br/>BarrierInitializeRequest<br/>BarrierSealRequest<br/>BarrierRekeyRequest"]
  Server["qrdbd / xkmsd / quicbaasd / platformd"]
  PINBackend["pkg/pin.PINBackend<br/>(persisted SO + User PIN)"]
  Strategy["pkg/seal strategy<br/>(software / shamir / none)"]

  Operator -->|both PINs as separate flags| CLI
  CLI -->|init: SOPin+UserPin<br/>seal/rekey/recovery: SOPin only| SDK
  SDK -->|encoded on the wire| Server
  Server --> PINBackend
  Server -->|wrapping passphrase / share set| Strategy
  PINBackend -.gates.-> Strategy
```

Two routes share the same operator-facing flags but reach different
server subsystems:

* `--passphrase` → `Strategy` (Argon2id-derived wrapping key or Shamir
  seed; `none` ignores it).
* `--so-pin` / `--user-pin` → `PINBackend` (compliance gating for
  rekey / seal / recovery-generate).

---

## 5. Operational sub-trees (not part of the canonical surface)

`qrdb` retains two operational sub-trees for parity with the legacy
ops workflow. These are **deliberately** not mirrored into the other
three CLIs:

```
barrier shamir list-shares
barrier shamir delete-share --index <i>
barrier shamir delete-all-shares
barrier shamir verify
barrier root-token --share <s> [--share <s> ...]
```

`barrier recovery` also exposes operational `delete` and `status`
subcommands. The canonical surface is `generate | recover`; the others
are clearly labelled as operational.

---

## 6. Cross-references

* Canonical spec: [`08-bootstrap-auth-architecture.md §6`](https://github.com/automatethethings/software/blob/main/docs/08-bootstrap-auth-architecture.md#6-cli-surface-convergence)
* Strategy taxonomy: [`docs/seal/README.md`](../seal/README.md)
* PIN backend interface: [`docs/pin/README.md`](../pin/README.md)
* Sibling CLI docs:
  [`xkmsctl barrier`](../../../go-xkms/docs/cli/barrier.md) ·
  [`quicbaas barrier`](../../../go-quicbaas/docs/cli/barrier.md) ·
  [`platformctl barrier`](../../../go-trusted-platform/docs/cli/barrier.md)
