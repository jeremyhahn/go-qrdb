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

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// User wire types
// ---------------------------------------------------------------------------

// createUserWireRequest is the JSON wire payload for user.Create.
type createUserWireRequest struct {
	Username             string   `json:"username"`
	Email                string   `json:"email,omitempty"`
	Name                 string   `json:"name,omitempty"`
	WithoutEmailLoginKey bool     `json:"withoutEmailLoginKey,omitempty"`
	TenantID             string   `json:"tenantID"`
	RoleIDs              []string `json:"roleIDs"`
	// GroupIDs are opaque group-membership identifiers; qrdb owns no Group
	// entity. AuthMethods are the permitted authentication methods.
	GroupIDs    []string `json:"groupIDs,omitempty"`
	AuthMethods []string `json:"authMethods,omitempty"`
	// Lifecycle is the state to create in — pending, active or disabled only.
	// Empty defaults to active, or pending when Pending is set.
	Lifecycle string `json:"lifecycle,omitempty"`
	Pending   bool   `json:"pending,omitempty"`
}

// getUserWireRequest is the JSON wire payload for user.Get.
type getUserWireRequest struct {
	ID uint64 `json:"id"`
}

// getUserByUsernameWireRequest is the JSON wire payload for
// user.GetByUsername.
type getUserByUsernameWireRequest struct {
	TenantID string `json:"tenantID"`
	Username string `json:"username"`
}

// getUserByEmailWireRequest is the JSON wire payload for a get-by-email
// lookup — the canonical login-key resolution.
type getUserByEmailWireRequest struct {
	TenantID string `json:"tenantID"`
	Email    string `json:"email"`
}

// getUserByCertThumbprintWireRequest is the JSON wire payload for
// user.GetByCertThumbprint.
type getUserByCertThumbprintWireRequest struct {
	Thumbprint string `json:"thumbprint"`
}

// addCertBindingWireRequest is the JSON wire payload for user.AddCertBinding.
type addCertBindingWireRequest struct {
	UserID       uint64 `json:"userID"`
	Thumbprint   string `json:"thumbprint"`
	NotAfterUnix int64  `json:"notAfterUnix"`
}

// removeCertBindingWireRequest is the JSON wire payload for
// user.RemoveCertBinding. It carries no expiry: withdrawal is unconditional.
type removeCertBindingWireRequest struct {
	UserID     uint64 `json:"userID"`
	Thumbprint string `json:"thumbprint"`
}

// updateUserWireRequest is the JSON wire payload for user.Update.
type updateUserWireRequest struct {
	ID       uint64   `json:"id"`
	Username string   `json:"username"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	RoleIDs  []string `json:"roleIDs"`
	// GroupIDs and AuthMethods replace their sets when PRESENT in the payload.
	// Absent leaves them alone; `[]` clears them. JSON carries that distinction
	// for free — an absent key leaves the slice nil — which is exactly the
	// domain request's nil-vs-empty contract, so a membership can be revoked.
	GroupIDs    []string `json:"groupIDs,omitempty"`
	AuthMethods []string `json:"authMethods,omitempty"`
	// Lifecycle transitions the account when non-empty.
	Lifecycle string `json:"lifecycle,omitempty"`
	// LastAuth raises the last-authentication high-water mark when non-empty.
	// RFC 3339, matching the timestamps this transport already emits.
	LastAuth string `json:"lastAuth,omitempty"`
}

// deleteUserWireRequest is the JSON wire payload for user.Delete.
type deleteUserWireRequest struct {
	ID uint64 `json:"id"`
}

// listUsersWireRequest is the JSON wire payload for user.List.
type listUsersWireRequest struct {
	TenantID string `json:"tenantID"`
	Page     int    `json:"page"`
	PageSize int    `json:"pageSize"`
}

// enableUserWireRequest is the JSON wire payload for user.Enable.
type enableUserWireRequest struct {
	ID uint64 `json:"id"`
}

// disableUserWireRequest is the JSON wire payload for user.Disable.
type disableUserWireRequest struct {
	ID uint64 `json:"id"`
}

// userWireResponse is the JSON wire representation of a user entity.
type userWireResponse struct {
	ID       uint64   `json:"id"`
	Username string   `json:"username"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	TenantID string   `json:"tenantID"`
	RoleIDs  []string `json:"roleIDs"`
	System   bool     `json:"system"`
	// Lifecycle is the canonical and only account state: pending, active,
	// suspended, locked, disabled or deleting. Only active can authenticate.
	Lifecycle   string   `json:"lifecycle,omitempty"`
	GroupIDs    []string `json:"groupIDs,omitempty"`
	AuthMethods []string `json:"authMethods,omitempty"`
	// LastAuth is RFC 3339, omitted entirely when the account has never
	// authenticated — a zero timestamp would read as 1970 rather than "never".
	LastAuth string `json:"lastAuth,omitempty"`
	// CertBindings are the account's enrolled mTLS keys, in full — expired
	// ones included (go-qrdb#535). Each is {thumbprint, kind, notAfter}.
	CertBindings []certBindingWire `json:"certBindings,omitempty"`
	CreatedAt    string            `json:"createdAt"`
	UpdatedAt    string            `json:"updatedAt"`
}

