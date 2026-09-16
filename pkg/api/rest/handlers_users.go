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

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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
	// GroupIDs are opaque group-membership identifiers; qrdb owns no Group
	// entity. AuthMethods are the permitted authentication methods.
	GroupIDs    []string `json:"group_ids,omitempty"`
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
	// GroupIDs and AuthMethods replace their sets when PRESENT in the body.
	// Absent leaves them alone; `[]` clears them. JSON gives that distinction
	// for free — an absent key leaves the slice nil, an empty array makes it
	// non-nil and empty — which is exactly the domain request's nil-vs-empty
	// contract, so a membership can actually be revoked.
	GroupIDs    []string `json:"group_ids,omitempty"`
	AuthMethods []string `json:"auth_methods,omitempty"`
	// Lifecycle transitions the account when non-empty.
	Lifecycle string `json:"lifecycle,omitempty"`
	// LastAuth raises the last-authentication high-water mark when non-empty.
	// RFC 3339, matching the timestamps this API already emits. A value at or
	// before the stored one writes nothing.
	LastAuth string `json:"last_auth,omitempty"`
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
	Enabled  bool     `json:"enabled"`
	System   bool     `json:"system"`
	// Lifecycle is the canonical account state: pending, active, suspended,
	// locked, disabled or deleting. Enabled is a derived projection of it
	// (exactly lifecycle == "active"), so a client that needs to tell a
	// suspended account from a locked or pending one reads this.
	Lifecycle   string   `json:"lifecycle,omitempty"`
	GroupIDs    []string `json:"group_ids,omitempty"`
	AuthMethods []string `json:"auth_methods,omitempty"`
	// LastAuth is RFC 3339, omitted entirely when the account has never
	// authenticated — a zero timestamp would read as 1970 rather than "never".
	LastAuth string `json:"last_auth,omitempty"`
	// CertBindings are the account's enrolled mTLS keys, in full — expired
	// ones included (go-qrdb#535). The wire type is the SDK's own, so the
	// REST client decodes this field without a mirror.
	CertBindings []transport.CertBinding `json:"cert_bindings,omitempty"`
	CreatedAt    string                  `json:"created_at"`
	UpdatedAt    string                  `json:"updated_at"`
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	u, err := s.userSvc.CreateUser(ctx, &user.CreateUserRequest{
		Username:             req.Username,
		Email:                req.Email,
		Name:                 req.Name,
		WithoutEmailLoginKey: req.WithoutEmailLoginKey,
		TenantID:             req.TenantID,
		RoleIDs:              req.RoleIDs,
		GroupIDs:             req.GroupIDs,
		AuthMethods:          req.AuthMethods,
		Lifecycle:            user.Lifecycle(req.Lifecycle),
		Pending:              req.Pending,
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
				writeErrorJSON(w, http.StatusBadRequest, err)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, err)
		return
	}
	if req.Email == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "email"})
		return
	}

	u, err := s.userSvc.GetUserByEmail(ctx, req.TenantID, req.Email)
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

// handleRemoveCertBinding handles DELETE /users/{userID}/cert-bindings/{thumbprint}
// -- withdraws an mTLS SPKI thumbprint so it no longer resolves and no longer
// authenticates. Withdrawing the LAST binding returns the account to admission
// on the certificate's CN and OU alone; see docs/identity-rbac/api.md.
func (s *Server) handleRemoveCertBinding(w http.ResponseWriter, r *http.Request, userID uint64, thumbprint string) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	u, err := s.userSvc.RemoveCertBinding(ctx, userID, thumbprint)
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

	updateReq := &user.UpdateUserRequest{
		ID:          id,
		Username:    req.Username,
		Email:       req.Email,
		Name:        req.Name,
		RoleIDs:     req.RoleIDs,
		GroupIDs:    req.GroupIDs,
		AuthMethods: req.AuthMethods,
		Lifecycle:   user.Lifecycle(req.Lifecycle),
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

	u, err := s.userSvc.UpdateUser(ctx, updateReq)
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
		ID:           u.ID,
		Username:     u.Username,
		Email:        u.Email,
		Name:         u.Name,
		TenantID:     u.TenantID,
		RoleIDs:      roleIDs,
		Enabled:      u.Enabled,
		System:       u.System,
		Lifecycle:    string(u.Lifecycle),
		GroupIDs:     u.GroupIDs,
		AuthMethods:  u.AuthMethods,
		LastAuth:     formatLastAuth(u.LastAuth),
		CertBindings: user.WireCertBindings(u.CertBindings),
		CreatedAt:    u.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    u.UpdatedAt.Format(time.RFC3339),
	}
}

