<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# User Management

The `pkg/user/` package is the **single canonical user/identity model for the entire stack**. There are no passwords, no JWT tokens, and no session cookies in core auth. Higher-level projects (go-xkms, go-quicbaas) consume this model through `go-qrdb/sdk/go` (re-exported in `sdk/go/types_user.go`) and must not define a parallel user type or cert-binding scheme.

Core authentication is **mTLS only**. Additional factors (WebAuthn / FIDO2 / PIV) are extensible enrollments registered via `pkg/authfactors` (the factor implementations are xkms-provided). OIDC / OAuth2 are quicbaas services layered on this same user — qrdb itself contains no WebAuthn or OAuth2 code.

## Identity Model

mTLS identity is bound by **one algorithm for the whole stack**: the SPKI SHA-256 thumbprint. The certificate's CN/OU still carry the human-readable `Username` / `TenantID`, but the authoritative match is the `CertBinding`:

| Source | User field | Purpose |
|--------|-----------|---------|
| Subject CN | `Username` | Unique identity within a tenant |
| Subject OU | `TenantID` | Tenant isolation boundary |
| SHA-256(SubjectPublicKeyInfo), base64url | `CertBindings[].Thumbprint` | The authoritative mTLS identity — `User.MatchesCert(cert)` |

Because the thumbprint is computed over the public key, a renewed certificate for the same key produces the same thumbprint, so bindings survive renewal. See [Cert-Binding](#cert-binding) below.

## User Entity

Source: `pkg/user/types.go`

```go
type User struct {
    ID           string             `json:"id"`           // Globally unique UUID
    Username     string             `json:"username"`     // Unique within tenant, maps to cert CN
    TenantID     string             `json:"tenantID"`     // Maps to cert OU
    RoleIDs      []string           `json:"roleIDs"`      // System-scoped role UUIDs
    Enabled      bool               `json:"enabled"`      // Whether the user can authenticate
    System       bool               `json:"system"`       // Built-in, cannot be deleted
    RoleBindings []RoleBinding      `json:"roleBindings"` // RBAC — role granted within a tenant scope
    Attributes   map[string]string  `json:"attributes"`   // ABAC — policy attributes
    CertBindings []CertBinding      `json:"certBindings"` // mTLS — SPKI thumbprint bindings
    Factors      []FactorEnrollment `json:"factors"`      // webauthn/fido2/piv enrollments (opaque)
    CreatedAt    time.Time          `json:"createdAt"`    // UTC creation time
    UpdatedAt    time.Time          `json:"updatedAt"`    // UTC last modification time
}
```

- **`RoleBindings` (tenant-scoped RBAC):** a `RoleBinding{RoleID, TenantID}` grants a role within a specific tenant scope, additive to the system-scoped `RoleIDs`. A control-plane user can hold a tenant-admin binding for tenant X without being an admin of the system tenant. Built-in roles are bootstrapped by `firstboot.Initialize` (see [Admin Bootstrap](#admin-bootstrap)).
- **`Attributes` (ABAC):** key/value pairs evaluated by policy.
- **`CertBindings` (mTLS):** SPKI-thumbprint bindings — the qrdb-owned mTLS identity (see [Cert-Binding](#cert-binding)).
- **`Factors`:** opaque auth-factor enrollment blobs; verification is handled by the registered `authfactors.Factor` (xkms-provided). qrdb stores `Data` and never interprets it.

## Cert-Binding

Source: `pkg/user/cert_binding.go`

```go
type CertBindingKind int
const (
    CertBindingSPKIThumbprint      CertBindingKind = iota // canonical default
    CertBindingFullCertFingerprint                        // exact-cert pinning (non-default)
    CertBindingPIVAttested                                // established via PIV attestation
)

type CertBinding struct {
    Thumbprint string          `json:"thumbprint"` // SPKI SHA-256, base64url
    Kind       CertBindingKind `json:"kind"`
    NotAfter   time.Time       `json:"notAfter,omitempty"`
}

// ComputeSPKIThumbprint is THE single mTLS-identity algorithm for the whole stack:
// SHA-256 over SubjectPublicKeyInfo, base64url (no padding). xkms and quicbaas must
// NOT reimplement it; they call sdk.ComputeSPKIThumbprint.
func ComputeSPKIThumbprint(cert *x509.Certificate) string

func (u *User) AddCertBinding(b CertBinding)
func (u *User) HasCertBinding(thumbprint string) bool
func (u *User) MatchesCert(cert *x509.Certificate) bool // SPKI thumbprint -> binding lookup
```

The store maintains a `cert_spki` secondary index so a presented certificate resolves to its user via `UserByCertThumbprint`. The default `CertBindingSPKIThumbprint` tracks the public key and survives certificate renewal with the same key; `CertBindingFullCertFingerprint` exists only for the rare exact-cert-pinning case.

**Binding security model.** Because a cert-binding controls which certificates authenticate as a user, the enrollment seam (`AddCertBinding`) and lookup (`UserByCertThumbprint`) enforce:

- **Canonical thumbprints only.** `AddCertBinding` rejects any value that is not a base64url (no padding) encoding of exactly 32 bytes (`IsValidSPKIThumbprint`), so the identity index cannot be polluted with arbitrary strings.
- **No cross-user hijack.** A thumbprint resolves to exactly one identity; binding a thumbprint already held by a different user is rejected with `CertBindingConflictError` rather than silently overwriting the mapping.
- **Expiry enforced on every resolution.** `UserByCertThumbprint`, `HasCertBinding`, and `MatchesCert` ignore a binding whose `NotAfter` is in the past, so a decommissioned/expired credential cannot be replayed (the index entry alone does not grant identity).
- **Authorization.** Resolving a user by thumbprint requires `users:get_by_cert_thumbprint`; binding a thumbprint requires the distinct, more sensitive `users:add_cert_binding`. Both are enforced on every transport (gRPC/REST/QUIC); the REST per-user routes (`/users/{id}/cert-bindings`, `/users/by-cert-thumbprint/{thumbprint}`) resolve through path-parameter-aware permission matching rather than defaulting to public.

### FactorEnrollment

```go
type FactorEnrollment struct {
    ID         string          `json:"id"`
    FactorName string          `json:"factorName"` // names the registered authfactors.Factor
    Data       json.RawMessage `json:"data"`       // opaque to qrdb
    CreatedAt  time.Time       `json:"createdAt"`
}
```

### Request Types

```go
type CreateUserRequest struct {
    Username string   // Required, must not be empty
    TenantID string   // Defaults to DefaultTenantID when empty
    RoleIDs  []string // Initial roles to assign
}

type UpdateUserRequest struct {
    ID       string   // Required, user UUID
    Username string   // Replaces current username when non-empty
    RoleIDs  []string // Replaces current role set when non-nil
}

type ListUsersRequest struct {
    TenantID string // Filters by tenant
    Page     int    // 1-based page number (default: 1)
    PageSize int    // Max users per page (default: 100)
}

type ListUsersResponse struct {
    Users   []*User `json:"users"`
    Total   int     `json:"total"`
    HasMore bool    `json:"hasMore"`
}
```

## Service Interface

Source: `pkg/user/service.go`

```go
type UserServicer interface {
    CreateUser(ctx context.Context, req *CreateUserRequest) (*User, error)
    GetUser(ctx context.Context, id string) (*User, error)
    GetUserByUsername(ctx context.Context, tenantID, username string) (*User, error)
    UpdateUser(ctx context.Context, req *UpdateUserRequest) (*User, error)
    DeleteUser(ctx context.Context, id string) error
    ListUsers(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error)
    EnableUser(ctx context.Context, id string) error
    DisableUser(ctx context.Context, id string) error
}
```

All implementations must be safe for concurrent use.

## Storage Schema

Source: `pkg/user/store.go`

Users are stored in the KVStore with three key patterns providing primary access and secondary indexes:

| Key Pattern | Value | Purpose |
|-------------|-------|---------|
| `__sys:users/id/{userID}` | User JSON | Primary record by UUID |
| `__sys:users/tenant/{tenantID}/{userID}` | userID bytes | Tenant-scoped listing index |
| `__sys:user_idx/username/{tenantID}/{username}` | userID bytes | Username uniqueness index |

The username index enforces uniqueness within a tenant. `GetUserByUsername` performs a secondary index lookup followed by a primary key read. `ListUsers` scans the tenant index prefix and loads each primary record.

## Admin Bootstrap

Source: `pkg/firstboot/firstboot.go` (driven by `sdk.FirstBootInitialize`)

First-boot is idempotent and runs on every startup. It bootstraps the initial admin identity:

1. **System tenant** -- Creates the structurally privileged tenant with ID `"system"`.
2. **Built-in roles** -- Creates the canonical roles if absent.
3. **Admin user** -- Creates the `admin` user (TenantID `system`, `System: true`, `Enabled: true`).
4. **Admin certificate (cert-based, no invite token)** -- when a new admin user is created, the admin cert is provisioned by one of two paths and its **SPKI thumbprint is bound** to the admin user via `CertBinder.BindUserCert`:
   - **CSR-sign (Path B):** `CertSigner.SignAdminCSR` signs an admin-supplied CSR; the private key never leaves the admin.
   - **Generate-and-distribute (Path A):** `CertProvider.GenerateAdminCert` emits a cert + key bundle (CN `admin`, OU `system`) for secure handover.
5. **Result** -- reports tenant/role/user IDs and `AdminCertThumbprint`.

The admin user is marked as a system user and cannot be deleted.

Tenant admins are enrolled the same way (cert-based, SPKI-bound) when the system admin provisions a tenant; the tenant admin then self-bootstraps their own tenant barrier. See [../bootstrap.md](../bootstrap.md) and [../seal/README.md](../seal/README.md).

## CLI Commands

Source: `cmd/qrdb/users.go`

```
qrdb users list [--tenant-id <id>]
qrdb users create --username <name> [--tenant-id <id>] [--roles <id1,id2>]
qrdb users get <id>
qrdb users update <id> [--username <name>] [--roles <id1,id2>]
qrdb users delete <id>
qrdb users enable <id>
qrdb users disable <id>
```

## API Endpoints

### REST

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/users` | Create user |
| `GET` | `/v1/users` | List users (query: `tenantId`, `page`, `pageSize`) |
| `GET` | `/v1/users/{id}` | Get user by ID |
| `PUT` | `/v1/users/{id}` | Update user |
| `DELETE` | `/v1/users/{id}` | Delete user |
| `POST` | `/v1/users/{id}/enable` | Enable user |
| `POST` | `/v1/users/{id}/disable` | Disable user |

### gRPC

```protobuf
rpc CreateUser(CreateUserRequest) returns (User);
rpc GetUser(GetUserRequest) returns (User);
rpc UpdateUser(UpdateUserRequest) returns (User);
rpc DeleteUser(DeleteUserRequest) returns (google.protobuf.Empty);
rpc ListUsers(ListUsersRequest) returns (ListUsersResponse);
rpc EnableUser(EnableUserRequest) returns (google.protobuf.Empty);
rpc DisableUser(DisableUserRequest) returns (google.protobuf.Empty);
```

### MCP Tools

| Tool | Description |
|------|-------------|
| `qrdb_user_create` | Create a new user |
| `qrdb_user_get` | Get user by ID |
| `qrdb_user_update` | Update user fields |
| `qrdb_user_delete` | Delete a user |
| `qrdb_user_list` | List users for a tenant |
| `qrdb_user_enable` | Enable a user account |
| `qrdb_user_disable` | Disable a user account |

## Required Permissions

| Operation | Permission |
|-----------|------------|
| Create user | `users:create` |
| Read user | `users:read` |
| Update user | `users:update` |
| Delete user | `users:delete` |
| List users | `users:list` |
| Enable user | `users:enable` |
| Disable user | `users:disable` |

See [permissions/README.md](../permissions/README.md) for the full permission reference.

## Error Types

Source: `pkg/user/errors.go`

| Error Type | Condition |
|------------|-----------|
| `UserNotFoundError` | User ID does not exist in the store |
| `UsernameExistsError` | Username already taken within the tenant |
| `UserDisabledError` | Operation attempted on a disabled user |
| `SystemUserImmutableError` | Attempted deletion of a system user |
| `StoreError` | Underlying KVStore operation failure (wraps cause) |
| `ValidationError` | Request field validation failure |

All error types implement the `error` interface. `StoreError` also implements `Unwrap()` for `errors.Is` and `errors.As` compatibility.

## SDK Usage

```go
import sdk "github.com/jeremyhahn/go-qrdb/sdk/go"

client, err := sdk.NewClient(
    sdk.WithTransport("grpc"),
    sdk.WithAddress("localhost:63003"),
    sdk.WithCertFile("/etc/qrdb/admin-cert.pem"),
    sdk.WithKeyFile("/etc/qrdb/admin-key.pem"),
)
defer client.Close()

// Create a user
user, err := client.Users().Create(ctx, &transport.CreateUserRequest{
    Username: "alice",
    TenantID: "system",
    RoleIDs:  []string{readwriteRoleID},
})

// List users
resp, err := client.Users().List(ctx, &transport.ListUsersRequest{
    TenantID: "system",
})
for _, u := range resp.Users {
    fmt.Println(u.Username, u.Enabled)
}
```

## See Also

- [roles/README.md](../roles/README.md) -- RBAC roles assigned to users
- [certs/README.md](../certs/README.md) -- Certificate generation for user identity
- [auth/README.md](../auth/README.md) -- mTLS authentication middleware
- [permissions/README.md](../permissions/README.md) -- Fine-grained permission constants
- [seal/README.md](../seal/README.md) -- Barrier lifecycle affecting cert operations
