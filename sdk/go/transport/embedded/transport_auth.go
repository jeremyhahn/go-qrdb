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
	"time"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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
	if err := t.gate("CreateUser"); err != nil {
		return nil, err
	}
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "CreateUser"}
	}
	if req == nil {
		return nil, nilRequestError("CreateUser")
	}
	if err := t.authorizeIdentityTenant(ctx, req.TenantID, permission.UsersCreate); err != nil {
		return nil, err
	}
	if len(req.RoleIDs) > 0 {
		if err := t.authMW.AuthorizeRoleAssignmentWriteOnCreate(ctx, t.roleSvc, req.TenantID, req.RoleIDs); err != nil {
			return nil, err
		}
	}
	u, err := t.userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             req.Username,
		Email:                req.Email,
		Name:                 req.Name,
		WithoutEmailLoginKey: req.WithoutEmailLoginKey,
		TenantID:             req.TenantID,
		RoleIDs:              req.RoleIDs,
		AuthMethods:          req.AuthMethods,
		Lifecycle:            user.Lifecycle(req.Lifecycle),
		Pending:              req.Pending,
	})
	if err != nil {
		return nil, err
	}
	return userToTransport(ctx, t.userAuthFactorSvc, u)
}

// GetUser retrieves a user by ID via the embedded user service.
func (t *Transport) GetUser(ctx context.Context, id uint64) (*transport.User, error) {
	if err := t.gate("GetUser"); err != nil {
		return nil, err
	}
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "GetUser"}
	}
	if err := t.authorizeUserTarget(ctx, id, permission.UsersRead); err != nil {
		return nil, err
	}
	u, err := t.userSvc.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	return userToTransport(ctx, t.userAuthFactorSvc, u)
}

// GetUserByUsername retrieves a user by tenant-scoped username via the
// embedded user service.
func (t *Transport) GetUserByUsername(ctx context.Context, tenantID, username string) (*transport.User, error) {
	if err := t.gate("GetUserByUsername"); err != nil {
		return nil, err
	}
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "GetUserByUsername"}
	}
	if err := t.authorizeIdentityTenant(ctx, tenantID, permission.UsersRead); err != nil {
		return nil, err
	}
	u, err := t.userSvc.GetUserByUsername(ctx, tenantID, username)
	if err != nil {
		return nil, err
	}
	return userToTransport(ctx, t.userAuthFactorSvc, u)
}

// GetUserByEmail resolves the canonical login key through the embedded service.
func (t *Transport) GetUserByEmail(ctx context.Context, tenantID, email string) (*transport.User, error) {
	if err := t.gate("GetUserByEmail"); err != nil {
		return nil, err
	}
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "GetUserByEmail"}
	}
	if err := t.authorizeIdentityTenant(ctx, tenantID, permission.UsersRead); err != nil {
		return nil, err
	}
	u, err := t.userSvc.GetUserByEmail(ctx, tenantID, email)
	if err != nil {
		return nil, err
	}
	return userToTransport(ctx, t.userAuthFactorSvc, u)
}

// GetUserByCertThumbprint resolves a user by mTLS SPKI thumbprint via the
// embedded user service.
func (t *Transport) GetUserByCertThumbprint(ctx context.Context, thumbprint string) (*transport.User, error) {
	if err := t.gate("GetUserByCertThumbprint"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetUserByCertThumbprint"); err != nil {
		return nil, err
	}
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "GetUserByCertThumbprint"}
	}
	u, err := t.userSvc.UserByCertThumbprint(ctx, thumbprint)
	if err != nil {
		return nil, err
	}
	return userToTransport(ctx, t.userAuthFactorSvc, u)
}

// AddCertBinding binds an mTLS SPKI thumbprint to a user via the embedded
// user service.
func (t *Transport) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*transport.User, error) {
	if err := t.gate("AddCertBinding"); err != nil {
		return nil, err
	}
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "AddCertBinding"}
	}
	if err := t.authorizeUserTarget(ctx, userID, permission.UsersAddCertBinding); err != nil {
		return nil, err
	}
	// Binding a thumbprint binds a key: the certificate target rule applies.
	if err := t.authMW.AuthorizeCertBindingTarget(ctx, t.userSvc, userID); err != nil {
		return nil, err
	}
	u, err := t.userSvc.AddCertBinding(ctx, userID, thumbprint, notAfterUnix)
	if err != nil {
		return nil, err
	}
	return userToTransport(ctx, t.userAuthFactorSvc, u)
}

