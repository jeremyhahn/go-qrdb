<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# Bootstrap Initialization

This document describes the startup and initialization sequence for a fresh go-qrdb daemon (`qrdbd`).

go-qrdb is the **single owner** of bootstrap, first-boot, ceremony, the user/identity model, and the barrier. Higher-level projects (go-xkms, go-quicbaas) drive these only through `go-qrdb/sdk/go` — never by importing `pkg/*` directly or reimplementing them.

## Barrier protections (canonical)

A barrier is protected by exactly one of three core strategies:

| Protection | Credential | Notes |
|------------|-----------|-------|
| `none` | none | Identity passthrough; data stored in cleartext. Dev/test or OS-level FDE. |
| `passphrase` | admin-supplied passphrase | KDF'd (Argon2id, or PBKDF2-SHA256 in FIPS 140 mode — see `seal.FIPSEnabled`). Supplied at `Initialize` and every `Unseal`; **never stored in config and never derived from a deployment ID**. Backed by `DeferredSoftwareStrategy`. |
| `shamir` | M-of-N shares | Shares enrolled via the M-of-N ceremony. |

Hardware strategies (`tpm2` / `pkcs11` / cloud KMS / Vault) are xkms-provided extensions of `passphrase`, registered into qrdb's `StrategyRegistry` at composition time — not new core options. See [seal/README.md](seal/README.md).

### System vs per-tenant barriers

- **System barrier** holds the **system tenant** (control plane: tenant records and tenant-admin identities). Created at admin bootstrap per config.
- **Per-tenant barriers** are registered via `seal.BarrierRegistry.RegisterTenant(...)` as a sealed, uninitialized shell when the system admin provisions a tenant. The **tenant admin** later initializes their own barrier (their choice of `none` / `passphrase` / `shamir`, their passphrase/shares). The system admin holds no tenant barrier key.

## Bootstrap entry points (`pkg/bootstrap`)

Admin enrollment is cert-based (CSR-sign or generate-and-distribute -> SPKI-thumbprint binding); there are no invite tokens. The control-plane (system) and data-plane (tenant) scopes are orchestrated through the `pkg/bootstrap` entry points:

- **System bootstrap** — system barrier + system tenant + system admin + admin-mTLS.
- **Provision tenant** — minimal control-plane tenant record + tenant-admin cert enrollment (no tenant barrier key held by the system admin).
- **Initialize tenant barrier** — tenant-admin self-service (authenticated): initialize THIS tenant's barrier and scope.

See [users/README.md](identity-rbac/architecture.md) for the user model and SPKI binding.

## Startup Sequence

When `qrdbd start` runs for the first time, it executes the following initialization steps:

### 1. Configuration Loading

```bash
qrdbd start --config /etc/qrdb/qrdb.yaml --data-dir /var/lib/qrdb
```

The daemon loads configuration from the YAML file (or uses CLI flag overrides) and validates all parameters: Raft address, listener ports, TLS certificate paths, storage backend, databases, seal strategy, etc.

### 2. TLS Certificate Generation

If no TLS certificates exist, the daemon generates them:

- **CA private key** — Generated and sealed in the barrier (or file if barrier is unavailable)
- **CA certificate** — Self-signed, written to `$DATA_DIR/ca.pem`
- **Server TLS key pair** — Written to `$DATA_DIR/qrdb-server.key` and `$DATA_DIR/qrdb-server.pem`
- **Admin client cert** — For the system tenant, written to `$DATA_DIR/admin.pem`

The CA key is sealed via `seal.EncryptionBarrier` (if the barrier is configured) or stored plaintext (file storage). Once sealed, all subsequent certificate operations require an unsealed barrier.

### 3. Storage Initialization

The daemon initializes the configured storage backend(s):

- **PebbleDB** — Creates/opens the database at `$DATA_DIR/db.pebble`
- **File Storage** — Creates directory structure for file-backed key-value store
- **Memory Storage** — Allocates in-memory maps

Each database (configured in YAML) gets its own storage backend instance.

### 4. Raft Initialization

The daemon initializes the Raft consensus layer:

- **Node ID** — Derived from CLI flags or YAML (`--replica-id`, `--deployment-id`)
- **Peer Discovery** — Uses configured method: static, multicast, DNS, or token. The discovery engine and cluster-formation orchestration live in `pkg/discovery` and `pkg/cluster`; see [discovery.md](clustering/architecture.md) for details.
- **Shard Creation** — Creates the default KV shard (ID `1`) with the configured state machine type and storage backend
- **Log Storage** — Opens the Raft WAL in `$DATA_DIR/raft.log`

### 5. Barrier Initialization (if needed)

If no barrier exists (fresh install):

- **Single-Admin Mode** (threshold < 2): Barrier is initialized and unsealed immediately
- **M-of-N Mode** (threshold >= 2): Barrier is initialized, Shamir shares are generated, and the system enters "enrolling" state to wait for officers to claim certificates and shares

The barrier status is checked via `BarrierStatus`. If sealed, data operations are blocked until `BarrierUnseal` or `BarrierUnsealWithShare(s)` is called.

### 6. Role and User Bootstrap

The daemon creates six system roles deterministically on first init. Each role is assigned a fixed UUID v5 derived from its name to ensure cross-cluster stability.

**System Roles** (undeletable, persist at `__sys:role/<id>`):

