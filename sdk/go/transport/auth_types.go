// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package transport

import (
	"strconv"
	"time"
)

// ---------------------------------------------------------------------------
// User management types
// ---------------------------------------------------------------------------

// User represents a passwordless user entity. Authentication is exclusively
// via mTLS X.509 certificates.
//
// The ID is a uint64 Snowflake that exceeds JavaScript's
// Number.MAX_SAFE_INTEGER, so it crosses the JSON wire as a string
// (proto3 JSON convention) to avoid precision loss in JS clients. The
// json tags mirror the REST server's snake_case wire contract.
type User struct {
	ID       uint64 `json:"id,string"`
	Username string `json:"username"`
	// Email is the canonical tenant-unique login key. Every layer above qrdb
	// keys a user by this — WebAuthn credential handle, session subject, CLI
	// identifier. Empty only for system users.
	Email string `json:"email,omitempty"`
	// Name is the display name. Presentation only; never a lookup key.
	Name     string   `json:"name,omitempty"`
	TenantID string   `json:"tenant_id"`
	RoleIDs  []string `json:"role_ids"`
	System   bool     `json:"system"`
	// Lifecycle is the canonical and only account state: pending, active,
	// suspended, locked, disabled or deleting. Only active can authenticate.
	Lifecycle string `json:"lifecycle,omitempty"`
	// AuthMethods are the permitted authentication methods ("webauthn",
	// "piv"). A policy statement, distinct from enrolled factors.
	AuthMethods []string `json:"auth_methods,omitempty"`
	// LastAuth is the last successful authentication, RFC 3339. Empty when the
	// account has never authenticated.
	LastAuth string `json:"last_auth,omitempty"`
	// CertBindings are the mTLS keys enrolled on the account, in the order
	// they were enrolled. The set is what decides the account's mTLS
	// admission: once it is non-empty, only a certificate matching one of
	// them authenticates as the account. It is listed in full, EXPIRED
	// bindings included — an expired binding no longer authenticates but
	// still holds the account in that enforcing posture, so omitting it
	// would misrepresent why the account refuses a certificate. A thumbprint
	// read here is byte-identical to the one enrolled and can be passed
	// straight to RemoveCertBinding. Empty when none are enrolled.
	CertBindings []CertBinding `json:"cert_bindings,omitempty"`
	// RoleBindings grants roles within a tenant scope (tenant-scoped RBAC),
	// additive alongside RoleIDs (system-scoped roles).
	RoleBindings []RoleBinding `json:"role_bindings,omitempty"`
	// Attributes carries attribute-based access-control (ABAC) key/value pairs
	// evaluated by policy.
	Attributes map[string]string `json:"attributes,omitempty"`
	// Factors lists the account's enrolled auth-factor metadata. The Data
	// blob a factor interprets during verification NEVER crosses this or any
	// other wire, on read or write (go-qrdb#192/#197) — only the registered
	// pkg/authfactors.Factor may see it.
	Factors []FactorMetadata `json:"factors,omitempty"`
	// MFAEnabled is DERIVED, never stored: true iff the account has at least
	// one enrolled factor. An enrolled factor is a fact about the account's
	// credentials, not a policy statement that MFA is REQUIRED for it — that
	// bit, if ever needed, belongs in policy.
	MFAEnabled bool `json:"mfa_enabled"`
	// MFAMethods lists the distinct pkg/authfactors names of the account's
	// currently enrolled factors, in enrollment order. Empty when MFAEnabled
	// is false.
	MFAMethods []string `json:"mfa_methods,omitempty"`
	// MFAEnrolledAt is when the account's FIRST currently-enrolled factor was
	// added, RFC 3339. Empty when MFAEnabled is false.
	MFAEnrolledAt string `json:"mfa_enrolled_at,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// RoleBinding grants a role within a tenant scope — the tenant-scoped RBAC
// wire projection of pkg/user.RoleBinding.
type RoleBinding struct {
	RoleID   string `json:"role_id"`
	TenantID string `json:"tenant_id"`
}

// FactorMetadata is the wire projection of a pkg/user.FactorEnrollment with
// its opaque Data blob omitted. qrdb never exposes Data over any transport;
// only the registered pkg/authfactors.Factor interprets it, in-process.
type FactorMetadata struct {
	ID         string `json:"id"`
	FactorName string `json:"factor_name"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// CertBinding is one mTLS key enrolled on a user: the hash the presented
// certificate is matched by, what the hash is over, and when the binding stops
// authenticating. The thumbprint is a hash of PUBLIC material, so listing it
// discloses nothing; it is the handle the withdrawal verb takes.
type CertBinding struct {
	// Thumbprint is the base64url SHA-256 the certificate is matched by — of
	// its SubjectPublicKeyInfo for the canonical kind, of the whole DER for
	// a full-certificate pin.
	Thumbprint string `json:"thumbprint"`
	// Kind is what the thumbprint is over: one of the CertBindingKind*
	// labels.
	Kind string `json:"kind"`
	// NotAfter is when the binding stops authenticating, RFC 3339. Empty
	// when the binding does not expire on its own.
	NotAfter string `json:"not_after,omitempty"`
}

// The labels a CertBinding.Kind carries. They are the wire form of
// pkg/user's CertBindingKind and are ordered as that enumeration is, which
// is what CertBindingKindLabel relies on.
const (
	// CertBindingKindSPKIThumbprint matches the certificate's
	// SubjectPublicKeyInfo: the canonical kind, which survives a renewal for
	// the same key.
	CertBindingKindSPKIThumbprint = "spki_thumbprint"
	// CertBindingKindFullCertFingerprint matches the whole certificate DER:
	// exact-certificate pinning, which a renewal invalidates.
	CertBindingKindFullCertFingerprint = "full_cert_fingerprint"
	// CertBindingKindPIVAttested marks a binding established through PIV
	// attestation.
	CertBindingKindPIVAttested = "piv_attested"
)

// certBindingKindLabels is indexed by pkg/user.CertBindingKind's ordinal.
var certBindingKindLabels = [...]string{
	CertBindingKindSPKIThumbprint,
	CertBindingKindFullCertFingerprint,
	CertBindingKindPIVAttested,
}

// CertBindingKindLabel returns the wire label for a pkg/user.CertBindingKind
// ordinal. An ordinal this build does not know — a newer server's kind
// reaching an older client — is rendered "unknown(<ordinal>)" so it is
// reported as unknown rather than as some other kind, and identically by
// every transport: the server projections and the MCP client's decode of the
// domain record both come here.
func CertBindingKindLabel(ordinal int) string {
	if ordinal < 0 || ordinal >= len(certBindingKindLabels) {
		return "unknown(" + strconv.Itoa(ordinal) + ")"
	}
	return certBindingKindLabels[ordinal]
}

// CreateUserRequest holds parameters for creating a user.
type CreateUserRequest struct {
	Username string `json:"username"`
	// Email is the canonical tenant-unique login key. Empty is permitted only
	// for system users, which authenticate by other means.
	Email string `json:"email,omitempty"`
	// Name is the display name. Presentation only.
	Name string `json:"name,omitempty"`
	// WithoutEmailLoginKey creates a user with no email login key (a
	// certificate-only or bootstrap identity). Required to omit Email.
	WithoutEmailLoginKey bool     `json:"without_email_login_key,omitempty"`
	TenantID             string   `json:"tenant_id,omitempty"`
	RoleIDs              []string `json:"role_ids,omitempty"`
	// AuthMethods are the initial permitted authentication methods. Empty
	// states no restriction.
	AuthMethods []string `json:"auth_methods,omitempty"`
	// Lifecycle is the state to create in — pending, active or disabled only.
	// Empty defaults to active, or pending when Pending is set.
	Lifecycle string `json:"lifecycle,omitempty"`
	// Pending is the shorthand for lifecycle "pending". Setting both is
	// refused unless they agree.
	Pending bool `json:"pending,omitempty"`
}

// UpdateUserRequest holds parameters for updating a user.
type UpdateUserRequest struct {
	ID       uint64 `json:"id,string"`
	Username string `json:"username,omitempty"`
	// Email replaces the login key when non-empty; uniqueness is re-validated
	// within the tenant and the secondary index moves with it.
	Email string `json:"email,omitempty"`
	// Name replaces the display name when non-empty.
	Name    string   `json:"name,omitempty"`
	RoleIDs []string `json:"role_ids,omitempty"`
	// AuthMethods replaces the set when NON-NIL. Nil leaves it alone; an empty
	// non-nil slice clears it, which is how a method restriction is lifted.
	//
	// NO `omitempty`, deliberately. omitempty drops an empty slice from the
	// encoded body, which would make "clear" indistinguishable from "leave
	// alone" on every JSON transport — a clear that silently did nothing. Nil
	// encodes as `null` and decodes back to nil, so the three-way distinction
	// (absent / null / []) survives the wire intact.
	AuthMethods []string `json:"auth_methods"`
	// Lifecycle transitions the account when non-empty. Checked against the
	// transition table; nothing moves out of "deleting".
	Lifecycle string `json:"lifecycle,omitempty"`
	// LastAuth raises the last-authentication high-water mark when non-empty.
	// RFC 3339. A value at or before the stored one writes nothing.
	LastAuth string `json:"last_auth,omitempty"`
	// RoleBindings replaces the tenant-scoped role bindings when NON-NIL. No
	// `omitempty`, for the same nil-vs-empty reason as AuthMethods: absent
	// leaves the set alone, `[]` clears it.
	RoleBindings []RoleBinding `json:"role_bindings"`
	// Attributes replaces the ABAC attributes when NON-NIL. No `omitempty`,
	// same reason: absent leaves it alone, `{}` clears it.
	Attributes map[string]string `json:"attributes"`
}

// ListUsersRequest holds parameters for listing users.
type ListUsersRequest struct {
	TenantID string `json:"tenant_id,omitempty"`
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"page_size,omitempty"`
}

// ListUsersResponse holds the paginated user list.
type ListUsersResponse struct {
	Users   []*User `json:"users"`
	Total   int     `json:"total"`
	HasMore bool    `json:"has_more"`
}

// ---------------------------------------------------------------------------
// Role management types
// ---------------------------------------------------------------------------

// Role represents an RBAC role entity. Roles aggregate fine-grained
// permissions and can be assigned to users.
type Role struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenant_id"`
	Permissions []string `json:"permissions"`
	System      bool     `json:"system"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`

	// RetiredPermissions lists the members of Permissions that the server's
	// build no longer defines. It is derived on read from the live permission
	// registry and never stored, so it reports drift between what the tenant
	// was seeded with and what the server now grants — visible on an ordinary
	// listing, BEFORE ReconcileBuiltInRoles is run to repair it.
	RetiredPermissions []string `json:"retired_permissions,omitempty"`
}

// CreateRoleRequest holds parameters for creating a role.
type CreateRoleRequest struct {
	// ID is an optional pre-assigned deterministic identifier. When empty,
	// the server generates a random UUID, preserving legacy behavior.
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenant_id,omitempty"`
	Permissions []string `json:"permissions"`
}

// UpdateRoleRequest holds parameters for updating a role.
type UpdateRoleRequest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
}

