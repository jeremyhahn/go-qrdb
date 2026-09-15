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

package grpc

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// User management handlers
// ---------------------------------------------------------------------------

// CreateUser creates a new passwordless user.
func (s *qrdbServiceServer) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	u, err := s.userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             req.GetUsername(),
		Email:                req.GetEmail(),
		Name:                 req.GetName(),
		WithoutEmailLoginKey: req.GetWithoutEmailLoginKey(),
		TenantID:             req.GetTenantId(),
		RoleIDs:              req.GetRoleIds(),
		GroupIDs:             req.GetGroupIds(),
		AuthMethods:          req.GetAuthMethods(),
		Lifecycle:            user.Lifecycle(req.GetLifecycle()),
		Pending:              req.GetPending(),
	})
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return userToProto(u), nil
}

// GetUser retrieves a user by ID.
func (s *qrdbServiceServer) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	u, err := s.userSvc.GetUser(ctx, req.GetId())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return userToProto(u), nil
}

// GetUserByUsername retrieves a user by tenant-scoped username.
func (s *qrdbServiceServer) GetUserByUsername(ctx context.Context, req *pb.GetUserByUsernameRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	u, err := s.userSvc.GetUserByUsername(ctx, req.GetTenantId(), req.GetUsername())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return userToProto(u), nil
}

// GetUserByEmail retrieves a user by tenant-scoped email — the canonical login
// key. Same permission and error mapping as its username sibling; they differ
// only in which secondary index answers.
func (s *qrdbServiceServer) GetUserByEmail(ctx context.Context, req *pb.GetUserByEmailRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	u, err := s.userSvc.GetUserByEmail(ctx, req.GetTenantId(), req.GetEmail())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return userToProto(u), nil
}

// GetUserByCertThumbprint resolves a user by mTLS SPKI thumbprint.
func (s *qrdbServiceServer) GetUserByCertThumbprint(ctx context.Context, req *pb.GetUserByCertThumbprintRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	u, err := s.userSvc.UserByCertThumbprint(ctx, req.GetThumbprint())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return userToProto(u), nil
}

// AddCertBinding binds an mTLS SPKI thumbprint to a user.
func (s *qrdbServiceServer) AddCertBinding(ctx context.Context, req *pb.AddCertBindingRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	u, err := s.userSvc.AddCertBinding(ctx, req.GetUserId(), req.GetThumbprint(), req.GetNotAfterUnix())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return userToProto(u), nil
}

// RemoveCertBinding withdraws an mTLS SPKI thumbprint from a user.
func (s *qrdbServiceServer) RemoveCertBinding(ctx context.Context, req *pb.RemoveCertBindingRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	u, err := s.userSvc.RemoveCertBinding(ctx, req.GetUserId(), req.GetThumbprint())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return userToProto(u), nil
}

// UpdateUser applies partial updates to an existing user.
func (s *qrdbServiceServer) UpdateUser(ctx context.Context, req *pb.UpdateUserProtoRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	updateReq := &user.UpdateUserRequest{
		ID:        req.GetId(),
		Username:  req.GetUsername(),
		Email:     req.GetEmail(),
		Name:      req.GetName(),
		Lifecycle: user.Lifecycle(req.GetLifecycle()),
	}
	if len(req.GetRoleIds()) > 0 {
		updateReq.RoleIDs = req.GetRoleIds()
	}
	// nil means "leave alone" and an empty non-nil slice means "clear", and
	// proto3 cannot tell an absent repeated field from an empty one — so the
	// explicit has_* flag is what carries the distinction. Without it a caller
	// could never REVOKE a membership over gRPC: an empty list would read as
	// "no change" and the revocation would silently do nothing.
	if req.GetHasGroupIds() {
		updateReq.GroupIDs = req.GetGroupIds()
		if updateReq.GroupIDs == nil {
			updateReq.GroupIDs = []string{}
		}
	}
	if req.GetHasAuthMethods() {
		updateReq.AuthMethods = req.GetAuthMethods()
		if updateReq.AuthMethods == nil {
			updateReq.AuthMethods = []string{}
		}
	}
	if sec := req.GetLastAuthUnix(); sec > 0 {
		at := time.Unix(sec, 0).UTC()
		updateReq.LastAuth = &at
	}

	u, err := s.userSvc.UpdateUser(ctx, updateReq)
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return userToProto(u), nil
}