// RemoveCertBinding withdraws an mTLS SPKI thumbprint from a user via the
// embedded user service.
func (t *Transport) RemoveCertBinding(ctx context.Context, userID uint64, thumbprint string) (*transport.User, error) {
	if err := t.gate("RemoveCertBinding"); err != nil {
		return nil, err
	}
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "RemoveCertBinding"}
	}
	if err := t.authorizeUserTarget(ctx, userID, permission.UsersRemoveCertBinding); err != nil {
		return nil, err
	}
	u, err := t.userSvc.RemoveCertBinding(ctx, userID, thumbprint)
	if err != nil {
		return nil, err
	}
	return userToTransport(ctx, t.userAuthFactorSvc, u)
}

// UpdateUser applies partial updates to a user via the embedded user service.
func (t *Transport) UpdateUser(ctx context.Context, req *transport.UpdateUserRequest) (*transport.User, error) {
	if err := t.gate("UpdateUser"); err != nil {
		return nil, err
	}
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "UpdateUser"}
	}
	if req == nil {
		return nil, nilRequestError("UpdateUser")
	}
	if err := t.authorizeUserTarget(ctx, req.ID, permission.UsersUpdate); err != nil {
		return nil, err
	}
	updateReq := &user.UpdateUserRequest{
		ID:           req.ID,
		Username:     req.Username,
		Email:        req.Email,
		Name:         req.Name,
		RoleIDs:      req.RoleIDs,
		AuthMethods:  req.AuthMethods,
		Lifecycle:    user.Lifecycle(req.Lifecycle),
		RoleBindings: user.RoleBindingsFromWire(req.RoleBindings),
		Attributes:   req.Attributes,
	}
	if req.LastAuth != "" {
		at, perr := time.Parse(time.RFC3339, req.LastAuth)
		if perr != nil {
			return nil, &transport.RequestError{Method: "UpdateUser", Err: perr}
		}
		utc := at.UTC()
		updateReq.LastAuth = &utc
	}
	if authmw.TouchesRoleAssignment(updateReq.RoleIDs, updateReq.RoleBindings, updateReq.Attributes) {
		if err := t.authMW.AuthorizeRoleAssignmentWrite(ctx, t.userSvc, t.roleSvc, req.ID, updateReq.RoleIDs, updateReq.RoleBindings); err != nil {
			return nil, err
		}
	}
	u, err := t.userSvc.UpdateUser(ctx, updateReq)
	if err != nil {
		return nil, err
	}
	return userToTransport(ctx, t.userAuthFactorSvc, u)
}

// DeleteUser removes a non-system user by ID via the embedded user service.
func (t *Transport) DeleteUser(ctx context.Context, id uint64) error {
	if err := t.gate("DeleteUser"); err != nil {
		return err
	}
	if t.userSvc == nil {
		return &transport.NotImplementedError{Method: "DeleteUser"}
	}
	if err := t.authorizeUserTarget(ctx, id, permission.UsersDelete); err != nil {
		return err
	}
	return t.userSvc.DeleteUser(ctx, id)
}

// ListUsers returns a paginated user list via the embedded user service.
func (t *Transport) ListUsers(ctx context.Context, req *transport.ListUsersRequest) (*transport.ListUsersResponse, error) {
	if err := t.gate("ListUsers"); err != nil {
		return nil, err
	}
	if t.userSvc == nil {
		return nil, &transport.NotImplementedError{Method: "ListUsers"}
	}
	if req == nil {
		return nil, nilRequestError("ListUsers")
	}
	if err := t.authorizeIdentityTenant(ctx, req.TenantID, permission.UsersList); err != nil {
		return nil, err
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
		if users[i], err = userToTransport(ctx, t.userAuthFactorSvc, u); err != nil {
			return nil, err
		}
	}
	return &transport.ListUsersResponse{
		Users:   users,
		Total:   resp.Total,
		HasMore: resp.HasMore,
	}, nil
}

// EnableUser activates a user account via the embedded user service.
func (t *Transport) EnableUser(ctx context.Context, id uint64) error {
	if err := t.gate("EnableUser"); err != nil {
		return err
	}
	if t.userSvc == nil {
		return &transport.NotImplementedError{Method: "EnableUser"}
	}
	if err := t.authorizeUserTarget(ctx, id, permission.UsersEnable); err != nil {
		return err
	}
	return t.userSvc.EnableUser(ctx, id)
}

