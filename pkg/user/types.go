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

// Package user provides user domain types, a service interface, and a
// KVStore-backed store with secondary indexes. Authentication is
// exclusively via mTLS X.509 certificates (passwordless).
package user

import "time"

// User represents a passwordless user entity in the system.
// Authentication is exclusively via mTLS X.509 certificates.
// Certificate CN is implicitly set to Username, OU to TenantID.
type User struct {
	// ID is the globally unique Snowflake identifier for this user. It is
	// assigned by the store at creation time (server-side) and is never zero
	// for a persisted user; zero is the invalid/sentinel value.
	ID uint64 `json:"id"`

	// Username is the human-readable login name, unique within a tenant.
	Username string `json:"username"`

	// Email is the CANONICAL LOGIN KEY for a human user, unique within a tenant.
	// It is what a login ceremony resolves a verified factor to, and what every
	// layer above this one keys a user by — the WebAuthn credential handle, the
	// session subject, the CLI's user identifier.
	//
	// It is tenant-scoped rather than global on purpose: the same person may
	// legitimately hold an account in two tenants, and every login path already
	// supplies the tenant alongside the handle. A global index would forbid that
	// while buying nothing.
	//
	// It is a SEPARATE field from Username rather than a re-use of it because
	// system users (System == true) are named "admin", not an address, and
	// forcing an email shape on them would be a lie. Human users carry both; the
	// email is the one anything above qrdb keys on.
	//
	// Empty means the user has no email login key, and therefore cannot be
	// resolved by GetUserByEmail. That is a legitimate state — a bootstrap
	// admin, an mTLS-only service identity — but never an accident: CreateUser
	// refuses an empty email unless the caller sets
	// CreateUserRequest.WithoutEmailLoginKey, so the omission is always
	// something someone declared.
	Email string `json:"email,omitempty"`

	// Name is the human display name. Presentation only; never a lookup key.
	Name string `json:"name,omitempty"`

	// TenantID identifies the tenant this user belongs to.
	TenantID string `json:"tenantID"`

	// RoleIDs lists the roles assigned to this user.
	RoleIDs []string `json:"roleIDs"`

	// Lifecycle is the CANONICAL account state — pending, active, suspended,
	// locked, disabled or deleting. It is the single source of truth for
	// whether this account may authenticate; see CanAuthenticate.
	//
	// It is never empty on a record the store returns: a record written before
	// this field existed has its state derived from Enabled on decode (see
	// lifecycleFromEnabled) and carries the derived value from then on.
	Lifecycle Lifecycle `json:"lifecycle,omitempty"`

	// Enabled reports whether the user can authenticate.
	//
	// It is a DERIVED PROJECTION of Lifecycle — exactly Lifecycle == active —
	// maintained by the store on every write. It is retained because it is the
	// field consumers already read, and it never lies; it is simply coarser
	// than the state it projects, collapsing pending, suspended, locked,
	// disabled and deleting into a single false. Read Lifecycle when the
	// distinction matters, and never write Enabled directly: there is no
	// request field that sets it, because a second writable source of truth is
	// how a record ends up claiming to be locked and active at once.
	Enabled bool `json:"enabled"`

	// System marks built-in users that cannot be deleted.
	System bool `json:"system"`

	// GroupIDs lists the group memberships this user holds.
	//
	// qrdb stores the MEMBERSHIP EDGE and does not own a Group entity: the
	// values are opaque identifiers, exactly as RoleIDs holds role identifiers
	// without the user record owning the roles. A group's name, description and
	// the roles it confers belong to whichever layer performs the authorization
	// that consumes them — the same division qrdb already applies to Attributes,
	// which it stores while the ABAC policy that evaluates them lives above.
	GroupIDs []string `json:"groupIDs,omitempty"`

	// AuthMethods lists the authentication methods this user is permitted to
	// use ("webauthn", "piv", ...). It is a POLICY statement about what the
	// account may present, distinct from Factors, which records what it has
	// actually enrolled: an account may be permitted webauthn before any
	// authenticator is registered, and an enrollment left over from a method
	// since withdrawn must not become a way in.
	//
	// Empty means qrdb states no restriction — the layer performing the
	// ceremony decides. Values are opaque, lowercased and de-duplicated.
	AuthMethods []string `json:"authMethods,omitempty"`

	// LastAuth records the last successful authentication (UTC). Nil for an
	// account that has never authenticated.
	LastAuth *time.Time `json:"lastAuth,omitempty"`

	// RoleBindings grants roles within a tenant scope (tenant-scoped RBAC). A
	// control-plane user may hold, e.g., a tenant-admin binding for another
	// tenant. Additive alongside RoleIDs (system-scoped roles).
	RoleBindings []RoleBinding `json:"roleBindings,omitempty"`

	// Attributes carries attribute-based access-control (ABAC) key/value pairs
	// evaluated by policy.
	Attributes map[string]string `json:"attributes,omitempty"`

	// CertBindings binds mTLS client certificates to this user by SPKI
	// thumbprint — qrdb-owned mTLS identity. See cert_binding.go.
	CertBindings []CertBinding `json:"certBindings,omitempty"`

	// Factors holds extensible auth-factor enrollments (webauthn/fido2/piv),
	// registered via pkg/authfactors. qrdb stores them opaquely; the registered
	// factor interprets Data.
	Factors []FactorEnrollment `json:"factors,omitempty"`

	// CreatedAt records when the user was created (UTC).
	CreatedAt time.Time `json:"createdAt"`

	// UpdatedAt records the last modification time (UTC).
	UpdatedAt time.Time `json:"updatedAt"`
}