// DeleteUser removes a non-system user by ID.
func (s *qrdbServiceServer) DeleteUser(ctx context.Context, req *pb.DeleteUserRequest) (*emptypb.Empty, error) {
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	if err := s.userSvc.DeleteUser(ctx, req.GetId()); err != nil {
		return nil, mapAuthServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// ListUsers returns a paginated list of users.
func (s *qrdbServiceServer) ListUsers(ctx context.Context, req *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	resp, err := s.userSvc.ListUsers(ctx, &user.ListUsersRequest{
		TenantID: req.GetTenantId(),
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
	})
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	users := make([]*pb.UserResponse, len(resp.Users))
	for i, u := range resp.Users {
		users[i] = userToProto(u)
	}

	return &pb.ListUsersResponse{
		Users:   users,
		Total:   int32(resp.Total),
		HasMore: resp.HasMore,
	}, nil
}

// EnableUser activates a user account.
func (s *qrdbServiceServer) EnableUser(ctx context.Context, req *pb.EnableUserRequest) (*emptypb.Empty, error) {
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	if err := s.userSvc.EnableUser(ctx, req.GetId()); err != nil {
		return nil, mapAuthServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// DisableUser deactivates a user account.
func (s *qrdbServiceServer) DisableUser(ctx context.Context, req *pb.DisableUserRequest) (*emptypb.Empty, error) {
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	if err := s.userSvc.DisableUser(ctx, req.GetId()); err != nil {
		return nil, mapAuthServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// ---------------------------------------------------------------------------
// Role management handlers
// ---------------------------------------------------------------------------

// CreateRole creates a new role.
func (s *qrdbServiceServer) CreateRole(ctx context.Context, req *pb.CreateRoleRequest) (*pb.RoleResponse, error) {
	if s.roleSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "role"}).Error())
	}

	perms := make([]permission.Permission, len(req.GetPermissions()))
	for i, p := range req.GetPermissions() {
		perms[i] = permission.Permission(p)
	}

	r, err := s.roleSvc.CreateRole(ctx, &role.CreateRoleRequest{
		ID:          req.GetId(),
		Name:        req.GetName(),
		TenantID:    req.GetTenantId(),
		Permissions: perms,
	})
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return roleToProto(r), nil
}

// GetRole retrieves a role by ID.
func (s *qrdbServiceServer) GetRole(ctx context.Context, req *pb.GetRoleRequest) (*pb.RoleResponse, error) {
	if s.roleSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "role"}).Error())
	}

	r, err := s.roleSvc.GetRole(ctx, req.GetId())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return roleToProto(r), nil
}

// GetRoleByName retrieves a role by its tenant-scoped name.
func (s *qrdbServiceServer) GetRoleByName(ctx context.Context, req *pb.GetRoleByNameRequest) (*pb.RoleResponse, error) {
	if s.roleSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "role"}).Error())
	}

	r, err := s.roleSvc.GetRoleByName(ctx, req.GetTenantId(), req.GetName())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return roleToProto(r), nil
}

// UpdateRole applies partial updates to an existing role.
func (s *qrdbServiceServer) UpdateRole(ctx context.Context, req *pb.UpdateRoleProtoRequest) (*pb.RoleResponse, error) {
	if s.roleSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "role"}).Error())
	}

	updateReq := &role.UpdateRoleRequest{
		ID:   req.GetId(),
		Name: req.GetName(),
	}
	if len(req.GetPermissions()) > 0 {
		perms := make([]permission.Permission, len(req.GetPermissions()))
		for i, p := range req.GetPermissions() {
			perms[i] = permission.Permission(p)
		}
		updateReq.Permissions = perms
	}

	r, err := s.roleSvc.UpdateRole(ctx, updateReq)
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return roleToProto(r), nil
}

