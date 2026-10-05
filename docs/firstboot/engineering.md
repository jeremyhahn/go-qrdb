# Firstboot and System Bootstrap — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

```go
// pkg/firstboot
func Initialize(ctx context.Context, deps Dependencies) (*Result, error)

// The auth.mtls_binding_policy "required" boot check.
type AdminLookup interface {
	GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error)
}
func VerifyAdminCertBinding(ctx context.Context, users AdminLookup, now time.Time) error

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
  pending (lifecycle `pending`, no credential bound) and the result carries
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
  tenant-scoped Admin role, a lifecycle of exactly `pending` (an absent,
  unrecognized or any other state is refused), no bound certificate, no
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
- **A role's or user's boot-time create is ONE guarded batch, not several
  proposals.** `role.WithAtomicIdentity` / `user.WithAtomicIdentity` commit
  the identity claim together with the primary record and its indexes in a
  single proposal. A leadership change anywhere in what used to be a
  multi-proposal sequence now leaves either everything or nothing — there is
  no partial state left for a later attempt to find, repair, or refuse
  against. `NewStore` refuses construction when `WithAtomicIdentity` is set
  without the required capability, so the guarantee is fixed at startup, not
  discovered missing at the first create. Both stores use the SAME
  multi-guard verb, `kvstore.MultiGuardedBatchKVStore.PutAllIfAllUnchanged`,
  because neither can fence its uniqueness handles with a single guard key:
  - **Role** guards the identity claim key AND the name index key together in
    one proposal. An id-only guard is exact for the deterministic built-in
    roles this store seeds (their id IS derived from their name, so the
    claim and the name denote the same collision), but not for an ordinary
    caller-supplied id: two concurrent creates with DIFFERENT ids and the
    SAME tenant/name would each satisfy their own distinct claim guard and
    both commit, with the second silently overwriting the name index while
    both primary records, tenant indexes, and claims remained. Guarding the
    name key too closes that window for every caller, not only the
    built-in-role case.
  - **User** guards the identity claim key, the username index key, AND —
    when the request has one — the email index key, all evaluated together
    in the SAME proposal. A single guard cannot fence three independent
    handles (a generated id, a chosen username, and an optional email) at
    once: an earlier design guarded only the username key on the theory that
    a freshly generated id essentially never collides, which left a
    reused-id create able to silently overwrite an existing record and claim
    under a different, unused username. `WithAtomicIdentity` now applies to
    EVERY create this store makes, email-bearing or not — there is no
    sequential fallback path.
- **A concurrent, identical built-in role's create is verified in
  `firstboot`, not assumed in `CreateRole`, on EITHER collision `CreateRole`
  can report.** Exactly one of two simultaneous first-boot attempts wins the
  guarded batch for a given built-in role; the other is refused with
  `RoleIDExistsError` or `RoleNameExistsError`, the SAME error types
  `CreateRole` has always returned for those collisions.
  `registerBuiltInRoles` resolves EITHER refusal by reading the persisted
  record back (`role.NameResolver.GetRoleByName`) and comparing tenant,
  canonical name, the `System` flag, and permission set against the
  canonical built-in definition — an id collision additionally requires the
  persisted id to match exactly, while a name collision does not, since
  `role.SystemRoleID` is a pure function of tenant and name and an idempotent
  re-attempt derives the identical id anyway. The
  `System` check exists because an earlier release persisted every built-in
  role with `System: false` (`CreateRoleRequest` had no such field then), and
  matching id/tenant/permissions while accepting that flag mismatch would
  leave a reserved-name role mutable and deletable through the ordinary role
  APIs. A mismatch on any required field is refused, not papered over. This
  lives in `firstboot` specifically because it is the one caller that knows
  the canonical definition a collision might legitimately mean; `CreateRole`
  itself never makes this judgment, so a general caller holding
  `roles:create` cannot use it to adopt another actor's role.
- **A guarded batch that commits with its response lost still emits the
  create audit event.** `PutAllIfUnchanged`/`PutAllIfAllUnchanged` report an
  UNKNOWN outcome, not a failure, when the proposal may have committed and
  only the acknowledgement was lost; both stores' reconciliation reads every
  write back and, on a full match, treats the create as successful — and
  emits the SAME audit event the normal success path does, so a durably
  created privileged role or bootstrap user is never missing from the audit
  trail merely because its confirmation was lost.
  The read-back is POLLED, not a single read: every 10 ms for up to 5 s, on
  a context that drops the request's cancellation but keeps its values.
  `cmd/qrdbd`'s boot-time stores read the local state machine (`StaleRead`)
  rather than through a linearizable read, so a proposal that committed on
  the cluster may not yet be applied on THIS node the instant its lost
  response is reported; one immediate read would see the old values and
  report a create that in fact committed as failed, without its audit
  event. A write that never committed still reports the original failure
  once the window closes.
  A refusal that PROVES nothing was written is not polled at all. The shared
  guarded-batch implementation (`lifecycle.ExecuteGuardedBatch`) marks those
  with `kvstore.ErrNotApplied`: a missing transaction service or a state
  machine without the subsystem (`kvstore.GuardedBatchUnsupportedError`), and
  anything the service layer marks `service.ErrNotStored`, a propose-time
  `NotLeader` most commonly. The create returns that error at once instead of
  waiting 5 s for a write that cannot appear.
  The guard-failure diagnosis polls through the same window for the mirror
  reason: the winner's keys are committed but may not be applied here yet.
- **Only a `NotLeader` failure waits.** The follower wait is entered for that
  condition alone, checked at both layers that express it
  (`service.NotLeaderError` and `quicraft.NotLeaderError`). Every other
  first-boot failure propagates with its own cause. It previously caught
  ANY error, so a local fault — a state machine unable to apply an op code,
  say — was reported 30 seconds later as `admin user not replicated`, a
  message naming the symptom while the cause appeared only in a log line.
- **Ceremony runs before firstboot**, and both are safe to re-run on cluster
  nodes.

## Error semantics

| Error | Condition | Fails closed? |
|---|---|---|
| `ConflictingAdminCertInputsError` | `AdminCSRPEM` **and** `AdminCertPEM` both supplied | **yes** — neither outcome is choosable on the operator's behalf |
| `MissingAdminCertPoolError` | `AdminCertPEM` without `AdminCertPool` | **yes** — an absent pool is refused, never treated as "trust anything" |
| `AdminCertRejectedError` | parse failure, expired, or no chain to the pool | **yes** |
| `NotLeader` | a follower attempted creation | n/a — it waits for replication |
| `AdminCertBindingRequiredError` | `MTLSBindingPolicy` is `required` and the admin could not log in by certificate: `Reason` is one of `AdminBindingNoBinder`, `AdminBindingNoCertPath`, `AdminBindingNone`, `AdminBindingAllExpired`, `AdminBindingMissing`, `AdminBindingCannotAuthenticate` (lifecycle forbids login), `AdminBindingMTLSNotPermitted` (method policy excludes mtls), `AdminBindingUnverifiable` | **yes** — returned before any write |
| `AdminLookupError` | the admin could not be read while checking that posture | **yes** — no verdict is not a pass |

Under `required`, `Initialize` also re-reads the admin after it finishes and
applies the same predicate to what the store holds, so an empty certificate, a
missing admin record, or a binder that did not bind fails first boot instead of
reporting success. A passing check sets
`qrdb_auth_admin_cert_binding_not_after_timestamp_seconds` (Unix time of the
admin's last live binding's `NotAfter`, `+Inf` when one never expires) and
warns when that is within `user.AdminBindingExpiryWarning` (30 days); the
metric and the warning live in `pkg/user` (`user.ObserveBootstrapAdminBindings`)
because the user store refreshes them on every committed change to the admin.
The predicate itself is `user.AdminLoginBlocker` — lifecycle, method policy,
live binding, in the order the middleware applies them — which the user
store's runtime guard (`user.WithBootstrapAdminBindingRequired`) shares, so
the boot check and the guard cannot disagree about what "the admin can log in"
means.

`VerifyAdminCertBindingWithin(ctx, users, timeout)` is the boot-time form. It
runs under `context.WithDeadline`, so a lookup that blocks is cut off at the
bound as well, and it retries only a lookup that FAILS (`AdminLookupError`)
until `timeout`
(`AdminBindingLookupTimeout`, 30 s), and returns any verdict at once. The
daemon and the SDK node call it with a linearizable store, so a follower
waits for the admin's bindings rather than judging a lagging copy.

## Built-in roles

| Role | Permissions | Notes |
|---|---|---|
| Security Officer (SO) | `so:*`, `barrier:*`, `certificate:*`, `audit:read`, `role:*`, `user:*` — **crypto ops explicitly denied** | governance only; dual-factor auth required |
| Admin | `keys:*` (except deleting infrastructure), `certificate:*`, `tenant:*`, `backup:*`, `audit:read` | day-to-day key lifecycle |
| Operator | `keys:sign`, `keys:encrypt`, `keys:decrypt`, `keys:verify`, `barrier:seal`, `barrier:unseal`, `audit:read` | routine operations |
| User | `keys:sign`, `keys:verify`, `keys:encrypt`, `keys:decrypt` | crypto ops only |
| Auditor | `audit:read`, `system:read`, `*:list`, `*:get-metadata` | read-only everywhere |

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
