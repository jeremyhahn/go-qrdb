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
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// User management handlers
// ---------------------------------------------------------------------------

// CreateUser creates a new passwordless user.
func (s *qrdbServiceServer) CreateUser(ctx context.Context, req *pb.CreateUserRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
	}

	if len(req.GetRoleIds()) > 0 {
		if err := s.authMW.AuthorizeRoleAssignmentWriteOnCreate(ctx, s.roleSvc, req.GetTenantId(), req.GetRoleIds()); err != nil {
			return nil, mapServiceError(err)
		}
	}

	u, err := s.userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             req.GetUsername(),
		Email:                req.GetEmail(),
		Name:                 req.GetName(),
		WithoutEmailLoginKey: req.GetWithoutEmailLoginKey(),
		TenantID:             req.GetTenantId(),
		RoleIDs:              req.GetRoleIds(),
		AuthMethods:          req.GetAuthMethods(),
		Lifecycle:            user.Lifecycle(req.GetLifecycle()),
		Pending:              req.GetPending(),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	return s.userResponse(ctx, u)
}

// GetUser retrieves a user by ID.
func (s *qrdbServiceServer) GetUser(ctx context.Context, req *pb.GetUserRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
	}

	u, err := s.userSvc.GetUser(ctx, req.GetId())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return s.userResponse(ctx, u)
}

// GetUserByUsername retrieves a user by tenant-scoped username.
func (s *qrdbServiceServer) GetUserByUsername(ctx context.Context, req *pb.GetUserByUsernameRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
	}

	u, err := s.userSvc.GetUserByUsername(ctx, req.GetTenantId(), req.GetUsername())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return s.userResponse(ctx, u)
}

// GetUserByEmail retrieves a user by tenant-scoped email — the canonical login
// key. Same permission and error mapping as its username sibling; they differ
// only in which secondary index answers.
func (s *qrdbServiceServer) GetUserByEmail(ctx context.Context, req *pb.GetUserByEmailRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
	}

	u, err := s.userSvc.GetUserByEmail(ctx, req.GetTenantId(), req.GetEmail())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return s.userResponse(ctx, u)
}

// GetUserByCertThumbprint resolves a user by mTLS SPKI thumbprint.
func (s *qrdbServiceServer) GetUserByCertThumbprint(ctx context.Context, req *pb.GetUserByCertThumbprintRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
	}

	u, err := s.userSvc.UserByCertThumbprint(ctx, req.GetThumbprint())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return s.userResponse(ctx, u)
}

// AddCertBinding binds an mTLS SPKI thumbprint to a user.
func (s *qrdbServiceServer) AddCertBinding(ctx context.Context, req *pb.AddCertBindingRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
	}

	// Binding a thumbprint binds a key: the certificate target rule applies.
	if err := s.authMW.AuthorizeCertBindingTarget(ctx, s.userSvc, req.GetUserId()); err != nil {
		return nil, mapServiceError(err)
	}
	u, err := s.userSvc.AddCertBinding(ctx, req.GetUserId(), req.GetThumbprint(), req.GetNotAfterUnix())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return s.userResponse(ctx, u)
}

// RemoveCertBinding withdraws an mTLS SPKI thumbprint from a user.
func (s *qrdbServiceServer) RemoveCertBinding(ctx context.Context, req *pb.RemoveCertBindingRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
	}

	u, err := s.userSvc.RemoveCertBinding(ctx, req.GetUserId(), req.GetThumbprint())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return s.userResponse(ctx, u)
}