// DeleteRole removes a non-system role by ID.
func (s *qrdbServiceServer) DeleteRole(ctx context.Context, req *pb.DeleteRoleRequest) (*emptypb.Empty, error) {
	if s.roleSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "role"}).Error())
	}

	if err := s.roleSvc.DeleteRole(ctx, req.GetId()); err != nil {
		return nil, mapAuthServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// ListRoles returns a paginated list of roles.
func (s *qrdbServiceServer) ListRoles(ctx context.Context, req *pb.ListRolesRequest) (*pb.ListRolesResponse, error) {
	if s.roleSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "role"}).Error())
	}

	resp, err := s.roleSvc.ListRoles(ctx, &role.ListRolesRequest{
		TenantID: req.GetTenantId(),
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
	})
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	roles := make([]*pb.RoleResponse, len(resp.Roles))
	for i, r := range resp.Roles {
		roles[i] = roleToProto(r)
	}

	return &pb.ListRolesResponse{
		Roles:   roles,
		Total:   int32(resp.Total),
		HasMore: resp.HasMore,
	}, nil
}

// AssignRole assigns a role to a user. The handler validates that the
// role exists, retrieves the user, and appends the role if not already
// assigned.
func (s *qrdbServiceServer) AssignRole(ctx context.Context, req *pb.AssignRoleRequest) (*emptypb.Empty, error) {
	if s.roleSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "role"}).Error())
	}
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	// Validate that the role exists.
	if _, err := s.roleSvc.GetRole(ctx, req.GetRoleId()); err != nil {
		return nil, mapAuthServiceError(err)
	}

	// Get the user and add the role.
	u, err := s.userSvc.GetUser(ctx, req.GetUserId())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	// Check if role is already assigned.
	for _, rid := range u.RoleIDs {
		if rid == req.GetRoleId() {
			return &emptypb.Empty{}, nil
		}
	}

	roleIDs := append(u.RoleIDs, req.GetRoleId())
	if _, err := s.userSvc.UpdateUser(ctx, &user.UpdateUserRequest{
		ID:      req.GetUserId(),
		RoleIDs: roleIDs,
	}); err != nil {
		return nil, mapAuthServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// UnassignRole removes a role assignment from a user.
func (s *qrdbServiceServer) UnassignRole(ctx context.Context, req *pb.UnassignRoleRequest) (*emptypb.Empty, error) {
	if s.roleSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "role"}).Error())
	}
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	// Get the user and remove the role.
	u, err := s.userSvc.GetUser(ctx, req.GetUserId())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	roleIDs := make([]string, 0, len(u.RoleIDs))
	for _, rid := range u.RoleIDs {
		if rid != req.GetRoleId() {
			roleIDs = append(roleIDs, rid)
		}
	}

	if _, err := s.userSvc.UpdateUser(ctx, &user.UpdateUserRequest{
		ID:      req.GetUserId(),
		RoleIDs: roleIDs,
	}); err != nil {
		return nil, mapAuthServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// ListPermissions returns all available permission categories.
func (s *qrdbServiceServer) ListPermissions(_ context.Context, _ *emptypb.Empty) (*pb.ListPermissionsResponse, error) {
	cats := permission.Categories()

	pbCats := make([]*pb.PermissionCategory, len(cats))
	for i, cat := range cats {
		perms := make([]string, len(cat.Permissions))
		for j, p := range cat.Permissions {
			perms[j] = p.String()
		}
		pbCats[i] = &pb.PermissionCategory{
			Name:        cat.Name,
			Group:       cat.Group,
			Permissions: perms,
		}
	}

	return &pb.ListPermissionsResponse{Categories: pbCats}, nil
}

// ResolveRolePermissions returns the union of effective permissions for the
// given role IDs (sorted, deduplicated).
func (s *qrdbServiceServer) ResolveRolePermissions(ctx context.Context, req *pb.ResolveRolePermissionsRequest) (*pb.ResolveRolePermissionsResponse, error) {
	if s.roleSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "role"}).Error())
	}

	perms, err := s.roleSvc.ResolvePermissions(ctx, req.GetRoleIds())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = p.String()
	}

	return &pb.ResolveRolePermissionsResponse{Permissions: out}, nil
}

