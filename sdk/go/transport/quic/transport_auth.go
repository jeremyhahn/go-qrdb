// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package quic

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.UserClient = (*Transport)(nil)
	_ transport.RoleClient = (*Transport)(nil)
	_ transport.CertClient = (*Transport)(nil)
)

// ---------------------------------------------------------------------------
// User wire types
// ---------------------------------------------------------------------------

// createUserWireRequest is the JSON wire payload for user.Create.
type createUserWireRequest struct {
	Username             string   `json:"username"`
	Email                string   `json:"email,omitempty"`
	Name                 string   `json:"name,omitempty"`
	WithoutEmailLoginKey bool     `json:"withoutEmailLoginKey,omitempty"`
	TenantID             string   `json:"tenantID"`
	RoleIDs              []string `json:"roleIDs"`
	GroupIDs             []string `json:"groupIDs,omitempty"`
	AuthMethods          []string `json:"authMethods,omitempty"`
	Lifecycle            string   `json:"lifecycle,omitempty"`
	Pending              bool     `json:"pending,omitempty"`
}

// getUserWireRequest is the JSON wire payload for user.Get.
type getUserWireRequest struct {
	ID uint64 `json:"id"`
}

// getUserByUsernameWireRequest is the JSON wire payload for
// user.GetByUsername.
// getUserByEmailWireRequest is the JSON wire payload for a get-by-email
// lookup — the canonical login-key resolution.
type getUserByEmailWireRequest struct {
	TenantID string `json:"tenantID"`
	Email    string `json:"email"`
}

type getUserByUsernameWireRequest struct {
	TenantID string `json:"tenantID"`
	Username string `json:"username"`
}

// getUserByCertThumbprintWireRequest is the JSON wire payload for
// user.GetByCertThumbprint.
type getUserByCertThumbprintWireRequest struct {
	Thumbprint string `json:"thumbprint"`
}

// addCertBindingWireRequest is the JSON wire payload for user.AddCertBinding.
type addCertBindingWireRequest struct {
	UserID       uint64 `json:"userID"`
	Thumbprint   string `json:"thumbprint"`
	NotAfterUnix int64  `json:"notAfterUnix"`
}

// updateUserWireRequest is the JSON wire payload for user.Update.
type updateUserWireRequest struct {
	ID       uint64   `json:"id"`
	Username string   `json:"username"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	RoleIDs  []string `json:"roleIDs"`
	// NO `omitempty` on GroupIDs/AuthMethods: it would drop an empty slice from
	// the payload, making "clear" indistinguishable from "leave alone" and a
	// revocation silently do nothing. Nil encodes as `null` and decodes back to
	// nil, so absent / null / [] all survive the wire.
	GroupIDs    []string `json:"groupIDs"`
	AuthMethods []string `json:"authMethods"`
	Lifecycle   string   `json:"lifecycle,omitempty"`
	LastAuth    string   `json:"lastAuth,omitempty"`
}

// deleteUserWireRequest is the JSON wire payload for user.Delete.
type deleteUserWireRequest struct {
	ID uint64 `json:"id"`
}

// listUsersWireRequest is the JSON wire payload for user.List.
type listUsersWireRequest struct {
	TenantID string `json:"tenantID"`
	Page     int    `json:"page"`
	PageSize int    `json:"pageSize"`
}

// enableUserWireRequest is the JSON wire payload for user.Enable.
type enableUserWireRequest struct {
	ID uint64 `json:"id"`
}

// disableUserWireRequest is the JSON wire payload for user.Disable.
type disableUserWireRequest struct {
	ID uint64 `json:"id"`
}

// userWireResponse is the JSON wire representation of a user entity
// returned by the server.
type userWireResponse struct {
	ID       uint64 `json:"id"`
	Username string `json:"username"`
	// Email is the canonical tenant-unique login key; Name is display only.
	// Both decode from the same JSON the user record marshals.
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	TenantID string   `json:"tenantID"`
	RoleIDs  []string `json:"roleIDs"`
	Enabled  bool     `json:"enabled"`
	System   bool     `json:"system"`
	// Lifecycle is the canonical account state (pending, active, suspended,
	// locked, disabled, deleting). Enabled is a derived projection of it.
	Lifecycle   string   `json:"lifecycle,omitempty"`
	GroupIDs    []string `json:"groupIDs,omitempty"`
	AuthMethods []string `json:"authMethods,omitempty"`
	LastAuth    string   `json:"lastAuth,omitempty"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

// listUsersWireResponse is the JSON wire payload returned by user.List.
type listUsersWireResponse struct {
	Users   []*userWireResponse `json:"users"`
	Total   int                 `json:"total"`
	HasMore bool                `json:"hasMore"`
}

// ---------------------------------------------------------------------------
// Role wire types
// ---------------------------------------------------------------------------

// createRoleWireRequest is the JSON wire payload for role.Create.
type createRoleWireRequest struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenantID"`
	Permissions []string `json:"permissions"`
}

