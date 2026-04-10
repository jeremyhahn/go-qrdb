// Copyright 2026 Jeremy Hahn
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

package grpc

import (
	"context"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
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

// CreateUser creates a new passwordless user.
func (t *Transport) CreateUser(ctx context.Context, req *transport.CreateUserRequest) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.CreateUser(ctx, &pb.CreateUserRequest{
		TenantId: req.TenantID,
		Username: req.Username,
		RoleIds:  req.RoleIDs,
	})
	if err != nil {
		return nil, mapGRPCError("CreateUser", err)
	}

	return protoToUser(resp), nil
}

// GetUser retrieves a user by ID.
func (t *Transport) GetUser(ctx context.Context, id string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.GetUser(ctx, &pb.GetUserRequest{Id: id})
	if err != nil {
		return nil, mapGRPCError("GetUser", err)
	}

	return protoToUser(resp), nil
}

// UpdateUser applies partial updates to an existing user.
func (t *Transport) UpdateUser(ctx context.Context, req *transport.UpdateUserRequest) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.UpdateUser(ctx, &pb.UpdateUserProtoRequest{
		Id:       req.ID,
		Username: req.Username,
		RoleIds:  req.RoleIDs,
	})
	if err != nil {
		return nil, mapGRPCError("UpdateUser", err)
	}

	return protoToUser(resp), nil
}

// DeleteUser removes a non-system user by ID.
func (t *Transport) DeleteUser(ctx context.Context, id string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.DeleteUser(ctx, &pb.DeleteUserRequest{Id: id})
	if err != nil {
		return mapGRPCError("DeleteUser", err)
	}

	return nil
}

// ListUsers returns a paginated list of users.
func (t *Transport) ListUsers(ctx context.Context, req *transport.ListUsersRequest) (*transport.ListUsersResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.ListUsers(ctx, &pb.ListUsersRequest{
		TenantId: req.TenantID,
		Page:     int32(req.Page),
		PageSize: int32(req.PageSize),
	})
	if err != nil {
		return nil, mapGRPCError("ListUsers", err)
	}

	users := make([]*transport.User, len(resp.GetUsers()))
	for i, u := range resp.GetUsers() {
		users[i] = protoToUser(u)
	}

	return &transport.ListUsersResponse{
		Users:   users,
		Total:   int(resp.GetTotal()),
		HasMore: resp.GetHasMore(),
	}, nil
}

// EnableUser activates a user account.
func (t *Transport) EnableUser(ctx context.Context, id string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.EnableUser(ctx, &pb.EnableUserRequest{Id: id})
	if err != nil {
		return mapGRPCError("EnableUser", err)
	}

	return nil
}

// DisableUser deactivates a user account.
func (t *Transport) DisableUser(ctx context.Context, id string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.DisableUser(ctx, &pb.DisableUserRequest{Id: id})
	if err != nil {
		return mapGRPCError("DisableUser", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// RoleClient implementation
// ---------------------------------------------------------------------------

// CreateRole creates a new role.
func (t *Transport) CreateRole(ctx context.Context, req *transport.CreateRoleRequest) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.CreateRole(ctx, &pb.CreateRoleRequest{
		TenantId:    req.TenantID,
		Name:        req.Name,
		Permissions: req.Permissions,
	})
	if err != nil {
		return nil, mapGRPCError("CreateRole", err)
	}

	return protoToRole(resp), nil
}

// GetRole retrieves a role by ID.
func (t *Transport) GetRole(ctx context.Context, id string) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.GetRole(ctx, &pb.GetRoleRequest{Id: id})
	if err != nil {
		return nil, mapGRPCError("GetRole", err)
	}

	return protoToRole(resp), nil
}

// UpdateRole applies partial updates to an existing role.
func (t *Transport) UpdateRole(ctx context.Context, req *transport.UpdateRoleRequest) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.UpdateRole(ctx, &pb.UpdateRoleProtoRequest{
		Id:          req.ID,
		Name:        req.Name,
		Permissions: req.Permissions,
	})
	if err != nil {
		return nil, mapGRPCError("UpdateRole", err)
	}

	return protoToRole(resp), nil
}

// DeleteRole removes a non-system role by ID.
func (t *Transport) DeleteRole(ctx context.Context, id string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.DeleteRole(ctx, &pb.DeleteRoleRequest{Id: id})
	if err != nil {
		return mapGRPCError("DeleteRole", err)
	}

	return nil
}

// ListRoles returns a paginated list of roles.
func (t *Transport) ListRoles(ctx context.Context, req *transport.ListRolesRequest) (*transport.ListRolesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.ListRoles(ctx, &pb.ListRolesRequest{
		TenantId: req.TenantID,
		Page:     int32(req.Page),
		PageSize: int32(req.PageSize),
	})
	if err != nil {
		return nil, mapGRPCError("ListRoles", err)
	}

	roles := make([]*transport.Role, len(resp.GetRoles()))
	for i, r := range resp.GetRoles() {
		roles[i] = protoToRole(r)
	}

	return &transport.ListRolesResponse{
		Roles:   roles,
		Total:   int(resp.GetTotal()),
		HasMore: resp.GetHasMore(),
	}, nil
}