// ReconcileBuiltInRoles converges a tenant's stored built-in roles onto this
// build's definitions.
func (s *qrdbServiceServer) ReconcileBuiltInRoles(ctx context.Context, req *pb.ReconcileBuiltInRolesRequest) (*pb.ReconcileBuiltInRolesResponse, error) {
	if s.roleSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "role"}).Error())
	}

	report, err := s.roleSvc.ReconcileBuiltInRoles(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return reconcileReportToProto(report), nil
}

// reconcileReportToProto converts a role.ReconcileReport to its protobuf
// representation.
func reconcileReportToProto(report *role.ReconcileReport) *pb.ReconcileBuiltInRolesResponse {
	if report == nil {
		return nil
	}
	roles := make([]*pb.RoleReconcileResult, len(report.Roles))
	for i, r := range report.Roles {
		roles[i] = &pb.RoleReconcileResult{
			RoleId:  r.RoleID,
			Name:    r.Name,
			Created: r.Created,
			Added:   permissionsToStrings(r.Added),
			Removed: permissionsToStrings(r.Removed),
		}
	}
	return &pb.ReconcileBuiltInRolesResponse{TenantId: report.TenantID, Roles: roles}
}

// permissionsToStrings renders a permission slice for the wire.
func permissionsToStrings(perms []permission.Permission) []string {
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = p.String()
	}
	return out
}

// ---------------------------------------------------------------------------
// Certificate management handlers
//
// The unary interceptor authorizes each RPC's declared permission against the
// CALLER's tenant. It cannot authorize the tenant the REQUEST names, and three
// of these four RPCs name theirs. The gates below ask that second question
// through the shared home of the rule in pkg/auth/middleware, which the REST
// server calls too — see pkg/auth/middleware/cert_authz.go.
//
// With no auth middleware wired the gates are no-ops, matching every other
// handler-level gate in this stack.
// ---------------------------------------------------------------------------

// authorizeCertTenant gates a certificate operation on the tenant the request
// names rather than on the tenant the caller happens to be in.
func (s *qrdbServiceServer) authorizeCertTenant(ctx context.Context, tenantID string, perm permission.Permission) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeCertTenant(ctx, tenantID, perm)
}

// authorizeCertRevoke gates a revocation on the tenant that owns the serial,
// falling back to the CA-operator authority when the serial owns no record
// here (deny-by-serial).
func (s *qrdbServiceServer) authorizeCertRevoke(ctx context.Context, serial string) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeCertRevoke(ctx, serial, s.certProv)
}

// GenerateCert creates a client certificate.
func (s *qrdbServiceServer) GenerateCert(ctx context.Context, req *pb.GenerateCertRequest) (*pb.CertBundleResponse, error) {
	if s.certProv == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "cert"}).Error())
	}

	genReq := &cert.GenerateRequest{
		Username: req.GetUsername(),
		TenantID: req.GetTenantId(),
	}

	if req.GetTtl() != "" {
		ttl, err := time.ParseDuration(req.GetTtl())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, (&TTLParseError{Value: req.GetTtl(), Err: err}).Error())
		}
		genReq.TTL = ttl
	}

	// The request's tenant_id becomes the certificate's Subject OU, and the
	// mTLS authenticator derives an identity's tenant from that OU. An
	// unchecked cross-tenant mint hands the caller a CA-signed credential for
	// another tenant.
	if err := s.authorizeCertTenant(ctx, genReq.TenantID, permission.CertsGenerate); err != nil {
		return nil, authmw.GRPCError(err, codes.PermissionDenied)
	}

	bundle, err := s.certProv.GenerateClientCert(ctx, genReq)
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return &pb.CertBundleResponse{
		CertPem: string(bundle.CertPEM),
		KeyPem:  string(bundle.KeyPEM),
		Serial:  bundle.Serial,
	}, nil
}