// UpdateUser applies partial updates to an existing user.
func (s *qrdbServiceServer) UpdateUser(ctx context.Context, req *pb.UpdateUserProtoRequest) (*pb.UserResponse, error) {
	if s.userSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
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
	// could never LIFT the restriction over gRPC: an empty list would read as
	// "no change" and the clear would silently do nothing.
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
	if req.GetHasRoleBindings() {
		updateReq.RoleBindings = protoToRoleBindings(req.GetRoleBindings())
	}
	if req.GetHasAttributes() {
		updateReq.Attributes = req.GetAttributes()
		if updateReq.Attributes == nil {
			updateReq.Attributes = map[string]string{}
		}
	}

	if authmw.TouchesRoleAssignment(updateReq.RoleIDs, updateReq.RoleBindings, updateReq.Attributes) {
		if err := s.authMW.AuthorizeRoleAssignmentWrite(ctx, s.userSvc, s.roleSvc, req.GetId(), updateReq.RoleIDs, updateReq.RoleBindings); err != nil {
			return nil, mapServiceError(err)
		}
	}

	u, err := s.userSvc.UpdateUser(ctx, updateReq)
	if err != nil {
		return nil, mapServiceError(err)
	}

	return s.userResponse(ctx, u)
}

// DeleteUser removes a non-system user by ID.
func (s *qrdbServiceServer) DeleteUser(ctx context.Context, req *pb.DeleteUserRequest) (*emptypb.Empty, error) {
	if s.userSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
	}

	if err := s.userSvc.DeleteUser(ctx, req.GetId()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// ListUsers returns a paginated list of users.
func (s *qrdbServiceServer) ListUsers(ctx context.Context, req *pb.ListUsersRequest) (*pb.ListUsersResponse, error) {
	if s.userSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
	}

	resp, err := s.userSvc.ListUsers(ctx, &user.ListUsersRequest{
		TenantID: req.GetTenantId(),
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}

	users := make([]*pb.UserResponse, len(resp.Users))
	for i, u := range resp.Users {
		if users[i], err = s.userResponse(ctx, u); err != nil {
			return nil, err
		}
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
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
	}

	if err := s.userSvc.EnableUser(ctx, req.GetId()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// DisableUser deactivates a user account.
func (s *qrdbServiceServer) DisableUser(ctx context.Context, req *pb.DisableUserRequest) (*emptypb.Empty, error) {
	if s.userSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
	}

	if err := s.userSvc.DisableUser(ctx, req.GetId()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// ---------------------------------------------------------------------------
// Role management handlers
// ---------------------------------------------------------------------------

// CreateRole creates a new role.
func (s *qrdbServiceServer) CreateRole(ctx context.Context, req *pb.CreateRoleRequest) (*pb.RoleResponse, error) {
	if s.roleSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "role"})
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
		return nil, mapServiceError(err)
	}

	return roleToProto(r), nil
}

// GetRole retrieves a role by ID.
func (s *qrdbServiceServer) GetRole(ctx context.Context, req *pb.GetRoleRequest) (*pb.RoleResponse, error) {
	if s.roleSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "role"})
	}

	r, err := s.roleSvc.GetRole(ctx, req.GetId())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return roleToProto(r), nil
}

// GetRoleByName retrieves a role by its tenant-scoped name.
func (s *qrdbServiceServer) GetRoleByName(ctx context.Context, req *pb.GetRoleByNameRequest) (*pb.RoleResponse, error) {
	if s.roleSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "role"})
	}

	r, err := s.roleSvc.GetRoleByName(ctx, req.GetTenantId(), req.GetName())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return roleToProto(r), nil
}

// UpdateRole applies partial updates to an existing role.
func (s *qrdbServiceServer) UpdateRole(ctx context.Context, req *pb.UpdateRoleProtoRequest) (*pb.RoleResponse, error) {
	if s.roleSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "role"})
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
		return nil, mapServiceError(err)
	}

	return roleToProto(r), nil
}

