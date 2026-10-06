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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

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

// userNotFound converts a 404 from a single-user endpoint into the typed
// not-found that every transport returns for this condition, so a REST
// consumer's errors.As is the same predicate it is over gRPC, unix and
// embedded (go-qrdb#289). Before this the write endpoints returned a bare
// RequestError carrying only a status code, so REST was not even consistent
// with itself: its read methods were typed and its write methods were not.
//
// It is applied ONLY where a 404 can mean exactly one thing. AssignRole and
// UnassignRole are deliberately NOT routed through it: their 404 carries
// either a missing user or a missing role, and measured on every remote
// transport the two are indistinguishable except by message text — which is
// the substring classification #289 exists to delete, not to relocate. Typing
// those needs the server to say which subject was missing.
func userNotFound(id uint64, err error) error {
	var reqErr *transport.RequestError
	if errors.As(err, &reqErr) && reqErr.StatusCode == http.StatusNotFound {
		return &transport.UserNotFoundError{ID: id}
	}
	return err
}

// leaseNotFoundServerMessagePrefix is service.LeaseNotFoundError's own
// Error() prefix, reproduced here rather than imported: sdk/go is a
// standalone client module that does not import the server's pkg/service,
// the same reason sdk/go/conformance's own confinement tests name a
// wire-level string directly rather than importing the constant it
// mirrors. Used ONLY to confirm a REST 404 is specifically this condition
// before mapOpsNotFoundError trusts it -- see that function's own doc
// comment for why REST, alone among the five remote transports, needs
// this extra check.
const leaseNotFoundServerMessagePrefix = "service: lease not found: "

// mapOpsNotFoundError is userNotFound for ExecuteTransaction and
// ExecuteBatch: checkResultError's only not-found-classed outcome for a
// transaction or batch command is a write naming a lease the state
// machine does not hold, so the same structural argument gRPC, Unix,
// QUIC and MCP's copies of this function rely on holds here too -- but
// REST's 404 is, uniquely among the five, HTTP's own generic status, the
// same one a reverse proxy, load balancer or misrouted path returns for
// reasons that have nothing to do with this service at all. A bare
// status-code check would mis-type an unrelated infrastructure 404 as a
// lease refusal merely because the request happened to name a lease, so
// REST additionally confirms the server's own message is specifically
// this condition before trusting it (go-qrdb#591's review, non-blocking)
// -- not the substring classification go-qrdb#289 deleted, which guessed
// WHICH SUBJECT a shared code was about; this call site already knows
// the subject (a lease, from its own request) and is only confirming the
// server agrees, against a message this codebase's own typed error
// generates deterministically, never a human-authored string.
//
// The offending lease ID itself still comes from the SAME ops the caller
// sent (transport.FirstLeaseID: success ops before failure ops), never
// parsed back out of the message, matching every other transport's copy
// of this function.
func mapOpsNotFoundError(ops []transport.Op, err error) error {
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != http.StatusNotFound {
		return err
	}
	if reqErr.Err == nil || !strings.HasPrefix(reqErr.Err.Error(), leaseNotFoundServerMessagePrefix) {
		return err
	}
	if id, found := transport.FirstLeaseID(ops); found {
		return &transport.LeaseNotFoundError{LeaseID: id}
	}
	return err
}

// userLookupNotFound is userNotFound for an account named by a non-numeric
// key.
func userLookupNotFound(lookup string, err error) error {
	var reqErr *transport.RequestError
	if errors.As(err, &reqErr) && reqErr.StatusCode == http.StatusNotFound {
		return &transport.UserNotFoundError{Lookup: lookup}
	}
	return err
}