// AssignRole assigns a role to a user.
func (t *Transport) AssignRole(ctx context.Context, userID, roleID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.AssignRole(ctx, &pb.AssignRoleRequest{
		UserId: userID,
		RoleId: roleID,
	})
	if err != nil {
		return mapGRPCError("AssignRole", err)
	}

	return nil
}

// UnassignRole removes a role assignment from a user.
func (t *Transport) UnassignRole(ctx context.Context, userID, roleID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.UnassignRole(ctx, &pb.UnassignRoleRequest{
		UserId: userID,
		RoleId: roleID,
	})
	if err != nil {
		return mapGRPCError("UnassignRole", err)
	}

	return nil
}

// ListPermissions returns all available permission categories.
func (t *Transport) ListPermissions(ctx context.Context) (*transport.ListPermissionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.ListPermissions(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, mapGRPCError("ListPermissions", err)
	}

	cats := make([]*transport.PermissionCategory, len(resp.GetCategories()))
	for i, c := range resp.GetCategories() {
		cats[i] = &transport.PermissionCategory{
			Name:        c.GetName(),
			Group:       c.GetGroup(),
			Permissions: c.GetPermissions(),
		}
	}

	return &transport.ListPermissionsResponse{Categories: cats}, nil
}

// ---------------------------------------------------------------------------
// CertClient implementation
// ---------------------------------------------------------------------------

// GenerateCert creates a client certificate.
func (t *Transport) GenerateCert(ctx context.Context, req *transport.GenerateCertRequest) (*transport.CertBundle, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.GenerateCert(ctx, &pb.GenerateCertRequest{
		Username: req.Username,
		TenantId: req.TenantID,
		Ttl:      req.TTL,
	})
	if err != nil {
		return nil, mapGRPCError("GenerateCert", err)
	}

	return &transport.CertBundle{
		CertPEM: resp.GetCertPem(),
		KeyPEM:  resp.GetKeyPem(),
		Serial:  resp.GetSerial(),
	}, nil
}

// ExportCert exports a certificate as PKCS#12.
func (t *Transport) ExportCert(ctx context.Context, req *transport.ExportCertRequest) (*transport.ExportCertResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.ExportCert(ctx, &pb.ExportCertRequest{
		CertPem:    req.CertPEM,
		KeyPem:     req.KeyPEM,
		Passphrase: req.Passphrase,
	})
	if err != nil {
		return nil, mapGRPCError("ExportCert", err)
	}

	return &transport.ExportCertResponse{
		PKCS12: resp.GetPkcs12(),
	}, nil
}

// RevokeCert marks a certificate serial as revoked.
func (t *Transport) RevokeCert(ctx context.Context, serial string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.RevokeCert(ctx, &pb.RevokeCertRequest{Serial: serial})
	if err != nil {
		return mapGRPCError("RevokeCert", err)
	}

	return nil
}

// ListCerts lists all issued client certificates for a tenant.
func (t *Transport) ListCerts(ctx context.Context, tenantID string) (*transport.ListCertsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.ListCerts(ctx, &pb.ListCertsRequest{TenantId: tenantID})
	if err != nil {
		return nil, mapGRPCError("ListCerts", err)
	}

	certs := make([]*transport.CertRecord, len(resp.GetCerts()))
	for i, c := range resp.GetCerts() {
		certs[i] = protoToCertRecord(c)
	}

	return &transport.ListCertsResponse{Certs: certs}, nil
}

// ---------------------------------------------------------------------------
// Proto-to-transport conversion helpers
// ---------------------------------------------------------------------------

// protoToUser converts a proto UserResponse to a transport User.
func protoToUser(resp *pb.UserResponse) *transport.User {
	if resp == nil {
		return nil
	}
	return &transport.User{
		ID:        resp.GetId(),
		TenantID:  resp.GetTenantId(),
		Username:  resp.GetUsername(),
		RoleIDs:   resp.GetRoleIds(),
		Enabled:   resp.GetEnabled(),
		System:    resp.GetSystem(),
		CreatedAt: resp.GetCreatedAt(),
		UpdatedAt: resp.GetUpdatedAt(),
	}
}

// protoToRole converts a proto RoleResponse to a transport Role.
func protoToRole(resp *pb.RoleResponse) *transport.Role {
	if resp == nil {
		return nil
	}
	return &transport.Role{
		ID:          resp.GetId(),
		TenantID:    resp.GetTenantId(),
		Name:        resp.GetName(),
		Permissions: resp.GetPermissions(),
		System:      resp.GetSystem(),
		CreatedAt:   resp.GetCreatedAt(),
		UpdatedAt:   resp.GetUpdatedAt(),
	}
}

// protoToCertRecord converts a proto CertRecord to a transport CertRecord.
func protoToCertRecord(rec *pb.CertRecord) *transport.CertRecord {
	if rec == nil {
		return nil
	}
	return &transport.CertRecord{
		Serial:    rec.GetSerial(),
		Username:  rec.GetUsername(),
		TenantID:  rec.GetTenantId(),
		IssuedAt:  rec.GetIssuedAt(),
		ExpiresAt: rec.GetExpiresAt(),
		Revoked:   rec.GetRevoked(),
	}
}