// DeleteRole removes a non-system role by ID.
func (s *qrdbServiceServer) DeleteRole(ctx context.Context, req *pb.DeleteRoleRequest) (*emptypb.Empty, error) {
	if s.roleSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "role"})
	}

	if err := s.roleSvc.DeleteRole(ctx, req.GetId()); err != nil {
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// ListRoles returns a paginated list of roles.
func (s *qrdbServiceServer) ListRoles(ctx context.Context, req *pb.ListRolesRequest) (*pb.ListRolesResponse, error) {
	if s.roleSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "role"})
	}

	resp, err := s.roleSvc.ListRoles(ctx, &role.ListRolesRequest{
		TenantID: req.GetTenantId(),
		Page:     int(req.GetPage()),
		PageSize: int(req.GetPageSize()),
	})
	if err != nil {
		return nil, mapServiceError(err)
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
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "role"})
	}
	if s.userSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
	}

	// The same gate every role-assignment write passes: roles:assign in the
	// user's tenant, and the role must exist there.
	if err := s.authMW.AuthorizeRoleAssignmentWrite(ctx, s.userSvc, s.roleSvc, req.GetUserId(), []string{req.GetRoleId()}, nil); err != nil {
		return nil, mapServiceError(err)
	}

	// Get the user and add the role.
	u, err := s.userSvc.GetUser(ctx, req.GetUserId())
	if err != nil {
		return nil, mapServiceError(err)
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
		return nil, mapServiceError(err)
	}

	return &emptypb.Empty{}, nil
}

// UnassignRole removes a role assignment from a user.
func (s *qrdbServiceServer) UnassignRole(ctx context.Context, req *pb.UnassignRoleRequest) (*emptypb.Empty, error) {
	if s.roleSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "role"})
	}
	if s.userSvc == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "user"})
	}

	if err := s.authMW.AuthorizeRoleUnassignment(ctx, s.userSvc, req.GetUserId()); err != nil {
		return nil, mapServiceError(err)
	}

	// Get the user and remove the role.
	u, err := s.userSvc.GetUser(ctx, req.GetUserId())
	if err != nil {
		return nil, mapServiceError(err)
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
		return nil, mapServiceError(err)
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
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "role"})
	}

	perms, err := s.roleSvc.ResolvePermissions(ctx, req.GetRoleIds())
	if err != nil {
		return nil, mapServiceError(err)
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
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "role"})
	}

	report, err := s.roleSvc.ReconcileBuiltInRoles(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapServiceError(err)
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

// callerTenant is the tenant every certificate enrolment RPC acts on: the
// caller's own. The certs verbs are ScopeSelfStrict, so no request names
// another.
func callerTenant(ctx context.Context) string {
	return tenantid.Normalize(tenant.FromContext(ctx))
}

func enrolledToProto(e *service.EnrolledCertificate) *pb.EnrolledCertResponse {
	return &pb.EnrolledCertResponse{
		UserId:     e.UserID,
		Username:   e.Username,
		TenantId:   e.TenantID,
		CertPem:    string(e.CertPEM),
		Serial:     e.Serial,
		Thumbprint: e.Thumbprint,
		NotAfter:   e.NotAfter.UTC().Format(time.RFC3339),
	}
}

// IssueCert issues a certificate from a CSR for an existing account of the
// caller's own tenant and binds it. The requester keeps its key.
func (s *qrdbServiceServer) IssueCert(ctx context.Context, req *pb.IssueCertRequest) (*pb.EnrolledCertResponse, error) {
	if s.certEnroll == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "cert enrollment"})
	}
	var ttl time.Duration
	if req.GetTtl() != "" {
		d, err := time.ParseDuration(req.GetTtl())
		if err != nil {
			return nil, mapServiceError(&TTLParseError{Value: req.GetTtl(), Err: err})
		}
		ttl = d
	}
	tenantID := callerTenant(ctx)
	if err := s.authorizeCertTenant(ctx, tenantID, permission.CertsIssue); err != nil {
		return nil, authmw.GRPCError(err, codes.PermissionDenied)
	}
	enrolled, err := s.certEnroll.SignCertificate(ctx, &service.SignCertificateRequest{
		TenantID: tenantID,
		Username: req.GetUsername(),
		CSRPEM:   []byte(req.GetCsrPem()),
		TTL:      ttl,
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return enrolledToProto(enrolled), nil
}

// EnrollCert binds a bring-your-own certificate to an existing account of the
// caller's own tenant, verified against the tenant's trust anchors.
func (s *qrdbServiceServer) EnrollCert(ctx context.Context, req *pb.EnrollCertRequest) (*pb.EnrolledCertResponse, error) {
	if s.certEnroll == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "cert enrollment"})
	}
	tenantID := callerTenant(ctx)
	if err := s.authorizeCertTenant(ctx, tenantID, permission.CertsEnroll); err != nil {
		return nil, authmw.GRPCError(err, codes.PermissionDenied)
	}
	enrolled, err := s.certEnroll.EnrollCertificate(ctx, &service.EnrollCertificateRequest{
		TenantID: tenantID,
		Username: req.GetUsername(),
		CertPEM:  []byte(req.GetCertPem()),
		ChainPEM: []byte(req.GetChainPem()),
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return enrolledToProto(enrolled), nil
}

// IssueDelegation has the caller's own tenant's CA issue a delegation
// certificate to a service principal. The principal keeps its key.
func (s *qrdbServiceServer) IssueDelegation(ctx context.Context, req *pb.IssueDelegationRequest) (*pb.IssuedDelegationResponse, error) {
	if s.certEnroll == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "cert enrollment"})
	}
	var ttl time.Duration
	if req.GetTtl() != "" {
		d, err := time.ParseDuration(req.GetTtl())
		if err != nil {
			return nil, mapServiceError(&TTLParseError{Value: req.GetTtl(), Err: err})
		}
		ttl = d
	}
	tenantID := callerTenant(ctx)
	if err := s.authorizeCertTenant(ctx, tenantID, permission.CertsDelegate); err != nil {
		return nil, authmw.GRPCError(err, codes.PermissionDenied)
	}
	issued, err := s.certEnroll.IssueDelegation(ctx, &service.IssueDelegationRequest{
		TenantID:  tenantID,
		Principal: req.GetPrincipal(),
		Scope:     req.GetScope(),
		CSRPEM:    []byte(req.GetCsrPem()),
		TTL:       ttl,
	})
	if err != nil {
		return nil, mapServiceError(err)
	}
	return &pb.IssuedDelegationResponse{
		TenantId:  issued.TenantID,
		Principal: issued.Principal,
		Scope:     issued.Scope,
		CertPem:   string(issued.CertPEM),
		Serial:    issued.Serial,
		NotAfter:  issued.NotAfter.UTC().Format(time.RFC3339),
	}, nil
}

