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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// tenantRequestTimeout bounds every tenant endpoint so a stalled Raft
// SyncRead never hangs the GUI. 5s is generous — the underlying kv ops
// should return in milliseconds on a healthy cluster; if they don't,
// something is wrong and the UI needs to hear about it, not wait forever.
const tenantRequestTimeout = 5 * time.Second

// ---------------------------------------------------------------------------
// Tenant request/response types
// ---------------------------------------------------------------------------

// createTenantRequest is the JSON payload for POST /v1/tenants.
type createTenantRequest struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	IsolationType tenant.IsolationType `json:"isolation_type,omitempty"`
}

// updateTenantRequest is the JSON payload for PUT /v1/tenants/{id}.
type updateTenantRequest struct {
	Name          string               `json:"name,omitempty"`
	IsolationType tenant.IsolationType `json:"isolation_type,omitempty"`
}

// setTenantQuotasRequest is the JSON payload for POST /v1/tenants/{id}/quotas.
type setTenantQuotasRequest struct {
	MaxDatabases    int64 `json:"max_databases"`
	MaxRaftGroups   int64 `json:"max_raft_groups"`
	MaxCollections  int64 `json:"max_collections"`
	MaxKeys         int64 `json:"max_keys"`
	MaxStorageBytes int64 `json:"max_storage_bytes"`
	MaxUsers        int64 `json:"max_users"`
	MaxRoles        int64 `json:"max_roles"`
	MaxCertificates int64 `json:"max_certificates"`
	MaxBackups      int64 `json:"max_backups"`
	MaxVectorDims   int64 `json:"max_vector_dims"`
	MaxTSMetrics    int64 `json:"max_ts_metrics"`
}

// tenantBarrierInitRequest is the JSON payload for
// POST /v1/tenants/{id}/barrier/init. Passphrase is the tenant admin's
// caller-supplied secret used to seal the per-tenant barrier; it is
// required for the passphrase strategy (there is no derived fallback). The
// threshold and shares fields are reserved for Shamir configuration.
// Strategy selects the seal strategy; when empty the passphrase (software)
// strategy is used for backward compatibility.
type tenantBarrierInitRequest struct {
	Passphrase string `json:"passphrase,omitempty"`
	Threshold  int    `json:"threshold,omitempty"`
	Shares     int    `json:"shares,omitempty"`
	Strategy   string `json:"strategy,omitempty"`
}

// tenantBarrierUnsealRequest is the JSON payload for
// POST /v1/tenants/{id}/barrier/unseal. The key field carries the unseal
// material and is base64-encoded on the wire by encoding/json. The key is
// required: the service rejects empty key material (there is no derived
// fallback).
type tenantBarrierUnsealRequest struct {
	Key []byte `json:"key,omitempty"`
}

// tenantResponse is the JSON representation of a tenant entity.
type tenantResponse struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	IsolationType tenant.IsolationType `json:"isolation_type"`
	Quotas        *quota.Quotas        `json:"quotas,omitempty"`
	CreatedAt     string               `json:"createdAt"`
	UpdatedAt     string               `json:"updatedAt"`
}

// listTenantsResponse is the JSON payload returned by GET /v1/tenants/list.
type listTenantsResponse struct {
	Tenants []*tenantResponse `json:"tenants"`
	Total   int               `json:"total"`
}

// ---------------------------------------------------------------------------
// Tenant handlers
// ---------------------------------------------------------------------------