// ListRolesRequest holds parameters for listing roles.
type ListRolesRequest struct {
	TenantID string `json:"tenant_id,omitempty"`
	Page     int    `json:"page,omitempty"`
	PageSize int    `json:"page_size,omitempty"`
}

// ListRolesResponse holds the paginated role list.
type ListRolesResponse struct {
	Roles   []*Role `json:"roles"`
	Total   int     `json:"total"`
	HasMore bool    `json:"has_more"`
}

// AssignRoleRequest holds parameters for assigning a role to a user.
//
// The user ID crosses the JSON wire as a string (proto3 JSON convention)
// because uint64 Snowflakes exceed JavaScript's Number.MAX_SAFE_INTEGER.
type AssignRoleRequest struct {
	UserID uint64 `json:"user_id,string"`
	RoleID string `json:"role_id"`
}

// UnassignRoleRequest holds parameters for unassigning a role from a user.
type UnassignRoleRequest struct {
	UserID uint64 `json:"user_id,string"`
	RoleID string `json:"role_id"`
}

// PermissionCategory groups related permissions under a display name and
// logical group for UI rendering.
type PermissionCategory struct {
	Name        string   `json:"name"`
	Group       string   `json:"group"`
	Permissions []string `json:"permissions"`
}

// ListPermissionsResponse holds all permission categories.
type ListPermissionsResponse struct {
	Categories []*PermissionCategory `json:"categories"`
}