// CreateUser creates a new passwordless user. Maps to POST /users.
func (t *Transport) CreateUser(ctx context.Context, req *transport.CreateUserRequest) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var user transport.User
	if err := t.postJSONDecode(ctx, "CreateUser", "/users", req, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

// GetUser retrieves a user by its globally unique ID. Maps to GET /users/{id}.
func (t *Transport) GetUser(ctx context.Context, id uint64) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	idStr := strconv.FormatUint(id, 10)
	reqURL := t.baseURL + "/users/" + idStr
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUser", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUser", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &transport.UserNotFoundError{ID: id}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetUser", resp)
	}

	var user transport.User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, &transport.RequestError{Method: "GetUser", Err: &jsonDecodeError{Err: err}}
	}

	return &user, nil
}

// GetUserByUsername retrieves a user by tenant-scoped username. Maps to
// GET /users/by-username/{tenantID}/{username}.
func (t *Transport) GetUserByUsername(ctx context.Context, tenantID, username string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	query := url.Values{}
	if tenantID != "" {
		query.Set("tenantID", tenantID)
	}
	reqURL := t.baseURL + "/users/by-username/" + url.PathEscape(username)
	if encoded := query.Encode(); encoded != "" {
		reqURL += "?" + encoded
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUserByUsername", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUserByUsername", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &transport.UserNotFoundError{Lookup: transport.UserLookupKey(tenantID, username)}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetUserByUsername", resp)
	}

	var user transport.User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, &transport.RequestError{Method: "GetUserByUsername", Err: &jsonDecodeError{Err: err}}
	}

	return &user, nil
}

// GetUserByEmail resolves the canonical login key over REST:
// POST /users/lookup with the email in the BODY.
//
// Deliberately not GET /users/by-email/{email}: email is the canonical login
// identifier, and a path parameter is captured by access logs, proxies and
// tracing on every successful lookup (Codex, PR #191).
func (t *Transport) GetUserByEmail(ctx context.Context, tenantID, email string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	body, mErr := json.Marshal(map[string]string{"email": email, "tenant_id": tenantID})
	if mErr != nil {
		return nil, &transport.RequestError{Method: "GetUserByEmail", Err: mErr}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/users/lookup", bytes.NewReader(body))
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUserByEmail", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUserByEmail", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &transport.UserNotFoundError{Lookup: transport.UserLookupKey(tenantID, email)}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetUserByEmail", resp)
	}

	var user transport.User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, &transport.RequestError{Method: "GetUserByEmail", Err: &jsonDecodeError{Err: err}}
	}

	return &user, nil
}

// GetUserByCertThumbprint resolves a user by mTLS SPKI thumbprint. Maps to
// GET /users/by-cert-thumbprint/{thumbprint}.
func (t *Transport) GetUserByCertThumbprint(ctx context.Context, thumbprint string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/users/by-cert-thumbprint/" + url.PathEscape(thumbprint)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUserByCertThumbprint", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetUserByCertThumbprint", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &transport.UserNotFoundError{Lookup: "cert_spki/" + thumbprint}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetUserByCertThumbprint", resp)
	}

	var user transport.User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, &transport.RequestError{Method: "GetUserByCertThumbprint", Err: &jsonDecodeError{Err: err}}
	}

	return &user, nil
}

// AddCertBinding binds an mTLS SPKI thumbprint to a user. Maps to
// POST /users/{userID}/cert-bindings.
func (t *Transport) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload := map[string]any{
		"thumbprint":     thumbprint,
		"not_after_unix": notAfterUnix,
	}

	var user transport.User
	path := "/users/" + strconv.FormatUint(userID, 10) + "/cert-bindings"
	if err := t.postJSONDecode(ctx, "AddCertBinding", path, payload, &user); err != nil {
		return nil, userNotFound(userID, err)
	}

	return &user, nil
}

