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

package embedded

import (
	"context"
	"encoding/base64"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertions.
var (
	_ transport.UserClient = (*Transport)(nil)
	_ transport.RoleClient = (*Transport)(nil)
	_ transport.CertClient = (*Transport)(nil)
)

// ---------------------------------------------------------------------------
// UserClient implementation
// ---------------------------------------------------------------------------

// CreateUser creates a new passwordless user via the embedded user service.
func (t *Transport) CreateUser(ctx context.Context, req *transport.CreateUserRequest) (*transport.User, error) {
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "CreateUser"}
	}
	u, err := t.userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             req.Username,
		Email:                req.Email,
		Name:                 req.Name,
		WithoutEmailLoginKey: req.WithoutEmailLoginKey,
		TenantID:             req.TenantID,
		RoleIDs:              req.RoleIDs,
		GroupIDs:             req.GroupIDs,
		AuthMethods:          req.AuthMethods,
		Lifecycle:            user.Lifecycle(req.Lifecycle),
		Pending:              req.Pending,
	})
	if err != nil {
		return nil, err
	}
	return userToTransport(u), nil
}

// GetUser retrieves a user by ID via the embedded user service.
func (t *Transport) GetUser(ctx context.Context, id uint64) (*transport.User, error) {
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "GetUser"}
	}
	u, err := t.userSvc.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	return userToTransport(u), nil
}

// GetUserByUsername retrieves a user by tenant-scoped username via the
// embedded user service.
func (t *Transport) GetUserByUsername(ctx context.Context, tenantID, username string) (*transport.User, error) {
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "GetUserByUsername"}
	}
	u, err := t.userSvc.GetUserByUsername(ctx, tenantID, username)
	if err != nil {
		return nil, err
	}
	return userToTransport(u), nil
}

// GetUserByEmail resolves the canonical login key through the embedded service.
func (t *Transport) GetUserByEmail(ctx context.Context, tenantID, email string) (*transport.User, error) {
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "GetUserByEmail"}
	}
	u, err := t.userSvc.GetUserByEmail(ctx, tenantID, email)
	if err != nil {
		return nil, err
	}
	return userToTransport(u), nil
}

// GetUserByCertThumbprint resolves a user by mTLS SPKI thumbprint via the
// embedded user service.
func (t *Transport) GetUserByCertThumbprint(ctx context.Context, thumbprint string) (*transport.User, error) {
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "GetUserByCertThumbprint"}
	}
	u, err := t.userSvc.UserByCertThumbprint(ctx, thumbprint)
	if err != nil {
		return nil, err
	}
	return userToTransport(u), nil
}

// AddCertBinding binds an mTLS SPKI thumbprint to a user via the embedded
// user service.
func (t *Transport) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*transport.User, error) {
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "AddCertBinding"}
	}
	u, err := t.userSvc.AddCertBinding(ctx, userID, thumbprint, notAfterUnix)
	if err != nil {
		return nil, err
	}
	return userToTransport(u), nil
}

// RemoveCertBinding withdraws an mTLS SPKI thumbprint from a user via the
// embedded user service.
func (t *Transport) RemoveCertBinding(ctx context.Context, userID uint64, thumbprint string) (*transport.User, error) {
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "RemoveCertBinding"}
	}
	u, err := t.userSvc.RemoveCertBinding(ctx, userID, thumbprint)
	if err != nil {
		return nil, err
	}
	return userToTransport(u), nil
}

// UpdateUser applies partial updates to a user via the embedded user service.
func (t *Transport) UpdateUser(ctx context.Context, req *transport.UpdateUserRequest) (*transport.User, error) {
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "UpdateUser"}
	}
	updateReq := &user.UpdateUserRequest{
		ID:          req.ID,
		Username:    req.Username,
		Email:       req.Email,
		Name:        req.Name,
		RoleIDs:     req.RoleIDs,
		GroupIDs:    req.GroupIDs,
		AuthMethods: req.AuthMethods,
		Lifecycle:   user.Lifecycle(req.Lifecycle),
	}
	if req.LastAuth != "" {
		at, perr := time.Parse(time.RFC3339, req.LastAuth)
		if perr != nil {
			return nil, &transport.RequestError{Method: "UpdateUser", Err: perr}
		}
		utc := at.UTC()
		updateReq.LastAuth = &utc
	}
	u, err := t.userSvc.UpdateUser(ctx, updateReq)
	if err != nil {
		return nil, err
	}
	return userToTransport(u), nil
}

