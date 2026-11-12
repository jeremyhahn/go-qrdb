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

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// User request/response types
// ---------------------------------------------------------------------------

// createUserRequest is the JSON payload for POST /users.
type createUserRequest struct {
	Username             string   `json:"username"`
	Email                string   `json:"email,omitempty"`
	Name                 string   `json:"name,omitempty"`
	WithoutEmailLoginKey bool     `json:"without_email_login_key,omitempty"`
	TenantID             string   `json:"tenant_id"`
	RoleIDs              []string `json:"role_ids"`
	// AuthMethods are the permitted authentication methods.
	AuthMethods []string `json:"auth_methods,omitempty"`
	// Lifecycle is the state to create in — pending, active or disabled only.
	// Empty defaults to active, or pending when Pending is set.
	Lifecycle string `json:"lifecycle,omitempty"`
	Pending   bool   `json:"pending,omitempty"`
}

// updateUserRequest is the JSON payload for PUT /users/{id}.
type updateUserRequest struct {
	Username string   `json:"username"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	RoleIDs  []string `json:"role_ids"`
	// AuthMethods replaces the set when PRESENT in the body. Absent leaves it
	// alone; `[]` clears it. JSON gives that distinction for free — an absent
	// key leaves the slice nil, an empty array makes it non-nil and empty —
	// which is exactly the domain request's nil-vs-empty contract, so a
	// restriction can actually be lifted.
	AuthMethods []string `json:"auth_methods,omitempty"`
	// Lifecycle transitions the account when non-empty.
	Lifecycle string `json:"lifecycle,omitempty"`
	// LastAuth raises the last-authentication high-water mark when non-empty.
	// RFC 3339, matching the timestamps this API already emits. A value at or
	// before the stored one writes nothing.
	LastAuth string `json:"last_auth,omitempty"`
	// RoleBindings replaces the tenant-scoped role bindings when PRESENT in
	// the body. Absent leaves it alone; `[]` clears it — the same nil-vs-empty
	// contract AuthMethods already has.
	RoleBindings []transport.RoleBinding `json:"role_bindings,omitempty"`
	// Attributes replaces the ABAC attributes when PRESENT in the body.
	// Absent leaves it alone; `{}` clears it.
	Attributes map[string]string `json:"attributes,omitempty"`
}

// userResponse is the JSON representation of a user entity.
//
// The ID is a uint64 Snowflake that exceeds JavaScript's
// Number.MAX_SAFE_INTEGER, so it is encoded as a JSON string
// (proto3 JSON convention) to avoid precision loss in JS clients.
type userResponse struct {
	ID       uint64   `json:"id,string"`
	Username string   `json:"username"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	TenantID string   `json:"tenant_id"`
	RoleIDs  []string `json:"role_ids"`
	System   bool     `json:"system"`
	// Lifecycle is the canonical and only account state: pending, active,
	// suspended, locked, disabled or deleting. Only active can authenticate.
	Lifecycle   string   `json:"lifecycle,omitempty"`
	AuthMethods []string `json:"auth_methods,omitempty"`
	// LastAuth is RFC 3339, omitted entirely when the account has never
	// authenticated — a zero timestamp would read as 1970 rather than "never".
	LastAuth string `json:"last_auth,omitempty"`
	// CertBindings are the account's enrolled mTLS keys, in full — expired
	// ones included (go-qrdb#535). The wire type is the SDK's own, so the
	// REST client decodes this field without a mirror.
	CertBindings []transport.CertBinding `json:"cert_bindings,omitempty"`
	// RoleBindings grants roles within a tenant scope, additive alongside
	// RoleIDs.
	RoleBindings []transport.RoleBinding `json:"role_bindings,omitempty"`
	// Attributes carries ABAC key/value pairs evaluated by policy.
	Attributes map[string]string `json:"attributes,omitempty"`
	// Factors lists enrolled auth-factor METADATA only; the opaque Data blob
	// a factor interprets during verification never crosses this transport
	// (go-qrdb#192/#197).
	Factors []transport.FactorMetadata `json:"factors,omitempty"`
	// MFAEnabled/MFAMethods/MFAEnrolledAt are DERIVED from Factors, never
	// stored: an enrolled factor is not a policy statement that MFA is
	// required for this account.
	MFAEnabled    bool     `json:"mfa_enabled"`
	MFAMethods    []string `json:"mfa_methods,omitempty"`
	MFAEnrolledAt string   `json:"mfa_enrolled_at,omitempty"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

// listUsersResponse is the JSON payload returned by GET /users/list.
type listUsersResponse struct {
	Users   []*userResponse `json:"users"`
	Total   int             `json:"total"`
	HasMore bool            `json:"has_more"`
}

// ---------------------------------------------------------------------------
// User handlers
// ---------------------------------------------------------------------------

// handleCreateUser handles POST /users -- creates a new passwordless user.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if s.userSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "user"})
		return
	}

	var req createUserRequest
	if !decodeIAMRequest(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	ctx, ok := s.operatorTenantContext(ctx, w, req.TenantID, permission.UsersWriteTenant)
	if !ok {
		return
	}

	if len(req.RoleIDs) > 0 {
		if err := s.authMW.AuthorizeRoleAssignmentWriteOnCreate(ctx, s.roleSvc, req.TenantID, req.RoleIDs); err != nil {
			writeErrorJSON(w, mapHTTPError(err), err)
			return
		}
	}

	u, err := s.userSvc.CreateUser(ctx, &user.CreateUserRequest{
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
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	s.writeUserJSON(ctx, w, http.StatusCreated, u)
}

// handleListUsers handles GET /users/list -- returns a paginated user list.
// Query parameters: tenantID, page, pageSize.
func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if s.userSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "user"})
		return
	}

	q := r.URL.Query()

	listReq := &user.ListUsersRequest{
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

	ctx, ok := s.operatorTenantContext(r.Context(), w, listReq.TenantID, permission.UsersReadTenant)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, apiRequestTimeout)
	defer cancel()

	resp, err := s.userSvc.ListUsers(ctx, listReq)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	users := make([]*userResponse, len(resp.Users))
	for i, u := range resp.Users {
		users[i], err = userToResponse(ctx, s.userAuthFactorSvc, u)
		if err != nil {
			writeErrorJSON(w, mapHTTPError(err), err)
			return
		}
	}

	writeJSON(w, listUsersResponse{
		Users:   users,
		Total:   resp.Total,
		HasMore: resp.HasMore,
	})
}

// handleUser dispatches user operations by extracting the user ID from
// the URL path and routing based on HTTP method and path suffix.
//
// Routes:
//   - GET    /users/{id}         -> get user
//   - PUT    /users/{id}         -> update user
//   - DELETE /users/{id}         -> delete user
//   - POST   /users/{id}/enable  -> enable user
//   - POST   /users/{id}/disable -> disable user
//   - POST   /users/{id}/cert-bindings              -> enrol a cert binding
//   - DELETE /users/{id}/cert-bindings/{thumbprint} -> withdraw one
func (s *Server) handleUser(w http.ResponseWriter, r *http.Request) {
	if s.userSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "user"})
		return
	}

	// Extract path after "/users/": e.g., "abc-123" or "abc-123/enable".
	rest := strings.TrimPrefix(r.URL.Path, "/users/")
	if rest == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "id"})
		return
	}

	// /users/by-username/{username} -> resolve user by tenant-scoped username.
	if username, found := strings.CutPrefix(rest, "by-username/"); found {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if username == "" {
			writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "username"})
			return
		}
		s.handleGetUserByUsername(w, r, username)
		return
	}

	// /users/lookup -> resolve a user by the canonical email login key, with the
	// key in the request BODY.
	//
	// It is deliberately not GET /users/by-email/{email}. Email is now the
	// canonical login identifier, and a path parameter is recorded by ordinary
	// reverse-proxy access logs, load balancers, tracing middleware and browser
	// history — turning every successful lookup into a disclosure of a
	// credential-adjacent identifier. The gRPC, QUIC and MCP surfaces carry it
	// in the payload; REST now does too.
	//
	// The first version of this endpoint DID use a path parameter, with a
	// comment acknowledging the logging exposure and accepting it "for symmetry
	// with by-username". That reasoning was wrong: symmetry with a
	// non-credential handle is not a reason to log a credential one
	// (Codex, PR #191).
	if rest == "lookup" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleLookupUser(w, r)
		return
	}

	// /users/by-cert-thumbprint/{thumbprint} -> resolve user by mTLS SPKI thumbprint.
	if thumbprint, found := strings.CutPrefix(rest, "by-cert-thumbprint/"); found {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if thumbprint == "" {
			writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "thumbprint"})
			return
		}
		s.handleGetUserByCertThumbprint(w, r, thumbprint)
		return
	}

	// Check for /enable and /disable suffixes.
	if r.Method == http.MethodPost {
		if seg, found := strings.CutSuffix(rest, "/enable"); found && seg != "" {
			id, err := parseUserID(seg)
			if err != nil {
				writeErrorJSON(w, mapHTTPError(err), err)
				return
			}
			s.handleEnableUser(w, r, id)
			return
		}
		if seg, found := strings.CutSuffix(rest, "/disable"); found && seg != "" {
			id, err := parseUserID(seg)
			if err != nil {
				writeErrorJSON(w, mapHTTPError(err), err)
				return
			}
			s.handleDisableUser(w, r, id)
			return
		}
		if seg, found := strings.CutSuffix(rest, "/suspend"); found && seg != "" {
			id, err := parseUserID(seg)
			if err != nil {
				writeErrorJSON(w, mapHTTPError(err), err)
				return
			}
			s.handleSuspendUser(w, r, id)
			return
		}
		if seg, found := strings.CutSuffix(rest, "/unsuspend"); found && seg != "" {
			id, err := parseUserID(seg)
			if err != nil {
				writeErrorJSON(w, mapHTTPError(err), err)
				return
			}
			s.handleUnsuspendUser(w, r, id)
			return
		}
		if seg, found := strings.CutSuffix(rest, "/cert-bindings"); found && seg != "" {
			id, err := parseUserID(seg)
			if err != nil {
				writeErrorJSON(w, mapHTTPError(err), err)
				return
			}
			s.handleAddCertBinding(w, r, id)
			return
		}
		w.Header().Set("Allow", "GET, PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// DELETE /users/{id}/cert-bindings/{thumbprint} -> withdraw a binding.
	//
	// The thumbprint is a PATH segment, matching
	// GET /users/by-cert-thumbprint/{thumbprint}. It is a hash of a PUBLIC key
	// — the same value the certificate carries in the clear — so it is not the
	// credential-adjacent identifier the email lookup moved into a body.
	// Withdrawal is a DELETE on the binding resource, which is what makes it
	// idempotent-shaped and distinguishable from the POST that creates one.
	if r.Method == http.MethodDelete {
		if idSeg, thumbprint, found := strings.Cut(rest, "/cert-bindings/"); found {
			id, err := parseUserID(idSeg)
			if err != nil {
				writeErrorJSON(w, mapHTTPError(err), err)
				return
			}
			if thumbprint == "" {
				writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "thumbprint"})
				return
			}
			s.handleRemoveCertBinding(w, r, id, thumbprint)
			return
		}
	}

	// The remaining path is the user ID (no suffix).
	id, err := parseUserID(rest)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	handler, ok := userMethodHandlers[r.Method]
	if !ok {
		w.Header().Set("Allow", "GET, PUT, DELETE, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	handler(s, w, r, id)
}

// userMethodHandler handles a user request for a specific HTTP method.
type userMethodHandler func(s *Server, w http.ResponseWriter, r *http.Request, id uint64)

// userMethodHandlers maps HTTP methods to their user handler functions.
var userMethodHandlers = map[string]userMethodHandler{
	http.MethodGet:    (*Server).handleGetUser,
	http.MethodPut:    (*Server).handleUpdateUser,
	http.MethodDelete: (*Server).handleDeleteUser,
}

// handleGetUser handles GET /users/{id} -- retrieves a user by ID.
func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request, id uint64) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	ctx, ok := s.operatorTenantContext(ctx, w, r.URL.Query().Get("tenantID"), permission.UsersReadTenant)
	if !ok {
		return
	}

	u, err := s.userSvc.GetUser(ctx, id)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	s.writeUserJSON(ctx, w, http.StatusOK, u)
}

// handleGetUserByUsername handles GET /users/by-username/{username}
// -- resolves a user by tenant-scoped username. The tenantID is read
// from the "tenantID" query parameter (empty selects the default
// tenant).
func (s *Server) handleGetUserByUsername(w http.ResponseWriter, r *http.Request, username string) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	ctx, ok := s.operatorTenantContext(ctx, w, r.URL.Query().Get("tenantID"), permission.UsersReadTenant)
	if !ok {
		return
	}

	tenantID := r.URL.Query().Get("tenantID")

	u, err := s.userSvc.GetUserByUsername(ctx, tenantID, username)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	s.writeUserJSON(ctx, w, http.StatusOK, u)
}

// lookupUserRequest is the JSON body for POST /users/lookup. The email travels
// here rather than in the URL so it stays out of access logs and history.
type lookupUserRequest struct {
	Email    string `json:"email"`
	TenantID string `json:"tenant_id,omitempty"`
}

// handleLookupUser handles POST /users/lookup, resolving the canonical email
// login key through the tenant-scoped email index.
func (s *Server) handleLookupUser(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	var req lookupUserRequest
	if !decodeIAMRequest(w, r, &req) {
		return
	}
	if req.Email == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "email"})
		return
	}
	ctx, ok := s.operatorTenantContext(ctx, w, req.TenantID, permission.UsersReadTenant)
	if !ok {
		return
	}

	u, err := s.userSvc.GetUserByEmail(ctx, req.TenantID, req.Email)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	s.writeUserJSON(ctx, w, http.StatusOK, u)
}

// handleGetUserByCertThumbprint handles GET /users/by-cert-thumbprint/{thumbprint}
// -- resolves a user by an mTLS certificate's SPKI thumbprint via the
// secondary index (the mTLS-auth hot path).
func (s *Server) handleGetUserByCertThumbprint(w http.ResponseWriter, r *http.Request, thumbprint string) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	ctx, ok := s.operatorTenantContext(ctx, w, r.URL.Query().Get("tenantID"), permission.UsersReadTenant)
	if !ok {
		return
	}

	u, err := s.userSvc.UserByCertThumbprint(ctx, thumbprint)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	s.writeUserJSON(ctx, w, http.StatusOK, u)
}

// addCertBindingRequest is the JSON payload for POST /users/{userID}/cert-bindings.
type addCertBindingRequest struct {
	Thumbprint   string `json:"thumbprint"`
	NotAfterUnix int64  `json:"not_after_unix"`
}

// handleAddCertBinding handles POST /users/{userID}/cert-bindings
// -- binds an mTLS certificate's SPKI thumbprint to a user so the
// secondary index resolves it (the enrollment seam).
func (s *Server) handleAddCertBinding(w http.ResponseWriter, r *http.Request, userID uint64) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	ctx, ok := s.operatorTenantContext(ctx, w, r.URL.Query().Get("tenantID"), permission.UsersWriteTenant)
	if !ok {
		return
	}

	var req addCertBindingRequest
	if !decodeIAMRequest(w, r, &req) {
		return
	}

	// Binding a thumbprint binds a key: the certificate target rule applies.
	if err := s.authMW.AuthorizeCertBindingTarget(ctx, s.userSvc, userID); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	u, err := s.userSvc.AddCertBinding(ctx, userID, req.Thumbprint, req.NotAfterUnix)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	s.writeUserJSON(ctx, w, http.StatusOK, u)
}

// handleRemoveCertBinding handles DELETE /users/{userID}/cert-bindings/{thumbprint}
// -- withdraws an mTLS SPKI thumbprint so it no longer resolves and no longer
// authenticates. Withdrawing the LAST binding returns the account to admission
// on the certificate's CN and OU alone; see docs/identity-rbac/api.md.
func (s *Server) handleRemoveCertBinding(w http.ResponseWriter, r *http.Request, userID uint64, thumbprint string) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	ctx, ok := s.operatorTenantContext(ctx, w, r.URL.Query().Get("tenantID"), permission.UsersWriteTenant)
	if !ok {
		return
	}

	u, err := s.userSvc.RemoveCertBinding(ctx, userID, thumbprint)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	s.writeUserJSON(ctx, w, http.StatusOK, u)
}

// handleUpdateUser handles PUT /users/{id} -- applies partial updates.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request, id uint64) {
	var req updateUserRequest
	if !decodeIAMRequest(w, r, &req) {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	ctx, ok := s.operatorTenantContext(ctx, w, r.URL.Query().Get("tenantID"), permission.UsersWriteTenant)
	if !ok {
		return
	}

	updateReq := &user.UpdateUserRequest{
		Guard:        s.authMW.SORoleGuard(ctx),
		ID:           id,
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
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "last_auth", Err: perr})
			return
		}
		utc := at.UTC()
		updateReq.LastAuth = &utc
	}

	if authmw.TouchesRoleAssignment(updateReq.RoleIDs, updateReq.RoleBindings, updateReq.Attributes) {
		if err := s.authMW.AuthorizeRoleAssignmentWrite(ctx, s.userSvc, s.roleSvc, id, updateReq.RoleIDs, updateReq.RoleBindings); err != nil {
			writeErrorJSON(w, mapHTTPError(err), err)
			return
		}
	}

	u, err := s.userSvc.UpdateUser(ctx, updateReq)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	s.writeUserJSON(ctx, w, http.StatusOK, u)
}

// handleDeleteUser handles DELETE /users/{id} -- removes a non-system user.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request, id uint64) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	ctx, ok := s.operatorTenantContext(ctx, w, r.URL.Query().Get("tenantID"), permission.UsersWriteTenant)
	if !ok {
		return
	}

	if err := s.userSvc.DeleteUser(ctx, id); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleEnableUser handles POST /users/{id}/enable -- activates a user.
func (s *Server) handleEnableUser(w http.ResponseWriter, r *http.Request, id uint64) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	ctx, ok := s.operatorTenantContext(ctx, w, r.URL.Query().Get("tenantID"), permission.UsersWriteTenant)
	if !ok {
		return
	}

	if err := s.userSvc.EnableUser(ctx, id); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleDisableUser handles POST /users/{id}/disable -- deactivates a user.
func (s *Server) handleDisableUser(w http.ResponseWriter, r *http.Request, id uint64) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	ctx, ok := s.operatorTenantContext(ctx, w, r.URL.Query().Get("tenantID"), permission.UsersWriteTenant)
	if !ok {
		return
	}

	if err := s.userSvc.DisableUser(ctx, id); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

func (s *Server) handleSuspendUser(w http.ResponseWriter, r *http.Request, id uint64) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	ctx, ok := s.operatorTenantContext(ctx, w, r.URL.Query().Get("tenantID"), permission.UsersWriteTenant)
	if !ok {
		return
	}

	if err := s.userSvc.SuspendUser(ctx, id); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

func (s *Server) handleUnsuspendUser(w http.ResponseWriter, r *http.Request, id uint64) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	ctx, ok := s.operatorTenantContext(ctx, w, r.URL.Query().Get("tenantID"), permission.UsersWriteTenant)
	if !ok {
		return
	}

	if err := s.userSvc.UnsuspendUser(ctx, id); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// ---------------------------------------------------------------------------
// User conversion and error mapping
// ---------------------------------------------------------------------------

// userToResponse converts a user.User to its JSON response representation.
// Factors metadata and the derived MFA fields are computed at read time from
// the real enrollment records (service.UserAuthFactorServicer, the same
// store /v1/users/factors reads), never from a stored field on u -- there is
// exactly one source of truth, read once. lister may be nil (the auth-factor
// subsystem is not configured for this deployment); a lister that fails
// returns the *user.FactorLookupError rather than a response claiming no
// factors.
func userToResponse(ctx context.Context, lister user.FactorLister, u *user.User) (*userResponse, error) {
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

	return &userResponse{
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

// writeUserJSON projects u (see userToResponse) and writes it with status,
// or writes the projection's error when its factors could not be listed.
func (s *Server) writeUserJSON(ctx context.Context, w http.ResponseWriter, status int, u *user.User) {
	resp, err := userToResponse(ctx, s.userAuthFactorSvc, u)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// parseUserID parses a user ID path segment into the uint64 wire type,
// returning an InvalidParameterError on malformed input.
func parseUserID(seg string) (uint64, *InvalidParameterError) {
	id, err := strconv.ParseUint(seg, 10, 64)
	if err != nil {
		return 0, &InvalidParameterError{Param: "id", Err: err}
	}
	return id, nil
}

// formatLastAuth renders a nil-able last-authentication time as RFC 3339, or
// the empty string when the account has never authenticated. Empty rather than
// a zero timestamp, so `omitempty` drops the key entirely instead of claiming
// the account last authenticated in 1970.
func formatLastAuth(at *time.Time) string {
	if at == nil {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