// certBindingWire is the JSON wire form of one enrolled mTLS key, matching
// the SDK's transport.CertBinding field for field in this transport's
// camelCase.
type certBindingWire struct {
	Thumbprint string `json:"thumbprint"`
	Kind       string `json:"kind"`
	NotAfter   string `json:"notAfter,omitempty"`
}

// listUsersWireResponse is the JSON wire payload returned by user.List.
type listUsersWireResponse struct {
	Users   []*userWireResponse `json:"users"`
	Total   int                 `json:"total"`
	HasMore bool                `json:"hasMore"`
}

// statusWireResponse is the JSON wire payload for operations that return
// a simple status acknowledgment.
type statusWireResponse struct {
	Status string `json:"status"`
}

// ---------------------------------------------------------------------------
// User handlers
// ---------------------------------------------------------------------------

// handleCreateUser unmarshals a JSON create user request, creates the user
// via the service layer, and returns a serialized JSON user response.
func (s *Server) handleCreateUser(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req createUserWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

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
		return nil, err
	}

	out, err := json.Marshal(userToWireResponse(u))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleGetUser unmarshals a JSON get user request, retrieves the user by
// ID via the service layer, and returns a serialized JSON user response.
func (s *Server) handleGetUser(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req getUserWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	u, err := s.userSvc.GetUser(ctx, req.ID)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(userToWireResponse(u))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleGetUserByUsername unmarshals a JSON get-by-username request,
// resolves the user via the service layer, and returns a JSON user
// response.
func (s *Server) handleGetUserByUsername(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req getUserByUsernameWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	u, err := s.userSvc.GetUserByUsername(ctx, req.TenantID, req.Username)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(userToWireResponse(u))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleGetUserByEmail unmarshals a JSON get-by-email request and resolves the
// canonical login key through the tenant-scoped email index.
func (s *Server) handleGetUserByEmail(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req getUserByEmailWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	u, err := s.userSvc.GetUserByEmail(ctx, req.TenantID, req.Email)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(userToWireResponse(u))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleGetUserByCertThumbprint unmarshals a JSON get-by-cert-thumbprint
// request, resolves the user via the service layer's secondary index, and
// returns a JSON user response (the mTLS-auth hot path).
func (s *Server) handleGetUserByCertThumbprint(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req getUserByCertThumbprintWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	u, err := s.userSvc.UserByCertThumbprint(ctx, req.Thumbprint)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(userToWireResponse(u))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleAddCertBinding unmarshals a JSON add-cert-binding request, binds the
// mTLS SPKI thumbprint to the user via the service layer, and returns a JSON
// user response (the enrollment seam).
func (s *Server) handleAddCertBinding(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req addCertBindingWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	u, err := s.userSvc.AddCertBinding(ctx, req.UserID, req.Thumbprint, req.NotAfterUnix)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(userToWireResponse(u))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleRemoveCertBinding unmarshals a JSON remove-cert-binding request,
// withdraws the mTLS SPKI thumbprint via the service layer, and returns a JSON
// user response. Withdrawing the LAST binding returns the account to admission
// on the certificate's CN and OU alone.
func (s *Server) handleRemoveCertBinding(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req removeCertBindingWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	u, err := s.userSvc.RemoveCertBinding(ctx, req.UserID, req.Thumbprint)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(userToWireResponse(u))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleUpdateUser unmarshals a JSON update user request, applies partial
// updates via the service layer, and returns a serialized JSON user response.
func (s *Server) handleUpdateUser(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req updateUserWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	updateReq := &user.UpdateUserRequest{
		ID:          req.ID,
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
			return nil, &UnmarshalError{Err: perr}
		}
		utc := at.UTC()
		updateReq.LastAuth = &utc
	}

	u, err := s.userSvc.UpdateUser(ctx, updateReq)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(userToWireResponse(u))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleDeleteUser unmarshals a JSON delete user request, removes the user
// via the service layer, and returns a serialized status response.
func (s *Server) handleDeleteUser(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req deleteUserWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.userSvc.DeleteUser(ctx, req.ID); err != nil {
		return nil, err
	}

	out, err := json.Marshal(statusWireResponse{Status: "ok"})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleListUsers unmarshals a JSON list users request, retrieves a paginated
// user list from the service layer, and returns a serialized JSON response.
func (s *Server) handleListUsers(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req listUsersWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	resp, err := s.userSvc.ListUsers(ctx, &user.ListUsersRequest{
		TenantID: req.TenantID,
		Page:     req.Page,
		PageSize: req.PageSize,
	})
	if err != nil {
		return nil, err
	}

	users := make([]*userWireResponse, len(resp.Users))
	for i, u := range resp.Users {
		users[i] = userToWireResponse(u)
	}

	out, err := json.Marshal(listUsersWireResponse{
		Users:   users,
		Total:   resp.Total,
		HasMore: resp.HasMore,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleEnableUser unmarshals a JSON enable user request, activates the
// user account via the service layer, and returns a serialized status
// response.
func (s *Server) handleEnableUser(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req enableUserWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.userSvc.EnableUser(ctx, req.ID); err != nil {
		return nil, err
	}

	out, err := json.Marshal(statusWireResponse{Status: "ok"})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleDisableUser unmarshals a JSON disable user request, deactivates the
// user account via the service layer, and returns a serialized status
// response.
func (s *Server) handleDisableUser(ctx context.Context, payload []byte) ([]byte, error) {
	if s.userSvc == nil {
		return nil, &ServiceNotConfiguredError{Service: "user"}
	}

	var req disableUserWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.userSvc.DisableUser(ctx, req.ID); err != nil {
		return nil, err
	}

	out, err := json.Marshal(statusWireResponse{Status: "ok"})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// User conversion helpers
// ---------------------------------------------------------------------------

// userToWireResponse converts a user.User domain object to its JSON wire
// representation.
func userToWireResponse(u *user.User) *userWireResponse {
	if u == nil {
		return nil
	}

	roleIDs := u.RoleIDs
	if roleIDs == nil {
		roleIDs = []string{}
	}

	return &userWireResponse{
		ID:           u.ID,
		Username:     u.Username,
		Email:        u.Email,
		Name:         u.Name,
		TenantID:     u.TenantID,
		RoleIDs:      roleIDs,
		System:       u.System,
		Lifecycle:    string(u.Lifecycle),
		GroupIDs:     u.GroupIDs,
		AuthMethods:  u.AuthMethods,
		LastAuth:     formatLastAuth(u.LastAuth),
		CertBindings: certBindingsToWire(u.CertBindings),
		CreatedAt:    u.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    u.UpdatedAt.Format(time.RFC3339),
	}
}

// certBindingsToWire renders the account's bindings through the shared
// projection, so this transport lists exactly what REST and gRPC list.
func certBindingsToWire(bindings []user.CertBinding) []certBindingWire {
	wire := user.WireCertBindings(bindings)
	if wire == nil {
		return nil
	}
	out := make([]certBindingWire, len(wire))
	for i, b := range wire {
		out[i] = certBindingWire{Thumbprint: b.Thumbprint, Kind: b.Kind, NotAfter: b.NotAfter}
	}
	return out
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