| Role | UUID | Permissions | Notes |
|------|------|-----------|-------|
| Security Officer (SO) | `uuid5(namespace, "go-qrdb/role/SO")` | `so:*`, `barrier:*`, `certificate:*`, `audit:read`, `role:*`, `user:*` (crypto ops explicitly denied) | Governance only; dual-factor auth required |
| Admin | `uuid5(namespace, "go-qrdb/role/Admin")` | `keys:*` (except delete infrastructure), `certificate:*`, `tenant:*`, `backup:*`, `audit:read` | Day-to-day key lifecycle |
| Operator | `uuid5(namespace, "go-qrdb/role/Operator")` | `keys:sign`, `keys:encrypt`, `keys:decrypt`, `keys:verify`, `barrier:seal`, `barrier:unseal`, `audit:read` | Routine operations |
| User | `uuid5(namespace, "go-qrdb/role/User")` | `keys:sign`, `keys:verify`, `keys:encrypt`, `keys:decrypt` | Crypto ops only |
| Auditor | `uuid5(namespace, "go-qrdb/role/Auditor")` | `audit:read`, `system:read`, `*:list`, `*:get-metadata` | Read-only everywhere |
| Custodian | `uuid5(namespace, "go-qrdb/role/Custodian")` | `barrier:unseal-share` | Shamir share holder only |

An admin user for the system tenant is also created:

| Object | ID | Permissions | Notes |
|--------|----|----|-------|
| `admin` user | Auto-generated | Bound to `admin` role | Created once, idempotent |

**Phase 1 Note**: These six roles are documented in [docs/auth/roles.md](auth/roles.md) with full FIPS 140-3 AS10.03 separation-of-duties constraints and permission matrix. Phase 2 implements RBAC enforcement.

This bootstrap is **idempotent**: if roles and admin user already exist, the function returns immediately without creating duplicates.

In multi-node clusters, only the **leader** can execute the creation. Followers detect `NotLeader` errors and wait for the leader's writes to replicate.

### 7. Multi-Protocol Servers Start

Once the daemon is operational, all configured protocol servers start listening:

- **Admin Server** (REST API) — Bound to `--listen` (e.g., `127.0.0.1:8443`), HTTPS with client cert auth
- **gRPC Server** — Bound to `--grpc-port` (e.g., `9090`)
- **QUIC Server** — Bound to `--quic-port` (e.g., `9443`)
- **SQL Servers** — MySQL (`--mysql-port 3306`) and PostgreSQL (`--pgsql-port 5432`) if enabled
- **Unix Socket** — gRPC over UDS at `--unix-socket-path` if enabled
- **MCP Server** — MCP protocol at `--mcp-endpoint` if enabled

## File Layout

After successful bootstrap, the data directory contains:

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

## Idempotency Guarantees

The bootstrap sequence is **idempotent** and safe to re-run:

- **Certificates** — If certs already exist, they are reused
- **Admin User** — If admin exists, creation is skipped
- **Barrier** — If sealed, unseal must be done manually; if unsealed, state remains
- **Raft** — If a node restarts, it re-joins the cluster using the stored log and snapshots

A fresh start vs. a restart of an existing node follows the same sequence; only the first run performs initialization, subsequent runs skip redundant operations.

## Cluster Bootstrap (Multi-Node)

When joining an existing cluster, the `--join` flag allows a new node to discover and sync with peers:

```bash
qrdbd start --join --peers node1.example.com:5000,node2.example.com:5000
```

The new node will:

1. Initialize its local storage and barrier
2. Contact the peers to request a snapshot and log entries
3. Apply the snapshot to bring itself up-to-date
4. Begin participating in Raft consensus

The admin user and roles are not re-created (they already exist from the cluster's initial bootstrap).

## Single-Admin Mode

Quickest to deploy for development and small installations:

```bash
qrdbd start --threshold 1
```

The barrier initializes, unseals immediately, and the system is operational without officer coordination.

## M-of-N Mode (Multi-Officer)

For production and high-security environments:

```bash
qrdbd start --threshold 2 --total 3
```

After the daemon starts, it is in "enrolling" state. Officers must claim their certificates and Shamir shares via the REST/gRPC APIs. Only after all officers have claimed will the system transition to "operational".

## Troubleshooting

### Barrier Sealed

If the barrier is sealed, data operations return `ErrBarrierSealed`. Unseal it via:

```bash
qrdb barrier unseal-shares --share share-1 --share share-2 --share share-3
```

Or via REST:

```bash
curl -X POST https://localhost:8443/v1/barrier/shamir/unseal-shares \
  -H "Content-Type: application/json" \
  --cert admin.pem \
  -d '{"shares":["share-1","share-2","share-3"]}'
```

### Admin User Not Created

Check logs for `auth bootstrap` messages. If `firstboot.Initialize` fails on a follower, wait for leader replication. If it fails on the leader, check barrier and storage backend status.

### TLS Certificate Issues

If TLS certs are corrupted or expired:

1. Delete the cert files from `$DATA_DIR/`
2. Stop the daemon
3. Restart — new certs will be generated
4. Regenerate admin client cert via `generateAdminClientCert()`

## See Also

- [ceremony/README.md](ceremony/architecture.md) — Ceremony state machine and officer enrollment
- [seal/README.md](seal/README.md) — Barrier architecture and unsealing
- [12-configuration.md](12-configuration.md) — Full YAML configuration reference
- [13-operations.md](clustering/cli.md) — Monitoring, health checks, and deployment