// DisableUser deactivates a user account via the embedded user service.
func (t *Transport) DisableUser(ctx context.Context, id uint64) error {
	if err := t.gate("DisableUser"); err != nil {
		return err
	}
	if t.userSvc == nil {
		return &transport.NotImplementedError{Method: "DisableUser"}
	}
	if err := t.authorizeUserTarget(ctx, id, permission.UsersDisable); err != nil {
		return err
	}
	return t.userSvc.DisableUser(ctx, id)
}

// ---------------------------------------------------------------------------
// RoleClient implementation
// ---------------------------------------------------------------------------

// CreateRole creates a new role via the embedded role service.
func (t *Transport) CreateRole(ctx context.Context, req *transport.CreateRoleRequest) (*transport.Role, error) {
	if err := t.gate("CreateRole"); err != nil {
		return nil, err
	}
	if t.roleSvc == nil {
		return nil, &transport.NotImplementedError{Method: "CreateRole"}
	}
	if err := t.authorizeIdentityTenant(ctx, req.TenantID, permission.RolesCreate); err != nil {
		return nil, err
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
	if err := t.gate("GetRole"); err != nil {
		return nil, err
	}
	if t.roleSvc == nil {
		return nil, &transport.NotImplementedError{Method: "GetRole"}
	}
	if err := t.authorizeRoleTarget(ctx, id, permission.RolesRead); err != nil {
		return nil, err
	}
	rl, err := t.roleSvc.GetRole(ctx, id)
	if err != nil {
		return nil, err
	}
	return roleToTransport(rl), nil
}

// UpdateRole applies partial updates to a role via the embedded role service.
func (t *Transport) UpdateRole(ctx context.Context, req *transport.UpdateRoleRequest) (*transport.Role, error) {
	if err := t.gate("UpdateRole"); err != nil {
		return nil, err
	}
	if t.roleSvc == nil {
		return nil, &transport.NotImplementedError{Method: "UpdateRole"}
	}
	if req == nil {
		return nil, nilRequestError("UpdateRole")
	}
	if err := t.authorizeRoleTarget(ctx, req.ID, permission.RolesUpdate); err != nil {
		return nil, err
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
	if err := t.gate("DeleteRole"); err != nil {
		return err
	}
	if t.roleSvc == nil {
		return &transport.NotImplementedError{Method: "DeleteRole"}
	}
	if err := t.authorizeRoleTarget(ctx, id, permission.RolesDelete); err != nil {
		return err
	}
	return t.roleSvc.DeleteRole(ctx, id)
}

// ListRoles returns a paginated role list via the embedded role service.
func (t *Transport) ListRoles(ctx context.Context, req *transport.ListRolesRequest) (*transport.ListRolesResponse, error) {
	if err := t.gate("ListRoles"); err != nil {
		return nil, err
	}
	if t.roleSvc == nil {
		return nil, &transport.NotImplementedError{Method: "ListRoles"}
	}
	if req == nil {
		return nil, nilRequestError("ListRoles")
	}
	if err := t.authorizeIdentityTenant(ctx, req.TenantID, permission.RolesList); err != nil {
		return nil, err
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
	if err := t.gate("AssignRole"); err != nil {
		return err
	}
	if t.roleSvc == nil {
		return &transport.NotImplementedError{Method: "AssignRole"}
	}
	if t.userSvc == nil {
		return &transport.NotImplementedError{Method: "AssignRole"}
	}

	// The same gate every role-assignment write passes: roles:assign in the
	// user's tenant (when an AuthMiddleware is installed), and the role must
	// exist there.
	if err := t.authMW.AuthorizeRoleAssignmentWrite(ctx, t.userSvc, t.roleSvc, userID, []string{roleID}, nil); err != nil {
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
	if err := t.gate("UnassignRole"); err != nil {
		return err
	}
	if t.roleSvc == nil {
		return &transport.NotImplementedError{Method: "UnassignRole"}
	}
	if t.userSvc == nil {
		return &transport.NotImplementedError{Method: "UnassignRole"}
	}
	if err := t.authMW.AuthorizeRoleUnassignment(ctx, t.userSvc, userID); err != nil {
		return err
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
func (t *Transport) ListPermissions(ctx context.Context) (*transport.ListPermissionsResponse, error) {
	if err := t.gate("ListPermissions"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ListPermissions"); err != nil {
		return nil, err
	}
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

// enrolledToTransport converts a bound certificate.
func enrolledToTransport(e *service.EnrolledCertificate) *transport.EnrolledCert {
	return &transport.EnrolledCert{
		UserID:     e.UserID,
		Username:   e.Username,
		TenantID:   e.TenantID,
		CertPEM:    string(e.CertPEM),
		Serial:     e.Serial,
		Thumbprint: e.Thumbprint,
		NotAfter:   e.NotAfter,
	}
}

// IssueCert issues a certificate from a CSR for an existing account of the
// caller's own tenant, signed by that tenant's own CA, and binds it. The
// tenant is the caller's (tenant.Confine of the context's own), and
// certs:issue's ScopeSelfStrict is enforced here through
// AuthMiddleware.AuthorizeCertTenant, the same shared gate every network
// transport authorizes this verb through.
func (t *Transport) IssueCert(ctx context.Context, req *transport.IssueCertRequest) (*transport.EnrolledCert, error) {
	if err := t.gate("IssueCert"); err != nil {
		return nil, err
	}
	if t.certEnroll == nil {
		return nil, &transport.NotImplementedError{Method: "IssueCert"}
	}
	tenantID, err := tenant.Confine(ctx, "", "issue a certificate")
	if err != nil {
		return nil, err
	}
	if err := t.authorizeCertTenant(ctx, tenantID, permission.CertsIssue); err != nil {
		return nil, err
	}
	var ttl time.Duration
	if req.TTL != "" {
		if ttl, err = time.ParseDuration(req.TTL); err != nil {
			return nil, err
		}
	}
	enrolled, err := t.certEnroll.SignCertificate(ctx, &service.SignCertificateRequest{
		TenantID: tenantID,
		Username: req.Username,
		CSRPEM:   []byte(req.CSRPEM),
		TTL:      ttl,
	})
	if err != nil {
		return nil, err
	}
	return enrolledToTransport(enrolled), nil
}

// IssueDelegation has the caller's own tenant's CA issue a delegation
// certificate to a service principal. certs:delegate's ScopeTenantOnly is
// enforced through AuthMiddleware.AuthorizeCertTenant, the same shared gate
// every network transport authorizes this verb through.
func (t *Transport) IssueDelegation(ctx context.Context, req *transport.IssueDelegationRequest) (*transport.IssuedDelegation, error) {
	if err := t.gate("IssueDelegation"); err != nil {
		return nil, err
	}
	if t.certEnroll == nil {
		return nil, &transport.NotImplementedError{Method: "IssueDelegation"}
	}
	tenantID, err := tenant.Confine(ctx, "", "issue a delegation")
	if err != nil {
		return nil, err
	}
	if err := t.authorizeCertTenant(ctx, tenantID, permission.CertsDelegate); err != nil {
		return nil, err
	}
	var ttl time.Duration
	if req.TTL != "" {
		if ttl, err = time.ParseDuration(req.TTL); err != nil {
			return nil, err
		}
	}
	issued, err := t.certEnroll.IssueDelegation(ctx, &service.IssueDelegationRequest{
		TenantID:  tenantID,
		Principal: req.Principal,
		Scope:     req.Scope,
		CSRPEM:    []byte(req.CSRPEM),
		TTL:       ttl,
	})
	if err != nil {
		return nil, err
	}
	return &transport.IssuedDelegation{
		TenantID:  issued.TenantID,
		Principal: issued.Principal,
		Scope:     issued.Scope,
		CertPEM:   string(issued.CertPEM),
		Serial:    issued.Serial,
		NotAfter:  issued.NotAfter,
	}, nil
}

// EnrollCert binds a bring-your-own certificate to an existing account of the
// caller's own tenant, verified against the tenant's trust anchors.
// certs:enroll's ScopeSelfStrict is enforced through
// AuthMiddleware.AuthorizeCertTenant, the same shared gate every network
// transport authorizes this verb through.
func (t *Transport) EnrollCert(ctx context.Context, req *transport.EnrollCertRequest) (*transport.EnrolledCert, error) {
	if err := t.gate("EnrollCert"); err != nil {
		return nil, err
	}
	if t.certEnroll == nil {
		return nil, &transport.NotImplementedError{Method: "EnrollCert"}
	}
	tenantID, err := tenant.Confine(ctx, "", "enrol a certificate")
	if err != nil {
		return nil, err
	}
	if err := t.authorizeCertTenant(ctx, tenantID, permission.CertsEnroll); err != nil {
		return nil, err
	}
	enrolled, err := t.certEnroll.EnrollCertificate(ctx, &service.EnrollCertificateRequest{
		TenantID: tenantID,
		Username: req.Username,
		CertPEM:  []byte(req.CertPEM),
		ChainPEM: []byte(req.ChainPEM),
	})
	if err != nil {
		return nil, err
	}
	return enrolledToTransport(enrolled), nil
}

// RevokeCert marks a certificate serial as revoked via the embedded cert
// provider, and reports whether it held a local record for the serial.
//
// BREAKING (go-qrdb#287): this method previously returned only error.
//
// A serial with an issued record is revoked only by its owning tenant
// (tenant.Confine -- structural, unconditional). A serial with none is the
// deny-by-serial revocation, a platform act on the CA the system tenant
// operates, so only a system-tenant caller may write it. Both targets are
// then also checked for the RBAC grant through the same shared gate every
// network transport uses (AuthMiddleware.AuthorizeCertRevoke): certs:revoke
// or certs:revoke:cross respectively.
func (t *Transport) RevokeCert(ctx context.Context, serial string) (*transport.RevokeResult, error) {
	if err := t.gate("RevokeCert"); err != nil {
		return nil, err
	}
	if t.certProv == nil {
		return nil, &transport.NotImplementedError{Method: "RevokeCert"}
	}
	target, _, err := authmw.CertRevokeTarget(ctx, serial, t.certProv)
	if err != nil {
		return nil, err
	}
	if _, err := tenant.Confine(ctx, target, "revoke a certificate"); err != nil {
		return nil, err
	}
	if err := t.authorizeCertRevoke(ctx, serial); err != nil {
		return nil, err
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

// ListCerts lists issued certificates for a tenant via the embedded cert
// provider. Only the caller's own tenant may be listed (tenant.Confine,
// unconditional), and the RBAC grant for it is checked the same way every
// network transport checks it (AuthMiddleware.AuthorizeCertTenant).
func (t *Transport) ListCerts(ctx context.Context, tenantID string) (*transport.ListCertsResponse, error) {
	if err := t.gate("ListCerts"); err != nil {
		return nil, err
	}
	if t.certProv == nil {
		return nil, &transport.NotImplementedError{Method: "ListCerts"}
	}
	if _, err := tenant.Confine(ctx, tenantID, "list certificates"); err != nil {
		return nil, err
	}
	if err := t.authorizeCertTenant(ctx, tenantID, permission.CertsList); err != nil {
		return nil, err
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
// Factors metadata and the derived MFA fields are computed at read time from
// the real enrollment records via lister (service.UserAuthFactorServicer),
// read once -- never from a stored field on u. lister may be nil (the
// auth-factor subsystem is not configured for this deployment); a lister that
// fails returns the *user.FactorLookupError rather than a user claiming no
// factors.
func userToTransport(ctx context.Context, lister user.FactorLister, u *user.User) (*transport.User, error) {
	if u == nil {
		return nil, nil
	}
	roleIDs := u.RoleIDs
	if roleIDs == nil {
		roleIDs = []string{}
	}
	factors, err := user.ResolveFactorView(ctx, lister, u.ID)
	if err != nil {
		return nil, err
	}
	return &transport.User{
		ID:            u.ID,
		Username:      u.Username,
		Email:         u.Email,
		Name:          u.Name,
		TenantID:      u.TenantID,
		RoleIDs:       roleIDs,
		System:        u.System,
		Lifecycle:     string(u.Lifecycle),
		AuthMethods:   u.AuthMethods,
		LastAuth:      formatLastAuth(u.LastAuth),
		CertBindings:  user.WireCertBindings(u.CertBindings),
		RoleBindings:  user.WireRoleBindings(u.RoleBindings),
		Attributes:    u.Attributes,
		Factors:       factors.Factors,
		MFAEnabled:    factors.MFAEnabled,
		MFAMethods:    factors.MFAMethods,
		MFAEnrolledAt: factors.MFAEnrolledAt,
		CreatedAt:     u.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     u.UpdatedAt.Format(time.RFC3339),
	}, nil
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

// nilRequestError is the typed refusal of a nil request, returned before the
// call is authorized: there is no request to name a target, and a nil
// pointer is a caller's bug, not an authorization question.
func nilRequestError(method string) error {
	return &transport.RequestError{Method: method, Err: &transport.NilRequestError{}}
}
