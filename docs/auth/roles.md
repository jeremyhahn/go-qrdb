# FIPS 140-3 Role Model

> **Status: REFERENCE** · Scope: go-qrdb · Last verified: 2026-10-26
> Companion reference; the feature folders under `docs/` are the contract and win on any disagreement.

<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

Role definitions and enforced separation of duties for go-qrdb, aligned with FIPS 140-3 AS10.03 (Module Roles, Services, and Authentication). go-qrdb is the foundation library for compliance-grade downstream systems (go-xkms). Roles apply to barrier operations, ceremony enrollment, KV store access, tenant management, key lifecycle, and audit visibility.

## Overview

go-qrdb implements a five-role model that enforces separation of duties between module initialization, cryptographic operations, and auditing. The critical design constraint is that the Security Officer (SO) role **cannot** perform cryptographic operations (sign, encrypt, decrypt). This prevents a single compromised identity from both managing the module infrastructure and using it for sensitive operations, meeting FIPS 140-3 AS10.03 and PCI-DSS v4 Req 7.1 separation-of-duties requirements.

go-qrdb splits Operator and User into distinct roles (Operator does maintenance; User is read-only crypto consumer) matching the xkms role architecture and the PKCS#11-extended two-role model. Key custodians, who hold Shamir shares for M-of-N unseal ceremonies, are share holders rather than an RBAC role: see [Key custodians](#key-custodians).

## Roles

| Role | Abbreviation | Description |
|------|-------------|-------------|
| Security Officer | SO | Module initialization, PIN management, CA operations, share-holder enrolment and revocation. **Cannot** sign/encrypt/decrypt. |
| Admin | Admin | Day-to-day key lifecycle (create, rotate, delete keys), tenant management, backup. |
| Operator | Operator | Operational key use, routine maintenance, barrier seal/unseal (except share-claim). |
| User | User | Cryptographic operations only (sign, encrypt, decrypt, verify). No key creation or deletion. |
| Auditor | Auditor | Read-only access to audit logs and system state. Cannot modify. |

## Role Details

### Security Officer (SO)

The SO performs all module initialization, administrative lifecycle operations, and dual-authentication governance. Per FIPS 140-3 AS10.03, the SO is **explicitly excluded** from cryptographic operations.

