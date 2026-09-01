# Firstboot and System Bootstrap — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

```go
// pkg/firstboot
func Initialize(ctx context.Context, deps Dependencies) (*Result, error)

// pkg/bootstrap — orchestration entry points
//   System bootstrap        — system barrier + system tenant + system admin + admin-mTLS
//   Provision tenant        — control-plane tenant record + tenant-admin enrollment
//                             (AdminEnrollmentMode: cert [default] | deferred/pending-invite)
//   Initialize tenant barrier — tenant-admin self-service, authenticated
```

`sdk/go` exposes the bootstrap and firstboot types through `types_bootstrap.go`
and `types_firstboot.go`.

## Admin credential paths

Selected by which `Dependencies` inputs are present, and **evaluated with path C
first**:

| Path | Input | What happens | Key custody |
|---|---|---|---|
| **C** adopt-presented | `AdminCertPEM` + `AdminCertPool` | the certificate the admin **already holds** is validated and adopted; nothing is issued | key never leaves the operator's token |
| **B** CSR-sign | `AdminCSRPEM` + `Signer` | `CertSigner.SignAdminCSR` signs the admin's CSR | key never leaves the admin |
| **A** generate-and-distribute | `CA` | `CertProvider.GenerateAdminCert` issues a key + cert bundle | CA emits the bundle |

Ordering is load-bearing: path C issues nothing, so evaluating it last would
mean a deployment that presented a certificate falls through to the CA and gets
a second, unrelated credential bound instead.

## Invariants

- **System-level enrolment is certificate-based; there are no system-level
  invite tokens.** The initial tenant admin may instead be provisioned via
  `ProvisionTenant`'s `AdminEnrollDeferred` mode: the admin user is created
  pending (`Enabled=false`, no credential bound) and the result carries
  `AdminUserID` + `AdminRoleID` so the composing host attaches a single-use
  invitation (`pkg/invite`) whose redemption binds the first auth factor and
  activates the account. Supplying `AdminCSRPEM` alongside deferred mode is
  `ConflictingAdminEnrollmentError`; an undeclared mode is
  `InvalidAdminEnrollmentModeError`; requesting deferred mode for the system
  tenant is `SystemTenantDeferredEnrollmentError` (system-level enrollment is
  cert-only, always). On an idempotent re-run the preexisting admin is
  resolved through the optional `AdminUserLookup` seam (satisfied by
  `user.UserServicer`). EVERY record deferred mode returns — freshly created
  included, since `Users` is an adapter seam that may ignore `Pending` —
  must be truly pending: persisted id, exact tenant and username, the
  tenant-scoped Admin role, still disabled, no bound certificate, no
  enrolled factors on the record, and no ACTIVE factor in the canonical
  auth-factor store (the required `Dependencies.AdminFactors` seam — the
  FAIL-CLOSED `service.ActiveFactorChecker` query, implemented by the
  production user auth-factor service over the canonical PRIMARY factor
  records rather than the per-user index, so a partially indexed enrollment
  or index corruption cannot hide a factor; the lenient `List`, which walks
  the index and skips unreadable entries, structurally cannot satisfy it.
  Deferred mode without the seam is `MissingAdminFactorsError`; a store
  read error is an error, never "credential-less"; revoked factors do not
  block, so re-inviting after credential revocation stays possible). Any other state (an activated
  admin, one disabled for incident response with live credentials, or a
  wrong record) fails closed with `PendingAdminUnresolvableError` rather
  than becoming an invitation target. Deferred mode strengthens the
  no-tenant-key invariant: nothing usable exists until redemption.
- **The admin is bound by SPKI thumbprint.**
- **`AdminCertPool` is not the platform CA.** It is an operator-supplied pool
  for a credential issued elsewhere.
- **Built-in role IDs are deterministic** — `uuid5(namespace,
  "go-qrdb/role/<Name>")` — so role identity is stable across clusters and
  re-inits.
- **The six built-in roles are undeletable**, persisted at `__sys:role/<id>`.
- **Bootstrap is idempotent.** Existing roles and the admin user cause an
  immediate return without duplicates.
- **Only the leader creates.** Followers detect `NotLeader` and wait for
  replication.