// CanAuthenticate reports whether this account is in a state that permits
// authentication. It is the check a login or credential-registration ceremony
// performs against the record it resolved.
//
// It applies the same two rules normalize does, so a record answers the same
// question the same way whether or not it has been through a decode:
//
//   - An ABSENT state falls back to Enabled. A value built before this field
//     existed — a record from an older node, or one a caller assembled by hand
//     for a fallback or a test double — carries no state, and reading that as
//     "cannot authenticate" would lock out every such account. Enabled IS the
//     state for a value that has no other.
//   - An UNRECOGNIZED state fails closed. A state written by a newer node
//     during a rolling upgrade must not be waved through because this build had
//     no case for it, and unlike an absent state there is no honest fallback:
//     the newer node meant something specific by it.
func (u *User) CanAuthenticate() bool {
	if u == nil {
		return false
	}
	if u.Lifecycle == "" {
		return lifecycleFromEnabled(u.Enabled).CanAuthenticate()
	}
	return u.Lifecycle.CanAuthenticate()
}

// normalize brings a decoded record to the canonical representation: it fills
// in a lifecycle state for records written before the field existed, and
// re-derives Enabled from it.
//
// It runs after EVERY decode rather than on a migration pass, because a
// migration pass would have to rewrite every user record to be correct and a
// derivation does not have to rewrite anything: the value is reconstructed
// identically on every read, and the record picks up the explicit field the
// next time it is written for some other reason.
func (u *User) normalize() {
	if u == nil {
		return
	}
	// ONLY an absent state is filled in. A state that is present but
	// unrecognized is left exactly as written — it came from a node running a
	// newer build, and overwriting it would destroy that node's record on the
	// next write while telling this one a story it made up. CanAuthenticate
	// already fails closed on it, so leaving it costs nothing but a coarser
	// Enabled.
	if u.Lifecycle == "" {
		u.Lifecycle = lifecycleFromEnabled(u.Enabled)
	}
	u.Enabled = u.Lifecycle.CanAuthenticate()
}

// CreateUserRequest holds parameters for creating a user.
type CreateUserRequest struct {
	// Username is the desired login name. Must not be empty.
	Username string

	// Email is the canonical login key. Unique within the tenant. Required
	// unless WithoutEmailLoginKey is set.
	Email string

	// WithoutEmailLoginKey creates a user that has no email login key and so
	// cannot be resolved by GetUserByEmail.
	//
	// It exists so the omission is DECLARED rather than defaulted. The contract
	// on User.Email said an empty email was valid "for system users only" while
	// CreateUser neither checked it nor set System — so an ordinary user could
	// be created with no entry in the canonical login index, and nothing said so
	// (Codex, PR #191). A store should not decide whether an identity needs an
	// email; it should refuse to let one go missing silently.
	//
	// Legitimate uses: the bootstrap/firstboot admin, and mTLS- or PIV-only
	// identities that authenticate by certificate.
	WithoutEmailLoginKey bool

	// Name is the display name. Optional; presentation only.
	Name string

	// TenantID identifies the tenant. Defaults to SystemTenantID when empty.
	TenantID string

	// RoleIDs are the initial roles to assign.
	RoleIDs []string

	// Pending, when true, creates the user in LifecyclePending: an account
	// awaiting activation, such as an invited user whose first credential is
	// bound at invitation redemption. EnableUser activates it. The zero value
	// preserves the existing create-active behavior.
	//
	// It predates Lifecycle and remains as the shorthand for the one state it
	// names. Setting both is allowed only when they agree; see Lifecycle.
	Pending bool

	// Lifecycle is the state to create the account in. Empty defers to
	// Pending — pending when set, active otherwise.
	//
	// Only pending, active and disabled may be created directly: suspended and
	// locked describe something that happened to an account that already
	// existed, and deleting describes one on its way out. Anything else is
	// refused with an InvalidLifecycleError naming what is allowed.
	//
	// Supplying both this and Pending is refused unless they agree, rather than
	// letting one silently win: a caller that sets Pending and then asks for
	// active is describing two different accounts and should be told so.
	Lifecycle Lifecycle

	// GroupIDs are the initial group memberships. Opaque identifiers; qrdb does
	// not own a Group entity (see User.GroupIDs).
	GroupIDs []string

	// AuthMethods are the initial permitted authentication methods. Empty
	// states no restriction.
	AuthMethods []string
}

