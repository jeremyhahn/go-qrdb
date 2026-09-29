<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Compliance Framework Reference

Cross-reference of go-qrdb cryptographic capabilities against FIPS 140-3, FedRAMP, PCI-DSS v4, and HIPAA requirements. This document maps each regulatory requirement to the qrdb feature that satisfies it and indicates implementation phase and status.

## Executive Summary

### FIPS 140-3 (ISO/IEC 19790)

go-qrdb implements the core compliance posture for FIPS 140-3 Level 2:

- **Separation of Duties** (AS10.03): Six-role model with SO governance-only constraint
- **Split Knowledge** (AS10.05): Shamir M-of-N unseal with quorum threshold
- **Dual Authorization** (AS10.06): mTLS + SO PIN for SO operations; challenge-response for Custodians
- **Key Zeroization** (AS10.09): go-enclave locked-memory buffers with secure wipe on barrier operations
- **Audit Logging** (AS10.10): Append-only hash-chained audit trail with tamper-evidence
- **Cryptographic Integrity** (AS10.11): SHA-256 hash-chain validation on audit entries

FIPS 140-3 Level 3 requires additional hardware measures (physical tamper detection, secure facility) provided by downstream integrations (HSM deployments via PKCS#11 strategy, TPM2 via xkms).

### FedRAMP Moderate/High

go-qrdb satisfies baseline access control, audit, and key lifecycle requirements:

- **AC-6**: Least Privilege — Role-based access with explicit deny-lists
- **AU-2**: Audit Events — All barrier, ceremony, RBAC, key operations logged
- **AU-12**: Audit Generation — Structured audit events with actor, resource, action, outcome
- **SI-12**: Information Handling — Key zeroization via go-enclave; recovery keys offline
- **CA-6**: Security Planning — Architecture documentation (this document)

FedRAMP High adds continuous monitoring, incident response, and SIEM integration (addressed via audit API export).

### PCI-DSS v4

go-qrdb satisfies key management, access control, and logging requirements:

- **3.2.x**: Key Management Program — Documented lifecycle (generate, store, rotate, retire, revoke)
- **3.6.x**: Key Lifecycle — Rotation, secure deletion via secure wipe
- **7.1**: Role-Based Access Control — Six-role model with denial enforcement
- **10.2.x**: Audit Event Logging — Structured logs with actor, resource, action, outcome
- **12.x**: Security Policies — Documented in code and compliance matrices

### HIPAA (45 CFR 164.308, 164.312, 164.412)

go-qrdb addresses encryption, access control, and breach notification requirements:

- **164.312(a)(2)(i)**: Encryption & Decryption — AES-256-GCM encryption at rest, TLS 1.3 in flight
- **164.312(b)**: Audit Controls — Structured event logging with retention policies
- **164.412**: Breach Notification — Audit trail integrity (tamper-evident hash-chain)
- **164.308(a)(1)(ii)(B)**: Information Access Control — Role-based access, principle of least privilege

## FIPS 140-3 Requirement Matrix

| Requirement ID | Requirement | go-qrdb Capability | Phase | File |
|---|---|---|---|---|
| **AS10.03** | Roles, Services, and Authentication | Six-role model (SO, Admin, Operator, User, Auditor, Custodian); SO governance-only; RBAC with explicit deny-lists | 2 | `docs/auth/roles.md`, `pkg/role/`, `pkg/auth/rbac/` |
| **AS10.05** | Split Knowledge Procedure | Shamir M-of-N custody of the unseal key by named share holders (Security Officers and custodians); no single holder, operator or administrator ever holds a key or sees a share | 4 | `docs/shamir-custody/`, `pkg/seal/quorum.go`, `pkg/seal/holders.go` |
| **AS10.06** | Dual Authorization Procedure | SO governance: mTLS + PIN; every share-gated operation (unseal, seal, root token, rekey, revocation) needs M distinct holders, each authenticated by an enrolled factor against the roster bound to the key | 4 | `pkg/auth/so_auth.go`, `pkg/seal/quorum.go`, `pkg/ceremony/holder_scope.go` |
| **AS10.09** | Key Zeroization | go-enclave locked pages for material in use and sealed enclaves for material at rest in memory; byte-by-byte wipe on barrier seal; `defer Destroy()` on all secret operations | 1 (inherited from go-quicraft) | `go-enclave`, `pkg/seal/`, `pkg/secure/` |
| **AS10.10** | Integrity of Key Information | SHA-256 hash-chain on audit entries; every audit event includes hash of previous entry; tamper-evident verification | 6 | `pkg/audit/file_logger.go` |
| **AS10.11** | Secret Information Integrity | Encrypted audit entries (via barrier); integrity validation via hash-chain; ACL on audit read (Auditor only) | 6 | `pkg/audit/` |
| **AS10.14** | Cryptographic Parameters | Approved algorithms: AES-256-GCM (FIPS 197), HKDF-SHA256 (NIST SP 800-56A), Argon2id/PBKDF2-SHA256 (FIPS-aware KDF mode), ECDSA P-256 (FIPS 186-4), HMAC-SHA256 | 1, 5 | `pkg/seal/strategy_software.go` |
| **AS10.19** | Delivery of Key Information | Each share HPKE-sealed (RFC 9180; P-256/P-384 DHKEM in FIPS mode) to its holder's enrolled key; delivered once to the holder's own mTLS session; server copy wiped; the holder opens it locally | 4 | `docs/shamir-custody/`, `pkg/ceremony/envelope.go`, `pkg/keydelivery/` |
| **AS10.23** | Access Control | RBAC with six roles; permission-based enforcement at middleware; explicit deny-lists for SO crypto ops | 2 | `docs/auth/roles.md`, `pkg/auth/rbac/rbac.go` |
| **AS10.27** | Identification and Authentication | mTLS certificates (X.509); SO PIN (via `pkg/pin.PINBackend`); challenge-response via auth factors (PIV, PKCS#11, FIDO2 implementations in go-xkms; software mTLS in qrdb) | 3, 4 | `pkg/cert/`, `pkg/authfactors/` (Factor framework + registry), `pkg/pin/` |
| **AS10.31** | User Zeroization | User PIN held in locked memory during verification; zeroed after use; barrier rekey wipes old DEK | 1, 5 | `pkg/seal/`, `pkg/secure/`, `go-enclave` |

## FedRAMP Moderate/High Requirement Matrix

| Control | Requirement | go-qrdb Capability | Phase | File |
|---|---|---|---|---|
| **AC-6** | Least Privilege | Role-based access with six roles; explicit deny-lists; Custodian minimal-access tier | 2 | `docs/auth/roles.md`, `pkg/auth/rbac/` |
| **AC-3** | Access Enforcement | RBAC middleware on all API endpoints; permission-based denial; audit-logged denials | 2, 6 | `pkg/auth/middleware/`, `pkg/audit/` |
| **IA-2** | Authentication (Enhanced) | mTLS certificates; SO dual-factor (PIN via `pkg/pin.PINBackend`); challenge-response for Custodian (FIDO2, PIV, PKCS#11 implementations injected from go-xkms via `WithFactorRegistry`) | 3, 4 | `pkg/cert/`, `pkg/authfactors/`, `pkg/pin/` |
| **IA-5** | Password Policy | SO PIN not stored (verified against HSM or hash-commit); User PIN configurable length/complexity | 2, 4 | `pkg/auth/so_auth.go`, `pkg/auth/pin_policy.go` |
| **AU-2** | Audit Events | Structured audit events for barrier, ceremony, RBAC, keys, tenants, users, roles | 6 | `pkg/audit/`, `docs/audit/README.md` |
| **AU-12** | Audit Generation | Slog integration; every event includes timestamp, actor, tenant, role, resource, action, outcome, attributes | 6 | `pkg/audit/slog_sink.go` |
| **SI-12** | Information Management | An independent recovery key split to its own holders; shares sealed to their holders and never persisted server side | 4 | `pkg/seal/recovery.go`, `docs/shamir-custody/` |
| **CA-6** | Security Planning | Documented architecture, role model, audit trail, key lifecycle, threat model | 1–6 | `docs/seal/`, `docs/auth/`, `docs/audit/` |

## PCI-DSS v4 Requirement Matrix

| Requirement | Sub-Requirement | go-qrdb Capability | Phase | File |
|---|---|---|---|---|
| **3.2** | Key Management Program | Documented key lifecycle (generate, store, rotate, retire, revoke, distribute, escrow) | 1–6 | `docs/seal/`, `pkg/dek/`, `pkg/cert/` |
| **3.2.1** | Key Generation | Cryptographically secure random (crypto/rand); FIPS 140-3 approved algorithms (AES, ECDSA, HMAC) | 1 | `pkg/seal/barrier.go`, `pkg/cert/` |
| **3.4.1** | Strong Cryptography | AES-256-GCM (at rest), TLS 1.3 (in flight), ECDSA P-256 / RSA-4096 (certificates) | 1 | `pkg/seal/`, `pkg/cert/` |
| **3.6.1** | Key Rotation | Quorum-gated rekey of the unseal key (new shares, all earlier shares revoked); DEK rotation (configurable); certificate rotation | 5 | `pkg/seal/quorum.go`, `pkg/seal/redistribute.go`, `pkg/dek/` |
| **3.6.2** | Cryptographic Key Destruction | Secure wipe via go-enclave (unmapped locked pages) and `pkg/secure`; byte-by-byte overwrite for operational keys; recovery keys in offline escrow | 1 | `go-enclave`, `pkg/secure/` |
| **7.1** | Limit User Access | Role-based access control; six-role model; explicit deny-lists; permission matrix documented | 2 | `docs/auth/roles.md`, `pkg/auth/rbac/` |
| **10.2.1** | User Access to Cardholder Data | Audit events for all access attempts (successful and failed) | 6 | `pkg/audit/` |
| **10.2.2** | User Access to Audit Trail | Auditor role with read-only access; permission enforcement | 6 | `pkg/audit/`, `docs/auth/roles.md` |
| **10.2.3** | User Actions on Cardholder Data | Structured audit events capture actor, timestamp, resource, action, outcome | 6 | `pkg/audit/` |
| **10.2.7** | Invalid Access Attempts | Audit logs both successful and failed authentication attempts | 6 | `pkg/audit/`, `pkg/auth/middleware/` |
| **12.2** | Security Policies | Documented in code comments, architecture docs, and compliance matrices | 1–6 | All `docs/` files |

## HIPAA Requirement Matrix

| CFR § | Requirement | go-qrdb Capability | Phase | File |
|---|---|---|---|---|
| **164.308(a)(1)(ii)(B)** | Information Access Control (Administrative) | Role-based access control; principle of least privilege; SO governance only; Auditor read-only | 2 | `docs/auth/roles.md`, `pkg/auth/rbac/` |
| **164.312(a)(2)(i)** | Encryption and Decryption (Technical) | AES-256-GCM at rest; TLS 1.3 in flight; approved algorithms per FIPS 140-3 | 1 | `pkg/seal/`, `pkg/cert/` |
| **164.312(b)** | Audit Controls (Technical) | Structured audit event logging with retention; query API; integrity validation via hash-chain | 6 | `pkg/audit/`, `docs/audit/README.md` |
| **164.412** | Breach Notification (Administrative) | Audit trail integrity (tamper-evident hash-chain); event immutability; comprehensive logging for breach investigation | 6 | `pkg/audit/file_logger.go` |

## Feature Implementation Status

| Feature | Phase | Status | Notes |
|---------|-------|--------|-------|
| **Separation of Duties (SO governance-only)** | 2 | Shipped | Explicit deny-lists in RBAC; defense-in-depth enforcement |
| **Shamir M-of-N Custody** | 4 | Shipped | `pkg/seal` (roster, quorum) + `pkg/ceremony` (holder engine) |
| **Share-Holder Enrolment & Authenticated Quorum** | 4 | Shipped | Security Officers and custodians enrolled alike; public records only; shares HPKE-sealed to each holder |
| **Auth Factor framework (Factor interface + FactorRegistry)** | 3 | Shipped | qrdb owns the `Factor` framework and the software mTLS reference factor. FIDO2, PIV, PKCS#11 factor implementations live in go-xkms and register through `WithFactorRegistry`. |
| **PKCS#11 Seal Strategy** | 5 | Shipped (in go-xkms) | Hardware seal strategies live in go-xkms and register into qrdb's `seal.StrategyRegistry`. |
| **FIPS-Aware KDF** | 5 | Shipped | `seal.FIPSEnabled()` — the runtime FIPS module state, then `GOFIPS140` (with `off` honoured as disabled) — selects PBKDF2-SHA256 (NIST SP 800-132); Argon2id otherwise. Both adapters always registered for cross-version unseal. |
| **PIN Management (canonical `pkg/pin`)** | 2 | Shipped | `pin.PINBackend` + `SoftwareBackend` in qrdb; TPM2 / PKCS#11 hardware backends in go-xkms injected via `WithPINBackend`. |
| **Versioned Credential Rotation** | 8 | Shipped | `pkg/credentials.RotateCredential` with grace periods; see `docs/credentials/README.md`. |
| **Audit Event Logging** | 6 | Shipped | Hash-chained, tamper-evident, query API across all protocols |
| **`StrategyNone` (no-encryption barrier)** | 0 | Shipped | Dev / OS-level FDE deployments; xkms `--require-encryption` compliance gate refuses it. |
| **Recovery Keys (Offline Escrow)** | 4 | Existing | `pkg/seal/recovery.go`; independent Shamir split |
| **Multi-Tenant Barriers** | Existing | Deployed | `pkg/seal/barrier_registry.go`, `pkg/dek/` |
| **Root Token Generation** | Existing | Deployed | Shamir share reconstruction for emergency unlock |

## What go-qrdb Does NOT Claim

The following are out of scope for go-qrdb and are handled by downstream consumers or deployment:

- **Physical Security** (FIPS 140-3 PH*): Secure facility, tamper detection, physical access controls — handled by deployment environment
- **Hardware-Specific Strategies** (TPM2, cloud KMS): Provided by go-xkms or consumer application
- **Long-Term Key Archival**: External storage (e.g., AWS Secrets Manager, HashiCorp Vault); qrdb focuses on runtime key lifecycle
- **SIEM Integration**: Audit events are exported via REST/gRPC/MCP API; consumer integrates with SIEM
- **Multi-Node Key Replication**: qrdb uses Raft for state replication; hardware-specific key replication (e.g., HSM synchronization) is deployment-specific
- **Forensics & Post-Incident Analysis**: qrdb provides audit logs; forensic analysis is consumer responsibility
- **Regulatory Compliance Certification**: This document is a capability roadmap, not a formal certification. Legal review is required before deployment.

## Compliance by Phase

| Phase | Closed Requirements | Partial Coverage | Open |
|-------|---|---|---|
| **1: Docs** | — | — | — |
| **2: RBAC** | FIPS AS10.03 (Roles/SoD), PCI-DSS 7.1, FedRAMP AC-6 | IA-2 (baseline mTLS) | AS10.05/06, AS10.10, audit, factors |
| **3: Auth Factors** | FIPS AS10.27, HIPAA 164.312(a)(2)(i) (baseline) | FedRAMP IA-2 (enhanced) | Full auth lifecycle, ceremony |
| **4: Ceremony + Custodian** | FIPS AS10.05 (split knowledge), AS10.06 (dual auth), AS10.19 (share delivery), FedRAMP SI-12 | — | Audit, PKCS#11 strategy, FIPS KDF |
| **5: PKCS#11 + FIPS KDF** | FIPS 140-3 Level 2 (with HSM), PCI-DSS 3.2.1, HIPAA 164.312(a)(2)(i) (HSM-backed) | — | Audit, full FedRAMP, post-incident forensics |
| **6: Audit Logging** | FIPS AS10.10/11, HIPAA 164.412, PCI-DSS 10.2.x, FedRAMP AU-2/12 | — | Certification, SIEM integration |

## Verification & Testing

Each phase includes compliance verification:

- **Unit tests**: Role matrix conformance, permission denial, algorithm selection
- **Integration tests**: End-to-end ceremony with audit trail; custodian unseal with share envelope delivery; PKCS#11 operations
- **Compliance tests**: Table-driven: for each (role, resource, action), assert allowed/denied; every FIPS/PCI/HIPAA event is emitted
- **Manual verification**: Chrome GUI — verify role-based UI filtering, Auditor can see logs, SO cannot access crypto endpoints

## See Also

- [fips140-mode.md](fips140-mode.md) — The `GODEBUG=fips140=only` CI job, the approved-algorithm remediation, the exemption register, what remains permanently unavailable under strict enforcement, and why the job is measured-clean but not yet a required check
- [roles.md](../auth/roles.md) — Canonical six-role definitions and SoD constraints
- [enabling-encryption.md](enabling-encryption.md) — Encryption-at-rest lifecycle: why disabling encryption is not a casual toggle, enabling it on an existing `none` deployment, and encrypted-backup recovery
- [shamir-custody](../shamir-custody/) — share holders, envelope delivery and the authenticated quorum
- [auth-factors.md](../seal/auth-factors.md) — `Factor` framework + `FactorRegistry` (hardware factor implementations ship in go-xkms)
- [audit/README.md](../audit/architecture.md) — Audit event schema and retrieval API
- [FIPS 140-3 ISO/IEC 19790](https://csrc.nist.gov/publications/detail/fips/140/3) — Official standard
- [NIST SP 800-57 Part 2 §5](https://csrc.nist.gov/publications/detail/sp/800-57-part-2/final) — Key management best practices
- [PCI-DSS v4](https://www.pcisecuritystandards.org/documents/PCI-DSS-v4-0.pdf) — Payment Card Industry Security Standard
- [FedRAMP Moderate/High Baseline](https://www.fedramp.gov/documents-documents/) — Federal security requirements
- [HIPAA 45 CFR 164](https://www.ecfr.gov/current/title-45/section-164.312) — HIPAA Technical Safeguards