// RemoveCertBinding withdraws an mTLS SPKI thumbprint from a user. Maps to
// DELETE /users/{userID}/cert-bindings/{thumbprint}.
//
// A 404 means "the user or the binding you named is not there" and is typed as
// a UserNotFoundError, the same as every other user-addressed call in this
// interface. The two causes differ only in the server's message: classifying
// them apart would mean matching on message text, which this transport does not
// do. The in-process (embedded) transport passes the store's typed
// *user.CertBindingNotFoundError through unchanged.
func (t *Transport) RemoveCertBinding(ctx context.Context, userID uint64, thumbprint string) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var user transport.User
	path := "/users/" + strconv.FormatUint(userID, 10) + "/cert-bindings/" + url.PathEscape(thumbprint)
	if err := t.deleteJSONDecode(ctx, "RemoveCertBinding", path, &user); err != nil {
		return nil, userNotFound(userID, err)
	}

	return &user, nil
}

// UpdateUser applies partial updates to an existing user. Maps to
// PUT /users/{id}.
func (t *Transport) UpdateUser(ctx context.Context, req *transport.UpdateUserRequest) (*transport.User, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var user transport.User
	if err := t.putJSONDecode(ctx, "UpdateUser", "/users/"+strconv.FormatUint(req.ID, 10), req, &user); err != nil {
		return nil, userNotFound(req.ID, err)
	}
	return &user, nil
}

// DeleteUser removes a non-system user by ID. Maps to DELETE /users/{id}.
func (t *Transport) DeleteUser(ctx context.Context, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/users/" + strconv.FormatUint(id, 10)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return &transport.RequestError{Method: "DeleteUser", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "DeleteUser", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}

	return userNotFound(id, t.decodeErrorResponse("DeleteUser", resp))
}

// ListUsers returns a paginated list of users for the given tenant.
// Maps to GET /users/list?tenantID=X&page=N&pageSize=N.
func (t *Transport) ListUsers(ctx context.Context, req *transport.ListUsersRequest) (*transport.ListUsersResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	if req.TenantID != "" {
		params.Set("tenantID", req.TenantID)
	}
	if req.Page > 0 {
		params.Set("page", strconv.Itoa(req.Page))
	}
	if req.PageSize > 0 {
		params.Set("pageSize", strconv.Itoa(req.PageSize))
	}

	reqURL := t.baseURL + "/users/list"
	if encoded := params.Encode(); encoded != "" {
		reqURL += "?" + encoded
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListUsers", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListUsers", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListUsers", resp)
	}

	var listResp transport.ListUsersResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, &transport.RequestError{Method: "ListUsers", Err: &jsonDecodeError{Err: err}}
	}

	return &listResp, nil
}

// EnableUser activates a user account. Maps to POST /users/{id}/enable.
func (t *Transport) EnableUser(ctx context.Context, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return userNotFound(id, t.postJSON(ctx, "EnableUser", "/users/"+strconv.FormatUint(id, 10)+"/enable", nil))
}

// DisableUser deactivates a user account. Maps to POST /users/{id}/disable.
func (t *Transport) DisableUser(ctx context.Context, id uint64) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	return userNotFound(id, t.postJSON(ctx, "DisableUser", "/users/"+strconv.FormatUint(id, 10)+"/disable", nil))
}

// ---------------------------------------------------------------------------
// RoleClient implementation
// ---------------------------------------------------------------------------

// CreateRole creates a new role with a generated UUID. Maps to POST /roles.
func (t *Transport) CreateRole(ctx context.Context, req *transport.CreateRoleRequest) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var role transport.Role
	if err := t.postJSONDecode(ctx, "CreateRole", "/roles", req, &role); err != nil {
		return nil, err
	}
	return &role, nil
}

// GetRole retrieves a role by its globally unique ID. Maps to GET /roles/{id}.
func (t *Transport) GetRole(ctx context.Context, id string) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/roles/" + url.PathEscape(id)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetRole", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "GetRole", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, &transport.RoleNotFoundError{ID: id}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("GetRole", resp)
	}

	var role transport.Role
	if err := json.NewDecoder(resp.Body).Decode(&role); err != nil {
		return nil, &transport.RequestError{Method: "GetRole", Err: &jsonDecodeError{Err: err}}
	}

	return &role, nil
}

