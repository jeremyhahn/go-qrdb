<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# FIPS 140-3 Role Model

Role definitions and enforced separation of duties for go-qrdb, aligned with FIPS 140-3 AS10.03 (Module Roles, Services, and Authentication). go-qrdb is the foundation library for compliance-grade downstream systems (go-xkms, go-trusted-ca). Roles apply to barrier operations, ceremony enrollment, KV store access, tenant management, key lifecycle, and audit visibility.

## Overview

go-qrdb implements a six-role model that enforces separation of duties between module initialization, cryptographic operations, and auditing. The critical design constraint is that the Security Officer (SO) role **cannot** perform cryptographic operations (sign, encrypt, decrypt). This prevents a single compromised identity from both managing the module infrastructure and using it for sensitive operations, meeting FIPS 140-3 AS10.03 and PCI-DSS v4 Req 7.1 separation-of-duties requirements.

Unlike Vault's four-role model, go-qrdb splits Operator and User into distinct roles (Operator does maintenance; User is read-only crypto consumer) and adds Custodian as a separate tier for M-of-N unseal ceremonies, matching the xkms role architecture and the PKCS#11-extended two-role model.

## Roles

| Role | Abbreviation | Description |
|------|-------------|-------------|
| Security Officer | SO | Module initialization, PIN management, CA operations, unseal-share enrollment. **Cannot** sign/encrypt/decrypt. |
| Admin | Admin | Day-to-day key lifecycle (create, rotate, delete operational keys), tenant management, backup. |
| Operator | Operator | Operational key use, routine maintenance, barrier seal/unseal (except share-claim). |
| User | User | Cryptographic operations only (sign, encrypt, decrypt, verify). No key creation or deletion. |
| Auditor | Auditor | Read-only access to audit logs and system state. Cannot modify. |
| Custodian | Custodian | Shamir share holder. Can **only** submit `barrier:unseal-share`. |

## Role Details

### Security Officer (SO)

The SO performs all module initialization, administrative lifecycle operations, and dual-authentication governance. Per FIPS 140-3 AS10.03, the SO is **explicitly excluded** from cryptographic operations.

**Authentication:** Dual-factor — mTLS certificate (something you have) + SO PIN (something you know, never stored, verified in-band per request).

**Assigned during:** `qrdbd start` with M-of-N ceremony (or single-admin if threshold < 2).

**Responsibilities:**
- Initialize the barrier (set strategy, root key, Shamir parameters)
- Manage SO PIN (set/reset; different from User PIN)
- Generate Recovery Keys (offline escrow for disaster recovery)
- Rotate barrier root key or rekey shares
- Enroll/revoke custodians in M-of-N ceremony
- Create, revoke, and rotate CA certificates
- Create and delete system roles
- Access audit logs for operational oversight
- Emergency unlock via root token (reconstruction)

**Key constraint:** The SO role **cannot** call `keys:sign`, `keys:encrypt`, `keys:decrypt`, `keys:verify`, or `keys:wrap`. This is enforced at the RBAC layer with explicit deny-list checks per FIPS 140-3 AS10.03. Even if an SO role is erroneously granted crypto permissions, the authorizer rejects the request (defense-in-depth).

**Authorization:** Permission prefix `so:*` and explicit `barrier:*`, `certificate:*`, `audit:read`, `role:*`, `user:*` for management. **Denied** prefixes: `keys:sign`, `keys:encrypt`, `keys:decrypt`, `keys:verify`, `keys:wrap`, `keys:unwrap`.

### Admin

Admins manage operational keys and certificates but cannot initialize or re-initialize the module. This role is appropriate for team leads or senior engineers who provision keys for applications.

**Authentication:** mTLS certificate or JWT (platform-dependent; xkms extends with multi-factor).

**Responsibilities:**
- Generate operational keys (create, rotate)
- Delete keys they created (not infrastructure keys)
- Manage certificates (issue, revoke for end-user certs)
- Manage tenants (create, configure per-tenant isolation)
- Perform backups and replication
- Access audit logs for operational oversight