// ExportCert exports a certificate as PKCS#12.
func (s *qrdbServiceServer) ExportCert(ctx context.Context, req *pb.ExportCertRequest) (*pb.ExportCertResponse, error) {
	if s.certProv == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "cert"}).Error())
	}

	p12Bytes, err := s.certProv.ExportPKCS12(
		ctx,
		[]byte(req.GetCertPem()),
		[]byte(req.GetKeyPem()),
		req.GetPassphrase(),
	)
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return &pb.ExportCertResponse{
		Pkcs12: base64.StdEncoding.EncodeToString(p12Bytes),
	}, nil
}

// RevokeCert marks a certificate serial as revoked and reports whether this
// node held a CertRecord for it. See RevokeCertResponse.
func (s *qrdbServiceServer) RevokeCert(ctx context.Context, req *pb.RevokeCertRequest) (*pb.RevokeCertResponse, error) {
	if s.certProv == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "cert"}).Error())
	}

	if err := s.authorizeCertRevoke(ctx, req.GetSerial()); err != nil {
		return nil, authmw.GRPCError(err, codes.PermissionDenied)
	}

	result, err := s.certProv.RevokeCert(ctx, req.GetSerial())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	return &pb.RevokeCertResponse{RecordFound: result.RecordFound()}, nil
}

// ListCerts lists all issued client certificates for a tenant.
func (s *qrdbServiceServer) ListCerts(ctx context.Context, req *pb.ListCertsRequest) (*pb.ListCertsResponse, error) {
	if s.certProv == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "cert"}).Error())
	}

	if err := s.authorizeCertTenant(ctx, req.GetTenantId(), permission.CertsList); err != nil {
		return nil, authmw.GRPCError(err, codes.PermissionDenied)
	}

	records, err := s.certProv.ListCerts(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapAuthServiceError(err)
	}

	pbCerts := make([]*pb.CertRecord, len(records))
	for i, rec := range records {
		pbCerts[i] = certRecordToProto(rec)
	}

	return &pb.ListCertsResponse{Certs: pbCerts}, nil
}

// ---------------------------------------------------------------------------
// Auth type conversion helpers
// ---------------------------------------------------------------------------

// userToProto converts a user.User to its protobuf representation.
func userToProto(u *user.User) *pb.UserResponse {
	if u == nil {
		return nil
	}
	return &pb.UserResponse{
		Id:          u.ID,
		TenantId:    u.TenantID,
		Username:    u.Username,
		Email:       u.Email,
		Name:        u.Name,
		RoleIds:     u.RoleIDs,
		Enabled:     u.Enabled,
		System:      u.System,
		Lifecycle:   string(u.Lifecycle),
		GroupIds:    u.GroupIDs,
		AuthMethods: u.AuthMethods,
		// Seconds rather than RFC 3339 because this is a point in time a client
		// compares and orders, not one it displays; zero says "never
		// authenticated" without a second presence field.
		LastAuthUnix: lastAuthUnix(u.LastAuth),
		CreatedAt:    u.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    u.UpdatedAt.Format(time.RFC3339),
	}
}

// roleToProto converts a role.Role to its protobuf representation.
func roleToProto(r *role.Role) *pb.RoleResponse {
	if r == nil {
		return nil
	}
	perms := make([]string, len(r.Permissions))
	for i, p := range r.Permissions {
		perms[i] = p.String()
	}
	retired := make([]string, len(r.RetiredPermissions))
	for i, p := range r.RetiredPermissions {
		retired[i] = p.String()
	}
	return &pb.RoleResponse{
		Id:                 r.ID,
		TenantId:           r.TenantID,
		Name:               r.Name,
		Permissions:        perms,
		System:             r.System,
		CreatedAt:          r.CreatedAt.Format(time.RFC3339),
		UpdatedAt:          r.UpdatedAt.Format(time.RFC3339),
		RetiredPermissions: retired,
	}
}