// GetRoleByNameRequest holds parameters for retrieving a role by its
// tenant-scoped name.
type GetRoleByNameRequest struct {
	TenantID string `json:"tenant_id,omitempty"`
	Name     string `json:"name"`
}

// ResolvePermissionsRequest holds the role IDs whose effective permissions
// should be resolved.
type ResolvePermissionsRequest struct {
	RoleIDs []string `json:"role_ids"`
}

// ResolvePermissionsResponse holds the resolved union of effective
// permissions for the requested roles.
type ResolvePermissionsResponse struct {
	Permissions []string `json:"permissions"`
}

// ReconcileRolesRequest names the tenant whose built-in roles are to be
// converged onto the server build's definitions.
type ReconcileRolesRequest struct {
	// TenantID is the tenant to reconcile. Empty means the caller's own
	// effective tenant.
	TenantID string `json:"tenant_id,omitempty"`
}

// RoleReconcileResult records what reconciliation changed for one built-in
// role. Only changed roles are reported.
type RoleReconcileResult struct {
	RoleID string `json:"role_id"`
	Name   string `json:"name"`

	// Created is true when the built-in was absent from the tenant and was
	// seeded. Added then holds its whole permission set.
	Created bool `json:"created"`

	// Added lists the permissions the build grants that the stored role did
	// not carry. Sorted.
	Added []string `json:"added,omitempty"`

	// Removed lists the permissions the stored role carried that the build no
	// longer grants. Sorted.
	Removed []string `json:"removed,omitempty"`
}

