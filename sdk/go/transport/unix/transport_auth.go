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

package unix

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// mapUserNotFound is mapGRPCError for the single-user endpoints: it turns the
// server's codes.NotFound into the typed not-found every transport returns for
// this condition, so a consumer's errors.As is the same predicate here as it
// is over REST and embedded (go-qrdb#289). Everything else falls through to
// mapGRPCError unchanged.
//
// It is a SEPARATE function rather than a new case in mapGRPCError on purpose.
// mapGRPCError sees only a code and a message and cannot know which subject
// was missing, so a NotFound case there could only guess — and it already
// guesses, mapping every NotFound to KeyNotFoundError. The call site knows,
// because it knows which user it asked for. Keeping the shared mapper
// untouched also leaves go-qrdb#169's edits to it conflict-free.
//
// It is wired ONLY where a NotFound can mean exactly one thing. AssignRole and
// UnassignRole are deliberately excluded: their NotFound carries either a
// missing user or a missing role, and measured on every remote transport the
// two differ only in message text — which is the substring classification #289
// exists to delete, not to relocate.
func mapUserNotFound(method string, id uint64, err error) error {
	if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
		return &transport.UserNotFoundError{ID: id}
	}
	return mapGRPCError(method, err)
}

// mapUserLookupNotFound is mapUserNotFound for a lookup by a non-numeric key.
func mapUserLookupNotFound(method, lookup string, err error) error {
	if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
		return &transport.UserNotFoundError{Lookup: lookup}
	}
	return mapGRPCError(method, err)
}

// CreateUser creates a new passwordless user.
func (t *Transport) CreateUser(ctx context.Context, req *transport.CreateUserRequest) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.CreateUser(ctx, &pb.CreateUserRequest{
		TenantId:             req.TenantID,
		Username:             req.Username,
		Email:                req.Email,
		Name:                 req.Name,
		WithoutEmailLoginKey: req.WithoutEmailLoginKey,
		RoleIds:              req.RoleIDs,
		AuthMethods:          req.AuthMethods,
		Lifecycle:            req.Lifecycle,
		Pending:              req.Pending,
	})
	if err != nil {
		return nil, mapGRPCError("CreateUser", err)
	}

	return protoToUser(resp), nil
}

// GetUser retrieves a user by ID.
func (t *Transport) GetUser(ctx context.Context, id uint64) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.GetUser(ctx, &pb.GetUserRequest{Id: id})
	if err != nil {
		return nil, mapUserNotFound("GetUser", id, err)
	}

	return protoToUser(resp), nil
}

// GetUserByUsername retrieves a user by tenant-scoped username.
func (t *Transport) GetUserByUsername(ctx context.Context, tenantID, username string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.GetUserByUsername(ctx, &pb.GetUserByUsernameRequest{
		TenantId: tenantID,
		Username: username,
	})
	if err != nil {
		return nil, mapUserLookupNotFound("GetUserByUsername", transport.UserLookupKey(tenantID, username), err)
	}

	return protoToUser(resp), nil
}

// GetUserByEmail resolves the canonical login key over the unix-socket gRPC
// channel.
func (t *Transport) GetUserByEmail(ctx context.Context, tenantID, email string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.GetUserByEmail(ctx, &pb.GetUserByEmailRequest{
		TenantId: tenantID,
		Email:    email,
	})
	if err != nil {
		return nil, mapUserLookupNotFound("GetUserByEmail", transport.UserLookupKey(tenantID, email), err)
	}

	return protoToUser(resp), nil
}

// GetUserByCertThumbprint resolves a user by mTLS SPKI thumbprint via the
// qrdb GetUserByCertThumbprint RPC over the unix socket.
func (t *Transport) GetUserByCertThumbprint(ctx context.Context, thumbprint string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.GetUserByCertThumbprint(ctx, &pb.GetUserByCertThumbprintRequest{
		Thumbprint: thumbprint,
	})
	if err != nil {
		return nil, mapUserLookupNotFound("GetUserByCertThumbprint", "cert_spki/"+thumbprint, err)
	}

	return protoToUser(resp), nil
}

// AddCertBinding binds an mTLS SPKI thumbprint to a user via the qrdb
// AddCertBinding RPC over the unix socket.
func (t *Transport) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.AddCertBinding(ctx, &pb.AddCertBindingRequest{
		UserId:       userID,
		Thumbprint:   thumbprint,
		NotAfterUnix: notAfterUnix,
	})
	if err != nil {
		return nil, mapUserNotFound("AddCertBinding", userID, err)
	}

	return protoToUser(resp), nil
}

// RemoveCertBinding withdraws an mTLS SPKI thumbprint from a user via the qrdb
// RemoveCertBinding RPC over the unix socket.
func (t *Transport) RemoveCertBinding(ctx context.Context, userID uint64, thumbprint string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.RemoveCertBinding(ctx, &pb.RemoveCertBindingRequest{
		UserId:     userID,
		Thumbprint: thumbprint,
	})
	if err != nil {
		return nil, mapUserNotFound("RemoveCertBinding", userID, err)
	}

	return protoToUser(resp), nil
}

