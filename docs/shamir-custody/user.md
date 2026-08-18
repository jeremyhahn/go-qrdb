# Shamir Custody — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

M-of-N custody means no one person can open the database alone. This guide is
for the people who hold the shares and the person who sets it up.

## Prerequisites

- A ceremony CA available to sign officer certificates — see
  [../ceremony/user.md](../ceremony/user.md).
- Client certificates for each officer, and the relevant `seal:*` permissions
  for the administrator doing the setup.
- Each officer needs their own private key, because they will sign a challenge
  with it.

## The shape of it

1. **Register** every officer, *before* initializing the barrier.
2. **Initialize** with `init-shamir --threshold M --total N`. The response
   contains **no shares** — they are sealed to the officers server side.
3. Each officer **claims** their own share, once.
4. To unseal, M officers submit their shares.
5. To seal, M officers vote.

## Setting it up

**Register the officers.** Each supplies a CSR; the ceremony CA signs it and
binds the officer to their certificate's thumbprint.

```bash
qrdb barrier officer register --username alice --csr-file alice.csr \
     --authenticator-type piv --authenticator-ref slot9a
qrdb barrier officer register --username bob   --csr-file bob.csr
qrdb barrier officer list
```

No shares exist yet. Registration produces the certificate and a share index.

**Initialize.**

```bash
qrdb barrier init-shamir --passphrase '<seed>' --threshold 3 --total 5
```

The response tells you M, N and how many shares were sealed. It does **not**
contain shares, and never will — that is the whole design.

**Each officer claims their share.** Two steps: get a nonce, sign it, hand back
the signature.

```bash
NONCE=$(qrdb barrier officer claim-begin --username alice --output json | jq -r .nonce)
# sign the nonce with alice's private key -> alice.sig
qrdb barrier officer claim-complete --username alice \
     --nonce-hex "$NONCE" --sig-file alice.sig
```

**The share is returned exactly once.** The server destroys its copy. Store it
before the terminal buffer scrolls away — there is no second retrieval. The
nonce expires after five minutes.

**Check who has claimed.**

```bash
qrdb barrier officer claim-status --output json
```

This never shows share bytes.

## Day to day

**Unseal.** Any M officers.

```bash
qrdb barrier unseal-shares --share <alice> --share <bob> --share <carol>
```

Or one at a time, from different terminals:

```bash
qrdb barrier unseal-shares --share <alice>    # progress: 1 of 3
qrdb barrier unseal-shares --share <bob>      # progress: 2 of 3
qrdb barrier unseal-shares --share <carol>    # unsealed
```

Below the threshold is **not an error** — it reports progress. But the
accumulator expires after 15 minutes by default, so coordinate.

**Seal.** One administrator cannot seal a Shamir barrier. M officers vote:

```bash
qrdb barrier shamir-seal-vote --share <alice>
qrdb barrier shamir-seal-vote --share <bob>
qrdb barrier shamir-seal-vote --share <carol>   # quorum met, barrier sealed
```

**Get a root token.** Officers submitting their shares *is* the consent signal.
No token exists until they do this.

```bash
qrdb barrier root-token --share <alice> --share <bob> --share <carol>
```

## When an officer leaves

Rekey. This generates a fresh unseal key and a fresh share set, and
**cryptographically revokes every share previously handed out** — including
ones already claimed.

```bash
qrdb barrier rekey --so-pin SO123
qrdb barrier rekey --threshold 3 --total 5     # change M/N at the same time
```

Every officer must claim again; claim history resets. Your data is **not**
re-encrypted — the root key does not change, only the key that guards it.

## Recovery keys

A separate, independent split of the root key for the case where the
operational quorum simply cannot be assembled. Different people, kept offline.

**Generate** — barrier must be **unsealed**:

```bash
qrdb barrier recovery generate --threshold 3 --total 5 --so-pin SO123
```

Recovery officers register and claim exactly like unseal officers, under
`qrdb barrier recovery officer ...`, and they are a **different set of people**.

**Recover** — barrier must be **sealed**:

```bash
qrdb barrier recovery recover --share key-a --share key-b --share key-c
```

Recovery keys are never persisted on the server. Where they live — an offline
vault, an HSM backup, a corporate escrow — is your decision and your
responsibility.

```bash
qrdb barrier recovery status      # are recovery keys configured at all?
```

## Troubleshooting

**"unseal key mismatch".** The shares reconstructed something that does not
open the wrapped root key. Either they are from different sets, one is
mistyped, or a rekey has revoked them. This is the check working — the
alternative would be unsealing with a wrong key and corrupting everything it
touched.

**"quorum expired".** The accumulator TTL elapsed (15 minutes by default,
`seal.share_ttl`). Start again with all M officers ready.

**"duplicate share".** The same share was submitted twice. It does not count
twice; find the missing officer.

**An officer lost their share.** There is no re-issue — the server destroyed
its copy on claim. **Rekey**, which redistributes the whole set, and have
everyone claim again.

**An officer's key is compromised.** Revoke the custodian and rekey. Rekey
revokes every prior share, so the compromised one stops working even though it
was already claimed.

**Claim fails with "already claimed".** Delivery is once, by design. If the
share was lost between the response and storage, rekey is the recovery.

**The claim nonce expired.** Five-minute TTL. Get a new one with `claim-begin`
and sign it promptly.

**`init-shamir` failed with "ceremony distributor not configured".** The
server has no wired distributor, so it cannot seal shares to officers. That is
a deployment wiring problem, not something a flag fixes.

**Registration was rejected before the barrier existed.** When the daemon
manages its own TLS, the ceremony CA is built from the cluster CA bundle and
registration works. With bring-your-own TLS certificates you must run the full
initialization ceremony first to establish a CA — see
[../ceremony/user.md](../ceremony/user.md).

**Nobody can unseal and there are no recovery keys.** The data is not
recoverable. Generate recovery keys **now**, on a barrier you can still unseal,
if you have not.
