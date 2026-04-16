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
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

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

	info, err := s.tenantSvc.CreateTenant(r.Context(), req.ID, req.Name, req.IsolationType)
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

	tenants, err := s.tenantSvc.ListTenants(r.Context())
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
//   - GET    /v1/tenants/{id}        -> get tenant
//   - DELETE /v1/tenants/{id}        -> delete tenant
//   - POST   /v1/tenants/{id}/quotas -> set tenant quotas
func (s *Server) handleTenantByID(w http.ResponseWriter, r *http.Request) {
	if s.tenantSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "tenant"})
		return
	}

	// Strip "/v1/tenants/" prefix; remainder is "{id}" or "{id}/quotas".
	rest := strings.TrimPrefix(r.URL.Path, "/v1/tenants/")
	if rest == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "id"})
		return
	}

	// POST /v1/tenants/{id}/quotas
	if r.Method == http.MethodPost {
		if id, found := strings.CutSuffix(rest, "/quotas"); found && id != "" {
			s.handleSetTenantQuotas(w, r, id)
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
	info, err := s.tenantSvc.GetTenant(r.Context(), id)
	if err != nil {
		writeErrorJSON(w, mapTenantError(err), err)
		return
	}

	writeJSON(w, tenantToResponse(info))
}

// handleDeleteTenant handles DELETE /v1/tenants/{id} -- removes a tenant.
func (s *Server) handleDeleteTenant(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.tenantSvc.DeleteTenant(r.Context(), id); err != nil {
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

	// When the caller is changing the isolation type, verify the tenant
	// has no data. An empty or matching isolation type skips this check.
	if req.IsolationType != "" {
		existing, err := s.tenantSvc.GetTenant(r.Context(), id)
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
					keys, listErr := kvSvc.List(r.Context(), "")
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

	info, err := s.tenantSvc.UpdateTenant(r.Context(), id, patch)
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