// UpdateUserRequest holds parameters for updating a user.
type UpdateUserRequest struct {
	// ID is the user ID to update. Required (must be non-zero).
	ID uint64

	// Username replaces the current username when non-empty.
	Username string

	// Email replaces the current login key when non-empty. Uniqueness is
	// re-validated within the tenant and the secondary index is moved.
	Email string

	// Name replaces the display name when non-empty.
	Name string

	// RoleIDs replaces the current role set when non-nil.
	RoleIDs []string

	// RoleBindings replaces the tenant-scoped role bindings when non-nil.
	RoleBindings []RoleBinding

	// Attributes replaces the ABAC attributes when non-nil.
	Attributes map[string]string

	// CertBindings replaces the mTLS cert bindings when non-nil; the store
	// maintains the SPKI-thumbprint secondary index accordingly.
	CertBindings []CertBinding

	// Factors replaces the auth-factor enrollments when non-nil.
	Factors []FactorEnrollment

	// Lifecycle transitions the account when non-empty. The move is checked
	// against the transition table: a state the record cannot reach from where
	// it is fails with a LifecycleTransitionError rather than being written.
	//
	// Setting the state the record already holds is a no-op, not an error, so a
	// retried request is safe — including a retried deleting, which is what
	// makes a partially-completed deletion resumable. Nothing transitions out
	// of deleting to a different state.
	//
	// This is the field a caller writes before beginning a multi-step deletion:
	// once it reads deleting, CanAuthenticate is false and no concurrent
	// request can make it true again.
	Lifecycle Lifecycle

	// GroupIDs replaces the group memberships when non-nil. An empty non-nil
	// slice clears them.
	GroupIDs []string

	// AuthMethods replaces the permitted authentication methods when non-nil.
	// An empty non-nil slice clears the restriction.
	AuthMethods []string

	// LastAuth raises the last-successful-authentication timestamp when
	// non-nil. It is stored in UTC.
	//
	// It is a HIGH-WATER MARK, not a replacement: the committed value is
	// max(stored, requested), so a value at or before the one already recorded
	// writes nothing. A delayed or retried login therefore cannot roll an
	// account's last-seen time backwards, which is what everything reading it —
	// an operator auditing dormant accounts, an idle-expiry policy deciding what
	// to reap — depends on.
	//
	// A request that sets ONLY this field takes a dedicated merge path that
	// RETRIES a lost conditional commit instead of reporting
	// ConcurrentUpdateError. It can, because the merge is commutative: unlike
	// every other field, there is nothing stale for the caller to reconcile —
	// re-reading and recomputing the max is always correct. Combining it with
	// any other field makes the request an ordinary update again, with ordinary
	// lost-update semantics.
	LastAuth *time.Time
}

// ListUsersRequest holds parameters for listing users.
type ListUsersRequest struct {
	// TenantID filters users by tenant. Defaults to SystemTenantID when empty.
	TenantID string

	// Page is the 1-based page number. Zero or negative defaults to 1.
	Page int

	// PageSize is the maximum number of users per page. Zero or negative
	// defaults to defaultPageSize.
	PageSize int
}

// ListUsersResponse holds the paginated user list.
type ListUsersResponse struct {
	// Users is the page of user records.
	Users []*User `json:"users"`

	// Total is the total number of users matching the query.
	Total int `json:"total"`

	// HasMore is true when additional pages exist.
	HasMore bool `json:"hasMore"`
}