// getRoleWireRequest is the JSON wire payload for role.Get.
type getRoleWireRequest struct {
	ID string `json:"id"`
}

// updateRoleWireRequest is the JSON wire payload for role.Update.
type updateRoleWireRequest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

// deleteRoleWireRequest is the JSON wire payload for role.Delete.
type deleteRoleWireRequest struct {
	ID string `json:"id"`
}

// listRolesWireRequest is the JSON wire payload for role.List.
type listRolesWireRequest struct {
	TenantID string `json:"tenantID"`
	Page     int    `json:"page"`
	PageSize int    `json:"pageSize"`
}

// assignRoleWireRequest is the JSON wire payload for role.Assign and
// role.Unassign.
type assignRoleWireRequest struct {
	UserID uint64 `json:"userID"`
	RoleID string `json:"roleID"`
}

// roleWireResponse is the JSON wire representation of a role entity
// returned by the server.
type roleWireResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenantID"`
	Permissions []string `json:"permissions"`
	System      bool     `json:"system"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`

	// RetiredPermissions are the members of Permissions the server build no
	// longer defines.
	RetiredPermissions []string `json:"retiredPermissions,omitempty"`
}

// listRolesWireResponse is the JSON wire payload returned by role.List.
type listRolesWireResponse struct {
	Roles   []*roleWireResponse `json:"roles"`
	Total   int                 `json:"total"`
	HasMore bool                `json:"hasMore"`
}

// permissionCategoryWireResponse is the JSON wire representation of a
// permission category for UI rendering.
type permissionCategoryWireResponse struct {
	Name        string   `json:"name"`
	Group       string   `json:"group"`
	Permissions []string `json:"permissions"`
}

// ---------------------------------------------------------------------------
// Cert wire types
// ---------------------------------------------------------------------------

// generateCertWireRequest is the JSON wire payload for cert.Generate.
type generateCertWireRequest struct {
	Username string `json:"username"`
	TenantID string `json:"tenantID"`
	TTL      string `json:"ttl"`
}

// exportCertWireRequest is the JSON wire payload for cert.Export. The
// client must supply the PEM-encoded certificate and key received from
// the generate endpoint, as the server does not retain private keys.
type exportCertWireRequest struct {
	CertPEM    string `json:"certPEM"`
	KeyPEM     string `json:"keyPEM"`
	Passphrase string `json:"passphrase"`
}

// revokeCertWireRequest is the JSON wire payload for cert.Revoke.
type revokeCertWireRequest struct {
	Serial string `json:"serial"`
}

// revokeCertWireResponse is the JSON wire representation of a revocation
// outcome. RecordFound distinguishes a certificate the server holds a
// record for from a deny-by-serial revocation of one it does not.
type revokeCertWireResponse struct {
	Status      string `json:"status"`
	RecordFound bool   `json:"recordFound"`
}

// listCertsWireRequest is the JSON wire payload for cert.List.
type listCertsWireRequest struct {
	TenantID string `json:"tenantID"`
}

// certBundleWireResponse is the JSON wire representation of a generated
// certificate bundle containing the PEM-encoded certificate, key, and
// serial number.
type certBundleWireResponse struct {
	CertPEM string `json:"certPEM"`
	KeyPEM  string `json:"keyPEM"`
	Serial  string `json:"serial"`
}

// exportCertWireResponse is the JSON wire representation of a PKCS#12
// export. The PKCS12 field contains the base64-encoded archive bytes.
type exportCertWireResponse struct {
	PKCS12 string `json:"pkcs12"`
}