// UpdateUser applies partial updates to an existing user.
func (t *Transport) UpdateUser(ctx context.Context, req *transport.UpdateUserRequest) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	pbReq := &pb.UpdateUserProtoRequest{
		Id:        req.ID,
		Username:  req.Username,
		Email:     req.Email,
		Name:      req.Name,
		RoleIds:   req.RoleIDs,
		Lifecycle: req.Lifecycle,
	}
	// proto3 cannot tell an absent repeated field from an empty one, so the
	// has_* flag carries the nil-vs-empty distinction that decides between
	// "leave alone" and "clear". Without it a method restriction could never
	// be lifted over gRPC.
	if req.AuthMethods != nil {
		pbReq.AuthMethods = req.AuthMethods
		pbReq.HasAuthMethods = true
	}
	if req.LastAuth != "" {
		at, perr := time.Parse(time.RFC3339, req.LastAuth)
		if perr != nil {
			return nil, &transport.RequestError{Method: "UpdateUser", Err: perr}
		}
		pbReq.LastAuthUnix = at.UTC().Unix()
	}

	resp, err := t.client.UpdateUser(ctx, pbReq)
	if err != nil {
		return nil, mapUserNotFound("UpdateUser", req.ID, err)
	}

	return protoToUser(resp), nil
}

// DeleteUser removes a non-system user by ID.
func (t *Transport) DeleteUser(ctx context.Context, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.DeleteUser(ctx, &pb.DeleteUserRequest{Id: id})
	if err != nil {
		return mapUserNotFound("DeleteUser", id, err)
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
func (t *Transport) EnableUser(ctx context.Context, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.EnableUser(ctx, &pb.EnableUserRequest{Id: id})
	if err != nil {
		return mapUserNotFound("EnableUser", id, err)
	}

	return nil
}

// DisableUser deactivates a user account.
func (t *Transport) DisableUser(ctx context.Context, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	_, err := t.client.DisableUser(ctx, &pb.DisableUserRequest{Id: id})
	if err != nil {
		return mapUserNotFound("DisableUser", id, err)
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
		Id:          req.ID,
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
func (t *Transport) AssignRole(ctx context.Context, userID uint64, roleID string) error {
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
func (t *Transport) UnassignRole(ctx context.Context, userID uint64, roleID string) error {
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

// RevokeCert marks a certificate serial as revoked and reports whether the
// server held a certificate record for it.
func (t *Transport) RevokeCert(ctx context.Context, serial string) (*transport.RevokeResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	resp, err := t.client.RevokeCert(ctx, &pb.RevokeCertRequest{Serial: serial})
	if err != nil {
		return nil, mapGRPCError("RevokeCert", err)
	}

	return &transport.RevokeResult{Serial: serial, RecordFound: resp.GetRecordFound()}, nil
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
		ID:           resp.GetId(),
		TenantID:     resp.GetTenantId(),
		Username:     resp.GetUsername(),
		Email:        resp.GetEmail(),
		Name:         resp.GetName(),
		RoleIDs:      resp.GetRoleIds(),
		System:       resp.GetSystem(),
		Lifecycle:    resp.GetLifecycle(),
		AuthMethods:  resp.GetAuthMethods(),
		LastAuth:     unixToRFC3339(resp.GetLastAuthUnix()),
		CertBindings: protoToCertBindings(resp.GetCertBindings()),
		CreatedAt:    resp.GetCreatedAt(),
		UpdatedAt:    resp.GetUpdatedAt(),
	}
}

// protoToCertBindings converts the proto binding list to the transport form.
// Nil for an empty list, so an account with no bindings reads the same on
// every transport.
func protoToCertBindings(in []*pb.CertBinding) []transport.CertBinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]transport.CertBinding, len(in))
	for i, b := range in {
		out[i] = transport.CertBinding{
			Thumbprint: b.GetThumbprint(),
			Kind:       b.GetKind(),
			NotAfter:   unixToRFC3339(b.GetNotAfterUnix()),
		}
	}
	return out
}

// protoToRole converts a proto RoleResponse to a transport Role.
func protoToRole(resp *pb.RoleResponse) *transport.Role {
	if resp == nil {
		return nil
	}
	return &transport.Role{
		ID:                 resp.GetId(),
		TenantID:           resp.GetTenantId(),
		Name:               resp.GetName(),
		Permissions:        resp.GetPermissions(),
		System:             resp.GetSystem(),
		CreatedAt:          resp.GetCreatedAt(),
		UpdatedAt:          resp.GetUpdatedAt(),
		RetiredPermissions: resp.GetRetiredPermissions(),
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

// unixToRFC3339 renders a last-authentication instant carried as seconds since
// the epoch back into the RFC 3339 form transport.User uses. Zero means "never
// authenticated" and maps to the empty string, not to 1970.
func unixToRFC3339(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}