// handleCreateTenant handles POST /v1/tenants -- creates a new tenant.
func (s *Server) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	if s.tenantSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "tenant"})
		return
	}

	var req createTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	info, err := s.tenantSvc.CreateTenant(ctx, req.ID, req.Name, req.IsolationType)
	if err != nil {
		writeErrorJSON(w, mapTenantError(err), err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(tenantToResponse(info))
}

// handleListTenants handles GET /v1/tenants/list -- returns all tenants.
func (s *Server) handleListTenants(w http.ResponseWriter, r *http.Request) {
	if s.tenantSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "tenant"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	tenants, err := s.tenantSvc.ListTenants(ctx)
	if err != nil {
		writeErrorJSON(w, mapTenantError(err), err)
		return
	}

	resp := make([]*tenantResponse, len(tenants))
	for i := range tenants {
		resp[i] = tenantToResponse(&tenants[i])
	}

	writeJSON(w, listTenantsResponse{
		Tenants: resp,
		Total:   len(resp),
	})
}

// handleTenantByID dispatches tenant operations that carry a tenant ID in
// the URL path.
//
// Routes:
//   - GET    /v1/tenants/{id}                 -> get tenant
//   - DELETE /v1/tenants/{id}                 -> delete tenant
//   - PUT    /v1/tenants/{id}                 -> update tenant
//   - POST   /v1/tenants/{id}/quotas          -> set tenant quotas
//   - POST   /v1/tenants/{id}/barrier/init    -> initialize tenant barrier
//   - POST   /v1/tenants/{id}/barrier/unseal  -> unseal tenant barrier
//   - POST   /v1/tenants/{id}/barrier/seal    -> seal tenant barrier
//   - GET    /v1/tenants/{id}/barrier/status  -> tenant barrier status
func (s *Server) handleTenantByID(w http.ResponseWriter, r *http.Request) {
	if s.tenantSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "tenant"})
		return
	}

	// Strip "/v1/tenants/" prefix; remainder is "{id}", "{id}/quotas",
	// "{id}/barrier/init", "{id}/barrier/unseal", "{id}/barrier/seal", or
	// "{id}/barrier/status".
	rest := strings.TrimPrefix(r.URL.Path, "/v1/tenants/")
	if rest == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "id"})
		return
	}

	// GET /v1/tenants/{id}/barrier/status — the only GET sub-path; check
	// before the bare-{id} GET dispatch below so the suffix is not treated
	// as part of the tenant ID.
	if r.Method == http.MethodGet {
		if id, found := strings.CutSuffix(rest, "/barrier/status"); found && id != "" {
			s.handleTenantBarrierStatus(w, r, id)
			return
		}
	}

	// POST sub-paths: /quotas, /barrier/init, /barrier/unseal, /barrier/seal.
	if r.Method == http.MethodPost {
		if id, found := strings.CutSuffix(rest, "/quotas"); found && id != "" {
			s.handleSetTenantQuotas(w, r, id)
			return
		}
		if id, found := strings.CutSuffix(rest, "/barrier/init"); found && id != "" {
			s.handleTenantBarrierInit(w, r, id)
			return
		}
		if id, found := strings.CutSuffix(rest, "/barrier/unseal"); found && id != "" {
			s.handleTenantBarrierUnseal(w, r, id)
			return
		}
		if id, found := strings.CutSuffix(rest, "/barrier/seal"); found && id != "" {
			s.handleTenantBarrierSeal(w, r, id)
			return
		}
		w.Header().Set("Allow", "GET, DELETE, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// GET /v1/tenants/{id}, DELETE /v1/tenants/{id}, PUT /v1/tenants/{id}
	id := rest
	handler, ok := tenantMethodHandlers[r.Method]
	if !ok {
		w.Header().Set("Allow", "GET, DELETE, PUT, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	handler(s, w, r, id)
}

// tenantMethodHandler handles a tenant request for a specific HTTP method.
type tenantMethodHandler func(s *Server, w http.ResponseWriter, r *http.Request, id string)

// tenantMethodHandlers maps HTTP methods to their tenant handler functions.
var tenantMethodHandlers = map[string]tenantMethodHandler{
	http.MethodGet:    (*Server).handleGetTenant,
	http.MethodDelete: (*Server).handleDeleteTenant,
	http.MethodPut:    (*Server).handleUpdateTenant,
}

// handleGetTenant handles GET /v1/tenants/{id} -- retrieves a tenant by ID.
func (s *Server) handleGetTenant(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	info, err := s.tenantSvc.GetTenant(ctx, id)
	if err != nil {
		writeErrorJSON(w, mapTenantError(err), err)
		return
	}

	writeJSON(w, tenantToResponse(info))
}

// handleDeleteTenant handles DELETE /v1/tenants/{id} -- removes a tenant.
func (s *Server) handleDeleteTenant(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	if err := s.tenantSvc.DeleteTenant(ctx, id); err != nil {
		writeErrorJSON(w, mapTenantError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleUpdateTenant handles PUT /v1/tenants/{id} -- applies a partial update
// to an existing tenant. Changing the IsolationType on a tenant with existing
// data returns 409 Conflict.
func (s *Server) handleUpdateTenant(w http.ResponseWriter, r *http.Request, id string) {
	var req updateTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()

	// When the caller is changing the isolation type, verify the tenant
	// has no data. An empty or matching isolation type skips this check.
	if req.IsolationType != "" {
		existing, err := s.tenantSvc.GetTenant(ctx, id)
		if err != nil {
			writeErrorJSON(w, mapTenantError(err), err)
			return
		}
		if req.IsolationType != existing.EffectiveIsolationType() {
			// Check whether this tenant has any stored data via the service
			// registry. We resolve against the default database to probe the
			// tenant's KV service and check for at least one key.
			if s.serviceRegistry != nil {
				kvSvc, resolveErr := s.serviceRegistry.KVService(id, "")
				if resolveErr == nil {
					keys, listErr := kvSvc.List(ctx, "")
					if listErr == nil && len(keys) > 0 {
						writeErrorJSON(w, http.StatusConflict, tenant.ErrTenantHasData)
						return
					}
				}
			}
		}
	}

	patch := tenant.TenantPatch{
		Name:          req.Name,
		IsolationType: req.IsolationType,
	}

	info, err := s.tenantSvc.UpdateTenant(ctx, id, patch)
	if err != nil {
		writeErrorJSON(w, mapTenantError(err), err)
		return
	}

	writeJSON(w, tenantToResponse(info))
}

// handleSetTenantQuotas handles POST /v1/tenants/{id}/quotas -- updates the
// resource quotas for an existing tenant.
func (s *Server) handleSetTenantQuotas(w http.ResponseWriter, r *http.Request, id string) {
	var req setTenantQuotasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	q := &quota.Quotas{
		MaxDatabases:    req.MaxDatabases,
		MaxRaftGroups:   req.MaxRaftGroups,
		MaxCollections:  req.MaxCollections,
		MaxKeys:         req.MaxKeys,
		MaxStorageBytes: req.MaxStorageBytes,
		MaxUsers:        req.MaxUsers,
		MaxRoles:        req.MaxRoles,
		MaxCertificates: req.MaxCertificates,
		MaxBackups:      req.MaxBackups,
		MaxVectorDims:   req.MaxVectorDims,
		MaxTSMetrics:    req.MaxTSMetrics,
	}

	info, err := s.tenantSvc.UpdateQuotas(r.Context(), id, q)
	if err != nil {
		writeErrorJSON(w, mapTenantError(err), err)
		return
	}

	writeJSON(w, tenantToResponse(info))
}

// authorizeTenantBarrierOp enforces the tenant-scoped authorization gate for
// a per-tenant barrier operation against the TRUE target tenant carried in
// the URL {id} (not the identity's own tenant). It delegates to the auth
// middleware's tenant-scoped authorizer, whose ScopeTenantOnly rule denies
// the system tenant and any cross-tenant access. When no auth middleware is
// configured (unauthenticated deployment), the gate is a no-op, mirroring
// the rest of the server's behavior in that mode.
func (s *Server) authorizeTenantBarrierOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenant(ctx, tenantID, perm)
}

// handleTenantBarrierInit handles POST /v1/tenants/{id}/barrier/init --
// initializes the per-tenant barrier with the given threshold and shares.
// Both fields are optional; when both are zero, the service uses default
// credentials derived from the tenant ID. Returns 204 No Content on success.
func (s *Server) handleTenantBarrierInit(w http.ResponseWriter, r *http.Request, id string) {
	// Decode the body when present. An empty body is permitted because
	// both threshold and shares default to zero.
	var req tenantBarrierInitRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	if err := s.authorizeTenantBarrierOp(ctx, id, permission.TenantBarrierInit); err != nil {
		writeErrorJSON(w, http.StatusForbidden, err)
		return
	}
	if err := s.tenantSvc.TenantBarrierInitWithStrategy(ctx, id, req.Passphrase, req.Threshold, req.Shares, seal.StrategyID(req.Strategy)); err != nil {
		writeErrorJSON(w, mapTenantError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleTenantBarrierUnseal handles POST /v1/tenants/{id}/barrier/unseal --
// unseals the per-tenant barrier using the supplied key material. The key
// material is required: the service rejects empty key material with
// ErrEmptyPassphrase (there is no derived fallback). Returns 204 No Content
// on success.
func (s *Server) handleTenantBarrierUnseal(w http.ResponseWriter, r *http.Request, id string) {
	// Decode the body when present. The unseal key is supplied in the body;
	// an empty key is rejected by the service (no derived fallback).
	var req tenantBarrierUnsealRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	if err := s.authorizeTenantBarrierOp(ctx, id, permission.TenantBarrierUnseal); err != nil {
		writeErrorJSON(w, http.StatusForbidden, err)
		return
	}
	if err := s.tenantSvc.TenantBarrierUnseal(ctx, id, req.Key); err != nil {
		writeErrorJSON(w, mapTenantError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleTenantBarrierSeal handles POST /v1/tenants/{id}/barrier/seal --
// seals the per-tenant barrier. The endpoint takes no body. Returns 204
// No Content on success.
func (s *Server) handleTenantBarrierSeal(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	if err := s.authorizeTenantBarrierOp(ctx, id, permission.TenantBarrierSeal); err != nil {
		writeErrorJSON(w, http.StatusForbidden, err)
		return
	}
	if err := s.tenantSvc.TenantBarrierSeal(ctx, id); err != nil {
		writeErrorJSON(w, mapTenantError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleTenantBarrierStatus handles GET /v1/tenants/{id}/barrier/status --
// reports the current sealed/initialized state and active strategy metadata
// for the per-tenant barrier. Returns 200 OK with a JSON-encoded
// tenant.TenantBarrierStatus on success.
func (s *Server) handleTenantBarrierStatus(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	// Reading a tenant's barrier state leaks isolation metadata, so it is gated
	// by the same tenant-scoped authorizer as init/unseal/seal: a tenant admin
	// may read only their own tenant's barrier, and the system tenant is denied.
	if err := s.authorizeTenantBarrierOp(ctx, id, permission.TenantBarrierStatus); err != nil {
		writeErrorJSON(w, http.StatusForbidden, err)
		return
	}
	status, err := s.tenantSvc.TenantBarrierStatus(ctx, id)
	if err != nil {
		writeErrorJSON(w, mapTenantError(err), err)
		return
	}

	writeJSON(w, status)
}

// ---------------------------------------------------------------------------
// Tenant conversion and error mapping
// ---------------------------------------------------------------------------

// tenantToResponse converts a tenant.TenantInfo to its JSON response.
func tenantToResponse(info *tenant.TenantInfo) *tenantResponse {
	if info == nil {
		return nil
	}
	return &tenantResponse{
		ID:            info.ID,
		Name:          info.Name,
		IsolationType: info.EffectiveIsolationType(),
		Quotas:        info.Quotas,
		CreatedAt:     info.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     info.UpdatedAt.Format(time.RFC3339),
	}
}

// mapTenantError maps tenant domain errors to the appropriate HTTP status code.
func mapTenantError(err error) int {
	var se *tenant.StoreError
	var be *tenant.BarrierError
	var ite *tenant.InvalidTenantIDError

	switch {
	case errors.Is(err, tenant.ErrEmptyTenantID):
		return http.StatusBadRequest
	case errors.Is(err, tenant.ErrEmptyTenantName):
		return http.StatusBadRequest
	case errors.Is(err, tenant.ErrEmptyPassphrase):
		return http.StatusBadRequest
	case errors.Is(err, tenant.ErrNilQuotas):
		return http.StatusBadRequest
	case errors.Is(err, tenant.ErrTenantAlreadyExists):
		return http.StatusConflict
	case errors.Is(err, tenant.ErrTenantHasData):
		return http.StatusConflict
	case errors.Is(err, tenant.ErrTenantNotFound):
		return http.StatusNotFound
	case errors.As(err, &ite):
		return http.StatusBadRequest
	case errors.As(err, &se):
		return http.StatusInternalServerError
	case errors.As(err, &be):
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
