<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Custodian Lifecycle & Share Delivery Protocol

Custodian-based M-of-N ceremony with Shamir threshold unsealing. This document specifies the enrollment, share-delivery, and unseal-quorum protocols. Two-tier share model: Operational Shamir shares (M-of-N Custodian quorum) and offline Recovery Keys (independent escrow for disaster recovery).

## Conceptual Model

### Two-Tier Share Architecture

```
Root Key (256-bit)
  |
  +--- Operational Shamir (M-of-N)
  |      |
  |      +--- Share 1 → Custodian A (PIV card, encrypted envelope)
  |      +--- Share 2 → Custodian B (FIDO2 authenticator, encrypted envelope)
  |      +--- Share 3 → Custodian C (PKCS#11 token, encrypted envelope)
  |      
  |      Unseal: M-of-N shares + challenge-response → Root Key reconstructed
  |
  +--- Recovery Keys (separate offline escrow)
         |
         +--- Key 1 → Secure vault (offline, air-gapped)
         +--- Key 2 → HSM backup location (with dual-access control)
         +--- Key 3 → Corporate backup system
         
         Unseal: M-of-N recovery keys (different M-of-N than operational) → Root Key reconstructed
```

**Operational Custodians** hold Shamir shares paired with hardware auth factors (PIV, FIDO2, PKCS#11). They participate in daily unseal ceremonies and must be online and responsive.

**Recovery Key Holders** are a **separate, offline tier**. Recovery Keys are generated independently via `BarrierGenerateRecoveryKeys()` and are never persisted in the qrdb system (instead held in external secure storage: vault, HSM backup, etc.). Recovery Keys are used only in disaster-recovery scenarios when the quorum of Operational Custodians is unavailable.

**Critical invariant**: No single Custodian holds the complete root key. Unseal requires threshold M of N to be present and to prove possession of their share via challenge-response.

## Custodian Record Schema

Each enrolled Custodian has a **public metadata record** persisted in the KV store at `__sys:custodian/<custodian_id>`:

```go
type Custodian struct {
    ID              string           // UUID v4, assigned at enrollment
    Username        string           // Human-readable identifier
    Method          string           // "pkcs11" | "piv" | "fido2"
    PublicCredential []byte          // Public key / credential metadata (no private key)
    EnrolledAt      time.Time        // Enrollment timestamp
    RevokedAt       *time.Time       // nil if active; set to time of revocation
    ShareIndex      int              // Position in Shamir split (0-indexed)
    CertificateID   string           // Reference to issued certificate (if MCA-signed)
}
```

**Public**: Persisted and Raft-replicated. Does NOT contain any share material.

**Private** (server-held, temporary): Custodian's encrypted share envelope, held in memory only during the enrollment transaction. Zeroed and discarded after delivery to the Custodian.

### Keys and the Username Claim

An enrolled Custodian occupies three keys:

| Key | Holds |
|---|---|
| `__sys:custodian/<id>` | the metadata record above |
| `__sys:custodian-idx/tenant/<tenant>/<id>` | the tenant index, walked by `ListCustodians` |
| `__sys:custodian-idx/username/<tenant>/<username>` | the username index, which resolves a tenant's username to its Custodian ID |

The username index is also the **registration claim**. Enrolling the same
username twice would seal a second share against one identity and leave one of
the two holders with an envelope the store no longer describes, so registration
takes that key with an atomic conditional write (`PutIfAbsent`) *before* it
seals anything:

1. **Claim** — `PutIfAbsent` writes a token unique to this attempt. Exactly one
   of two concurrent registrations for a username can create the key; the other
   is refused with `ErrCustodianAlreadyExists`.
2. **Seal and write** — the share is sealed and the record and tenant index are
   written.
3. **Commit** — the last write replaces the claim token with the Custodian ID,
   which is what makes the enrolment visible as an existing Custodian.

A failed write is compensated: what was written is deleted in reverse order and
the claim is released last, so the username is never freed before the debris it
guards is gone. Compensation runs only while the claim still carries this
attempt's token (or, for an ambiguous commit, while the record still carries
this attempt's bytes) — a registration that has lost the identity deletes
nothing.

Because the claim is required, the KV store backing the custodian service must
implement `kvstore.AtomicKVStore`. `custodian.NewService` refuses a store
without it (`ErrAtomicWriteUnsupported`) rather than emulating the claim with a
read followed by a write, which is the check-then-act window it exists to
close. Every qrdb engine provides the primitive natively — Raft evaluates the
condition inside the state machine, so replicas cannot diverge.

## Enrollment Flow

```
SO (xkmsd init)                 Daemon (qrdbd start)
    |                                    |
    |------ Initialize() -------->|
    |   {Members: [A, B, C], Threshold: 2}
    |                                    |
    |                         Shamir-split root key:
    |                         Share 1: [...]
    |                         Share 2: [...]
    |                         Share 3: [...]
    |                         (all in memguard)
    |                                    |
    |------ EnrollCustodian(A) ------->|
    |   {Username, Method, PubKey}       |
    |                         1. Verify enrollment request
    |                         2. Select Share 1 from memguard
    |                         3. Encrypt Share 1 → envelope (ECDH+AES-GCM)
    |                         4. Create custodian metadata record
    |                         5. Persist record: __sys:custodian/A
    |                         6. Return envelope over TLS
    |<--- envelope (ciphertext) <------|
    |   (SO receives sealed share)       7. Zero Share 1 in memguard
    |                                    |
    |------ EnrollCustodian(B) ------->|
    |                         (repeat for Share 2)
    |<--- envelope (ciphertext) <------|
    |                                    |
    |------ EnrollCustodian(C) ------->|
    |                         (repeat for Share 3)
    |<--- envelope (ciphertext) <------|
    |
    | SO distributes envelopes to Custodians A, B, C
    | (offline, secure channels; not through the daemon)
    |
    |------ CeremonyStatus() ------->|
    |                                   |
    |<--- {Pending: [A, B, C], ... } ---|
```

### Enrollment Steps

1. **SO Initializes Ceremony**
   - Specifies Members (Custodian identities) and Threshold M
   - Server generates Shamir split: M-of-N shares in memguard
   - Ceremony state transitions to `enrolling`

2. **SO Enrolls Each Custodian**
   - Provides custodian's **identity** (username, UUID)
   - Provides custodian's **auth factor enrollment** (public key, credential metadata)
   - Server selects next unencrypted share from memguard
   - Server performs ECDH key agreement with custodian's public key → shared secret
   - Server encrypts share via AES-256-GCM (shared secret + random nonce)
   - Server creates custodian metadata record (public: no share material)
   - Server persists metadata at `__sys:custodian/<id>` via Raft
   - Server returns sealed envelope to SO over authenticated TLS
   - Server **immediately zeros the plaintext share in memguard** (step critical for security)

3. **SO Distributes Envelopes Offline**
   - SO delivers each envelope to the corresponding Custodian through a secure out-of-band channel
   - **Not through the daemon**: envelopes are sensitive, delivered via secure courier, encrypted email with key escrow, etc.
   - SO **does not persist envelopes** on the daemon

4. **Custodian-Side Reception**
   - Custodian receives envelope (sealed Shamir share, ECDH params, nonce)
   - Custodian stores envelope securely (encrypted file, hardware factor, etc.)
   - Custodian keeps their private key in HSM / hardware factor (never leaves the device)

## Unseal Flow

```
Custodian A (PIV card)           Daemon (sealed, listening)
    |                                    |
    |------ UnsealShare(begin) ------->|
    |  {username: "custodian_a"}         |
    |                         1. Lookup custodian metadata
    |                         2. Generate nonce (32 bytes, crypto/rand)
    |                         3. Store nonce in accumulator
    |                         4. Return nonce + envelope digest
    |<--- {nonce, Th, M} <--|
    |                                    |
    | [On PIV card]                      |
    | private_key = read PIV slot 9A     |
    | signature = ECDSA_Sign(nonce, priv_key)
    | (private key never leaves card)
    |                                    |
    |------ UnsealShare(complete) ----->|
    |  {nonce, signature, envelope}      |
    |                         5. Verify signature against pubkey
    |                         6. Decrypt envelope via ECDH (nonce, pubkey)
    |                         7. Recover plaintext share
    |                         8. Accumulate in threshold counter
    |                         |
    |<--- {submitted: 1, required: 2} ---|
    |     (share accepted)               |
    |                                    |
[Custodian B & C repeat steps in parallel or series]
    |                                    |
    |                         When threshold M reached:
    |                         9. Reconstruct root key via shamir.Combine()
    |                         10. Unseal barrier with root key
    |                         11. Transition to unsealed
    |                         12. Zero all memguard buffers
    |                         |
```

### Unseal Steps

1. **Custodian Claims Share (Begin)**
   - Custodian authenticates via mTLS (cert issued during ceremony)
   - Custodian submits username to `UnsealShare(begin)`
   - Server looks up custodian metadata at `__sys:custodian/<id>`
   - Server generates nonce (32 bytes, `crypto/rand`)
   - Server returns nonce + envelope digest (commitment)

2. **Custodian Signs Challenge**
   - Custodian reads their private key from hardware factor (PIV slot, FIDO2 key, PKCS#11 token)
   - Custodian signs nonce with private key (e.g., ECDSA P-256)
   - **Private key never leaves the hardware factor**

3. **Custodian Submits Response (Complete)**
   - Custodian sends nonce, signature, and envelope (the sealed share they received during enrollment)
   - Server verifies signature against custodian's public key (from metadata)
   - Server decrypts envelope via ECDH key agreement (shared secret = ECDH(server_ephemeral_private, custodian_public))
   - Server recovers plaintext Shamir share
   - Server accumulates share in atomic accumulator (threshold counter)
   - Server returns progress: `{submitted: 1, required: 2}`

4. **Threshold Reached**
   - Server detects threshold M reached (via atomic `CompareAndSwap`)
   - Server reconstructs root key via `shamir.Combine(shares[:M])`
   - Server unseals barrier with root key
   - Server transitions to `unsealed` state
   - **Server zeros all plaintext shares and the reconstructed root key in memguard** (memguard reuse for next cycle)

## Share-Delivery Protocol Details

### ECDH Key Agreement

During `EnrollCustodian`, server and custodian public key perform Ephemeral ECDH:

```
Curve: P-256 or X25519 (TLS 1.3 standard curves)

Server generates ephemeral private key:
  ephemeral_priv ← crypto/rand (32 or 32 bytes, depending on curve)

Shared secret:
  ss = ECDH(ephemeral_priv, custodian_public_key)

Key derivation (HKDF-SHA256):
  dek = HKDF-SHA256(ss, salt=nil, info="go-qrdb/shamir/envelope/v1", length=32)
```

The shared secret `ss` is used to initialize AES-256-GCM. The ephemeral private key is immediately discarded (only the public point is sent to the custodian for their decryption).

### AES-256-GCM Envelope Format

```
Envelope = ephemeral_public || nonce || ciphertext || tag

| Field | Size | Description |
|-------|------|-------------|
| ephemeral_public | 65 bytes (P-256) or 32 bytes (X25519) | Ephemeral public key for ECDH |
| nonce | 12 bytes | Random AES-GCM nonce from crypto/rand |
| ciphertext | N bytes | AES-256-GCM(dek, plaintext_share, aad=metadata) |
| tag | 16 bytes | GCM authentication tag |

Total: 93 + N bytes (P-256), or 60 + N bytes (X25519)
```

### Nonce Handling

- **Enrollment nonce**: Unique per envelope; generated once, included in envelope; custodian never sees it
- **Challenge nonce**: Unique per unseal attempt; sent by server, signed by custodian, prevents replay attacks

Example challenge-response flow:

```
Server generates: nonce1 = random 32 bytes
Server sends: {nonce1, custodian_id, enrollment_envelope_digest}

Custodian signs: signature = Sign(nonce1, private_key_from_HSM)
Custodian sends: {nonce1, signature, envelope}

Server verifies:
  - Verify(signature, custodian_public_key, nonce1) → OK
  - (prevents custodian from replaying an old signature for a different nonce)
```

### Replay Prevention

Each unseal attempt generates a **new nonce**. Custodian must sign the current nonce. An attacker who captures a signed response cannot replay it for a future unseal attempt because:

1. Each unseal generates a new nonce
2. The signature is valid only for that specific nonce
3. Custodian will generate a new signature for the new nonce

Nonce TTL: configurable (default 5 minutes). If custodian takes too long to sign, the nonce expires and unseal fails.

## Zeroization Guarantees

All secret material is protected via `go-quicraft/pkg/enclave/ProtectedKey` (memguard wrapper):

```go
// Enrollment
for each custodian {
    share := shamir.Split()[custodian_index]  // Share in ProtectedKey (mlock'd memory)
    envelope := encryptShare(share, custodian_pubkey)
    share.Zero()  // Secure wipe via memguard
}

// Unseal
custodian_shares := make([]*enclave.ProtectedKey, threshold)
for i := 0; i < threshold; i++ {
    custodian_shares[i] = decryptEnvelope(envelope)
}
root_key := shamir.Combine(custodian_shares)  // Reconstructed root key in ProtectedKey
unseal(root_key)
for i := range custodian_shares {
    custodian_shares[i].Destroy()  // Secure wipe
}
```

**Guarantees**:
- Plaintext shares **never stored** on disk (except via barrier encryption, which is a different operation)
- Memguard buffers are **mlock'd** (prevent swapping to disk)
- Buffers are **guard-paged** (detect buffer overflows)
- Wipe is **byte-by-byte** with random data (not just zero; prevents forensics)
- All wipes use `memguard.LockedBuffer.Destroy()` which is guaranteed to zero before returning memory

## Failure Modes & Recovery

### Custodian Loses Share

If a Custodian loses their sealed share envelope (physical loss, corrupted file, etc.):

**Option 1: Rekey**
- SO revokes the custodian from the current ceremony
- SO initiates a rekey operation: `BarrierRekey(threshold=M, total=N-1)`
- Server generates new Shamir split (N-1 shares instead of N)
- SO re-enrolls remaining custodians with new shares

**Option 2: Use Recovery Keys**
- If recovery keys are available, use them to unseal the barrier instead
- SO distributes new operational shares once unsealed
- Custodian cannot participate in that unseal cycle

### Custodian Compromised

If a Custodian's private key is compromised (HSM theft, card cloned, etc.):

**Option 1: Immediate Revocation**
- SO revokes the custodian: `RevokeCustodian(custodian_id)`
- Custodian metadata is marked `RevokedAt = now()`
- Custodian cannot participate in future unseal ceremonies
- Unseal still works if threshold M-1 of N-1 remaining custodians participate

**Option 2: Rekey for Long-Term Security**
- SO initiates rekey to rotate shares and change the threshold
- Compromised custodian cannot decrypt new envelopes (different ECDH key agreement, different root key post-rekey)
- New shares are issued to remaining custodians

**Security property**: A single compromised Custodian cannot unseal the barrier if M > 1 (requires threshold M of N).

### Daemon Restart Mid-Ceremony

If the daemon restarts during enrollment or unseal:

**Enrollment restart**: All in-flight ceremony state is lost (shares in memguard are zeroed on process exit). SO must restart the ceremony from `Initialize()`. **This is intentional**: ceremony state is transient, not persisted.

**Unseal restart**: Accumulated shares are lost. Custodians must resubmit shares from the beginning. No state pollution or race conditions because accumulator is in-memory only.

**Interrupted registration**: a process that dies between the username claim
and the commit leaves the claim behind. The username cannot enroll until it is
cleared, and registration says so — `IncompleteEnrolmentError` names the key,
distinguishing it from an existing Custodian. Nothing is lost with it: the
claim is taken before the share is sealed, so an enrollment interrupted in this
window never produced envelope material. Retry first — the same state is also
what an in-flight registration on another caller looks like — and remove the
named key only if it persists.

### All Custodians Unavailable

If threshold M custodians cannot be reached or have lost their shares:

**Use Recovery Keys**: Unseal via `BarrierRecoverWithKeys(keys[:M])` using independent offline keys.

After recovery, initiate a rekey to establish a new custodian group.

## Audit Events

The ceremony and unseal processes emit audit events for all state transitions and operations:

| Event | Resource | Action | Trigger |
|-------|----------|--------|---------|
| Ceremony initialized | ceremony | initialize | SO calls `Initialize(Members, Threshold)` |
| Custodian enrolled | custodian:{id} | enroll | SO calls `EnrollCustodian(username, method, pubkey)` |
| Custodian revoked | custodian:{id} | revoke | SO calls `RevokeCustodian(id)` |
| Share claim begun | barrier | unseal-share-claim-begin | Custodian calls `UnsealShare(begin)` |
| Share claim completed | barrier | unseal-share-claim-complete | Custodian calls `UnsealShare(complete)` with valid signature |
| Share claim rejected | barrier | unseal-share-claim-rejected | Signature verification failed or nonce expired |
| Unseal quorum reached | barrier | unseal | Threshold M shares accumulated; barrier transitioned to unsealed |
| Ceremony completed | ceremony | complete | All custodians claimed shares; ceremony transitioned to operational |

Each event includes: timestamp, actor (SO or Custodian username), tenant ID, outcome (success/failure + reason), and attributes (share index, signature algorithm, ECDH curve, etc.).

## SDK and Protocol Exposure

All custody lifecycle operations are exposed through the qrdb SDK (Go, TypeScript, REST, gRPC, MCP, QUIC, Unix socket):

### REST Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/ceremony/initialize` | SO: Initialize ceremony with Officers (CSR-enrolled) and Threshold |
| `POST` | `/v1/ceremony/enroll-custodian` | SO: Enroll a custodian with auth factor enrollment |
| `POST` | `/v1/ceremony/revoke-custodian` | SO: Revoke a custodian from the active ceremony |
| `GET` | `/v1/ceremony/status` | SO/Auditor: Get ceremony state and pending custodian list |
| `POST` | `/v1/ceremony/claim-share/begin` | Custodian: Request challenge nonce for share claim |
| `POST` | `/v1/ceremony/claim-share/complete` | Custodian: Submit signed response to claim their share |

### Go SDK

```go
client, _ := sdk.NewClient(transport)
client.Connect(ctx)

// SO initializes the ceremony. Officers are registered by CSR; no share
// bytes are returned (INV-1) — custodians claim shares individually below.
initResp, _ := client.Ceremony().CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{
	SOPin:     soPin,
	UserPin:   userPin,
	Threshold: 2,
	Officers: []transport.CeremonyOfficerRequest{
		{Username: "custodian_a", CSRPEM: csrA},
		{Username: "custodian_b", CSRPEM: csrB},
		{Username: "custodian_c", CSRPEM: csrC},
	},
})
_ = initResp.ShareCount // share metadata only; never raw shares

// Custodian enrollment with a hardware auth factor is SO-driven over REST:
// POST /v1/ceremony/enroll-custodian
//   {username, method, enrollmentID, publicKeyPEM, shareIndex, shareBytes}
// The service seals the share into an ECDH+AES-256-GCM envelope bound to the
// custodian's factor public key and zeroes the raw bytes after sealing.

// Custodian claims their sealed share envelope (factor challenge handshake).
begin, _ := client.Ceremony().CeremonyClaimShareBegin(ctx, &transport.CeremonyClaimShareBeginRequest{
	CustodianID: custodianID,
})

// Custodian signs begin.Nonce with their hardware factor (PIV/FIDO2/PKCS#11)
// in-process on the custodian's machine.
factorResponse := signWithFactor(begin.Nonce)

info, _ := client.Ceremony().CeremonyClaimShareComplete(ctx, &transport.CeremonyClaimShareCompleteRequest{
	CustodianID:    custodianID,
	ChallengeID:    challengeID, // from the factor subsystem's challenge
	FactorResponse: factorResponse,
})
_ = info.ShareClaimed // true; envelope delivered exactly once

// Custodian submits their plaintext share toward the unseal quorum. The
// custodian decrypts the envelope offline with their hardware private key;
// the server never sees the envelope key and zeroes ShareBytes after
// accumulation.
progress, _ := client.Ceremony().CeremonySubmitUnsealShare(ctx, &transport.CeremonySubmitUnsealShareRequest{
	CustodianID:    custodianID,
	ChallengeID:    challengeID,
	FactorResponse: factorResponse,
	ShareBytes:     plaintextShare,
})
// progress.Submitted = 1, progress.Required = 2, progress.Unsealed = false
```

## See Also

- [so-share-distribution-design.md](so-share-distribution-design.md) — Security Officer share-distribution model for the storage barrier (CSR enrollment, one-time enclave-backed claims, quorum-gated sealing)
- [shamir.md](shamir.md) — Barrier-level Shamir lifecycle as exposed through `/v1/barrier/*`
- [roles.md](../auth/roles.md) — Custodian role definition and permissions
- [compliance.md](../seal/compliance.md) — FIPS 140-3 AS10.05/06 (split knowledge, dual auth)
- [auth-factors.md](../seal/auth-factors.md) — PIV, PKCS#11, FIDO2 factor implementations
- [README.md](../seal/README.md) — Barrier architecture and sealing strategies
- [xkms docs/init/ceremony.md](https://github.com/jeremyhahn/go-xkms/blob/main/docs/init/ceremony.md) — Extended xkms ceremony with HSM hardware support
- [FIPS 140-3 AS10.05, AS10.06](https://csrc.nist.gov/publications/detail/fips/140/3) — Split knowledge and dual authorization
- [NIST SP 800-56A Rev 3](https://csrc.nist.gov/publications/detail/sp/800-56a/rev-3/final) — ECDH key agreement
