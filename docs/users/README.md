<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# User Management

The `pkg/user/` package implements passwordless user management backed by mTLS X.509 certificates. There are no passwords, no JWT tokens, and no session cookies. Identity is derived exclusively from the client certificate presented during TLS handshake.

## Identity Model

Authentication is based on two fields extracted from the X.509 client certificate:

| Certificate Field | User Field | Purpose |
|-------------------|------------|---------|
| Subject CN (Common Name) | `Username` | Unique identity within a tenant |
| Subject OU (Organizational Unit) | `TenantID` | Tenant isolation boundary |

The mTLS authenticator (see [auth/README.md](../auth/README.md)) extracts these fields and constructs an `auth.Identity` that flows through the middleware chain.

## User Entity

Source: `pkg/user/types.go`

```go
type User struct {
    ID        string    `json:"id"`        // Globally unique UUID
    Username  string    `json:"username"`  // Unique within tenant, maps to cert CN
    TenantID  string    `json:"tenantID"`  // Maps to cert OU
    RoleIDs   []string  `json:"roleIDs"`   // Assigned role UUIDs
    Enabled   bool      `json:"enabled"`   // Whether the user can authenticate
    System    bool      `json:"system"`    // Built-in, cannot be deleted
    CreatedAt time.Time `json:"createdAt"` // UTC creation time
    UpdatedAt time.Time `json:"updatedAt"` // UTC last modification time
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

Source: `cmd/qrdbd/start.go`

On first start, the daemon bootstraps the initial admin identity:

1. **Default tenant** -- Creates tenant with ID `"default"`
2. **Admin user** -- Creates user with:
   - Username: `admin`
   - TenantID: `default`
   - RoleIDs: `[superadmin-role-uuid]`
   - System: `true`
   - Enabled: `true`
3. **Admin certificate** -- Generates a client certificate via the `CertProvider`:
   - CN: `admin`
   - OU: `default`
4. **Certificate output** -- Writes to the data directory:
   - `{dataDir}/admin-cert.pem`
   - `{dataDir}/admin-key.pem`

The admin user is marked as a system user and cannot be deleted.

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
    TenantID: "default",
    RoleIDs:  []string{readwriteRoleID},
})

// List users
resp, err := client.Users().List(ctx, &transport.ListUsersRequest{
    TenantID: "default",
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