func anchorsToProto(anchors []trustanchor.Anchor) *pb.TrustAnchorListResponse {
	out := &pb.TrustAnchorListResponse{Anchors: make([]*pb.TrustAnchor, 0, len(anchors))}
	for _, a := range anchors {
		ta := &pb.TrustAnchor{
			Fingerprint: a.Fingerprint,
			Subject:     a.Subject,
			NotBefore:   a.NotBefore.UTC().Format(time.RFC3339),
			NotAfter:    a.NotAfter.UTC().Format(time.RFC3339),
			CertPem:     string(a.CertPEM),
		}
		if !a.AddedAt.IsZero() {
			ta.AddedAt = a.AddedAt.UTC().Format(time.RFC3339)
		}
		out.Anchors = append(out.Anchors, ta)
	}
	return out
}

// TrustAnchorAdd registers trust anchors of tenant_id, the caller's own.
func (s *qrdbServiceServer) TrustAnchorAdd(ctx context.Context, req *pb.TrustAnchorAddRequest) (*pb.TrustAnchorListResponse, error) {
	if s.certEnroll == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "cert enrollment"})
	}
	if err := s.authorizeCertTenant(ctx, req.GetTenantId(), permission.TrustAnchorsAdd); err != nil {
		return nil, authmw.GRPCError(err, codes.PermissionDenied)
	}
	anchors, err := s.certEnroll.AddTrustAnchors(ctx, req.GetTenantId(), []byte(req.GetCertsPem()))
	if err != nil {
		return nil, mapServiceError(err)
	}
	return anchorsToProto(anchors), nil
}