// certRecordWireResponse is the JSON wire representation of an issued
// certificate record.
type certRecordWireResponse struct {
	Serial    string `json:"serial"`
	Username  string `json:"username"`
	TenantID  string `json:"tenantID"`
	IssuedAt  string `json:"issuedAt"`
	ExpiresAt string `json:"expiresAt"`
	Revoked   bool   `json:"revoked"`
}

// ---------------------------------------------------------------------------
// UserClient implementation
// ---------------------------------------------------------------------------

// CreateUser creates a new passwordless user via the QUIC user.Create
// method. The username must be unique within the tenant.
func (t *Transport) CreateUser(ctx context.Context, req *transport.CreateUserRequest) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(createUserWireRequest{
		Username:             req.Username,
		Email:                req.Email,
		Name:                 req.Name,
		WithoutEmailLoginKey: req.WithoutEmailLoginKey,
		TenantID:             req.TenantID,
		RoleIDs:              req.RoleIDs,
		GroupIDs:             req.GroupIDs,
		AuthMethods:          req.AuthMethods,
		Lifecycle:            req.Lifecycle,
		Pending:              req.Pending,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateUser", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "user.Create", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateUser", Err: err}
	}

	var wire userWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "CreateUser", Err: &UnmarshalError{Err: err}}
	}

	return wireToUser(&wire), nil
}

// GetUser retrieves a user by its globally unique ID via the QUIC
// user.Get method.
func (t *Transport) GetUser(ctx context.Context, id uint64) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(getUserWireRequest{ID: id})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUser", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "user.Get", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUser", Err: err}
	}

	var wire userWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "GetUser", Err: &UnmarshalError{Err: err}}
	}

	return wireToUser(&wire), nil
}

// GetUserByUsername retrieves a user by tenant-scoped username via the
// QUIC user.GetByUsername method.
func (t *Transport) GetUserByUsername(ctx context.Context, tenantID, username string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(getUserByUsernameWireRequest{TenantID: tenantID, Username: username})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUserByUsername", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "user.GetByUsername", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUserByUsername", Err: err}
	}

	var wire userWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "GetUserByUsername", Err: &UnmarshalError{Err: err}}
	}

	return wireToUser(&wire), nil
}

// GetUserByEmail resolves the canonical login key over QUIC.
func (t *Transport) GetUserByEmail(ctx context.Context, tenantID, email string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(getUserByEmailWireRequest{TenantID: tenantID, Email: email})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUserByEmail", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "user.GetByEmail", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUserByEmail", Err: err}
	}

	var wire userWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "GetUserByEmail", Err: &UnmarshalError{Err: err}}
	}

	return wireToUser(&wire), nil
}

// GetUserByCertThumbprint resolves a user by mTLS SPKI thumbprint via the
// QUIC user.GetByCertThumbprint method.
func (t *Transport) GetUserByCertThumbprint(ctx context.Context, thumbprint string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(getUserByCertThumbprintWireRequest{Thumbprint: thumbprint})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUserByCertThumbprint", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "user.GetByCertThumbprint", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUserByCertThumbprint", Err: err}
	}

	var wire userWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "GetUserByCertThumbprint", Err: &UnmarshalError{Err: err}}
	}

	return wireToUser(&wire), nil
}

// AddCertBinding binds an mTLS SPKI thumbprint to a user via the QUIC
// user.AddCertBinding method.
func (t *Transport) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(addCertBindingWireRequest{
		UserID:       userID,
		Thumbprint:   thumbprint,
		NotAfterUnix: notAfterUnix,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "AddCertBinding", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "user.AddCertBinding", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "AddCertBinding", Err: err}
	}

	var wire userWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "AddCertBinding", Err: &UnmarshalError{Err: err}}
	}

	return wireToUser(&wire), nil
}

// UpdateUser applies partial updates to an existing user via the QUIC
// user.Update method.
func (t *Transport) UpdateUser(ctx context.Context, req *transport.UpdateUserRequest) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(updateUserWireRequest{
		ID:          req.ID,
		Username:    req.Username,
		Email:       req.Email,
		Name:        req.Name,
		RoleIDs:     req.RoleIDs,
		GroupIDs:    req.GroupIDs,
		AuthMethods: req.AuthMethods,
		Lifecycle:   req.Lifecycle,
		LastAuth:    req.LastAuth,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateUser", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "user.Update", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateUser", Err: err}
	}

	var wire userWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "UpdateUser", Err: &UnmarshalError{Err: err}}
	}

	return wireToUser(&wire), nil
}