// certRecordToProto converts a cert.CertRecord to its protobuf representation.
func certRecordToProto(rec *cert.CertRecord) *pb.CertRecord {
	if rec == nil {
		return nil
	}
	return &pb.CertRecord{
		Serial:    rec.Serial,
		Username:  rec.Username,
		TenantId:  rec.TenantID,
		IssuedAt:  rec.IssuedAt.Format(time.RFC3339),
		ExpiresAt: rec.ExpiresAt.Format(time.RFC3339),
		Revoked:   rec.Revoked,
	}
}

// ---------------------------------------------------------------------------
// Auth error mapping
// ---------------------------------------------------------------------------

// mapAuthServiceError translates user/role/cert domain errors into gRPC
// status errors with appropriate codes.
func mapAuthServiceError(err error) error {
	if err == nil {
		return nil
	}

	// A barrier-isolated tenant's IAM (user/role) store resolved to a SEALED
	// barrier. The StoreResolver fails closed rather than leaking the operation
	// through the shared system store — surface Unavailable so the caller
	// retries after the tenant's barrier is unsealed, matching the
	// BarrierSealedError data-plane mapping. Checked first because the sentinel
	// carries no typed detail the store-error cases below would match.
	if errors.Is(err, tenant.ErrTenantSealed) {
		return status.Error(codes.Unavailable, err.Error())
	}

	// A normal tenant caller supplied a target tenant other than its own. The
	// IAM routing layer refuses to act on a client-supplied tenant — surface
	// PermissionDenied, matching the tenant-isolation posture across transports.
	var xt *tenant.CrossTenantDeniedError
	if errors.As(err, &xt) {
		return status.Error(codes.PermissionDenied, err.Error())
	}

	// CHECKED BEFORE EVERY CASE THAT COULD BE ITS CAUSE, which is the whole point
	// of where this branch sits. IndexCompensationError Unwraps to its cause, so
	// whichever cause-branch is listed above it matches first and downgrades
	// "rollback was incomplete" to that cause's code — "retry" for the
	// concurrency errors, AlreadyExists for a duplicate handle. DataLoss is the
	// honest code: secondary state may be inconsistent and a blind client retry
	// is not the remedy.
	//
	// It sat below the duplicate-handle branches because when only UpdateUser and
	// DeleteUser produced this error, its causes were the concurrency errors
	// below. CreateUser now produces it too, and its cause can be a
	// UsernameExists/EmailExists conflict: a create that could not release the
	// username it had already claimed would have been reported as an ordinary
	// AlreadyExists, leaving that username permanently unclaimable and resolving
	// to nothing. The cause set of a wrapper error grows silently — this branch
	// belongs above all of them.
	var icee *user.IndexCompensationError
	if errors.As(err, &icee) {
		return status.Error(codes.DataLoss, err.Error())
	}

	// User errors
	var unf *user.UserNotFoundError
	if errors.As(err, &unf) {
		return status.Error(codes.NotFound, err.Error())
	}

	// A withdrawal naming a thumbprint the account does not hold is NotFound on
	// the binding, exactly as a missing user is NotFound on the user. It is
	// listed beside that branch because a caller distinguishes the two by the
	// message, not the code: both mean "the thing you named is not there".
	var cbnf *user.CertBindingNotFoundError
	if errors.As(err, &cbnf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var uee *user.UsernameExistsError
	if errors.As(err, &uee) {
		return status.Error(codes.AlreadyExists, err.Error())
	}

	// A taken email is AlreadyExists for the same reason a taken username is;
	// without this it fell through to Internal.
	var eee *user.EmailExistsError
	if errors.As(err, &eee) {
		return status.Error(codes.AlreadyExists, err.Error())
	}

	// A thumbprint already bound to ANOTHER user in the tenant is AlreadyExists
	// for the same reason again: the store refuses to rebind it, because doing
	// so silently would let the caller take over that user's mTLS identity,
	// and a deliberate refusal is not Internal. The message names the user
	// that holds the thumbprint (go-qrdb#536).
	var cbce *user.CertBindingConflictError
	if errors.As(err, &cbce) {
		return status.Error(codes.AlreadyExists, err.Error())
	}

	// A lost concurrent update is Aborted — gRPC's designated code for a failed
	// optimistic-concurrency check, which tells the client to re-read and retry.
	var cue *user.ConcurrentUpdateError
	if errors.As(err, &cue) {
		return status.Error(codes.Aborted, err.Error())
	}

	// An unknown commit outcome is Unavailable, not Internal: the operation may
	// have landed, so the client must reconcile rather than assume failure.
	var coue *user.CommitOutcomeUnknownError
	if errors.As(err, &coue) {
		return status.Error(codes.Unavailable, err.Error())
	}

	var ude *user.UserDisabledError
	if errors.As(err, &ude) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}

	var sui *user.SystemUserImmutableError
	if errors.As(err, &sui) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}

	// An undefined lifecycle state, or one not permitted where it was used, is
	// a malformed argument — the error names the allowed set, so the caller can
	// fix it. Placed with the other argument errors and above the generic store
	// branch.
	var ile *user.InvalidLifecycleError
	if errors.As(err, &ile) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	// A transition the table does not connect is FailedPrecondition, not
	// InvalidArgument: the state named is valid and the caller is not wrong
	// about it — the RECORD is not somewhere the move can start from. The
	// commonest instance is an EnableUser racing a delete, which without this
	// branch was reported as Internal: a server fault for a refusal the server
	// made deliberately, and one no client could learn to expect.
	var lte *user.LifecycleTransitionError
	if errors.As(err, &lte) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}

	var uve *user.ValidationError
	if errors.As(err, &uve) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var use *user.StoreError
	if errors.As(err, &use) {
		return status.Error(codes.Internal, err.Error())
	}

	// Role errors
	var rnf *role.RoleNotFoundError
	if errors.As(err, &rnf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var rne *role.RoleNameExistsError
	if errors.As(err, &rne) {
		return status.Error(codes.AlreadyExists, err.Error())
	}

	// A caller-supplied role ID already in use — including by another tenant,
	// which the identity-index claim refuses. The error does not say where.
	var rie *role.RoleIDExistsError
	if errors.As(err, &rie) {
		return status.Error(codes.AlreadyExists, err.Error())
	}

	// A create under a canonical system role name that is not the canonical
	// role itself. InvalidArgument, NOT AlreadyExists: the name is reserved
	// whether or not anything holds it, so this is a malformed request rather
	// than a collision.
	var rrn *role.ReservedRoleNameError
	if errors.As(err, &rrn) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var sri *role.SystemRoleImmutableError
	if errors.As(err, &sri) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}

	var rve *role.ValidationError
	if errors.As(err, &rve) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var rip *role.InvalidPermissionError
	if errors.As(err, &rip) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var rse *role.StoreError
	if errors.As(err, &rse) {
		return status.Error(codes.Internal, err.Error())
	}

	// Cert errors
	var cnf *cert.CertNotFoundError
	if errors.As(err, &cnf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var cre *cert.CertRevokedError
	if errors.As(err, &cre) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}

	var cge *cert.CertGenerateError
	if errors.As(err, &cge) {
		return status.Error(codes.Internal, err.Error())
	}

	var cee *cert.CertExportError
	if errors.As(err, &cee) {
		return status.Error(codes.Internal, err.Error())
	}

	var cka *cert.CAKeyNotAvailableError
	if errors.As(err, &cka) {
		return status.Error(codes.Unavailable, err.Error())
	}

	var cbs *cert.BarrierSealedError
	if errors.As(err, &cbs) {
		return status.Error(codes.Unavailable, err.Error())
	}

	var cve *cert.ValidationError
	if errors.As(err, &cve) {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	var cse *cert.StoreError
	if errors.As(err, &cse) {
		return status.Error(codes.Internal, err.Error())
	}

	return status.Error(codes.Internal, err.Error())
}

// lastAuthUnix renders a nil-able last-authentication time as seconds since the
// epoch, with zero standing for "never authenticated". Zero is unambiguous here
// because no account authenticates at the epoch.
func lastAuthUnix(at *time.Time) int64 {
	if at == nil {
		return 0
	}
	return at.UTC().Unix()
}
