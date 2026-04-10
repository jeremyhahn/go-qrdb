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
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
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
		Username: req.GetUsername(),
		TenantID: req.GetTenantId(),
		RoleIDs:  req.GetRoleIds(),
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

// UpdateUser applies partial updates to an existing user.
func (s *qrdbServiceServer) UpdateUser(ctx context.Context, req *pb.UpdateUserProtoRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "user"}).Error())
	}

	updateReq := &user.UpdateUserRequest{
		ID:       req.GetId(),
		Username: req.GetUsername(),
	}
	if len(req.GetRoleIds()) > 0 {
		updateReq.RoleIDs = req.GetRoleIds()
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

// ---------------------------------------------------------------------------
// Certificate management handlers
// ---------------------------------------------------------------------------

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

// RevokeCert marks a certificate serial as revoked.
func (s *qrdbServiceServer) RevokeCert(ctx context.Context, req *pb.RevokeCertRequest) (*emptypb.Empty, error) {
	if s.certProv == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "cert"}).Error())
	}

	if err := s.certProv.RevokeCert(ctx, req.GetSerial()); err != nil {
		return nil, mapAuthServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// ListCerts lists all issued client certificates for a tenant.
func (s *qrdbServiceServer) ListCerts(ctx context.Context, req *pb.ListCertsRequest) (*pb.ListCertsResponse, error) {
	if s.certProv == nil {
		return nil, status.Error(codes.Unimplemented, (&ServiceNotConfiguredError{Service: "cert"}).Error())
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
		Id:        u.ID,
		TenantId:  u.TenantID,
		Username:  u.Username,
		RoleIds:   u.RoleIDs,
		Enabled:   u.Enabled,
		System:    u.System,
		CreatedAt: u.CreatedAt.Format(time.RFC3339),
		UpdatedAt: u.UpdatedAt.Format(time.RFC3339),
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
	return &pb.RoleResponse{
		Id:          r.ID,
		TenantId:    r.TenantID,
		Name:        r.Name,
		Permissions: perms,
		System:      r.System,
		CreatedAt:   r.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   r.UpdatedAt.Format(time.RFC3339),
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

	// User errors
	var unf *user.UserNotFoundError
	if errors.As(err, &unf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var uee *user.UsernameExistsError
	if errors.As(err, &uee) {
		return status.Error(codes.AlreadyExists, err.Error())
	}

	var ude *user.UserDisabledError
	if errors.As(err, &ude) {
		return status.Error(codes.FailedPrecondition, err.Error())
	}

	var sui *user.SystemUserImmutableError
	if errors.As(err, &sui) {
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