// DeleteUser removes a non-system user by ID via the QUIC user.Delete
// method.
func (t *Transport) DeleteUser(ctx context.Context, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := json.Marshal(deleteUserWireRequest{ID: id})
	if err != nil {
		return &transport.RequestError{Method: "DeleteUser", Err: &MarshalError{Err: err}}
	}

	_, err = t.call(ctx, "user.Delete", payload)
	if err != nil {
		return &transport.RequestError{Method: "DeleteUser", Err: err}
	}

	return nil
}

// ListUsers returns a paginated list of users for the given tenant via
// the QUIC user.List method.
func (t *Transport) ListUsers(ctx context.Context, req *transport.ListUsersRequest) (*transport.ListUsersResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(listUsersWireRequest{
		TenantID: req.TenantID,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "ListUsers", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "user.List", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListUsers", Err: err}
	}

	var wire listUsersWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "ListUsers", Err: &UnmarshalError{Err: err}}
	}

	users := make([]*transport.User, len(wire.Users))
	for i, w := range wire.Users {
		users[i] = wireToUser(w)
	}

	return &transport.ListUsersResponse{
		Users:   users,
		Total:   wire.Total,
		HasMore: wire.HasMore,
	}, nil
}

// EnableUser activates a user account via the QUIC user.Enable method.
func (t *Transport) EnableUser(ctx context.Context, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := json.Marshal(enableUserWireRequest{ID: id})
	if err != nil {
		return &transport.RequestError{Method: "EnableUser", Err: &MarshalError{Err: err}}
	}

	_, err = t.call(ctx, "user.Enable", payload)
	if err != nil {
		return &transport.RequestError{Method: "EnableUser", Err: err}
	}

	return nil
}

// DisableUser deactivates a user account via the QUIC user.Disable
// method.
func (t *Transport) DisableUser(ctx context.Context, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := json.Marshal(disableUserWireRequest{ID: id})
	if err != nil {
		return &transport.RequestError{Method: "DisableUser", Err: &MarshalError{Err: err}}
	}

	_, err = t.call(ctx, "user.Disable", payload)
	if err != nil {
		return &transport.RequestError{Method: "DisableUser", Err: err}
	}

	return nil
}

// ---------------------------------------------------------------------------
// RoleClient implementation
// ---------------------------------------------------------------------------

// CreateRole creates a new role with a generated UUID via the QUIC
// role.Create method. The name must be unique within the tenant.
func (t *Transport) CreateRole(ctx context.Context, req *transport.CreateRoleRequest) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(createRoleWireRequest{
		ID:          req.ID,
		Name:        req.Name,
		TenantID:    req.TenantID,
		Permissions: req.Permissions,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateRole", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "role.Create", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "CreateRole", Err: err}
	}

	var wire roleWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "CreateRole", Err: &UnmarshalError{Err: err}}
	}

	return wireToRole(&wire), nil
}

// GetRole retrieves a role by its globally unique ID via the QUIC
// role.Get method.
func (t *Transport) GetRole(ctx context.Context, id string) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(getRoleWireRequest{ID: id})
	if err != nil {
		return nil, &transport.RequestError{Method: "GetRole", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "role.Get", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetRole", Err: err}
	}

	var wire roleWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "GetRole", Err: &UnmarshalError{Err: err}}
	}

	return wireToRole(&wire), nil
}

// UpdateRole applies partial updates to an existing role via the QUIC
// role.Update method.
func (t *Transport) UpdateRole(ctx context.Context, req *transport.UpdateRoleRequest) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(updateRoleWireRequest{
		ID:          req.ID,
		Name:        req.Name,
		Permissions: req.Permissions,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateRole", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "role.Update", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "UpdateRole", Err: err}
	}

	var wire roleWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "UpdateRole", Err: &UnmarshalError{Err: err}}
	}

	return wireToRole(&wire), nil
}