// DeleteUser removes a non-system user by ID via the embedded user service.
func (t *Transport) DeleteUser(ctx context.Context, id uint64) error {
	if t.userSvc == nil {
		return &transport.NotImplementedError{Method: "DeleteUser"}
	}
	return t.userSvc.DeleteUser(ctx, id)
}

// ListUsers returns a paginated user list via the embedded user service.
func (t *Transport) ListUsers(ctx context.Context, req *transport.ListUsersRequest) (*transport.ListUsersResponse, error) {
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "ListUsers"}
	}
	resp, err := t.userSvc.ListUsers(ctx, &user.ListUsersRequest{
		TenantID: req.TenantID,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, err
	}
	users := make([]*transport.User, len(resp.Users))
	for i, u := range resp.Users {
		users[i] = userToTransport(u)
	}
	return &transport.ListUsersResponse{
		Users:   users,
		Total:   resp.Total,
		HasMore: resp.HasMore,
	}, nil
}

// EnableUser activates a user account via the embedded user service.
func (t *Transport) EnableUser(ctx context.Context, id uint64) error {
	if t.userSvc == nil {
		return &transport.NotImplementedError{Method: "EnableUser"}
	}
	return t.userSvc.EnableUser(ctx, id)
}

// DisableUser deactivates a user account via the embedded user service.
func (t *Transport) DisableUser(ctx context.Context, id uint64) error {
	if t.userSvc == nil {
		return &transport.NotImplementedError{Method: "DisableUser"}
	}
	return t.userSvc.DisableUser(ctx, id)
}

// ---------------------------------------------------------------------------
// RoleClient implementation
// ---------------------------------------------------------------------------

// CreateRole creates a new role via the embedded role service.
func (t *Transport) CreateRole(ctx context.Context, req *transport.CreateRoleRequest) (*transport.Role, error) {
	if t.roleSvc == nil {
		return nil, &transport.NotImplementedError{Method: "CreateRole"}
	}
	perms := stringsToPermissions(req.Permissions)
	rl, err := t.roleSvc.CreateRole(ctx, &role.CreateRoleRequest{
		ID:          req.ID,
		Name:        req.Name,
		TenantID:    req.TenantID,
		Permissions: perms,
	})
	if err != nil {
		return nil, err
	}
	return roleToTransport(rl), nil
}

// GetRole retrieves a role by ID via the embedded role service.
func (t *Transport) GetRole(ctx context.Context, id string) (*transport.Role, error) {
	if t.roleSvc == nil {
		return nil, &transport.NotImplementedError{Method: "GetRole"}
	}
	rl, err := t.roleSvc.GetRole(ctx, id)
	if err != nil {
		return nil, err
	}
	return roleToTransport(rl), nil
}

// UpdateRole applies partial updates to a role via the embedded role service.
func (t *Transport) UpdateRole(ctx context.Context, req *transport.UpdateRoleRequest) (*transport.Role, error) {
	if t.roleSvc == nil {
		return nil, &transport.NotImplementedError{Method: "UpdateRole"}
	}
	perms := stringsToPermissions(req.Permissions)
	rl, err := t.roleSvc.UpdateRole(ctx, &role.UpdateRoleRequest{
		ID:          req.ID,
		Name:        req.Name,
		Permissions: perms,
	})
	if err != nil {
		return nil, err
	}
	return roleToTransport(rl), nil
}

// DeleteRole removes a non-system role by ID via the embedded role service.
func (t *Transport) DeleteRole(ctx context.Context, id string) error {
	if t.roleSvc == nil {
		return &transport.NotImplementedError{Method: "DeleteRole"}
	}
	return t.roleSvc.DeleteRole(ctx, id)
}

