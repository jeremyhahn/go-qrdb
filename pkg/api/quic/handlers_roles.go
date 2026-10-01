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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

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

// getRoleByNameWireRequest is the JSON wire payload for role.GetByName.
type getRoleByNameWireRequest struct {
	TenantID string `json:"tenantID"`
	Name     string `json:"name"`
}

// resolvePermissionsWireRequest is the JSON wire payload for
// role.ResolvePermissions.
type resolvePermissionsWireRequest struct {
	RoleIDs []string `json:"roleIDs"`
}

// resolvePermissionsWireResponse is the JSON wire payload returned by
// role.ResolvePermissions.
type resolvePermissionsWireResponse struct {
	Permissions []string `json:"permissions"`
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

// roleWireResponse is the JSON wire representation of a role entity.
type roleWireResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenantID"`
	Permissions []string `json:"permissions"`
	System      bool     `json:"system"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`

	// RetiredPermissions are the members of Permissions this build no longer
	// defines — drift visible before role.ReconcileBuiltIn repairs it.
	RetiredPermissions []string `json:"retiredPermissions,omitempty"`
}

// reconcileRolesWireRequest is the JSON wire payload for
// role.ReconcileBuiltIn.
type reconcileRolesWireRequest struct {
	TenantID string `json:"tenantID"`
}

// roleReconcileResultWire is the JSON wire representation of one built-in
// role's reconciliation outcome.
type roleReconcileResultWire struct {
	RoleID  string   `json:"roleID"`
	Name    string   `json:"name"`
	Created bool     `json:"created"`
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

// reconcileRolesWireResponse is the JSON wire payload returned by
// role.ReconcileBuiltIn.
type reconcileRolesWireResponse struct {
	TenantID string                    `json:"tenantID"`
	Roles    []roleReconcileResultWire `json:"roles,omitempty"`
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
// Role handlers
// ---------------------------------------------------------------------------

// handleCreateRole unmarshals a JSON create role request, creates the role
// via the service layer, and returns a serialized JSON role response.
func (s *Server) handleCreateRole(ctx context.Context, payload []byte) ([]byte, error) {
	if s.roleSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "role"}
	}

	var req createRoleWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	perms := stringsToPermissions(req.Permissions)

	rl, err := s.roleSvc.CreateRole(ctx, &role.CreateRoleRequest{
		ID:          req.ID,
		Name:        req.Name,
		TenantID:    req.TenantID,
		Permissions: perms,
	})
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(roleToWireResponse(rl))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleGetRole unmarshals a JSON get role request, retrieves the role by
// ID via the service layer, and returns a serialized JSON role response.
func (s *Server) handleGetRole(ctx context.Context, payload []byte) ([]byte, error) {
	if s.roleSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "role"}
	}

	var req getRoleWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	rl, err := s.roleSvc.GetRole(ctx, req.ID)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(roleToWireResponse(rl))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleGetRoleByName unmarshals a JSON get-role-by-name request, retrieves
// the role by tenant-scoped name via the service layer, and returns a
// serialized JSON role response.
func (s *Server) handleGetRoleByName(ctx context.Context, payload []byte) ([]byte, error) {
	if s.roleSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "role"}
	}

	var req getRoleByNameWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	rl, err := s.roleSvc.GetRoleByName(ctx, req.TenantID, req.Name)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(roleToWireResponse(rl))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleResolvePermissions unmarshals a JSON resolve-permissions request,
// resolves the union of effective permissions for the given role IDs via
// the service layer, and returns a serialized JSON response.
func (s *Server) handleResolvePermissions(ctx context.Context, payload []byte) ([]byte, error) {
	if s.roleSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "role"}
	}

	var req resolvePermissionsWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	perms, err := s.roleSvc.ResolvePermissions(ctx, req.RoleIDs)
	if err != nil {
		return nil, err
	}

	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = p.String()
	}

	data, err := json.Marshal(resolvePermissionsWireResponse{Permissions: out})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return data, nil
}

// handleReconcileRoles unmarshals a JSON reconcile request, converges the
// tenant's stored built-in roles onto this build's definitions via the service
// layer, and returns a serialized JSON report.
func (s *Server) handleReconcileRoles(ctx context.Context, payload []byte) ([]byte, error) {
	if s.roleSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "role"}
	}

	var req reconcileRolesWireRequest
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, &UnmarshalError{Err: err}
		}
	}

	report, err := s.roleSvc.ReconcileBuiltInRoles(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(reconcileReportToWire(report))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return data, nil
}

// reconcileReportToWire converts a role.ReconcileReport to its wire
// representation.
func reconcileReportToWire(report *role.ReconcileReport) reconcileRolesWireResponse {
	if report == nil {
		return reconcileRolesWireResponse{}
	}
	out := reconcileRolesWireResponse{TenantID: report.TenantID}
	out.Roles = make([]roleReconcileResultWire, len(report.Roles))
	for i, r := range report.Roles {
		out.Roles[i] = roleReconcileResultWire{
			RoleID:  r.RoleID,
			Name:    r.Name,
			Created: r.Created,
			Added:   permissionsToStrings(r.Added),
			Removed: permissionsToStrings(r.Removed),
		}
	}
	return out
}