// mapUserError maps user domain errors to the appropriate HTTP status code.
func mapUserError(err error) int {
	var ve *user.ValidationError
	var unf *user.UserNotFoundError
	var ue *user.UsernameExistsError
	var ee *user.EmailExistsError
	var ice *user.IndexCompensationError
	var cu *user.ConcurrentUpdateError
	var cou *user.CommitOutcomeUnknownError
	var si *user.SystemUserImmutableError
	var il *user.InvalidLifecycleError
	var lt *user.LifecycleTransitionError
	var cbnf *user.CertBindingNotFoundError
	var cbc *user.CertBindingConflictError
	var se *user.StoreError
	var xt *tenant.CrossTenantDeniedError

	switch {
	case errors.As(err, &xt):
		// A normal tenant caller supplied a target tenant other than its own.
		// The IAM routing layer refuses to act on a client-supplied tenant —
		// fail closed with 403, matching the tenant-isolation posture.
		return http.StatusForbidden
	case errors.Is(err, tenant.ErrTenantSealed):
		// A barrier-isolated tenant's IAM store resolved to a SEALED barrier.
		// The StoreResolver fails closed rather than leaking the operation
		// through the shared system store — surface 503, matching the
		// BarrierSealedError data-plane mapping.
		return http.StatusServiceUnavailable
	case errors.As(err, &ice):
		// CHECKED BEFORE EVERY CASE THAT COULD BE ITS CAUSE, which is the whole
		// point of where this arm sits. IndexCompensationError Unwraps to its
		// cause so existing matching keeps working — so whichever cause-arm is
		// listed above it wins instead, and the caller is told the cause while
		// the far more serious fact, that rollback FAILED and a login index is
		// inconsistent, is dropped.
		//
		// It sat below the duplicate-handle arms because when only UpdateUser
		// and DeleteUser produced this error its causes were the concurrency
		// errors below. CreateUser now produces it too, and its cause can be a
		// UsernameExists/EmailExists conflict: a create that could not release
		// the username it had already claimed would have been reported as an
		// ordinary 409, leaving that username permanently unclaimable and
		// resolving to nothing, with no signal to anyone. The cause set of a
		// wrapper error grows silently — position this arm above all of them.
		return http.StatusInternalServerError
	case errors.As(err, &ve):
		return http.StatusBadRequest
	case errors.As(err, &unf):
		return http.StatusNotFound
	case errors.As(err, &cbnf):
		// A withdrawal naming a binding the account does not hold. 404 on the
		// binding sub-resource, the same answer a DELETE of any absent resource
		// gets — and deliberately NOT a 204, because a vacuous success would
		// tell an operator who mistyped the thumbprint that a live key had been
		// withdrawn.
		return http.StatusNotFound
	case errors.As(err, &ue):
		return http.StatusConflict
	case errors.As(err, &ee):
		// A taken email is a client-correctable conflict, exactly as a taken
		// username is. Without this case it fell through to the StoreError arm
		// and was reported as 500 — a server fault for a condition the caller
		// can fix, and one that hides the real reason.
		return http.StatusConflict
	case errors.As(err, &cbc):
		// A thumbprint already bound to ANOTHER user in the tenant. The store
		// refuses to rebind it because doing so silently would let the caller
		// take over that user's mTLS identity — one of the more
		// security-relevant answers it gives, and without this arm it was
		// reported as 500. 409 like the other taken handles; the message
		// names the user that holds the thumbprint, which is the one fact an
		// operator investigating a failed enrolment needs (go-qrdb#536).
		return http.StatusConflict
	case errors.As(err, &cu):
		// Lost a concurrent update: nothing is wrong, the caller's view is
		// stale. Conflict tells it to re-read and retry; 500 would tell it the
		// server is broken.
		return http.StatusConflict
	case errors.As(err, &cou):
		// The commit outcome is UNKNOWN. Distinct from both a conflict and a
		// failure: the caller must reconcile before retrying, because a blind
		// retry could double-apply. 503 says "ask again", not "it failed".
		return http.StatusServiceUnavailable
	case errors.As(err, &il):
		// A state that is undefined, or defined but not permitted where it was
		// used. The caller sent something wrong and the error names the allowed
		// set, so this is a 400 — the same class as any other bad field.
		return http.StatusBadRequest
	case errors.As(err, &lt):
		// The state is valid, the CALLER is not wrong about it, and the record
		// simply is not somewhere this move can start from — the account is
		// already being deleted, or was never activated. That is a precondition
		// on the resource, not a malformed request, so it is 409 rather than
		// 400. Without this arm the commonest instance, an EnableUser racing a
		// delete, was reported as 500: a server fault for a refusal the server
		// made on purpose, and one a client could never learn to expect.
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