// TrustAnchorList lists tenant_id's trust anchors, the caller's own.
func (s *qrdbServiceServer) TrustAnchorList(ctx context.Context, req *pb.TrustAnchorListRequest) (*pb.TrustAnchorListResponse, error) {
	if s.certEnroll == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "cert enrollment"})
	}
	if err := s.authorizeCertTenant(ctx, req.GetTenantId(), permission.TrustAnchorsList); err != nil {
		return nil, authmw.GRPCError(err, codes.PermissionDenied)
	}
	anchors, err := s.certEnroll.ListTrustAnchors(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapServiceError(err)
	}
	return anchorsToProto(anchors), nil
}

// TrustAnchorRemove withdraws one of tenant_id's trust anchors, the
// caller's own.
func (s *qrdbServiceServer) TrustAnchorRemove(ctx context.Context, req *pb.TrustAnchorRemoveRequest) (*emptypb.Empty, error) {
	if s.certEnroll == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "cert enrollment"})
	}
	if err := s.authorizeCertTenant(ctx, req.GetTenantId(), permission.TrustAnchorsRemove); err != nil {
		return nil, authmw.GRPCError(err, codes.PermissionDenied)
	}
	if err := s.certEnroll.RemoveTrustAnchor(ctx, req.GetTenantId(), req.GetFingerprint()); err != nil {
		return nil, mapServiceError(err)
	}
	return &emptypb.Empty{}, nil
}

// RevokeCert marks a certificate serial as revoked and reports whether this
// node held a CertRecord for it. See RevokeCertResponse.
func (s *qrdbServiceServer) RevokeCert(ctx context.Context, req *pb.RevokeCertRequest) (*pb.RevokeCertResponse, error) {
	if s.certProv == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "cert"})
	}

	if err := s.authorizeCertRevoke(ctx, req.GetSerial()); err != nil {
		return nil, authmw.GRPCError(err, codes.PermissionDenied)
	}

	result, err := s.certProv.RevokeCert(ctx, req.GetSerial())
	if err != nil {
		return nil, mapServiceError(err)
	}

	return &pb.RevokeCertResponse{RecordFound: result.RecordFound()}, nil
}