**Authorization:** `keys:*` (except `keys:delete` on infrastructure keys), `certificate:*`, `tenant:*`, `backup:*`, `audit:read`.

### Operator

Operators perform routine key operations and maintenance. They can use keys and perform system maintenance but cannot delete or modify key policies.

**Authentication:** mTLS certificate, JWT, or API key.

**Responsibilities:**
- Seal/unseal barrier (does NOT receive shares; different from Custodian)
- Rotate keys
- Perform crypto operations (sign, encrypt, decrypt) using existing keys
- Query key metadata
- Access audit logs

**Authorization:** `keys:sign`, `keys:encrypt`, `keys:decrypt`, `keys:verify`, `barrier:seal`, `barrier:unseal`, `audit:read`.

### User

Users perform cryptographic operations (sign, verify, encrypt, decrypt) using existing keys. They cannot create, delete, or modify keys.

**Authentication:** mTLS certificate, JWT, or API key (depending on application context).

**Responsibilities:**
- Sign data
- Verify signatures
- Encrypt data
- Decrypt data
- Query key metadata (public key only, not material)

**Authorization:** `keys:sign`, `keys:verify`, `keys:encrypt`, `keys:decrypt` (read-only key access).

### Auditor

Auditors have read-only access to audit logs, system state, and configuration. They cannot modify any state.

**Authentication:** mTLS certificate or JWT.

**Responsibilities:**
- View audit event logs (with filtering and full-text search)
- Query system health and metrics
- List users, roles, keys, certificates (metadata only)

**Authorization:** `audit:read`, `system:read`, with no write/delete permissions.

**Guarantee:** Auditor role has no capability to delete or modify audit events. Audit trail integrity is enforced at the storage layer.

### Custodian

Custodians hold one Shamir share of the barrier root key. Their only operational capability is submitting their share during barrier unseal. They have no access to keys, certificates, audit logs, or system configuration. Recovery Key Holders are a **separate, offline tier** — not persisted in the qrdb roles table. See [custodians.md](../seal/custodians.md) for the two-tier share model.

