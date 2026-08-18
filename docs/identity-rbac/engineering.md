# Identity and RBAC — Engineering Contract

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

## Exported surface

`sdk/go`:

```go
func (c *Client) Users()       transport.UserClient
func (c *Client) Roles()       transport.RoleClient
func (c *Client) Permissions() transport.PermissionClient  // nil if unsupported
func (c *Client) RBAC()        transport.RBACClient        // nil if unsupported
```

`Permissions()` and `RBAC()` return **nil** when the transport does not
implement the corresponding provider interface — nil-check them.

### User entity — `pkg/user/types.go`

```go
type User struct {
	ID           string             `json:"id"`           // globally unique UUID
	Username     string             `json:"username"`     // unique within tenant; maps to cert CN
	TenantID     string             `json:"tenantID"`     // maps to cert OU
	RoleIDs      []string           `json:"roleIDs"`      // system-scoped role UUIDs
	Enabled      bool               `json:"enabled"`
	System       bool               `json:"system"`       // built-in, cannot be deleted
	RoleBindings []RoleBinding      `json:"roleBindings"` // {RoleID, TenantID} — tenant-scoped RBAC
	Attributes   map[string]string  `json:"attributes"`   // ABAC
	CertBindings []CertBinding      `json:"certBindings"` // mTLS SPKI thumbprints
	Factors      []FactorEnrollment `json:"factors"`      // opaque; qrdb never interprets Data
	CreatedAt    time.Time          `json:"createdAt"`
	UpdatedAt    time.Time          `json:"updatedAt"`
}
```

### Cert binding — `pkg/user/cert_binding.go`

```go
type CertBindingKind int
const (
	CertBindingSPKIThumbprint      CertBindingKind = iota // canonical default
	CertBindingFullCertFingerprint                        // exact-cert pinning, NOT the default
	CertBindingPIVAttested                                // established via PIV attestation
)
```

`User.MatchesCert(cert)` is the authoritative check.

### Permission vocabulary — `pkg/permission`

```go
type Permission string
type Category struct {
	Name        string       // display name, e.g. "KV Service"
	Group       string       // logical grouping, e.g. "Data Services"
	Permissions []Permission
}

func Register(perms ...Permission)
func All() []Permission                    // sorted
func IsRegistered(p Permission) bool
func Parse(s string) (Permission, error)   // validates against the registry
func Resource(p Permission) string         // before the colon
func Action(p Permission) string           // after the colon
func Categories() []Category
func ByResource(resource string) []Permission
```

All permissions are registered by the `init()` in `pkg/permission/constants.go`,
which walks the category definitions and calls `Register` for each.

## Invariants

- **`Username` is unique within a tenant**, not globally. `ID` is the global
  UUID.
- **The SPKI thumbprint is base64url SHA-256 over `SubjectPublicKeyInfo`.**
  Computing it over the public key rather than the certificate is what makes a
  binding survive renewal.
- **`CertBindingSPKIThumbprint` is the canonical default.**
  `CertBindingFullCertFingerprint` pins an exact certificate and breaks on
  renewal — that is its purpose, and it is not the default.
- **`RoleBindings` are additive to `RoleIDs`**, not a replacement. Effective
  permissions are the union.
- **Effective permissions resolve through one PDP** on every transport;
  `ResolveRolePermissions` exposes that resolution for inspection.
- **`Parse` rejects unregistered permission strings.** The vocabulary cannot be
  extended at runtime by a caller.
- **`FactorEnrollment.Data` is opaque.** qrdb stores it and never interprets it;
  verification is the registered `authfactors.Factor`'s job.
- **`Enabled: false` blocks authentication** and is reversible; `System: true`
  records are undeletable.
- **Built-in role IDs are deterministic** UUID v5 — see
  [../firstboot/](../firstboot/engineering.md).

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Certificate presents no matching binding | authentication fails | **yes** |
| User disabled | authentication fails | **yes** |
| Unregistered permission string | `Parse` error | **yes** |
| Missing permission for the operation | permission denied | **yes** |
| Deleting a `System: true` user or a built-in role | refused | **yes** |
| Duplicate username within a tenant | refused | yes |
| `Permissions()` / `RBAC()` on an unsupporting transport | returns **nil** — callers must nil-check | — |

The nil returns are the sharp edge here: they are not error values, so an
unchecked call panics rather than failing cleanly.

## Persistence

- Users and roles live under the internal `__sys:` namespace, Raft-replicated,
  invisible to user KV enumeration.
- Roles are at `__sys:role/<id>` with deterministic identifiers for the built-in
  six.
- User lookup by certificate thumbprint is a secondary index —
  `GET /users/by-cert-thumbprint/{thumbprint}`.

**Compatibility**: the thumbprint derivation (base64url SHA-256 over SPKI) is
part of the on-disk contract. Changing it invalidates every existing binding.

## Test strategy

- **Unit**: `MatchesCert` for each `CertBindingKind`, thumbprint stability
  across certificate renewal for the same key, effective-permission resolution
  over `RoleIDs` ∪ `RoleBindings`, `Parse` rejecting unregistered strings, and
  the undeletability of system users and built-in roles.
- **Renewal stability is its own test** — it is the property the SPKI choice
  exists for.
- **Authorization tests are security tests**: every transport must reach the
  same decision for the same identity and permission.
- **Error tests** per function.

## Observability

Authentication and authorization decisions are audited through
[../audit/](../audit/), including the actor, the role at event time, the
resource, the action and the outcome (`success`, `failure:<reason>`,
`denied:<reason>`).