**Authentication:** Dual-factor — mTLS certificate (something you have) + SO PIN (something you know, kept only as a hash commitment, verified in-band on each SO-PIN-gated request; see [Dual Authentication (SO)](#dual-authentication-so)).

**Assigned during:** `qrdbd start` with M-of-N ceremony (or single-admin if threshold < 2).

**Responsibilities:**
- Initialize the barrier (set strategy, root key, Shamir parameters)
- Manage PINs (set, change, reset; `pin:write`)
- Generate Recovery Keys (offline escrow for disaster recovery)
- Rotate barrier root key or rekey shares
- Enroll/revoke custodians in M-of-N ceremony
- Create, revoke, and rotate CA certificates
- Create and delete system roles
- Access audit logs for operational oversight
- Emergency unlock via root token (reconstruction)

**Key constraint:** The SO role **cannot** perform a cryptographic operation. That is not a `keys:*` rule: it covers every permission declared `DutyCryptoUse` (`keys:sign`, `keys:verify`, `keys:encrypt`, `keys:decrypt`, `keys:wrap`, `keys:unwrap`, `keys:derive`, `piv:use`, `dek:encrypt`, `dek:decrypt`, `dek:rewrap`) and every resource whose action names a cryptographic primitive (`sign`, `encrypt`, `decrypt`, `wrap`, `unwrap`, `derive`, `kdf`, `mac`, `hmac`, `attest`, `quote`) -- including resources contributed by a higher layer, such as go-quicbaas's `attestation:attest`, which extends a PCR on the host TPM and returns a signed quote. Enforcement is dual-layer per FIPS 140-3 AS10.03: `permission.RegisterRoleGrant` refuses the **grant** at registration time, and the RBAC deny-list refuses the **request** at authorization time even if a grant reached a role by some other path (defense-in-depth). See [SO Cannot Sign/Encrypt/Decrypt](#so-cannot-signencryptdecrypt-fips-140-3-as1003).

**Authorization:** SO is granted `seal:*` (bootstrap, unseal, seal, status, rotate_root_key, rekey, generate_recovery, initialize_shamir, officer_register, officer_list, officer_revoke, store_manage), `certs:*` (issue, enroll, delegate, revoke, list, read), `trust_anchors:*` (add, list, remove), `roles:*` (create, read, update, delete, list, assign, unassign), `permissions:list`, `users:*` (create, read, update, delete, list, enable, disable, invite, suspend, unsuspend), `tenants:*` (create, read, update, delete, list, set_status, set_entitlements, set_isolation), `keys:create`, `keys:delete`, `keys:import`, `keys:export`, `keys:list`, `keys:get`, `escrow:*` (create, list, recover, revoke), `ceremony:*` (initialize, sign_csr, status), `ca:*` (create, read, list, update), `acme_accounts:*` (read, list, disable), `acme_orders:*` (read, list), `acme_eab:*` (read, list, create, delete), `audit:read`, `audit:query`, `audit:delete`, `audit:verify`, `audit:verify_replicated`, `admin:*` (health, status, peers, add_member, remove_member, snapshot, drain, capabilities), `backends:*` (list, read), `pin:*` (read, write), `piv:*` (read, write, delete), `authfactor:*` (enroll, list, revoke). **Denied**: every cryptographic operation -- `keys:sign`, `keys:encrypt`, `keys:decrypt`, `keys:verify`, `keys:wrap`, `keys:unwrap`, `keys:derive`, `piv:use`, `dek:encrypt`, `dek:decrypt`, `dek:rewrap`, and any `resource:action` whose action is a cryptographic primitive. Note that `seal:unseal`, `seal:seal`, `seal:rekey`, `ceremony:sign_csr` and `certs:issue` are cryptographic by mechanism but are granted to the SO on purpose: they are module lifecycle and issuance governance, the duties FIPS 140-3 assigns to the Security Officer, not use of a key on a requester's behalf.

### Admin

Admins manage keys and certificates but cannot initialize or re-initialize the module. This role is appropriate for team leads or senior engineers who provision keys for applications.

**Authentication:** mTLS certificate or JWT (platform-dependent; xkms extends with multi-factor).

**Responsibilities:**
- Generate keys (create, rotate)
- Delete keys
- Manage certificates (issue, revoke for end-user certs)
- Manage their own tenant's custom roles (create, update, assign; not delete), and reconcile its built-in roles. The built-in roles keep reserved names and fixed grants, and another tenant's roles, or the `system` tenant's, are out of reach
- Manage their own tenant (read, rename, delete it; list it). Creating tenants and setting a tenant's plan -- quotas, entitlements, isolation tier -- is reserved to Admin in the `system` tenant (the platform operator); see [Tenant plan permissions](#tenant-plan-permissions-are-platform-operator-only)
- Perform backups and replication
- Access audit logs for operational oversight

**Authorization:** `keys:*` (create, rotate, delete, wrap, unwrap, import, export, list, get), `escrow:*` (create, list, recover, revoke), `dek:*` (rotate, retire, rewrap, status), `certs:*` (issue, enroll, delegate, revoke, list, read; and, cross-scope, revoke:cross), `trust_anchors:*` (add, list, remove), `tenants:*` (read, update, delete, list; and, cross-scope, create, set_quotas, set_entitlements, set_isolation, set_status, read:cross, update:cross, delete:cross, list:cross, create:cross, approve, list_deleted, reconcile_deleted), `admin:*` (health, status, peers, capabilities; and, cross-scope, add_member, remove_member, snapshot, drain, node_compaction, state_hash), `audit:read`, `audit:query`, `audit:delete`, `seal:unseal`, `seal:seal`, `seal:status`, `seal:officer_register`, `seal:officer_list`, `seal:officer_revoke`, `ceremony:status`, `tenant_barrier:*` (init, unseal, seal, status, enable_encryption, officer_register, officer_list, officer_revoke, rekey), `backends:*` (list, read), `pin:*` (read, write), `piv:*` (read, write, delete), `passwords:*` (read, write, delete, generate, lock, unlock), `ssh:*` (read, write, delete), `users:*` (create, read, update, delete, list, enable, disable, invite, suspend, unsuspend, add_cert_binding), `roles:create`, `roles:read`, `roles:update`, `roles:list`, `roles:assign`, `roles:unassign` (in its own tenant; `roles:delete` stays with the SO), `permissions:list`, `abac_policies:*` (create, read, update, delete, list), `mvcc:restore`, the database lifecycle in its own tenant (`databases:create`, `databases:read`, `databases:list`, `databases:update`, `databases:delete`, `databases:create_raft`, `databases:create_volume`, `databases:scale`, `databases:barrier_status`, `databases:seal`, `databases:unseal`; no database key custody), `ca:*` (create, read, list, update), `acme:read`, `acme:write`, `acme_accounts:*` (read, list, disable), `acme_orders:*` (read, list), `acme_eab:*` (read, list, create, delete), `secrets:*` (read, write, list, delete), `trust:read`, `trust:write`, `devices:onboard`, `ota:activate`, `ota:confirm`, the broker families as wildcards (`objstore:*`, `orchestrator:*`, `scheduler:*`, `messaging:*`, `parameters:*`, `dynamic_secrets:*`, `project:*`, `ollama:*`), and, cross-scope, `volume_pools:*` (create, read, update, delete, list), `registration_config:read`, `registration_config:update` and `cluster:join_token` (cross-scope — mints a member-side CSR join token; effective only for the `system` tenant's Admin, see [Cluster-membership permissions are platform-operator only](#cluster-membership-permissions-are-platform-operator-only)).

### Operator

Operators perform routine key operations and maintenance. They can use keys and perform system maintenance but cannot delete or modify key policies.

**Authentication:** mTLS certificate, JWT, or API key.

**Responsibilities:**
- Seal/unseal barrier (does NOT receive shares; different from Custodian)
- Rotate keys
- Perform crypto operations (sign, encrypt, decrypt) using existing keys
- Query key metadata
- Access audit logs

**Authorization:** `keys:sign`, `keys:verify`, `keys:encrypt`, `keys:decrypt`, `keys:rotate`, `keys:derive`, `keys:list`, `keys:get`, `seal:unseal`, `seal:seal`, `seal:status`, `ceremony:status`, `audit:read`, `kv:*` (put, get, delete, list, exists, range), `mvcc:*` (put_with_lease, get_revision, get_at_revision, range, compact, current_revision, restore), `transaction:execute`, `batch:execute`, `watch:*` (watch, watch_prefix), `lease:*` (grant, revoke, keepalive, ttl, list), `index:*` (create, drop, query, scan), `timeseries:*` (write, write_batch, query, aggregate), `vector:*` (namespace_create, namespace_delete, namespace_list, namespace_info, upsert, get, delete, batch_upsert, batch_delete, list, update_metadata, search_similar, search_by_metadata, hybrid_search, search_by_id, rebuild_index, get_rebuild_progress, get_index_stats, compact, get_compaction_progress), `admin:health`, `admin:status`, `backends:*` (list, read), `pin:read`, `piv:read`, `piv:use`, `passwords:*` (read, write, delete, generate, lock, unlock), `ssh:*` (read, write, delete), `dek:encrypt`, `dek:decrypt`, `dek:rewrap`, `dek:status`, `ca:read`, `ca:list`, `acme_accounts:read`, `acme_accounts:list`, `acme_orders:read`, `acme_orders:list`, `acme_eab:read`, `acme_eab:list`, broker data-plane families (objstore, orchestrator, scheduler, messaging, parameters, dynamic_secrets, project, ollama).

### User

Users perform cryptographic operations (sign, verify, encrypt, decrypt) using existing keys. They cannot create, delete, or modify keys.

**Authentication:** mTLS certificate, JWT, or API key (depending on application context).

**Responsibilities:**
- Sign data
- Verify signatures
- Encrypt data
- Decrypt data
- Query key metadata (public key only, not material)
- Use, change and remove their own passwords

**Authorization:** `keys:sign`, `keys:verify`, `keys:encrypt`, `keys:decrypt`, `keys:derive`, `keys:get`, `keys:list` (read-only key access), `piv:use`, `dek:encrypt`, `dek:decrypt`, `admin:health`, `admin:status`, an orchestrator subset (status, list, health, inference, prompt, spawn, close, task_create, task_decompose, task_read, task_list, job_start, job_read, job_list, job_cancel, workflow_read, artifact_read), `passwords:read`, `passwords:write`, `passwords:delete`, `passwords:lock`, `passwords:unlock` (self-scoped: confined to the caller's tenant by the authorizer, and to the caller's own entries by the password service). No `passwords:generate`.

### Auditor

Auditors have read-only access to audit logs, system state, and configuration. They cannot modify any state.

**Authentication:** mTLS certificate or JWT.

**Responsibilities:**
- View audit event logs (with filtering and full-text search)
- Query system health and metrics
- List users, roles, keys, certificates (metadata only)

**Authorization:** `audit:read`, `audit:query`, `audit:stream`, `audit:verify`, `audit:verify_replicated`, `admin:health`, `admin:status`, `admin:peers`, `users:list`, `users:read`, `roles:list`, `roles:read`, `permissions:list`, `certs:list`, `certs:read`, `backends:list`, `backends:read`, `pin:read`, `piv:read`, `passwords:read`, `tenants:list`, `tenants:read`, `keys:list`, `keys:get`, `escrow:list`, `ceremony:status`, `authfactor:list`, `ca:read`, `ca:list`, `acme_accounts:read`, `acme_accounts:list`, `acme_orders:read`, `acme_orders:list`, `acme_eab:read`, `acme_eab:list`, and orchestrator read-only verbs (status, list, task_read, task_list, job_read, job_list, workflow_read, agent_read, agent_list, backend_list, health). No write/delete permissions.

**Guarantee:** Auditor role has no capability to delete or modify audit events. Audit trail integrity is enforced at the storage layer.

### Key custodians

A key custodian holds one Shamir share of a barrier key, exactly as a Security
Officer does, but carries no governance rights. Custodians and Security
Officers are both **share holders**: enrolled the same way, claiming their
sealed envelopes the same way, and submitting their shares to the same
authenticated quorum. See [../shamir-custody/](../shamir-custody/).

A custodian is **not an RBAC role**, and there is no custodian role to assign.
The holder role (`custodian` or `so`) is recorded in the holder roster at
enrolment, by an Admin or a Security Officer (never by the holder), in the
initialization ceremony or with `qrdb barrier officer register --role custodian`.

**Authentication:** a CA-issued holder certificate --
`CN=<username>`, `OU=[<tenant>, <profile>]` with a holder profile
(`security-officer` or `custodian`), no SAN -- which authenticates with no
roles and is refused every RBAC permission, plus the holder's enrolled factors: `mtls` (possession of the
certificate key) and any factor the composer registered through
`WithFactorRegistry` (FIDO2, PIV, PKCS#11 — go-xkms implementations). Every
share submission carries a factor proof checked against the holder roster.

**Responsibilities:**
- Claim their sealed envelope once and keep it
- Take part in quorum rounds (unseal, seal, root token, rekey) when asked

**Key constraint:** a holder identity is refused as a login identity; the
holder routes are identity-gated and authorize the holder themselves, so a
custodian holds no RBAC permission at all.

## FIPS 140-3 Role-to-Operation Matrix

The following matrix defines which roles can perform each operation. This is enforced by the RBAC middleware on all API endpoints (REST, gRPC, MCP, QUIC, Unix socket).

```
                                        SO    Admin  Operator  User  Auditor
BARRIER OPERATIONS
  Initialize barrier                    YES   --     --        --    --
  Seal barrier                          YES   YES    YES       --    --
  Unseal barrier (master secret)        YES   YES    YES       --    --
  Initialize Shamir custody             YES   --     --        --    --
  Rotate barrier root key               YES   --     --        --    --
  Start a rekey (quorum completes it)   YES   --     --        --    --
  Enrol / revoke share holders          YES   YES    --        --    --
  Generate recovery keys                YES   --     --        --    --
  Barrier status                        YES   YES    YES       --    --

  Unseal, seal, root token, recovery and rekey completion on a Shamir barrier
  are not role permissions: each needs a quorum of authenticated share holders
  (Security Officers and custodians), whatever their RBAC role.

CEREMONY OPERATIONS
  Initialize ceremony                   YES   --     --        --    --
  Sign CSR (for new certs)              YES   --     --        --    --
  Ceremony status                       YES   YES    YES       --    YES

PIN MANAGEMENT (delegated to pin.PINBackend)
  Set/change/reset PINs                 YES   YES    --        --    --
  Read PIN state                        YES   YES    YES       --    YES
  Change own password                   --    YES    YES       YES   --

KEY LIFECYCLE
  Create keys                           YES   YES    --        --    --
  Delete keys                           YES   YES    --        --    --
  Import/export keys                    YES   YES    --        --    --
  Rotate keys                           --    YES    YES       --    --
  List keys                             YES   YES    YES       YES   YES
  Get key metadata                      YES   YES    YES       YES   YES

CRYPTO OPERATIONS (SO explicitly denied)
  Sign data                             ✗✗   --     YES       YES   --
  Verify signature                      ✗✗   --     YES       YES   --
  Encrypt data                          ✗✗   --     YES       YES   --
  Decrypt data                          ✗✗   --     YES       YES   --
  Wrap/unwrap keys                      ✗✗   YES    --        --    --

CERTIFICATE MANAGEMENT
  Create CA                             YES   YES    --        --    --
  Issue certificates                    YES   YES    --        --    --
  Revoke certificates                   YES   YES    --        --    --
  List certificates                     YES   YES    --        --    YES

TENANT MANAGEMENT
  Create/delete tenants                 YES   YES    --        --    --
  List tenants                          YES   YES    --        --    YES
  Configure tenant barriers             --    YES    --        --    --

ROLE & USER MANAGEMENT
  Create roles                          YES   YES    --        --    --
  Delete roles                          YES   --     --        --    --
  Modify role permissions               YES   YES    --        --    --
  Create users                          YES   YES    --        --    --
  Delete users                          YES   YES    --        --    --
  Assign roles to users                 YES   YES    --        --    --
  List users                            YES   YES    --        --    YES
  List roles                            YES   YES    --        --    YES

AUDIT & MONITORING
  Query audit events                    YES   YES    --        --    YES
  View system health                    YES   YES    YES       YES   YES
  View system status                    YES   YES    YES       YES   YES

Legend: YES = permitted, -- = denied, ✗✗ = explicitly denied even with erroneous permission grant
```

## Authentication Requirements

### Dual Authentication (SO)

With `auth.so_dual_factor` set and an SO authenticator wired (`server.WithSOAuthenticator`), the barrier and ceremony operations in `authmw.SOPermissions` -- `ceremony:initialize`, `ceremony:sign_csr`, `seal:rotate_root_key`, `seal:rekey`, `seal:generate_recovery` and `seal:initialize_shamir` -- require two factors:

1. **mTLS client certificate** — proves possession of the SO's private key
2. **SO PIN** — proves knowledge of the PIN set during initialization

The mTLS certificate is verified at the TLS layer. The SO PIN is verified in-band on each such request and is kept only as a hash commitment (Argon2id, or PBKDF2-SHA256 in FIPS mode), with a per-tenant lockout on failed checks. It travels in the `X-QRDB-SO-PIN` header (REST, MCP), the `x-qrdb-so-pin` metadata key (gRPC, Unix socket), or the request message's `so_pin` field (QUIC). Other SO operations need the certificate alone.

### Share Holders

A share holder — Security Officer or custodian — authenticates for a quorum
submission, not a login:

1. **The holder asks for a challenge** for their designated (or, under the
   `any-enrolled` policy, any enrolled) factor.
2. **The holder answers it** — a signature with the certificate key for `mtls`,
   an assertion from the device for a hardware factor, dispatched through the
   `authfactors.FactorRegistry`.
3. **The holder opens their envelope locally** with their envelope key and
   submits the share with the factor response.
4. **The server verifies the response against the roster** the key is bound
   to, counts the holder once, and runs the operation at M.

See [../shamir-custody/architecture.md](../shamir-custody/architecture.md).

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

Enforcement is **dual-layer**, and both layers are structural:

1. **Registration time** -- `permission.RegisterRoleGrant` panics if the `so` role is granted a cryptographic permission (`SoDViolationError`), a permission whose separated duty nobody declared (`UndeclaredDutyError`), or a whole-segment wildcard (`WildcardGrantError`). The invariant cannot be violated by adding a bad grant, and cannot be side-stepped by adding an unclassified one -- from qrdb or from a higher layer through the `sdk/go` re-export.
2. **Request time** -- the RBAC deny-list (`soDeniedActions` + `soDeniedCryptoVerbs` in `pkg/auth/rbac/rbac.go`) rejects the request before permissions are resolved, so a grant that reached a role by another path (a hand-authored custom role named `so`, or a role store written by an earlier build) is still denied. The two mechanisms are independent implementations -- the request-time one classifies the `resource:action` itself and never consults the duty registry -- and a test proves they agree in both directions.

### Share Holders Have No RBAC Access (FIPS 140-3 AS10.06)

A key custodian is a share holder, not an RBAC principal. RBAC refuses a holder identity everything: it cannot view keys, certificates, audit logs, users or tenants, and its only acts — claiming its envelope and submitting its share to a quorum — are identity-gated. This limits the attack surface of compromised custodian credentials. A custodian compromise:

- Does NOT grant access to plaintext keys or data
- Does NOT allow quorum unseal by a single individual (requires threshold M of N)
- Does NOT persist the Shamir share on the server (encrypted envelope returned to custodian; server zeros the plaintext)

### Auditors Are Read-Only (FIPS 140-3 AS10.10)

Auditors can observe but not modify. This ensures audit trail integrity — an auditor cannot cover their tracks by deleting logs or modifying system state.

**Enforcement:** by the composition of the auditor grant list only (`registerAuditorGrants` in `pkg/permission/builtin_role_grants.go` lists read-only verbs, and the golden-set drift guard in `pkg/role/builtin_grants_equivalence_test.go` fails if it changes). There is **no** structural deny-all-writes rule for the auditor role: unlike the SO constraint above, nothing panics if a future contributor adds a write permission to the auditor role — the drift guard turns it into a deliberate review item rather than an impossibility. AS10.10 is therefore a maintained invariant, not an enforced one.

### Key Deletion Is Limited to SO and Admin (FIPS 140-3 AS10.03)

`keys:delete` is granted to the SO and to Admin, and to no other built-in role — an Operator can rotate a key but not destroy one, and User and Auditor cannot either. This limits destruction of critical keys to the two governance roles.

**Enforcement:** by the grant alone. `keys:delete` is a coarse permission: qrdb has no per-key ownership model, so there is no "delete only keys you created" check anywhere in this repo, and the key service that consumes the `keys:*` vocabulary lives in go-xkms. Treat this as a role-level constraint, not an object-level one.

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

The Auditor role exists at the qrdb application layer, not in PKCS#11; key custodians are share holders, not a role.

## Mapping to qrdb RBAC

The five roles are built-in roles of qrdb's RBAC system (`pkg/role/`, `pkg/auth/rbac/`), with the grants listed under [Role Details](#role-details). Beyond those lists:

- No built-in role holds `backup:*`: cluster backup and restore is granted to no built-in role.
- `mvcc:restore` -- selective point-in-time restore of a bounded key set -- is Admin's only `kv:*`/`mvcc:*` verb.
- Admin's `cluster:join_token` is the only built-in grant of that permission besides Owner, and the only one that survives strict separation of duties, which voids Owner.
- The Auditor holds no `seal:status` and no write or delete permission.
- Admin is the only one of the five roles that holds the database lifecycle, `databases:seal` and `databases:unseal` included, and the only one that holds `tenant_barrier:enable_encryption`; it holds them in its own tenant only. None of the five holds a database's key custody: `databases:rekey`, `databases:initialize_shamir`, `databases:officer_register`, `databases:officer_list`, `databases:officer_unenrol` or `databases:officer_revoke`. Under the [grant rule](../identity-rbac/architecture.md#the-grant-rule), a role carrying one of these can be created only by a principal that already holds it.

### Tenant plan permissions are platform-operator only

`tenants:create`, `tenants:set_quotas`, `tenants:set_entitlements` and
`tenants:set_isolation` are **cross-scope** (`ScopeCross`): the RBAC authorizer
admits them only for an identity in the `system` tenant, whatever tenant the
request names. The built-in Admin role carries them in every tenant, exactly as
it carries its other cross-scope grants (`tenants:list:cross`,
`volume_pools:*`, `registration_config:*`), and they are inert outside `system`.
So Admin in the `system` tenant creates tenants and sets their plans, and a
tenant's own Admin manages everything inside its tenant but cannot create
tenants, raise its own quotas, grant itself entitlements or change its own
isolation tier. The SO role's `tenants:create`, `tenants:set_isolation`,
`tenants:set_entitlements` and `tenants:set_status` grants are cross-scope for
the same reason.

The quotas routes check `tenants:set_quotas`, not `tenants:update`, and
`tenants:update` covers only a tenant's display name. `tenants:delete` stays
self-scoped: a tenant's Admin may delete its own tenant. See
[../multi-tenancy/api.md](../multi-tenancy/api.md#who-may-call-what).

### Node-barrier permissions are platform-operator only

The node barrier is a singleton: one per node, protecting every tenant's data,
owned by no tenant. Every permission that changes it or decides who holds a
share of its key is **cross-scope** (`ScopeCross`, declared in
`pkg/permission/node_barrier_scope.go`):

- `seal:bootstrap`, `seal:unseal`, `seal:seal`, `seal:rekey`,
  `seal:rotate_root_key`, `seal:initialize_shamir`, `seal:generate_recovery`,
  `seal:enable_encryption`, `seal:store_manage`;
- `seal:officer_register`, `seal:officer_list` and `seal:officer_revoke` --
  enrolling a share holder into the node barrier's unseal or recovery domain,
  listing those holders with their certificate thumbprints, and revoking one
  through a quorum-gated rekey;
- `ceremony:initialize` and `ceremony:sign_csr` -- the node key ceremony,
  whose CSR signing uses the cluster CA.

None of these routes names a tenant, so `ScopeSelf` would compare the caller's
tenant with itself and admit every holder: a tenant's SO -- which holds most of
them through its own tenant's built-in SO role -- could register holders into
the node barrier. Every rekey, revocation and
recovery-key generation sends a share to every registered holder, so M planted
holders could reconstruct the node unseal key or reach the root-token quorum.
The built-in roles still carry these grants in every tenant, and they are inert
outside `system`: the SO, Admin or Operator of the `system` tenant exercises
them, and a tenant's own SO, Admin or Operator is refused on every transport.

A tenant governs its own barrier through `tenant_barrier:*`, which is
`ScopeTenantOnly` and bound to the tenant the request names; its holders
submit shares on that barrier's identity-gated routes. `seal:status` and
`ceremony:status` stay `ScopeSelf`: status names no
share holder, and the claim verbs are bound to an enrollment the caller must
possess. Migrating any barrier to an encrypting strategy
(`seal:enable_encryption`) is a platform-operator act with no tenant-side
route. First-boot officer registration on the boot-time sealed-window listener
is anonymous and runs no RBAC at all, so it is unaffected.

### Cluster-membership permissions are platform-operator only

`admin:add_member`, `admin:remove_member`, `admin:drain` and `admin:snapshot`
-- and `admin:start_shard`, `admin:stop_shard`, `admin:shard_state`,
`admin:topology`, `admin:replica_state`, `admin:state_hash` and `admin:node_compaction` -- are **cross-scope** (`ScopeCross`, declared in
`pkg/permission/cluster_admin_scope.go`). They act on the cluster's Raft
membership and shards, addressed by shard id: the system shard every
prefix-isolated tenant's data lives on, and every raft-isolated tenant's own
group. No route names a tenant, and the admin service does no tenant check of
its own, so under `ScopeSelf` the built-in Admin and SO roles -- which grant
add_member, remove_member, drain and snapshot in every tenant -- let any
tenant's Admin remove replicas from the system shard and break quorum for
every tenant, or drain the node. Only the `system` tenant's Admin or SO
exercises them; a tenant's is refused on every transport. `rebalance:*` is
cross-scope for the same reason: `rebalance:execute` physically moves shards
between nodes, and the plan, report and stats reads describe every shard. No
built-in role below Owner carries `rebalance:*` or the shard-lifecycle and
topology permissions, but a tenant's SO holds `roles:create` and
`roles:assign` and could grant them to itself. `backup:*` is cross-scope for
the same reason: a backup is of shards, holding every tenant's data on them,
and a restore overwrites every tenant's state.

The topology reads go with them because they enumerate every tenant's raft
group and node address, and no built-in tenant role carries them; the web
console treats a refused topology probe as a standalone server.
`admin:peers`, `admin:status`, `admin:health` and `admin:capabilities` stay
`ScopeSelf`: the etcd listener's `Cluster.MemberList` is a tenant-facing call
backed by `admin:peers` and authorized against the database's owning tenant,
and status, health and capabilities are what a tenant's tools poll.

`cluster:join_token` (`pkg/permission/cluster_join_token_scope.go`) is the
same shape, for the same reason: minting a member-side CSR join token admits
a node to the whole cluster, not to one tenant. Among the built-in roles only
Admin carries it -- not SO, which otherwise holds every other platform-only
cluster permission in this section. That asymmetry is deliberate: strict
separation of duties (always on in a FIPS build) strips the `Owner` role id
outright, so without the Admin grant no identity could mint a join token under
a FIPS build. See
[../seal/fips140-mode.md](../seal/fips140-mode.md#strict-separation-of-duties-and-clusterjoin_token-under-fips).

## Bootstrap and Role Lifecycle

### Role Bootstrap (Deterministic)

On daemon initialization (`qrdbd start`), the five system roles are created deterministically and idempotently. Under the standard posture the `owner` role (`pkg/role.OwnerRole`) is created beside them; under strict separation of duties it is not stored:

- Each role is assigned a fixed UUIDv8 (RFC 9562 §5.8) derived over SHA-256 from
  the fixed `"go-qrdb:role-namespace/v8/"` prefix plus the tenant ID and role
  name — `pkg/role.SystemRoleID(tenantID, roleName)`. See
  [identity-rbac/engineering.md](../identity-rbac/engineering.md#persistence)
  for the derivation rules.
- Every built-in role is marked as a **system role** (undeletable)
- Roles are persisted in the KV store at `__sys:roles/id/<id>` (Raft-replicated)
- A role that already exists (restart scenario) is tolerated, not duplicated

See [bootstrap.md](../bootstrap.md) for the full bootstrap sequence.

### Role Assignment and Revocation

Roles are assigned to users via `pkg/user/` role assignment (many-to-many: a user can hold multiple roles). Only SO and Admin can create and modify roles and user-role assignments; only SO can delete a role.

## Compliance Mapping

| Standard | Requirement | Coverage |
|----------|-------------|----------|
| FIPS 140-3 | AS10.03 (Roles, Services, Authentication) | Five-role model, SO separation of duties, explicit deny-lists |
| FIPS 140-3 | AS10.05 (Split Knowledge) | Shamir M-of-N unseal — `pkg/seal/shamir.go` + custodian lifecycle |
| FIPS 140-3 | AS10.06 (Dual Authorization) | SO dual-factor (mTLS + PIN), share-holder challenge-response |
| PCI-DSS v4 | Req 7.1 (Role-Based Access Control) | Fine-grained role model, permission matrix, deny-lists |
| FedRAMP Moderate | AC-6 (Least Privilege) | Role-based access, explicit deny-lists, read-only Auditor |
| HIPAA | 164.312(a)(1)(ii)(A) (Access Control) | Role-based access control with audit log visibility |

## See Also

- [xkms docs/init/roles.md](https://github.com/jeremyhahn/go-xkms/blob/main/docs/init/roles.md) — Extended xkms role model with hardware attestation
- [compliance.md](../seal/compliance.md) — Per-framework requirement cross-reference
- [../shamir-custody/](../shamir-custody/) — share holders (Security Officers and custodians), the quorum, and the recovery tier
- [bootstrap.md](../bootstrap.md) — Deterministic role initialization
- [FIPS 140-3 ISO/IEC 19790](https://csrc.nist.gov/publications/detail/fips/140/3) — AS10.03, AS10.05, AS10.06, AS10.09, AS10.10
- [PCI-DSS v4](https://www.pcisecuritystandards.org/documents/PCI-DSS-v4-0.pdf) — Requirement 7.1, 3.2.x, 3.6.x, 10.2.x