// UpdateRole applies partial updates to an existing role. Maps to
// PUT /roles/{id}.
func (t *Transport) UpdateRole(ctx context.Context, req *transport.UpdateRoleRequest) (*transport.Role, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	var role transport.Role
	if err := t.putJSONDecode(ctx, "UpdateRole", "/roles/"+url.PathEscape(req.ID), req, &role); err != nil {
		return nil, err
	}
	return &role, nil
}

// DeleteRole removes a non-system role by ID. Maps to DELETE /roles/{id}.
func (t *Transport) DeleteRole(ctx context.Context, id string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/roles/" + url.PathEscape(id)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return &transport.RequestError{Method: "DeleteRole", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: "DeleteRole", Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}

	return t.decodeErrorResponse("DeleteRole", resp)
}

// ListRoles returns a paginated list of roles for the given tenant.
// Maps to GET /roles/list?tenantID=X&page=N&pageSize=N.
func (t *Transport) ListRoles(ctx context.Context, req *transport.ListRolesRequest) (*transport.ListRolesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	params := url.Values{}
	if req.TenantID != "" {
		params.Set("tenantID", req.TenantID)
	}
	if req.Page > 0 {
		params.Set("page", strconv.Itoa(req.Page))
	}
	if req.PageSize > 0 {
		params.Set("pageSize", strconv.Itoa(req.PageSize))
	}

	reqURL := t.baseURL + "/roles/list"
	if encoded := params.Encode(); encoded != "" {
		reqURL += "?" + encoded
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListRoles", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListRoles", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListRoles", resp)
	}

	var listResp transport.ListRolesResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, &transport.RequestError{Method: "ListRoles", Err: &jsonDecodeError{Err: err}}
	}

	return &listResp, nil
}

// AssignRole assigns a role to a user. Maps to POST /roles/assign.
func (t *Transport) AssignRole(ctx context.Context, userID uint64, roleID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := transport.AssignRoleRequest{
		UserID: userID,
		RoleID: roleID,
	}

	return t.postJSON(ctx, "AssignRole", "/roles/assign", payload)
}

// UnassignRole removes a role assignment from a user. Maps to
// POST /roles/unassign.
func (t *Transport) UnassignRole(ctx context.Context, userID uint64, roleID string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}

	payload := transport.UnassignRoleRequest{
		UserID: userID,
		RoleID: roleID,
	}

	return t.postJSON(ctx, "UnassignRole", "/roles/unassign", payload)
}

// ListPermissions returns all available permission categories. Maps to
// GET /permissions.
func (t *Transport) ListPermissions(ctx context.Context) (*transport.ListPermissionsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/permissions", nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListPermissions", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListPermissions", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListPermissions", resp)
	}

	var permsResp transport.ListPermissionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&permsResp); err != nil {
		return nil, &transport.RequestError{Method: "ListPermissions", Err: &jsonDecodeError{Err: err}}
	}

	return &permsResp, nil
}

// ---------------------------------------------------------------------------
// CertClient implementation
// ---------------------------------------------------------------------------

// IssueCert issues a certificate from a CSR for an existing account of the
// caller's own tenant and binds it. Maps to POST /certs/issue.
func (t *Transport) IssueCert(ctx context.Context, req *transport.IssueCertRequest) (*transport.EnrolledCert, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var out transport.EnrolledCert
	if err := t.postJSONDecode(ctx, "IssueCert", "/certs/issue", req, &out); err != nil {
		// The only thing IssueCert can fail to find is the account.
		return nil, userLookupNotFound(req.Username, err)
	}
	return &out, nil
}

