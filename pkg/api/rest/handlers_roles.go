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
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Role request/response types
// ---------------------------------------------------------------------------

// createRoleRequest is the JSON payload for POST /roles.
type createRoleRequest struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenant_id"`
	Permissions []string `json:"permissions"`
}

// updateRoleRequest is the JSON payload for PUT /roles/{id}.
type updateRoleRequest struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
}

// assignRoleRequest is the JSON payload for POST /roles/assign and
// POST /roles/unassign.
//
// The user ID is a uint64 Snowflake that exceeds JavaScript's
// Number.MAX_SAFE_INTEGER, so it is encoded as a JSON string
// (proto3 JSON convention) to avoid precision loss in JS clients.
type assignRoleRequest struct {
	UserID uint64 `json:"user_id,string"`
	RoleID string `json:"role_id"`
}

// roleResponse is the JSON representation of a role entity.
type roleResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TenantID    string   `json:"tenant_id"`
	Permissions []string `json:"permissions"`
	System      bool     `json:"system"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`

	// RetiredPermissions are the members of Permissions this build no longer
	// defines — drift between what the tenant was seeded with and what the
	// server now grants, visible before POST /roles/reconcile repairs it.
	RetiredPermissions []string `json:"retired_permissions,omitempty"`
}

// listRolesResponse is the JSON payload returned by GET /roles/list.
type listRolesResponse struct {
	Roles   []*roleResponse `json:"roles"`
	Total   int             `json:"total"`
	HasMore bool            `json:"has_more"`
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

// resolvePermissionsRequest is the JSON payload for
// POST /roles/resolve-permissions.
type resolvePermissionsRequest struct {
	RoleIDs []string `json:"role_ids"`
}

// resolvePermissionsResponse is the JSON payload returned by
// POST /roles/resolve-permissions.
type resolvePermissionsResponse struct {
	Permissions []string `json:"permissions"`
}

// reconcileRolesRequest is the JSON payload for POST /roles/reconcile.
type reconcileRolesRequest struct {
	TenantID string `json:"tenant_id,omitempty"`
}

// roleReconcileResultResponse is the JSON representation of one built-in
// role's reconciliation outcome.
type roleReconcileResultResponse struct {
	RoleID  string   `json:"role_id"`
	Name    string   `json:"name"`
	Created bool     `json:"created"`
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

// reconcileRolesResponse is the JSON payload returned by
// POST /roles/reconcile.
type reconcileRolesResponse struct {
	TenantID string                        `json:"tenant_id"`
	Roles    []roleReconcileResultResponse `json:"roles,omitempty"`
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
	if !decodeIAMRequest(w, r, &req) {
		return
	}

	perms := stringsToPermissions(req.Permissions)

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	rl, err := s.roleSvc.CreateRole(ctx, &role.CreateRoleRequest{
		ID:          req.ID,
		Name:        req.Name,
		TenantID:    req.TenantID,
		Permissions: perms,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
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

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	resp, err := s.roleSvc.ListRoles(ctx, listReq)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
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
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	rl, err := s.roleSvc.GetRole(ctx, id)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, roleToResponse(rl))
}

// handleUpdateRole handles PUT /roles/{id} -- applies partial updates.
func (s *Server) handleUpdateRole(w http.ResponseWriter, r *http.Request, id string) {
	var req updateRoleRequest
	if !decodeIAMRequest(w, r, &req) {
		return
	}

	perms := stringsToPermissions(req.Permissions)

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	rl, err := s.roleSvc.UpdateRole(ctx, &role.UpdateRoleRequest{
		ID:          id,
		Name:        req.Name,
		Permissions: perms,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, roleToResponse(rl))
}

// handleDeleteRole handles DELETE /roles/{id} -- removes a non-system role.
func (s *Server) handleDeleteRole(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := s.roleSvc.DeleteRole(ctx, id); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
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
	if !decodeIAMRequest(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	// The same gate every role-assignment write passes: roles:assign in the
	// user's tenant, and the role must exist there.
	if err := s.authMW.AuthorizeRoleAssignment(ctx, s.userSvc, s.roleSvc, req.UserID, req.RoleID); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	// Get the user and add the role.
	u, err := s.userSvc.GetUser(ctx, req.UserID)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	// Check if role is already assigned.
	for _, rid := range u.RoleIDs {
		if rid == req.RoleID {
			s.writeUserJSON(ctx, w, http.StatusOK, u)
			return
		}
	}

	roleIDs := append(u.RoleIDs, req.RoleID)
	updated, err := s.userSvc.UpdateUser(ctx, &user.UpdateUserRequest{
		Guard:   s.authMW.SORoleGuard(ctx),
		ID:      req.UserID,
		RoleIDs: roleIDs,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	s.writeUserJSON(ctx, w, http.StatusOK, updated)
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
	if !decodeIAMRequest(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := s.authMW.AuthorizeRoleUnassignment(ctx, s.userSvc, req.UserID, req.RoleID); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	// Get the user and remove the role.
	u, err := s.userSvc.GetUser(ctx, req.UserID)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	roleIDs := make([]string, 0, len(u.RoleIDs))
	for _, rid := range u.RoleIDs {
		if rid != req.RoleID {
			roleIDs = append(roleIDs, rid)
		}
	}

	updated, err := s.userSvc.UpdateUser(ctx, &user.UpdateUserRequest{
		Guard:   s.authMW.SORoleGuard(ctx),
		ID:      req.UserID,
		RoleIDs: roleIDs,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	s.writeUserJSON(ctx, w, http.StatusOK, updated)
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

// handleGetRoleByName handles GET /roles/by-name?tenant_id=&name= --
// retrieves a role by its tenant-scoped name.
func (s *Server) handleGetRoleByName(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("name")
	if name == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "name"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	rl, err := s.roleSvc.GetRoleByName(ctx, q.Get("tenant_id"), name)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, roleToResponse(rl))
}

// handleResolvePermissions handles POST /roles/resolve-permissions --
// resolves the union of effective permissions for the given role IDs.
func (s *Server) handleResolvePermissions(w http.ResponseWriter, r *http.Request) {
	var req resolvePermissionsRequest
	if !decodeIAMRequest(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	perms, err := s.roleSvc.ResolvePermissions(ctx, req.RoleIDs)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = p.String()
	}

	writeJSON(w, resolvePermissionsResponse{Permissions: out})
}

// listOwnPermissionsResponse is the JSON body of GET /permissions/own.
type listOwnPermissionsResponse struct {
	Permissions []role.PermissionInfo `json:"permissions"`
}

// handleListOwnPermissions handles GET /permissions/own -- the authenticated
// caller's own effective permissions. The identity comes from the request
// context only; the route is identity-gated, so any authenticated caller may
// read its own grants and no one else's.
func (s *Server) handleListOwnPermissions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	perms, err := role.ListOwnPermissions(ctx, s.roleSvc)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, listOwnPermissionsResponse{Permissions: perms})
}

// authorizeRequest is the JSON body of POST /permissions/authorize. Identity
// names the identity to evaluate; absent, it is the caller. Only its
// subject, tenant and roles cross the wire: metadata is set by
// authentication alone and is never taken from a request.
type authorizeRequest struct {
	Identity *authorizeIdentity `json:"identity,omitempty"`
	Resource string             `json:"resource"`
	Action   string             `json:"action"`
}

// authorizeIdentity is the wire form of the identity an Authorize request
// evaluates.
type authorizeIdentity struct {
	Subject  string   `json:"subject"`
	TenantID string   `json:"tenant_id"`
	Roles    []string `json:"roles,omitempty"`
}

// authorizeResponse is the JSON body of POST /permissions/authorize: the
// decision, with the denial's reason when it is not allowed.
type authorizeResponse struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// handleAuthorize handles POST /permissions/authorize -- whether an identity
// may perform an action on a resource, decided by the server's auth chain
// through AuthMiddleware.EvaluateAuthorization. Asking about the caller
// needs authentication only; asking about another identity needs
// permissions:resolve in that identity's tenant, which the evaluator
// enforces.
func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	var req authorizeRequest
	if !decodeIAMRequest(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	var target *auth.Identity
	if req.Identity != nil {
		target = &auth.Identity{Subject: req.Identity.Subject, TenantID: req.Identity.TenantID, Roles: req.Identity.Roles}
	}
	decision, err := s.authMW.EvaluateAuthorization(ctx, target, req.Resource, req.Action)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	resp := authorizeResponse{Allowed: decision == nil}
	if decision != nil {
		resp.Reason = decision.Error()
	}
	writeJSON(w, resp)
}

// handleReconcileRoles handles POST /roles/reconcile -- converges the
// tenant's stored built-in roles onto this build's definitions.
func (s *Server) handleReconcileRoles(w http.ResponseWriter, r *http.Request) {
	if s.roleSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "role"})
		return
	}

	// An empty body means "my own tenant", which is the common case for a
	// tenant administrator, so it is not a malformed request.
	var req reconcileRolesRequest
	if !decodeOptionalIAMRequest(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	report, err := s.roleSvc.ReconcileBuiltInRoles(ctx, req.TenantID)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, reconcileReportToResponse(report))
}

// reconcileReportToResponse converts a role.ReconcileReport to its JSON
// response representation.
func reconcileReportToResponse(report *role.ReconcileReport) reconcileRolesResponse {
	if report == nil {
		return reconcileRolesResponse{}
	}
	out := reconcileRolesResponse{TenantID: report.TenantID}
	out.Roles = make([]roleReconcileResultResponse, len(report.Roles))
	for i, r := range report.Roles {
		out.Roles[i] = roleReconcileResultResponse{
			RoleID:  r.RoleID,
			Name:    r.Name,
			Created: r.Created,
			Added:   permissionsToStrings(r.Added),
			Removed: permissionsToStrings(r.Removed),
		}
	}
	return out
}

// roleReconcileSkippedTenantResponse names a tenant the all-tenant reconcile
// did not open, and why.
type roleReconcileSkippedTenantResponse struct {
	TenantID string `json:"tenant_id"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail,omitempty"`
}

// roleReconcileFailedTenantResponse names a tenant whose reconcile failed.
type roleReconcileFailedTenantResponse struct {
	TenantID string `json:"tenant_id"`
	Error    string `json:"error"`
}

// reconcileAllTenantsRolesResponse is the JSON body of
// POST /roles/reconcile/all-tenants.
type reconcileAllTenantsRolesResponse struct {
	Tenants []reconcileRolesResponse             `json:"tenants,omitempty"`
	Skipped []roleReconcileSkippedTenantResponse `json:"skipped,omitempty"`
	Failed  []roleReconcileFailedTenantResponse  `json:"failed,omitempty"`
}

// handleReconcileRolesAllTenants converges every tenant's stored built-in
// roles onto this build's definitions (role.ReconcileAllTenants). The route
// has already required roles:reconcile_all; the request body is ignored.
func (s *Server) handleReconcileRolesAllTenants(w http.ResponseWriter, r *http.Request) {
	if s.roleSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "role"})
		return
	}
	if s.tenantSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "tenant"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	report, err := role.ReconcileAllTenants(ctx, s.roleSvc, s.tenantSvc)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, allTenantsReconcileReportToResponse(report))
}

// allTenantsReconcileReportToResponse converts a role.AllTenantsReconcileReport
// to its JSON response.
func allTenantsReconcileReportToResponse(report *role.AllTenantsReconcileReport) reconcileAllTenantsRolesResponse {
	var out reconcileAllTenantsRolesResponse
	if report == nil {
		return out
	}
	for i := range report.Tenants {
		out.Tenants = append(out.Tenants, reconcileReportToResponse(&report.Tenants[i]))
	}
	for _, sk := range report.Skipped {
		out.Skipped = append(out.Skipped, roleReconcileSkippedTenantResponse{TenantID: sk.TenantID, Reason: sk.Reason, Detail: sk.Detail})
	}
	for _, f := range report.Failed {
		out.Failed = append(out.Failed, roleReconcileFailedTenantResponse{TenantID: f.TenantID, Error: f.Error})
	}
	return out
}

// permissionsToStrings renders a permission slice for a JSON response.
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
