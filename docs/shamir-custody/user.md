# Shamir Custody — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-29

M-of-N custody means no one person can open the database alone. This guide is
for the people who hold the shares — Security Officers and key custodians —
and for the administrator who sets custody up.

## Who is who

- **Administrator** — enrols holders, starts rekeys and revocations. Never
  sees a share.
- **Security Officer** — holds a share and also has governance rights.
- **Key custodian** — holds a share and nothing else.

Officers and custodians are **share holders**. They receive, protect and use
their shares in exactly the same way.

## Prerequisites

- A ceremony CA on the node (see [../ceremony/user.md](../ceremony/user.md)).
- For each holder: a private key and a CSR for it. The CSR contributes only the
  public key; the CA decides the certificate's name.
- Optionally, a separate envelope key per holder — for instance an ML-KEM
  hybrid key for post-quantum protection of the envelope — and any hardware
  factor your deployment provides (FIDO2, PIV, PKCS#11).

## The shape of it

1. The administrator **enrols** every holder.
2. The administrator **initializes** custody at M-of-N. Nobody sees a share:
   each is sealed to its holder.
3. Each holder **claims** their sealed envelope, once, and keeps it.
4. Whenever the node needs its key — to unseal, to seal, to mint a root token,
   to rekey — M holders each **submit** their share, authenticated.

## Setting it up

**Enrol the holders.** Two officers and three custodians, for a 3-of-5:

```bash
qrdb barrier officer register --username alice --role so --csr-file alice.csr
qrdb barrier officer register --username bob   --role so --csr-file bob.csr
qrdb barrier officer register --username carol --role custodian --csr-file carol.csr \
     --encryption-kem mlkem768-x25519 --encryption-key-file carol-kem.pub
qrdb barrier officer register --username dave  --role custodian --csr-file dave.csr \
     --factor "fido2=$(cat dave-fido2-enrol.json)" --designated fido2
qrdb barrier officer register --username erin  --role custodian --csr-file erin.csr
qrdb barrier officer list
```

Each command prints the holder's certificate; give it to the holder. You
cannot enrol yourself, and one person cannot hold two shares.

`qrdb init --barrier-strategy shamir` can do this in one step for a new
system: `--officers name=csr` and `--custodians name=csr`, repeated.

**Initialize.**

```bash
qrdb barrier init-shamir --threshold 3 --total 5
```

`--total` must equal the number of holders you enrolled. The answer contains
M, N and how many envelopes were sealed — never a share.

**Choose a factor policy.** By default each holder must authenticate with the
one factor fixed at enrolment (`designated`): a holder with a hardware key
cannot be let in with a weaker one. `--policy any-enrolled` lets holders use
any factor they enrolled. The policy is part of the share set; changing it is
a rekey.

## For holders: claim your envelope

Connect with your certificate and claim, once:

```bash
qrdb --cert-file alice.crt --key-file alice.key \
     barrier officer claim --username alice --key alice.key --out alice.env
```

The envelope is sealed to your key — only you can open it — and the node keeps
no copy. Keep the file safe; it is useless without your private key, and your
key is useless without it. If your key lives on a smart card, use
`claim-begin`, sign the nonce with the card, and finish with `claim-complete`.

Claim within 24 hours; an unclaimed envelope is wiped, and only a rekey
replaces it.

The web UI does not claim or submit: your keys never enter a browser page. It
enrols and lists holders, opens rekey and revocation rounds, and shows who has
claimed. Claim and submit with the CLI, as here, or from the desktop app.

## For holders: take part in a quorum

When the node is sealed — after a restart, for instance — it serves the holder
routes on its sealed window. Each holder submits:

```bash
qrdb barrier quorum submit --op unseal --username alice \
     --envelope alice.env --key alice.key
```

A holder with a hardware factor asks for a challenge first, answers it with
the device, then submits:

```bash
qrdb barrier quorum begin --op unseal --username dave -o json
qrdb barrier quorum submit --op unseal --username dave --envelope dave.env \
     --key dave.key --challenge-id <id> --factor fido2 --response-file dave.assertion
```

Each submission reports how many of M have been counted. The M-th unseals the
node. The same flow with `--op seal` seals it, and `--op root-token` mints a
one-time root token.

You are counted once per round; submitting again is refused. So is a
submission with a factor the policy does not admit, and a submission from
someone no longer a holder.

## Adding, removing and rotating holders

Every change to who holds the key is a **rekey**, and a rekey needs M current
holders to agree.

- **Add a holder**: enrol them, then `qrdb barrier rekey --threshold M --so-pin …`, and M
  holders submit with `--op rekey`.
- **Remove a holder**: `qrdb barrier officer revoke --username erin --so-pin …`, and M of
  the *remaining* holders submit with `--op rekey`. It is refused if the
  remaining holders could not meet the threshold.
- **Change M or the policy**: `qrdb barrier rekey --threshold 4 --policy any-enrolled --so-pin …`.

Starting a rekey or revocation on the system barrier needs the Security
Officer PIN.

When the round completes every earlier share stops working, including the
revoked holder's. Every remaining holder claims a new envelope.

## Recovery holders

A separate group can hold an independent **recovery key**, for when the
unseal holders are unavailable. Enrol them with `--domain recovery`, generate
the key with `qrdb barrier recovery generate --threshold M`, have them claim
with `--domain recovery`, and they unseal with `quorum submit --domain
recovery --op recover`. The two groups
never share keys, and a rekey of one never affects the other.

## Tenants

A tenant with its own barrier has its own holders: the same commands under
`qrdb tenant barrier-officer`, `barrier-quorum` and `barrier-rekey`, with
`--tenant-id`.

## Troubleshooting

| Symptom | Meaning |
|---|---|
| `claim` refused: certificate | the connection is not presenting your enrolled certificate |
| `claim` refused: already claimed / expired | claims are once only; ask for a rekey |
| submission refused: already submitted | you are already counted in this round |
| submission refused: factor not admitted | use your designated factor, or ask for a rekey to `any-enrolled` |
| submission refused: not in roster / revoked | you are not a holder of the current share set |
| round expired | a round lasts 15 minutes by default; submit again |
| revocation refused | the remaining holders could not meet the threshold; lower it or enrol a holder first |