// ListCerts lists all issued client certificates for a tenant.
func (s *qrdbServiceServer) ListCerts(ctx context.Context, req *pb.ListCertsRequest) (*pb.ListCertsResponse, error) {
	if s.certProv == nil {
		return nil, mapServiceError(&ServiceNotConfiguredError{Service: "cert"})
	}

	if err := s.authorizeCertTenant(ctx, req.GetTenantId(), permission.CertsList); err != nil {
		return nil, authmw.GRPCError(err, codes.PermissionDenied)
	}

	records, err := s.certProv.ListCerts(ctx, req.GetTenantId())
	if err != nil {
		return nil, mapServiceError(err)
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

// userResponse projects u (see userToProto) with this server's factor
// lister, mapping a failed factor lookup to its gRPC status.
func (s *qrdbServiceServer) userResponse(ctx context.Context, u *user.User) (*pb.UserResponse, error) {
	resp, err := userToProto(ctx, s.userAuthFactorSvc, u)
	if err != nil {
		return nil, mapServiceError(err)
	}
	return resp, nil
}

// userToProto converts a user.User to its protobuf representation. Factors
// metadata and the derived MFA fields are computed at read time from the
// real enrollment records via lister (service.UserAuthFactorServicer), read
// once -- never from a stored field on u -- so they cannot drift from what
// /v1/users/factors (or its gRPC equivalent) reports. lister may be nil (the
// auth-factor subsystem is not configured for this deployment); a lister
// that fails returns the *user.FactorLookupError rather than a response
// claiming no factors.
func userToProto(ctx context.Context, lister user.FactorLister, u *user.User) (*pb.UserResponse, error) {
	if u == nil {
		return nil, nil
	}
	factors, err := user.ResolveFactorView(ctx, lister, u.ID)
	if err != nil {
		return nil, err
	}
	return &pb.UserResponse{
		Id:          u.ID,
		TenantId:    u.TenantID,
		Username:    u.Username,
		Email:       u.Email,
		Name:        u.Name,
		RoleIds:     u.RoleIDs,
		System:      u.System,
		Lifecycle:   string(u.Lifecycle),
		AuthMethods: u.AuthMethods,
		// Seconds rather than RFC 3339 because this is a point in time a client
		// compares and orders, not one it displays; zero says "never
		// authenticated" without a second presence field.
		LastAuthUnix:  lastAuthUnix(u.LastAuth),
		CertBindings:  certBindingsToProto(u.CertBindings),
		RoleBindings:  roleBindingsToProto(u.RoleBindings),
		Attributes:    u.Attributes,
		Factors:       factorsToProto(factors.Factors),
		MfaEnabled:    factors.MFAEnabled,
		MfaMethods:    factors.MFAMethods,
		MfaEnrolledAt: factors.MFAEnrolledAt,
		CreatedAt:     u.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     u.UpdatedAt.Format(time.RFC3339),
	}, nil
}

// certBindingsToProto renders the account's bindings in full, expired ones
// included, exactly as user.WireCertBindings does for the JSON transports;
// only the expiry's encoding differs, seconds here for the same reason
// last_auth_unix is seconds.
func certBindingsToProto(bindings []user.CertBinding) []*pb.CertBinding {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]*pb.CertBinding, len(bindings))
	for i, b := range bindings {
		out[i] = &pb.CertBinding{Thumbprint: b.Thumbprint, Kind: b.Kind.String()}
		if !b.NotAfter.IsZero() {
			out[i].NotAfterUnix = b.NotAfter.UTC().Unix()
		}
	}
	return out
}

// roleBindingsToProto renders the account's tenant-scoped role bindings.
func roleBindingsToProto(bindings []user.RoleBinding) []*pb.RoleBinding {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]*pb.RoleBinding, len(bindings))
	for i, b := range bindings {
		out[i] = &pb.RoleBinding{RoleId: b.RoleID, TenantId: b.TenantID}
	}
	return out
}

// protoToRoleBindings is the inverse of roleBindingsToProto, used by
// UpdateUser to translate a wire request back into the domain shape.
func protoToRoleBindings(bindings []*pb.RoleBinding) []user.RoleBinding {
	out := make([]user.RoleBinding, len(bindings))
	for i, b := range bindings {
		out[i] = user.RoleBinding{RoleID: b.GetRoleId(), TenantID: b.GetTenantId()}
	}
	return out
}

// factorsToProto renders factor METADATA (see user.FactorView); the opaque
// Data blob a factor interprets during verification never crosses this or
// any other transport (go-qrdb#192/#197).
func factorsToProto(factors []transport.FactorMetadata) []*pb.FactorMetadata {
	if len(factors) == 0 {
		return nil
	}
	out := make([]*pb.FactorMetadata, len(factors))
	for i, f := range factors {
		out[i] = &pb.FactorMetadata{Id: f.ID, FactorName: f.FactorName, CreatedAt: f.CreatedAt}
	}
	return out
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

// lastAuthUnix renders a nil-able last-authentication time as seconds since the
// epoch, with zero standing for "never authenticated". Zero is unambiguous here
// because no account authenticates at the epoch.
func lastAuthUnix(at *time.Time) int64 {
	if at == nil {
		return 0
	}
	return at.UTC().Unix()
}