// ListRoles returns a paginated role list via the embedded role service.
func (t *Transport) ListRoles(ctx context.Context, req *transport.ListRolesRequest) (*transport.ListRolesResponse, error) {
	if t.roleSvc == nil {
		return nil, &transport.NotImplementedError{Method: "ListRoles"}
	}
	resp, err := t.roleSvc.ListRoles(ctx, &role.ListRolesRequest{
		TenantID: req.TenantID,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, err
	}
	roles := make([]*transport.Role, len(resp.Roles))
	for i, rl := range resp.Roles {
		roles[i] = roleToTransport(rl)
	}
	return &transport.ListRolesResponse{
		Roles:   roles,
		Total:   resp.Total,
		HasMore: resp.HasMore,
	}, nil
}

// AssignRole adds a role to a user's role set via the embedded services.
func (t *Transport) AssignRole(ctx context.Context, userID uint64, roleID string) error {
	if t.roleSvc == nil {
		return &transport.NotImplementedError{Method: "AssignRole"}
	}
	if t.userSvc == nil {
		return &transport.NotImplementedError{Method: "AssignRole"}
	}

	// Validate role exists.
	if _, err := t.roleSvc.GetRole(ctx, roleID); err != nil {
		return err
	}

	// Get user and add role (idempotent).
	u, err := t.userSvc.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, rid := range u.RoleIDs {
		if rid == roleID {
			return nil // already assigned
		}
	}
	roleIDs := append(u.RoleIDs, roleID)
	_, err = t.userSvc.UpdateUser(ctx, &user.UpdateUserRequest{
		ID:      userID,
		RoleIDs: roleIDs,
	})
	return err
}

// UnassignRole removes a role from a user's role set via the embedded services.
func (t *Transport) UnassignRole(ctx context.Context, userID uint64, roleID string) error {
	if t.roleSvc == nil {
		return &transport.NotImplementedError{Method: "UnassignRole"}
	}
	if t.userSvc == nil {
		return &transport.NotImplementedError{Method: "UnassignRole"}
	}

	u, err := t.userSvc.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	roleIDs := make([]string, 0, len(u.RoleIDs))
	for _, rid := range u.RoleIDs {
		if rid != roleID {
			roleIDs = append(roleIDs, rid)
		}
	}
	_, err = t.userSvc.UpdateUser(ctx, &user.UpdateUserRequest{
		ID:      userID,
		RoleIDs: roleIDs,
	})
	return err
}

// ListPermissions returns all registered permission categories.
func (t *Transport) ListPermissions(_ context.Context) (*transport.ListPermissionsResponse, error) {
	cats := permission.Categories()
	result := make([]*transport.PermissionCategory, len(cats))
	for i, cat := range cats {
		perms := make([]string, len(cat.Permissions))
		for j, p := range cat.Permissions {
			perms[j] = p.String()
		}
		result[i] = &transport.PermissionCategory{
			Name:        cat.Name,
			Group:       cat.Group,
			Permissions: perms,
		}
	}
	return &transport.ListPermissionsResponse{Categories: result}, nil
}

// ---------------------------------------------------------------------------
// CertClient implementation
// ---------------------------------------------------------------------------

// GenerateCert creates a client certificate via the embedded cert provider.
func (t *Transport) GenerateCert(ctx context.Context, req *transport.GenerateCertRequest) (*transport.CertBundle, error) {
	if t.certProv == nil {
		return nil, &transport.NotImplementedError{Method: "GenerateCert"}
	}
	genReq := &cert.GenerateRequest{
		Username: req.Username,
		TenantID: req.TenantID,
	}
	if req.TTL != "" {
		ttl, err := time.ParseDuration(req.TTL)
		if err != nil {
			return nil, err
		}
		genReq.TTL = ttl
	}
	bundle, err := t.certProv.GenerateClientCert(ctx, genReq)
	if err != nil {
		return nil, err
	}
	return &transport.CertBundle{
		CertPEM: string(bundle.CertPEM),
		KeyPEM:  string(bundle.KeyPEM),
		Serial:  bundle.Serial,
	}, nil
}

// ExportCert exports a certificate as PKCS#12 via the embedded cert provider.
func (t *Transport) ExportCert(ctx context.Context, req *transport.ExportCertRequest) (*transport.ExportCertResponse, error) {
	if t.certProv == nil {
		return nil, &transport.NotImplementedError{Method: "ExportCert"}
	}
	p12Bytes, err := t.certProv.ExportPKCS12(ctx, []byte(req.CertPEM), []byte(req.KeyPEM), req.Passphrase)
	if err != nil {
		return nil, err
	}
	return &transport.ExportCertResponse{
		PKCS12: base64.StdEncoding.EncodeToString(p12Bytes),
	}, nil
}

// RevokeCert marks a certificate serial as revoked via the embedded cert
// provider, and reports whether it held a local record for the serial.
//
// BREAKING (go-qrdb#287): this method previously returned only error.
func (t *Transport) RevokeCert(ctx context.Context, serial string) (*transport.RevokeResult, error) {
	if t.certProv == nil {
		return nil, &transport.NotImplementedError{Method: "RevokeCert"}
	}
	result, err := t.certProv.RevokeCert(ctx, serial)
	if err != nil {
		return nil, err
	}
	return &transport.RevokeResult{
		Serial:      result.Serial,
		RecordFound: result.RecordFound(),
	}, nil
}

// ListCerts lists issued certificates for a tenant via the embedded cert provider.
func (t *Transport) ListCerts(ctx context.Context, tenantID string) (*transport.ListCertsResponse, error) {
	if t.certProv == nil {
		return nil, &transport.NotImplementedError{Method: "ListCerts"}
	}
	records, err := t.certProv.ListCerts(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	certs := make([]*transport.CertRecord, len(records))
	for i, rec := range records {
		certs[i] = &transport.CertRecord{
			Serial:    rec.Serial,
			Username:  rec.Username,
			TenantID:  rec.TenantID,
			IssuedAt:  rec.IssuedAt.Format(time.RFC3339),
			ExpiresAt: rec.ExpiresAt.Format(time.RFC3339),
			Revoked:   rec.Revoked,
		}
	}
	return &transport.ListCertsResponse{Certs: certs}, nil
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

// userToTransport converts a domain user to its transport representation.
func userToTransport(u *user.User) *transport.User {
	if u == nil {
		return nil
	}
	roleIDs := u.RoleIDs
	if roleIDs == nil {
		roleIDs = []string{}
	}
	return &transport.User{
		ID:           u.ID,
		Username:     u.Username,
		Email:        u.Email,
		Name:         u.Name,
		TenantID:     u.TenantID,
		RoleIDs:      roleIDs,
		Enabled:      u.Enabled,
		System:       u.System,
		Lifecycle:    string(u.Lifecycle),
		GroupIDs:     u.GroupIDs,
		AuthMethods:  u.AuthMethods,
		LastAuth:     formatLastAuth(u.LastAuth),
		CertBindings: user.WireCertBindings(u.CertBindings),
		CreatedAt:    u.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    u.UpdatedAt.Format(time.RFC3339),
	}
}

// roleToTransport converts a domain role to its transport representation.
// permissionsToStrings renders a permission slice for the transport types. It
// answers nil for an empty input so an omitempty field stays absent.
func permissionsToStrings(perms []permission.Permission) []string {
	if len(perms) == 0 {
		return nil
	}
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = p.String()
	}
	return out
}

func roleToTransport(rl *role.Role) *transport.Role {
	if rl == nil {
		return nil
	}
	perms := make([]string, len(rl.Permissions))
	for i, p := range rl.Permissions {
		perms[i] = p.String()
	}
	return &transport.Role{
		ID:                 rl.ID,
		Name:               rl.Name,
		TenantID:           rl.TenantID,
		Permissions:        perms,
		System:             rl.System,
		CreatedAt:          rl.CreatedAt.Format(time.RFC3339),
		UpdatedAt:          rl.UpdatedAt.Format(time.RFC3339),
		RetiredPermissions: permissionsToStrings(rl.RetiredPermissions),
	}
}

// stringsToPermissions converts string slices to permission values.
func stringsToPermissions(ss []string) []permission.Permission {
	if ss == nil {
		return nil
	}
	perms := make([]permission.Permission, len(ss))
	for i, s := range ss {
		perms[i] = permission.Permission(s)
	}
	return perms
}

// formatLastAuth renders a nil-able last-authentication time as RFC 3339, or
// the empty string when the account has never authenticated — empty rather than
// a zero timestamp, so a consumer never reads "never" as 1970.
func formatLastAuth(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