// DeleteRole removes a non-system role by ID via the QUIC role.Delete
// method.
func (t *Transport) DeleteRole(ctx context.Context, id string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := json.Marshal(deleteRoleWireRequest{ID: id})
	if err != nil {
		return &transport.RequestError{Method: "DeleteRole", Err: &MarshalError{Err: err}}
	}

	_, err = t.call(ctx, "role.Delete", payload)
	if err != nil {
		return &transport.RequestError{Method: "DeleteRole", Err: err}
	}

	return nil
}

// ListRoles returns a paginated list of roles for the given tenant via
// the QUIC role.List method.
func (t *Transport) ListRoles(ctx context.Context, req *transport.ListRolesRequest) (*transport.ListRolesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(listRolesWireRequest{
		TenantID: req.TenantID,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "ListRoles", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "role.List", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListRoles", Err: err}
	}

	var wire listRolesWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "ListRoles", Err: &UnmarshalError{Err: err}}
	}

	roles := make([]*transport.Role, len(wire.Roles))
	for i, w := range wire.Roles {
		roles[i] = wireToRole(w)
	}

	return &transport.ListRolesResponse{
		Roles:   roles,
		Total:   wire.Total,
		HasMore: wire.HasMore,
	}, nil
}

// AssignRole assigns a role to a user via the QUIC role.Assign method.
func (t *Transport) AssignRole(ctx context.Context, userID uint64, roleID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := json.Marshal(assignRoleWireRequest{
		UserID: userID,
		RoleID: roleID,
	})
	if err != nil {
		return &transport.RequestError{Method: "AssignRole", Err: &MarshalError{Err: err}}
	}

	_, err = t.call(ctx, "role.Assign", payload)
	if err != nil {
		return &transport.RequestError{Method: "AssignRole", Err: err}
	}

	return nil
}

// UnassignRole removes a role assignment from a user via the QUIC
// role.Unassign method.
func (t *Transport) UnassignRole(ctx context.Context, userID uint64, roleID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload, err := json.Marshal(assignRoleWireRequest{
		UserID: userID,
		RoleID: roleID,
	})
	if err != nil {
		return &transport.RequestError{Method: "UnassignRole", Err: &MarshalError{Err: err}}
	}

	_, err = t.call(ctx, "role.Unassign", payload)
	if err != nil {
		return &transport.RequestError{Method: "UnassignRole", Err: err}
	}

	return nil
}

// ListPermissions returns all available permission categories for UI
// rendering via the QUIC role.ListPermissions method.
func (t *Transport) ListPermissions(ctx context.Context) (*transport.ListPermissionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	data, err := t.call(ctx, "role.ListPermissions", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListPermissions", Err: err}
	}

	// The server returns a raw JSON array of permission categories.
	var wire []*permissionCategoryWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "ListPermissions", Err: &UnmarshalError{Err: err}}
	}

	cats := make([]*transport.PermissionCategory, len(wire))
	for i, w := range wire {
		cats[i] = &transport.PermissionCategory{
			Name:        w.Name,
			Group:       w.Group,
			Permissions: w.Permissions,
		}
	}

	return &transport.ListPermissionsResponse{Categories: cats}, nil
}

// ---------------------------------------------------------------------------
// CertClient implementation
// ---------------------------------------------------------------------------

// GenerateCert creates a client certificate signed by the internal CA
// via the QUIC cert.Generate method.
func (t *Transport) GenerateCert(ctx context.Context, req *transport.GenerateCertRequest) (*transport.CertBundle, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(generateCertWireRequest{
		Username: req.Username,
		TenantID: req.TenantID,
		TTL:      req.TTL,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "GenerateCert", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "cert.Generate", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "GenerateCert", Err: err}
	}

	var wire certBundleWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "GenerateCert", Err: &UnmarshalError{Err: err}}
	}

	return &transport.CertBundle{
		CertPEM: wire.CertPEM,
		KeyPEM:  wire.KeyPEM,
		Serial:  wire.Serial,
	}, nil
}