**Authentication:** Dual-factor — mTLS certificate (issued during ceremony) + private key held by an auth factor (PIV card, PKCS#11 token, FIDO2 authenticator, or software mTLS for low-assurance deployments). Challenge-response verification at unseal time.

**Assigned during:** SO-driven enrollment in M-of-N ceremony. Each custodian is paired with an auth factor looked up by name in the `authfactors.FactorRegistry` injected via `WithFactorRegistry`. qrdb ships the `Factor` framework and the software mTLS factor; the hardware factor implementations (FIDO2, PIV, PKCS#11) live in go-xkms.

**Responsibilities:**
- Store their Shamir share securely (encrypted, in hardware factor or local file)
- Participate in unseal quorum when barrier is sealed
- Sign challenge during quorum unseal to prove possession of share

**Key constraint:** Custodian role can **only** submit `barrier:unseal-share`. Any other operation is denied.

**Authorization:** Single permission: `barrier:unseal-share`. All other resource access is explicitly denied.

## FIPS 140-3 Role-to-Operation Matrix

The following matrix defines which roles can perform each operation. This is enforced by the RBAC middleware on all API endpoints (REST, gRPC, MCP, QUIC, Unix socket).

```
                                        SO    Admin  Operator  User  Auditor  Custodian
BARRIER OPERATIONS
  Initialize barrier                    YES   --     --        --    --       --
  Seal barrier                          YES   YES    YES       --    --       --
  Unseal barrier (master secret)        YES   YES    YES       --    --       --
  Unseal with share (quorum)            --    --     --        --    --       YES
  Initialize Shamir shares              YES   --     --        --    --       --
  Rotate barrier root key               YES   --     --        --    --       --
  Rekey shares                          YES   --     --        --    --       --
  Generate recovery keys                YES   --     --        --    --       --
  Recover with recovery keys            YES   YES    --        --    --       --
  Barrier status                        YES   YES    YES       --    YES      --

CEREMONY OPERATIONS
  Initialize ceremony                   YES   --     --        --    --       --
  Enroll custodian (SO-driven)          YES   --     --        --    --       --
  Claim cert (enrolled officer)         YES   --     --        --    --       --
  Claim share (enrolled custodian)      --    --     --        --    --       YES
  Sign CSR (for new certs)              YES   --     --        --    --       --
  Revoke custodian                      YES   --     --        --    --       --
  Ceremony status                       YES   YES    YES       --    YES      --

PIN MANAGEMENT (delegated to pin.PINBackend; see docs/pin/README.md)
  Set/reset SO PIN                      YES   --     --        --    --       --
  Reset User PIN                        YES   YES    --        --    --       --
  Change own password                   --    YES    YES       YES   --       --

KEY LIFECYCLE
  Generate infrastructure keys          YES   --     --        --    --       --
  Generate operational keys             --    YES    YES       --    --       --
  Delete any key                        YES   --     --        --    --       --
  Delete own keys                       --    YES    YES       --    --       --
  Rotate keys                           --    YES    YES       --    --       --
  List keys                             --    YES    YES       YES   YES      --
  Get key metadata                      --    YES    YES       YES   YES      --

CRYPTO OPERATIONS (SO explicitly denied)
  Sign data                             ✗✗   --     YES       YES   --       --
  Verify signature                      ✗✗   --     YES       YES   --       --
  Encrypt data                          ✗✗   --     YES       YES   --       --
  Decrypt data                          ✗✗   --     YES       YES   --       --
  Wrap/unwrap keys                      ✗✗   YES    YES       --    --       --

CERTIFICATE MANAGEMENT
  Create CA                             YES   --     --        --    --       --
  Issue certificates                    YES   YES    --        --    --       --
  Revoke certificates                   YES   YES    --        --    --       --
  Store/delete certificates             YES   YES    YES       --    --       --
  List certificates                     --    YES    YES       YES   YES      --

TENANT MANAGEMENT
  Create/delete tenants                 YES   YES    --        --    --       --
  List tenants                          YES   YES    YES       --    YES      --
  Assign backend to tenant              YES   YES    --        --    --       --
  Configure tenant barriers             YES   YES    --        --    --       --

ROLE & USER MANAGEMENT
  Create roles                          YES   --     --        --    --       --
  Delete roles                          YES   --     --        --    --       --
  Modify role permissions               YES   --     --        --    --       --
  Create users                          YES   YES    --        --    --       --
  Delete users                          YES   YES    --        --    --       --
  Assign roles to users                 YES   YES    --        --    --       --
  List users                            YES   YES    YES       --    YES      --
  List roles                            YES   YES    YES       --    YES      --

AUDIT & MONITORING
  Query audit events                    --    --     --        --    YES      --
  View system health                    YES   YES    YES       YES   YES      --
  View metrics                          YES   YES    YES       --    YES      --

Legend: YES = permitted, -- = denied, ✗✗ = explicitly denied even with erroneous permission grant
```

## Authentication Requirements

### Dual Authentication (SO)

The SO role requires two-factor authentication for all operations:

1. **mTLS client certificate** — proves possession of the SO's private key
2. **SO PIN** — proves knowledge of the PIN set during initialization

Both factors must be present on every SO-authenticated request. The mTLS certificate is verified at the TLS layer. The SO PIN is verified in-band on each request via the `X-QRDB-SO-PIN` HTTP header (REST) or gRPC metadata (gRPC), never stored, and always transmitted over TLS 1.3.

Example REST request (SO changing User PIN):

```bash
curl -X POST https://localhost:8443/v1/barrier/pin/reset \
  --cert so.pem \
  --key so-key.pem \
  -H "X-QRDB-SO-PIN: <PIN>" \
  -H "Content-Type: application/json" \
  -d '{"new_pin": "new-pin-value"}'
```

### Factor-Authenticated Custodian

During ceremony enrollment, the SO provides the custodian with an authentication factor enrollment (public credential + metadata). At unseal time, the custodian proves possession of their private key via challenge-response:

1. **Custodian authenticates** with their mTLS cert (issued during ceremony)
2. **Server issues a challenge** (cryptographic nonce)
3. **Custodian signs the challenge** with their private key held in the authentication factor (PIV card, PKCS#11 token, FIDO2 authenticator, or software mTLS). The signing path is dispatched through the `authfactors.FactorRegistry`; concrete hardware implementations ship in go-xkms.
4. **Server verifies the signature** and, if valid, returns the sealed share envelope (ciphertext of the Shamir share, encrypted to the custodian's enrollment public key)
5. **Custodian-side software decrypts the share locally** (private key never leaves the hardware factor)
6. **Custodian submits the plaintext share** to `barrier:unseal-share` for quorum accumulation

See [custodians.md](../seal/custodians.md) for the full share-delivery protocol.

### Standard Authentication (Other Roles)

Non-SO roles authenticate via:
- **mTLS client certificate** (mutual TLS)
- **JWT token** (issued by external OIDC provider, platform-dependent)
- **API key** (for service-to-service; platform-dependent)

The authentication adapter is configured at deployment time and is platform-specific (xkms may add HSM-backed mTLS cert chains, cloud deployments may use OAuth2, etc.).

## Separation of Duties

The role model enforces several separation-of-duties constraints required by FIPS 140-3 AS10.03 and PCI-DSS v4 Req 7.1:

### SO Cannot Sign/Encrypt/Decrypt (FIPS 140-3 AS10.03)

This is the fundamental constraint. The SO manages the module lifecycle and key infrastructure but cannot use keys for cryptographic operations. This prevents:

- An SO from signing arbitrary data with production keys (data exfiltration vector)
- An SO from decrypting sensitive data
- A compromised SO credential from being used for unauthorized cryptographic operations
- Accidental misuse of administrative credentials for user-level operations

Enforcement is **dual-layer**: RBAC denies the permission at request-time, and the service layer re-validates with an explicit deny-list check.

### Custodians Have Minimal Access (FIPS 140-3 AS10.06)

Custodians can only submit Shamir shares during unseal. They cannot view keys, certificates, audit logs, users, tenants, or perform any other operation. This limits the attack surface of compromised custodian credentials. A custodian compromise:

- Does NOT grant access to plaintext keys or data
- Does NOT allow quorum unseal by a single individual (requires threshold M of N)
- Does NOT persist the Shamir share on the server (encrypted envelope returned to custodian; server zeros the plaintext)

### Auditors Are Read-Only (FIPS 140-3 AS10.10)

Auditors can observe but not modify. This ensures audit trail integrity — an auditor cannot cover their tracks by deleting logs or modifying system state. Enforced via permission grants and RBAC deny-all-writes policy.

### Key Deletion Requires SO (FIPS 140-3 AS10.03)

Only the SO can delete arbitrary keys (infrastructure keys). Admins and Operators can delete keys they created. This prevents accidental or malicious destruction of critical keys by lower-privilege users.

### Split Knowledge for Unseal (FIPS 140-3 AS10.05, AS10.06)

Shamir M-of-N unsealing embodies the split-knowledge principle: no single Custodian holds the complete root key. To unseal the barrier, M custodians must participate, and each must prove possession of their private key (via challenge-response). This prevents:

- Single-point unseals (if M > 1)
- Offline unseal attacks (each custodian must be online and responsive)
- Replay attacks (nonce-based challenge-response prevents sharing a signed response)

## Mapping to PKCS#11 Roles

The go-qrdb role model extends the PKCS#11 two-role model (SO + User) to support finer-grained access control and audit compliance:

| PKCS#11 Role | go-qrdb Roles | Notes |
|--------------|---------------|-------|
| SO | SO | Module initialization, PIN management. Cannot perform crypto. |
| User | Admin, Operator, User | Subdivided for finer-grained access control. |
| N/A | Auditor | go-qrdb extension for compliance (HIPAA, PCI-DSS, FedRAMP require audit read-access). |
| N/A | Custodian | go-qrdb extension for M-of-N operations (FIPS 140-3 AS10.06 split knowledge). |

When qrdb is deployed as a PKCS#11 library, the SO role maps to `CKU_SO` user type, and Admin/Operator/User map to `CKU_USER`. The Auditor and Custodian roles exist at the qrdb application layer (not PKCS#11 protocol layer).

## Mapping to Existing qrdb RBAC

The role model integrates with qrdb's existing RBAC system (`pkg/role/`, `pkg/auth/rbac/`):

| Role | RBAC Permissions | Key Differences |
|------|------------------|-----------------|
| SO | `so:*`, `barrier:*`, `certificate:*`, `audit:read`, `role:*`, `user:*` | Explicitly **denied** `keys:sign`, `keys:encrypt`, `keys:decrypt`, `keys:verify`, `keys:wrap`, `keys:unwrap`. Dual-factor auth. |
| Admin | `keys:*` (except delete infrastructure), `certificate:*`, `tenant:*`, `backup:*`, `audit:read` | No barrier init, no SO PIN reset. |
| Operator | `keys:sign`, `keys:encrypt`, `keys:decrypt`, `keys:verify`, `barrier:seal`, `barrier:unseal`, `audit:read` | No key creation/deletion, no SO operations. |
| User | `keys:sign`, `keys:verify`, `keys:encrypt`, `keys:decrypt` | Minimal: crypto-only. No key metadata beyond public fields. |
| Auditor | `audit:read`, `system:read`, `*:list`, `*:get-metadata` | Read-only everywhere. |
| Custodian | `barrier:unseal-share` | Single permission. Everything else denied. |

## Bootstrap and Role Lifecycle

### Role Bootstrap (Deterministic)

On daemon initialization (`qrdbd start`), the six system roles are created deterministically and idempotently:

- Each role is assigned a fixed UUID v5 derived from its name (e.g., `uuid.NewSHA1(UUID_NAMESPACE_DNS, "go-qrdb/role/SO")`)
- All six roles are marked as **system roles** (undeletable)
- Roles are persisted in the KV store at `__sys:role/<id>` (Raft-replicated)
- If roles already exist (restart scenario), the function returns immediately without duplication

See [bootstrap.md](../bootstrap.md) for the full bootstrap sequence.

### Role Assignment and Revocation

Roles are assigned to users via `pkg/auth/user.go` role assignment (many-to-many: a user can hold multiple roles). Only SO and Admin can create/delete/modify roles and user-role assignments.

## Compliance Mapping

| Standard | Requirement | Coverage |
|----------|-------------|----------|
| FIPS 140-3 | AS10.03 (Roles, Services, Authentication) | Six-role model, SO separation of duties, explicit deny-lists |
| FIPS 140-3 | AS10.05 (Split Knowledge) | Shamir M-of-N unseal — `pkg/seal/shamir.go` + custodian lifecycle |
| FIPS 140-3 | AS10.06 (Dual Authorization) | SO dual-factor (mTLS + PIN), Custodian challenge-response |
| PCI-DSS v4 | Req 7.1 (Role-Based Access Control) | Fine-grained role model, permission matrix, deny-lists |
| FedRAMP Moderate | AC-6 (Least Privilege) | Role-based access, explicit deny-lists, read-only Auditor |
| HIPAA | 164.312(a)(1)(ii)(A) (Access Control) | Role-based access control with audit log visibility |

## See Also

- [xkms docs/init/roles.md](https://github.com/jeremyhahn/go-xkms/blob/main/docs/init/roles.md) — Extended xkms role model with hardware attestation
- [compliance.md](../seal/compliance.md) — Per-framework requirement cross-reference
- [custodians.md](../seal/custodians.md) — Custodian lifecycle and two-tier share model
- [bootstrap.md](../bootstrap.md) — Deterministic role initialization
- [FIPS 140-3 ISO/IEC 19790](https://csrc.nist.gov/publications/detail/fips/140/3) — AS10.03, AS10.05, AS10.06, AS10.09, AS10.10
- [PCI-DSS v4](https://www.pcisecuritystandards.org/documents/PCI-DSS-v4-0.pdf) — Requirement 7.1, 3.2.x, 3.6.x, 10.2.x