- **Only a `NotLeader` failure waits.** The follower wait is entered for that
  condition alone, checked at both layers that express it
  (`service.NotLeaderError` and `quicraft.NotLeaderError`). Every other
  first-boot failure propagates with its own cause. It previously caught
  ANY error, so a local fault — a state machine unable to apply an op code,
  say — was reported 30 seconds later as `admin user not replicated`, a
  message naming the symptom while the cause appeared only in a log line
  (go-qrdb#86).
- **Ceremony runs before firstboot**, and both are safe to re-run on cluster
  nodes.

## Error semantics

| Error | Condition | Fails closed? |
|---|---|---|
| `ConflictingAdminCertInputsError` | `AdminCSRPEM` **and** `AdminCertPEM` both supplied | **yes** — neither outcome is choosable on the operator's behalf |
| `MissingAdminCertPoolError` | `AdminCertPEM` without `AdminCertPool` | **yes** — an absent pool is refused, never treated as "trust anything" |
| `AdminCertRejectedError` | parse failure, expired, or no chain to the pool | **yes** |
| `NotLeader` | a follower attempted creation | n/a — it waits for replication |

## Built-in roles

| Role | Permissions | Notes |
|---|---|---|
| Security Officer (SO) | `so:*`, `barrier:*`, `certificate:*`, `audit:read`, `role:*`, `user:*` — **crypto ops explicitly denied** | governance only; dual-factor auth required |
| Admin | `keys:*` (except deleting infrastructure), `certificate:*`, `tenant:*`, `backup:*`, `audit:read` | day-to-day key lifecycle |
| Operator | `keys:sign`, `keys:encrypt`, `keys:decrypt`, `keys:verify`, `barrier:seal`, `barrier:unseal`, `audit:read` | routine operations |
| User | `keys:sign`, `keys:verify`, `keys:encrypt`, `keys:decrypt` | crypto ops only |
| Auditor | `audit:read`, `system:read`, `*:list`, `*:get-metadata` | read-only everywhere |
| Custodian | `barrier:unseal-share` | Shamir share holder only |

The SO's explicit denial of crypto operations is the separation-of-duties
control: governance and key use are different people. Full matrix and the FIPS
140-3 AS10.03 constraints: [auth/roles.md](../auth/roles.md).

## Configuration

Firstboot itself is driven by `Dependencies` rather than config keys. The
surrounding daemon startup is configured through
[../configuration/](../configuration/); mode selection is
[../run-modes/](../run-modes/).

Single-admin versus M-of-N is selected at init:

```bash
qrdbd start --threshold 1            # single-admin: initialize, unseal, operational
qrdbd start --threshold 2 --total 3  # M-of-N: stays in "enrolling" until officers claim
```

## File layout after bootstrap


```
$DATA_DIR/
├── ca.pem                    # CA certificate (readable by operators)
├── qrdb-server.key           # Server TLS private key (encrypted in barrier)
├── qrdb-server.pem           # Server TLS certificate
├── admin.pem                 # Admin client certificate (system tenant)
├── db.pebble/                # PebbleDB storage (if pebble backend)
│   ├── MANIFEST
│   ├── 000001.sst
│   └── ...
├── raft.log                  # Raft WAL
└── .qrdb.lock                # File lock (prevents concurrent daemon instances)
```

All sensitive keys are stored in the barrier (encrypted). Once unsealed, they are available in memory via `seal.EncryptionBarrier.Decrypt()`.

**All sensitive keys are stored in the barrier.** Once unsealed they are
available in memory via `seal.EncryptionBarrier.Decrypt()`.

## Persistence

- Roles: `__sys:role/<id>` with deterministic UUIDv8 (SHA-256) identifiers.
- Users and their certificate bindings: see
  [../identity-rbac/](../identity-rbac/engineering.md).
- Certificates: see
  [../certificate-authority/](../certificate-authority/engineering.md).

**Compatibility**: the deterministic role UUIDs are part of the on-disk
contract. Changing the derivation would orphan every existing role assignment.

## Test strategy

- **Unit**: each admin credential path, and each refusal — conflicting inputs,
  missing pool, rejected certificate. **Path-C-first ordering is its own test**:
  a deployment presenting a certificate with a CA also configured must adopt,
  not issue.
- **Idempotence**: a second `Initialize` must create nothing and return
  immediately.
- **Deterministic role IDs**: the same name must yield the same UUID across
  runs.
- **Error tests** per function.

## Observability

`GET /v1/config/initialized` is a **public** endpoint reporting whether the
system has been initialized — that is the check a client makes before it has
any credentials. Bootstrap operations are audited through
[../audit/](../audit/).