// ExportCert exports a certificate and key as a PKCS#12 archive for
// browser import via the QUIC cert.Export method.
func (t *Transport) ExportCert(ctx context.Context, req *transport.ExportCertRequest) (*transport.ExportCertResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(exportCertWireRequest{
		CertPEM:    req.CertPEM,
		KeyPEM:     req.KeyPEM,
		Passphrase: req.Passphrase,
	})
	if err != nil {
		return nil, &transport.RequestError{Method: "ExportCert", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "cert.Export", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "ExportCert", Err: err}
	}

	var wire exportCertWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "ExportCert", Err: &UnmarshalError{Err: err}}
	}

	return &transport.ExportCertResponse{PKCS12: wire.PKCS12}, nil
}

// RevokeCert marks a certificate serial as revoked via the QUIC
// cert.Revoke method, and reports whether the server held a certificate
// record for it.
func (t *Transport) RevokeCert(ctx context.Context, serial string) (*transport.RevokeResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(revokeCertWireRequest{Serial: serial})
	if err != nil {
		return nil, &transport.RequestError{Method: "RevokeCert", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "cert.Revoke", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "RevokeCert", Err: err}
	}

	var wire revokeCertWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "RevokeCert", Err: &UnmarshalError{Err: err}}
	}

	return &transport.RevokeResult{Serial: serial, RecordFound: wire.RecordFound}, nil
}

// ListCerts lists all issued client certificates for a tenant via the
// QUIC cert.List method.
func (t *Transport) ListCerts(ctx context.Context, tenantID string) (*transport.ListCertsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload, err := json.Marshal(listCertsWireRequest{TenantID: tenantID})
	if err != nil {
		return nil, &transport.RequestError{Method: "ListCerts", Err: &MarshalError{Err: err}}
	}

	data, err := t.call(ctx, "cert.List", payload)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListCerts", Err: err}
	}

	// The server returns a raw JSON array of certificate records.
	var wire []*certRecordWireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, &transport.RequestError{Method: "ListCerts", Err: &UnmarshalError{Err: err}}
	}

	certs := make([]*transport.CertRecord, len(wire))
	for i, w := range wire {
		certs[i] = &transport.CertRecord{
			Serial:    w.Serial,
			Username:  w.Username,
			TenantID:  w.TenantID,
			IssuedAt:  w.IssuedAt,
			ExpiresAt: w.ExpiresAt,
			Revoked:   w.Revoked,
		}
	}

	return &transport.ListCertsResponse{Certs: certs}, nil
}

// ---------------------------------------------------------------------------
// Wire conversion helpers
// ---------------------------------------------------------------------------

// wireToUser converts a user wire response to the transport User type.
func wireToUser(w *userWireResponse) *transport.User {
	if w == nil {
		return nil
	}
	return &transport.User{
		ID:          w.ID,
		Username:    w.Username,
		Email:       w.Email,
		Name:        w.Name,
		TenantID:    w.TenantID,
		RoleIDs:     w.RoleIDs,
		Enabled:     w.Enabled,
		System:      w.System,
		Lifecycle:   w.Lifecycle,
		GroupIDs:    w.GroupIDs,
		AuthMethods: w.AuthMethods,
		LastAuth:    w.LastAuth,
		CreatedAt:   w.CreatedAt,
		UpdatedAt:   w.UpdatedAt,
	}
}

// wireToRole converts a role wire response to the transport Role type.
func wireToRole(w *roleWireResponse) *transport.Role {
	if w == nil {
		return nil
	}
	return &transport.Role{
		ID:                 w.ID,
		Name:               w.Name,
		TenantID:           w.TenantID,
		Permissions:        w.Permissions,
		System:             w.System,
		CreatedAt:          w.CreatedAt,
		UpdatedAt:          w.UpdatedAt,
		RetiredPermissions: w.RetiredPermissions,
	}
}

// ---------------------------------------------------------------------------
// Auth error types
// ---------------------------------------------------------------------------

// MarshalError is returned when JSON encoding of a request payload fails.
type MarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *MarshalError) Error() string {
	return "quic: failed to marshal request: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MarshalError) Unwrap() error {
	return e.Err
}

// UnmarshalError is returned when JSON decoding of a response payload
// fails.
type UnmarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *UnmarshalError) Error() string {
	return "quic: failed to unmarshal response: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *UnmarshalError) Unwrap() error {
	return e.Err
}