// EnrollCert binds a bring-your-own certificate to an existing account of the
// caller's own tenant. Maps to POST /certs/enroll.
func (t *Transport) EnrollCert(ctx context.Context, req *transport.EnrollCertRequest) (*transport.EnrolledCert, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	var out transport.EnrolledCert
	if err := t.postJSONDecode(ctx, "EnrollCert", "/certs/enroll", req, &out); err != nil {
		// The only thing EnrollCert can fail to find is the account.
		return nil, userLookupNotFound(req.Username, err)
	}
	return &out, nil
}

// RevokeCert marks a certificate serial as revoked, and reports whether
// the server held a certificate record for it. Maps to POST /certs/revoke.
func (t *Transport) RevokeCert(ctx context.Context, serial string) (*transport.RevokeResult, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	payload := struct {
		Serial string `json:"serial"`
	}{Serial: serial}

	var resp revokeCertResponse
	if err := t.postJSONDecode(ctx, "RevokeCert", "/certs/revoke", payload, &resp); err != nil {
		return nil, err
	}
	return &transport.RevokeResult{
		Serial:      serial,
		RecordFound: resp.RecordFound,
	}, nil
}

// revokeCertResponse decodes the server's revocation response.
type revokeCertResponse struct {
	Status      string `json:"status"`
	RecordFound bool   `json:"recordFound"`
}

// ListCerts lists all issued client certificates for a tenant.
// Maps to GET /certs/list?tenantID=X.
func (t *Transport) ListCerts(ctx context.Context, tenantID string) (*transport.ListCertsResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	reqURL := t.baseURL + "/certs/list"
	if tenantID != "" {
		reqURL += "?tenantID=" + url.QueryEscape(tenantID)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListCerts", Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return nil, &transport.RequestError{Method: "ListCerts", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, t.decodeErrorResponse("ListCerts", resp)
	}

	var listResp transport.ListCertsResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		return nil, &transport.RequestError{Method: "ListCerts", Err: &jsonDecodeError{Err: err}}
	}

	return &listResp, nil
}

// ---------------------------------------------------------------------------
// Auth transport helpers
// ---------------------------------------------------------------------------

// postJSONDecode encodes a payload as JSON, POSTs it to the given path,
// and decodes the JSON response body into dest.
func (t *Transport) postJSONDecode(ctx context.Context, method, path string, payload, dest any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return &transport.RequestError{Method: method, Err: &jsonEncodeError{Err: err}}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return t.decodeErrorResponse(method, resp)
	}

	if dest != nil {
		if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
			return &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: err}}
		}
	}

	return nil
}

// deleteJSONDecode issues a DELETE to the given path and decodes the JSON
// response body into dest. It is the withdrawal shape: the resource removed is
// named entirely by the path, and the server answers with the record as it now
// stands.
func (t *Transport) deleteJSONDecode(ctx context.Context, method, path string, dest any) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.baseURL+path, nil)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return t.decodeErrorResponse(method, resp)
	}

	if dest != nil {
		if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
			return &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: err}}
		}
	}

	return nil
}

// putJSONDecode encodes a payload as JSON, PUTs it to the given path,
// and decodes the JSON response body into dest.
func (t *Transport) putJSONDecode(ctx context.Context, method, path string, payload, dest any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return &transport.RequestError{Method: method, Err: &jsonEncodeError{Err: err}}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, t.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return t.decodeErrorResponse(method, resp)
	}

	if dest != nil {
		if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
			return &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: err}}
		}
	}

	return nil
}

// patchJSONDecode encodes a payload as JSON, PATCHes it to the given path,
// and decodes the JSON response body into dest.
func (t *Transport) patchJSONDecode(ctx context.Context, method, path string, payload, dest any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return &transport.RequestError{Method: method, Err: &jsonEncodeError{Err: err}}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, t.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return &transport.RequestError{Method: method, Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return t.decodeErrorResponse(method, resp)
	}

	if dest != nil {
		if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
			return &transport.RequestError{Method: method, Err: &jsonDecodeError{Err: err}}
		}
	}

	return nil
}
