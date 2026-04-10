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

package rest

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Role request/response types
// ---------------------------------------------------------------------------

// createRoleRequest is the JSON payload for POST /roles.
type createRoleRequest struct {
	Name        string   `json:"name"`
	TenantID    string   `json:"tenantID"`
	Permissions []string `json:"permissions"`
}

// updateRoleRequest is the JSON payload for PUT /roles/{id}.
type updateRoleRequest struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

// assignRoleRequest is the JSON payload for POST /roles/assign and
// POST /roles/unassign.
type assignRoleRequest struct {
	UserID string `json:"userID"`
	RoleID string `json:"roleID"`
}

// roleResponse is the JSON representation of a role entity.
type roleResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenantID"`
	Permissions []string `json:"permissions"`
	System      bool     `json:"system"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

// listRolesResponse is the JSON payload returned by GET /roles/list.
type listRolesResponse struct {
	Roles   []*roleResponse `json:"roles"`
	Total   int             `json:"total"`
	HasMore bool            `json:"hasMore"`
}

// permissionCategoryResponse is the JSON representation of a permission
// category for UI rendering.
type permissionCategoryResponse struct {
	Name        string   `json:"name"`
	Group       string   `json:"group"`
	Permissions []string `json:"permissions"`
}

// listPermissionsResponse is the JSON payload returned by GET /permissions.
type listPermissionsResponse struct {
	Categories []*permissionCategoryResponse `json:"categories"`
}

// ---------------------------------------------------------------------------
// Role handlers
// ---------------------------------------------------------------------------

// handleCreateRole handles POST /roles -- creates a new role with the
// specified name, tenant, and permissions.
func (s *Server) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	if s.roleSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "role"})
		return
	}

	var req createRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	perms := stringsToPermissions(req.Permissions)

	rl, err := s.roleSvc.CreateRole(r.Context(), &role.CreateRoleRequest{
		Name:        req.Name,
		TenantID:    req.TenantID,
		Permissions: perms,
	})
	if err != nil {
		writeErrorJSON(w, mapRoleError(err), err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(roleToResponse(rl))
}

// handleListRoles handles GET /roles/list -- returns a paginated role list.
// Query parameters: tenantID, page, pageSize.
func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	if s.roleSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "role"})
		return
	}

	q := r.URL.Query()

	listReq := &role.ListRolesRequest{
		TenantID: q.Get("tenantID"),
	}

	if pageStr := q.Get("page"); pageStr != "" {
		page, err := strconv.Atoi(pageStr)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "page", Err: err})
			return
		}
		listReq.Page = page
	}

	if pageSizeStr := q.Get("pageSize"); pageSizeStr != "" {
		pageSize, err := strconv.Atoi(pageSizeStr)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "pageSize", Err: err})
			return
		}
		listReq.PageSize = pageSize
	}

	resp, err := s.roleSvc.ListRoles(r.Context(), listReq)
	if err != nil {
		writeErrorJSON(w, mapRoleError(err), err)
		return
	}

	roles := make([]*roleResponse, len(resp.Roles))
	for i, rl := range resp.Roles {
		roles[i] = roleToResponse(rl)
	}

	writeJSON(w, listRolesResponse{
		Roles:   roles,
		Total:   resp.Total,
		HasMore: resp.HasMore,
	})
}

// handleRole dispatches role operations by extracting the role ID from
// the URL path and routing based on HTTP method.
//
// Routes:
//   - GET    /roles/{id} -> get role
//   - PUT    /roles/{id} -> update role
//   - DELETE /roles/{id} -> delete role
func (s *Server) handleRole(w http.ResponseWriter, r *http.Request) {
	if s.roleSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "role"})
		return
	}

	// Extract path after "/roles/": e.g., "abc-123".
	id := strings.TrimPrefix(r.URL.Path, "/roles/")
	if id == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "id"})
		return
	}

	handler, ok := roleMethodHandlers[r.Method]
	if !ok {
		w.Header().Set("Allow", "GET, PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	handler(s, w, r, id)
}

// roleMethodHandler handles a role request for a specific HTTP method.
type roleMethodHandler func(s *Server, w http.ResponseWriter, r *http.Request, id string)

// roleMethodHandlers maps HTTP methods to their role handler functions.
var roleMethodHandlers = map[string]roleMethodHandler{
	http.MethodGet:    (*Server).handleGetRole,
	http.MethodPut:    (*Server).handleUpdateRole,
	http.MethodDelete: (*Server).handleDeleteRole,
}

// handleGetRole handles GET /roles/{id} -- retrieves a role by ID.
func (s *Server) handleGetRole(w http.ResponseWriter, r *http.Request, id string) {
	rl, err := s.roleSvc.GetRole(r.Context(), id)
	if err != nil {
		writeErrorJSON(w, mapRoleError(err), err)
		return
	}

	writeJSON(w, roleToResponse(rl))
}

// handleUpdateRole handles PUT /roles/{id} -- applies partial updates.
func (s *Server) handleUpdateRole(w http.ResponseWriter, r *http.Request, id string) {
	var req updateRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	perms := stringsToPermissions(req.Permissions)

	rl, err := s.roleSvc.UpdateRole(r.Context(), &role.UpdateRoleRequest{
		ID:          id,
		Name:        req.Name,
		Permissions: perms,
	})
	if err != nil {
		writeErrorJSON(w, mapRoleError(err), err)
		return
	}

	writeJSON(w, roleToResponse(rl))
}

// handleDeleteRole handles DELETE /roles/{id} -- removes a non-system role.
func (s *Server) handleDeleteRole(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.roleSvc.DeleteRole(r.Context(), id); err != nil {
		writeErrorJSON(w, mapRoleError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleAssignRole handles POST /roles/assign -- adds a role to a user's
// role set. This handler requires both the role and user services.
func (s *Server) handleAssignRole(w http.ResponseWriter, r *http.Request) {
	if s.roleSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "role"})
		return
	}
	if s.userSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "user"})
		return
	}

	var req assignRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	// Validate that the role exists.
	if _, err := s.roleSvc.GetRole(r.Context(), req.RoleID); err != nil {
		writeErrorJSON(w, mapRoleError(err), err)
		return
	}

	// Get the user and add the role.
	u, err := s.userSvc.GetUser(r.Context(), req.UserID)
	if err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	// Check if role is already assigned.
	for _, rid := range u.RoleIDs {
		if rid == req.RoleID {
			writeJSON(w, userToResponse(u))
			return
		}
	}

	roleIDs := append(u.RoleIDs, req.RoleID)
	updated, err := s.userSvc.UpdateUser(r.Context(), &user.UpdateUserRequest{
		ID:      req.UserID,
		RoleIDs: roleIDs,
	})
	if err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	writeJSON(w, userToResponse(updated))
}

// handleUnassignRole handles POST /roles/unassign -- removes a role from
// a user's role set. This handler requires both the role and user services.
func (s *Server) handleUnassignRole(w http.ResponseWriter, r *http.Request) {
	if s.roleSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "role"})
		return
	}
	if s.userSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "user"})
		return
	}

	var req assignRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	// Get the user and remove the role.
	u, err := s.userSvc.GetUser(r.Context(), req.UserID)
	if err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	roleIDs := make([]string, 0, len(u.RoleIDs))
	for _, rid := range u.RoleIDs {
		if rid != req.RoleID {
			roleIDs = append(roleIDs, rid)
		}
	}

	updated, err := s.userSvc.UpdateUser(r.Context(), &user.UpdateUserRequest{
		ID:      req.UserID,
		RoleIDs: roleIDs,
	})
	if err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	writeJSON(w, userToResponse(updated))
}

// handleListPermissions handles GET /permissions -- returns all registered
// permissions grouped by category for UI rendering.
func (s *Server) handleListPermissions(w http.ResponseWriter, _ *http.Request) {
	cats := permission.Categories()

	resp := make([]*permissionCategoryResponse, len(cats))
	for i, cat := range cats {
		perms := make([]string, len(cat.Permissions))
		for j, p := range cat.Permissions {
			perms[j] = p.String()
		}
		resp[i] = &permissionCategoryResponse{
			Name:        cat.Name,
			Group:       cat.Group,
			Permissions: perms,
		}
	}

	writeJSON(w, listPermissionsResponse{Categories: resp})
}

// ---------------------------------------------------------------------------
// Role conversion and error mapping
// ---------------------------------------------------------------------------

// roleToResponse converts a role.Role to its JSON response representation.
func roleToResponse(rl *role.Role) *roleResponse {
	if rl == nil {
		return nil
	}

	perms := make([]string, len(rl.Permissions))
	for i, p := range rl.Permissions {
		perms[i] = p.String()
	}

	return &roleResponse{
		ID:          rl.ID,
		Name:        rl.Name,
		TenantID:    rl.TenantID,
		Permissions: perms,
		System:      rl.System,
		CreatedAt:   rl.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   rl.UpdatedAt.Format(time.RFC3339),
	}
}

// stringsToPermissions converts a slice of strings to permission.Permission values.
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

// mapRoleError maps role domain errors to the appropriate HTTP status code.
func mapRoleError(err error) int {
	var ve *role.ValidationError
	var rnf *role.RoleNotFoundError
	var rne *role.RoleNameExistsError
	var sri *role.SystemRoleImmutableError
	var se *role.StoreError
	var ip *role.InvalidPermissionError

	switch {
	case errors.As(err, &ve):
		return http.StatusBadRequest
	case errors.As(err, &rnf):
		return http.StatusNotFound
	case errors.As(err, &rne):
		return http.StatusConflict
	case errors.As(err, &sri):
		return http.StatusForbidden
	case errors.As(err, &ip):
		return http.StatusBadRequest
	case errors.As(err, &se):
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