// handleUpdateRole unmarshals a JSON update role request, applies partial
// updates via the service layer, and returns a serialized JSON role response.
func (s *Server) handleUpdateRole(ctx context.Context, payload []byte) ([]byte, error) {
	if s.roleSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "role"}
	}

	var req updateRoleWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	perms := stringsToPermissions(req.Permissions)

	rl, err := s.roleSvc.UpdateRole(ctx, &role.UpdateRoleRequest{
		ID:          req.ID,
		Name:        req.Name,
		Permissions: perms,
	})
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(roleToWireResponse(rl))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleDeleteRole unmarshals a JSON delete role request, removes the role
// via the service layer, and returns a serialized status response.
func (s *Server) handleDeleteRole(ctx context.Context, payload []byte) ([]byte, error) {
	if s.roleSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "role"}
	}

	var req deleteRoleWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.roleSvc.DeleteRole(ctx, req.ID); err != nil {
		return nil, err
	}

	out, err := json.Marshal(statusWireResponse{Status: "ok"})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleListRoles unmarshals a JSON list roles request, retrieves a paginated
// role list from the service layer, and returns a serialized JSON response.
func (s *Server) handleListRoles(ctx context.Context, payload []byte) ([]byte, error) {
	if s.roleSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "role"}
	}

	var req listRolesWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	resp, err := s.roleSvc.ListRoles(ctx, &role.ListRolesRequest{
		TenantID: req.TenantID,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, err
	}

	roles := make([]*roleWireResponse, len(resp.Roles))
	for i, rl := range resp.Roles {
		roles[i] = roleToWireResponse(rl)
	}

	out, err := json.Marshal(listRolesWireResponse{
		Roles:   roles,
		Total:   resp.Total,
		HasMore: resp.HasMore,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleAssignRole unmarshals a JSON assign role request, validates that
// the role exists, adds it to the user's role set via the user service,
// and returns the updated user as a serialized JSON response.
func (s *Server) handleAssignRole(ctx context.Context, payload []byte) ([]byte, error) {
	if s.roleSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "role"}
	}
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req assignRoleWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	// The same gate every role-assignment write passes: roles:assign in the
	// user's tenant, and the role must exist there.
	if err := s.authMW.AuthorizeRoleAssignmentWrite(ctx, s.userSvc, s.roleSvc, req.UserID, []string{req.RoleID}, nil); err != nil {
		return nil, err
	}

	// Get the user and add the role.
	u, err := s.userSvc.GetUser(ctx, req.UserID)
	if err != nil {
		return nil, err
	}

	// Check if role is already assigned.
	for _, rid := range u.RoleIDs {
		if rid == req.RoleID {
			return s.marshalUser(ctx, u)
		}
	}

	roleIDs := append(u.RoleIDs, req.RoleID)
	updated, err := s.userSvc.UpdateUser(ctx, &user.UpdateUserRequest{
		ID:      req.UserID,
		RoleIDs: roleIDs,
	})
	if err != nil {
		return nil, err
	}

	return s.marshalUser(ctx, updated)
}

// handleUnassignRole unmarshals a JSON unassign role request, removes the
// role from the user's role set via the user service, and returns the
// updated user as a serialized JSON response.
func (s *Server) handleUnassignRole(ctx context.Context, payload []byte) ([]byte, error) {
	if s.roleSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "role"}
	}
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req assignRoleWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.authMW.AuthorizeRoleUnassignment(ctx, s.userSvc, req.UserID); err != nil {
		return nil, err
	}

	// Get the user and remove the role.
	u, err := s.userSvc.GetUser(ctx, req.UserID)
	if err != nil {
		return nil, err
	}

	roleIDs := make([]string, 0, len(u.RoleIDs))
	for _, rid := range u.RoleIDs {
		if rid != req.RoleID {
			roleIDs = append(roleIDs, rid)
		}
	}

	updated, err := s.userSvc.UpdateUser(ctx, &user.UpdateUserRequest{
		ID:      req.UserID,
		RoleIDs: roleIDs,
	})
	if err != nil {
		return nil, err
	}

	return s.marshalUser(ctx, updated)
}

// handleListPermissions returns all registered permissions grouped by
// category for UI rendering. The payload is ignored.
func (s *Server) handleListPermissions(_ context.Context, _ []byte) ([]byte, error) {
	cats := permission.Categories()

	resp := make([]*permissionCategoryWireResponse, len(cats))
	for i, cat := range cats {
		perms := make([]string, len(cat.Permissions))
		for j, p := range cat.Permissions {
			perms[j] = p.String()
		}
		resp[i] = &permissionCategoryWireResponse{
			Name:        cat.Name,
			Group:       cat.Group,
			Permissions: perms,
		}
	}

	out, err := json.Marshal(resp)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Role conversion helpers
// ---------------------------------------------------------------------------

// roleToWireResponse converts a role.Role domain object to its JSON wire
// representation.
func roleToWireResponse(rl *role.Role) *roleWireResponse {
	if rl == nil {
		return nil
	}

	perms := make([]string, len(rl.Permissions))
	for i, p := range rl.Permissions {
		perms[i] = p.String()
	}

	return &roleWireResponse{
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

// permissionsToStrings renders a permission slice for the wire. It answers nil
// for an empty input so an omitempty field stays absent.
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

// stringsToPermissions converts a slice of strings to permission.Permission
// values.
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
