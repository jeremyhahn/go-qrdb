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
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// User request/response types
// ---------------------------------------------------------------------------

// createUserRequest is the JSON payload for POST /users.
type createUserRequest struct {
	Username string   `json:"username"`
	TenantID string   `json:"tenantID"`
	RoleIDs  []string `json:"roleIDs"`
}

// updateUserRequest is the JSON payload for PUT /users/{id}.
type updateUserRequest struct {
	Username string   `json:"username"`
	RoleIDs  []string `json:"roleIDs"`
}

// userResponse is the JSON representation of a user entity.
type userResponse struct {
	ID        uint64   `json:"id"`
	Username  string   `json:"username"`
	TenantID  string   `json:"tenantID"`
	RoleIDs   []string `json:"roleIDs"`
	Enabled   bool     `json:"enabled"`
	System    bool     `json:"system"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

// listUsersResponse is the JSON payload returned by GET /users/list.
type listUsersResponse struct {
	Users   []*userResponse `json:"users"`
	Total   int             `json:"total"`
	HasMore bool            `json:"hasMore"`
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	u, err := s.userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username: req.Username,
		TenantID: req.TenantID,
		RoleIDs:  req.RoleIDs,
	})
	if err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(userToResponse(u))
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

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	resp, err := s.userSvc.ListUsers(ctx, listReq)
	if err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	users := make([]*userResponse, len(resp.Users))
	for i, u := range resp.Users {
		users[i] = userToResponse(u)
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
				writeErrorJSON(w, http.StatusBadRequest, err)
				return
			}
			s.handleEnableUser(w, r, id)
			return
		}
		if seg, found := strings.CutSuffix(rest, "/disable"); found && seg != "" {
			id, err := parseUserID(seg)
			if err != nil {
				writeErrorJSON(w, http.StatusBadRequest, err)
				return
			}
			s.handleDisableUser(w, r, id)
			return
		}
		if seg, found := strings.CutSuffix(rest, "/cert-bindings"); found && seg != "" {
			id, err := parseUserID(seg)
			if err != nil {
				writeErrorJSON(w, http.StatusBadRequest, err)
				return
			}
			s.handleAddCertBinding(w, r, id)
			return
		}
		w.Header().Set("Allow", "GET, PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// The remaining path is the user ID (no suffix).
	id, err := parseUserID(rest)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, err)
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

	u, err := s.userSvc.GetUser(ctx, id)
	if err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	writeJSON(w, userToResponse(u))
}

// handleGetUserByUsername handles GET /users/by-username/{username}
// -- resolves a user by tenant-scoped username. The tenantID is read
// from the "tenantID" query parameter (empty selects the default
// tenant).
func (s *Server) handleGetUserByUsername(w http.ResponseWriter, r *http.Request, username string) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	tenantID := r.URL.Query().Get("tenantID")

	u, err := s.userSvc.GetUserByUsername(ctx, tenantID, username)
	if err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	writeJSON(w, userToResponse(u))
}

// handleGetUserByCertThumbprint handles GET /users/by-cert-thumbprint/{thumbprint}
// -- resolves a user by an mTLS certificate's SPKI thumbprint via the
// secondary index (the mTLS-auth hot path).
func (s *Server) handleGetUserByCertThumbprint(w http.ResponseWriter, r *http.Request, thumbprint string) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	u, err := s.userSvc.UserByCertThumbprint(ctx, thumbprint)
	if err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	writeJSON(w, userToResponse(u))
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

	var req addCertBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	u, err := s.userSvc.AddCertBinding(ctx, userID, req.Thumbprint, req.NotAfterUnix)
	if err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	writeJSON(w, userToResponse(u))
}

// handleUpdateUser handles PUT /users/{id} -- applies partial updates.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request, id uint64) {
	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	u, err := s.userSvc.UpdateUser(ctx, &user.UpdateUserRequest{
		ID:       id,
		Username: req.Username,
		RoleIDs:  req.RoleIDs,
	})
	if err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	writeJSON(w, userToResponse(u))
}

// handleDeleteUser handles DELETE /users/{id} -- removes a non-system user.
func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request, id uint64) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := s.userSvc.DeleteUser(ctx, id); err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleEnableUser handles POST /users/{id}/enable -- activates a user.
func (s *Server) handleEnableUser(w http.ResponseWriter, r *http.Request, id uint64) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := s.userSvc.EnableUser(ctx, id); err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleDisableUser handles POST /users/{id}/disable -- deactivates a user.
func (s *Server) handleDisableUser(w http.ResponseWriter, r *http.Request, id uint64) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := s.userSvc.DisableUser(ctx, id); err != nil {
		writeErrorJSON(w, mapUserError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// ---------------------------------------------------------------------------
// User conversion and error mapping
// ---------------------------------------------------------------------------

// userToResponse converts a user.User to its JSON response representation.
func userToResponse(u *user.User) *userResponse {
	if u == nil {
		return nil
	}

	roleIDs := u.RoleIDs
	if roleIDs == nil {
		roleIDs = []string{}
	}

	return &userResponse{
		ID:        u.ID,
		Username:  u.Username,
		TenantID:  u.TenantID,
		RoleIDs:   roleIDs,
		Enabled:   u.Enabled,
		System:    u.System,
		CreatedAt: u.CreatedAt.Format(time.RFC3339),
		UpdatedAt: u.UpdatedAt.Format(time.RFC3339),
	}
}

// mapUserError maps user domain errors to the appropriate HTTP status code.
func mapUserError(err error) int {
	var ve *user.ValidationError
	var unf *user.UserNotFoundError
	var ue *user.UsernameExistsError
	var si *user.SystemUserImmutableError
	var se *user.StoreError

	switch {
	case errors.As(err, &ve):
		return http.StatusBadRequest
	case errors.As(err, &unf):
		return http.StatusNotFound
	case errors.As(err, &ue):
		return http.StatusConflict
	case errors.As(err, &si):
		return http.StatusForbidden
	case errors.As(err, &se):
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
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