// ReconcileRolesResponse is the per-tenant reconciliation report. An empty
// Roles slice means the tenant already agreed with the build and nothing was
// written.
type ReconcileRolesResponse struct {
	TenantID string                `json:"tenant_id"`
	Roles    []RoleReconcileResult `json:"roles,omitempty"`
}

// ---------------------------------------------------------------------------
// Certificate management types
// ---------------------------------------------------------------------------

// IssueCertRequest asks for a certificate, from a certificate signing
// request, for an existing account of the caller's own tenant. The caller
// keeps its private key: the server only ever sees the CSR, and the
// certificate names the account whatever the CSR's Subject says.
type IssueCertRequest struct {
	Username string `json:"username"`
	CSRPEM   string `json:"csrPEM"`
	// TTL is a Go duration; empty is the issuer's default.
	TTL string `json:"ttl,omitempty"`
}

// EnrollCertRequest enrols a bring-your-own certificate on an existing
// account of the caller's own tenant. CertPEM holds the leaf first; any
// further certificates in it, and all of ChainPEM, are offered as
// intermediates. It must chain to one of the tenant's trust anchors.
type EnrollCertRequest struct {
	Username string `json:"username"`
	CertPEM  string `json:"certPEM"`
	ChainPEM string `json:"chainPEM,omitempty"`
}

// EnrolledCert is a certificate now bound to an account. There is no key:
// the requester keeps its own. Serial is empty for a bring-your-own
// certificate, which the cluster did not issue.
type EnrolledCert struct {
	UserID     uint64    `json:"userID,string"`
	Username   string    `json:"username"`
	TenantID   string    `json:"tenantID"`
	CertPEM    string    `json:"certPEM"`
	Serial     string    `json:"serial,omitempty"`
	Thumbprint string    `json:"thumbprint"`
	NotAfter   time.Time `json:"notAfter"`
}

// CertRecord is the metadata for an issued certificate.
type CertRecord struct {
	Serial    string `json:"serial"`
	Username  string `json:"username"`
	TenantID  string `json:"tenantID"`
	IssuedAt  string `json:"issuedAt"`
	ExpiresAt string `json:"expiresAt"`
	Revoked   bool   `json:"revoked"`
}

// ListCertsResponse holds a list of issued certificate records.
type ListCertsResponse struct {
	Certs []*CertRecord `json:"certs"`
}

// RevokeResult reports what RevokeCert did, distinguishing a revocation of
// a certificate the target node holds a record for from a deny-by-serial
// revocation of one it does not -- both are successes, so the distinction
// is carried here rather than in the error return. It mirrors
// cert.RevokeResult at the wire boundary.
type RevokeResult struct {
	Serial      string `json:"serial"`
	RecordFound bool   `json:"recordFound"`
}
