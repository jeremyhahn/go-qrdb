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
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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
	// BarrierAutoUnseal opts a dedicated-isolation tenant into the
	// system-managed auto-unseal key-management mode (entitlement-gated). The
	// default (false) is the secure self-managed mode.
	BarrierAutoUnseal bool `json:"barrier_auto_unseal,omitempty"`
	// VolumePool names the volume pool an IsolationVolume/IsolationRaft
	// tenant is placed on (see tenant.WithVolumePool). Empty (the default)
	// uses the tenant's default pool, the first entry of its
	// AllowedVolumePools.
	VolumePool string `json:"volume_pool,omitempty"`

	// AdminUsername is the tenant's first admin. Required: a tenant is
	// created with its first admin's login credential, or not at all.
	AdminUsername string `json:"admin_username"`
	// AdminCSRPEM is the admin's certificate signing request, signed by the
	// tenant's own Key B CA; the admin keeps its key. Exactly one of this and
	// AdminCertPEM.
	AdminCSRPEM string `json:"admin_csr_pem,omitempty"`
	// AdminCertPEM is the admin's own certificate, leaf first, which must
	// chain to one of TrustAnchorsPEM.
	AdminCertPEM string `json:"admin_cert_pem,omitempty"`
	// AdminChainPEM holds intermediates for AdminCertPEM.
	AdminChainPEM string `json:"admin_chain_pem,omitempty"`
	// TrustAnchorsPEM registers the tenant's trust anchors with it.
	TrustAnchorsPEM string `json:"trust_anchors_pem,omitempty"`
}

// createTenantResponse is the 201 body of POST /v1/tenants: the tenant and its
// first admin's credential. There is never a private key.
type createTenantResponse struct {
	*tenantResponse
	Admin tenantAdminResponse `json:"admin"`
}

// tenantAdminResponse is a tenant's first admin as onboarding left it.
type tenantAdminResponse struct {
	UserID            uint64 `json:"user_id,omitempty,string"`
	Username          string `json:"username"`
	CertPEM           string `json:"cert_pem"`
	Serial            string `json:"serial,omitempty"`
	Thumbprint        string `json:"thumbprint"`
	AwaitsBarrierInit bool   `json:"awaits_barrier_init,omitempty"`
}

// updateTenantRequest is the JSON payload for PUT /v1/tenants/{id}.
type updateTenantRequest struct {
	Name          string               `json:"name,omitempty"`
	IsolationType tenant.IsolationType `json:"isolation_type,omitempty"`
}

// setTenantQuotasRequest is the JSON payload for POST /v1/tenants/{id}/quotas.
type setTenantQuotasRequest struct {
	MaxDatabases         int64 `json:"max_databases"`
	MaxRaftGroups        int64 `json:"max_raft_groups"`
	MaxReplicasPerGroup  int64 `json:"max_replicas_per_group"`
	MaxTotalReplicas     int64 `json:"max_total_replicas"`
	MaxCollections       int64 `json:"max_collections"`
	MaxKeys              int64 `json:"max_keys"`
	MaxStorageBytes      int64 `json:"max_storage_bytes"`
	MaxDatabaseSizeBytes int64 `json:"max_database_size_bytes"`
	MaxUsers             int64 `json:"max_users"`
	MaxRoles             int64 `json:"max_roles"`
	MaxCertificates      int64 `json:"max_certificates"`
	MaxBackups           int64 `json:"max_backups"`
	MaxVectorDims        int64 `json:"max_vector_dims"`
	MaxTSMetrics         int64 `json:"max_ts_metrics"`
	// SystemBarrierQuota governs an isolated tenant's use of the shared system
	// barrier (bytes): 0 makes the option unavailable, -1 unlimited, >0 a cap.
	SystemBarrierQuota int64 `json:"system_barrier_quota"`
	// MaxDedicatedWALGroups caps the number of dedicated-raft groups a tenant
	// may opt into a per-tenant-key WAL: -1 = unlimited.
	MaxDedicatedWALGroups int64 `json:"max_dedicated_wal_groups"`
}

// setTenantEntitlementsRequest is the JSON payload for
// PATCH /v1/tenants/{id}/entitlements.
type setTenantEntitlementsRequest struct {
	AllowedIsolationModes     []string `json:"allowed_isolation_modes,omitempty"`
	AllowDedicatedRaft        bool     `json:"allow_dedicated_raft,omitempty"`
	AllowVolumeStorage        bool     `json:"allow_volume_storage,omitempty"`
	AllowSystemManagedBarrier bool     `json:"allow_system_managed_barrier,omitempty"`
	AllowDedicatedWAL         bool     `json:"allow_dedicated_wal,omitempty"`
	// AllowedBarrierModes lists the dedicated-barrier sealing strategies the
	// isolated tenant may select (none/passphrase/shamir or an xkms hardware id).
	AllowedBarrierModes []string `json:"allowed_barrier_modes,omitempty"`
	// AllowedVolumePools lists the named volume pools a volume/raft tenant
	// may be placed on; the first entry is its default. Fail-closed: empty
	// entitles no pool.
	AllowedVolumePools []string `json:"allowed_volume_pools,omitempty"`
}

// tenantBarrierInitRequest is the JSON payload for
// POST /v1/tenants/{id}/barrier/init. Passphrase is the tenant admin's
// caller-supplied secret used to seal the per-tenant barrier; it is
// required for the passphrase strategy (there is no derived fallback). The
// threshold and shares fields are reserved for Shamir configuration.
// Strategy selects the seal strategy; when empty the passphrase (software)
// strategy is used for backward compatibility.
//
// Passphrase is []byte (base64 on the wire, matching tenantBarrierUnsealRequest's
// key) so it can be zeroized; a Go string cannot be.
type tenantBarrierInitRequest struct {
	Passphrase []byte `json:"passphrase,omitempty"`
	Threshold  int    `json:"threshold,omitempty"`
	Shares     int    `json:"shares,omitempty"`
	Strategy   string `json:"strategy,omitempty"`

	// Officers is the inline Shamir share-holder roster to validate and
	// register atomically with initialization; see
	// transport.TenantBarrierInitRequest.Officers. Refused on any strategy
	// but shamir.
	Officers []transport.OfficerRegisterRequest `json:"officers,omitempty"`
	// Policy is the factor policy Officers enrol under ("" selects the
	// holder engine's default, "designated").
	Policy string `json:"policy,omitempty"`
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
	// VolumePool is the named volume pool for an IsolationVolume /
	// IsolationRaft tenant. Empty for prefix/barrier tenants.
	VolumePool   string              `json:"volume_pool,omitempty"`
	Quotas       *quota.Quotas       `json:"quotas,omitempty"`
	Entitlements *quota.Entitlements `json:"entitlements,omitempty"`
	CreatedAt    string              `json:"createdAt"`
	UpdatedAt    string              `json:"updatedAt"`
	// Refusal is set when the answering node will not serve this tenant's
	// data; absent otherwise. Another node may still serve it.
	Refusal *tenantRefusal `json:"refusal,omitempty"`
}

// tenantRefusal is the JSON form of tenant.Refusal.
type tenantRefusal struct {
	Code    string `json:"code"`
	Message string `json:"message"`
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

	if s.tenantOnboarding == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "tenant onboarding"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	result, err := s.tenantOnboarding.OnboardTenant(ctx, &service.OnboardTenantRequest{
		ID:                req.ID,
		Name:              req.Name,
		IsolationType:     req.IsolationType,
		BarrierAutoUnseal: req.BarrierAutoUnseal,
		VolumePool:        req.VolumePool,
		AdminUsername:     req.AdminUsername,
		AdminCSRPEM:       []byte(req.AdminCSRPEM),
		AdminCertPEM:      []byte(req.AdminCertPEM),
		AdminChainPEM:     []byte(req.AdminChainPEM),
		TrustAnchorsPEM:   []byte(req.TrustAnchorsPEM),
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(createTenantResponse{
		tenantResponse: tenantToResponse(result.Tenant),
		Admin: tenantAdminResponse{
			UserID:            result.Admin.UserID,
			Username:          result.Admin.Username,
			CertPEM:           string(result.Admin.CertPEM),
			Serial:            result.Admin.Serial,
			Thumbprint:        result.Admin.Thumbprint,
			AwaitsBarrierInit: result.Admin.AwaitsBarrierInit,
		},
	})
}

// handleListTenants handles GET /v1/tenants/list -- returns every tenant to a
// caller holding tenants:list:cross, and only the caller's own tenant to
// anyone else (authmw.FilterTenantList).
func (s *Server) handleListTenants(w http.ResponseWriter, r *http.Request) {
	if s.tenantSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "tenant"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	tenants, err := s.tenantSvc.ListTenants(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	if s.authMW != nil {
		if tenants, err = s.authMW.FilterTenantList(ctx, tenants); err != nil {
			writeAuthzErrorJSON(w, err)
			return
		}
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

// handleListDeletedTenants handles GET /v1/tenants/deleted/list -- one page of
// retired tenant ids. Query parameters: page_size (0 or absent = server
// default) and page_token (the previous page's next_page_token).
func (s *Server) handleListDeletedTenants(w http.ResponseWriter, r *http.Request) {
	if s.tenantSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "tenant"})
		return
	}
	pageSize := 0
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &tenant.InvalidPageRequestError{Field: "page_size", Reason: "not an integer"})
			return
		}
		pageSize = n
	}

	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	page, err := s.tenantSvc.ListDeletedTenants(ctx, r.URL.Query().Get("page_token"), pageSize)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, page)
}

// handleReconcileDeletedTenants handles POST /v1/tenants/deleted/reconcile --
// reclaim, on THIS node, the on-disk residue of retired tenant ids. It is
// node-local by nature, so it is never forwarded to the leader: each replica
// reclaims its own disk.
func (s *Server) handleReconcileDeletedTenants(w http.ResponseWriter, r *http.Request) {
	if s.tenantSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "tenant"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	report, err := s.tenantSvc.ReconcileDeletedTenants(ctx)
	// A pass that failed only for some ids answers 200 with its report, whose
	// failed list names them; only a guard failure is an error status.
	if rerr := tenant.ReconcileResponseError(report, err); rerr != nil {
		writeErrorJSON(w, mapHTTPError(rerr), rerr)
		return
	}
	writeJSON(w, report)
}

// handleTenantByID dispatches tenant operations that carry a tenant ID in
// the URL path.
//
// Routes:
//   - GET    /v1/tenants/{id}                                                -> get tenant
//   - DELETE /v1/tenants/{id}                                                -> delete tenant
//   - PUT    /v1/tenants/{id}                                                -> update tenant
//   - POST   /v1/tenants/{id}/quotas                                         -> set tenant quotas
//   - POST   /v1/tenants/{id}/barrier/init                                   -> initialize tenant barrier
//   - POST   /v1/tenants/{id}/barrier/unseal                                 -> unseal tenant barrier
//   - POST   /v1/tenants/{id}/barrier/seal                                   -> seal tenant barrier
//   - GET    /v1/tenants/{id}/barrier/status                                 -> tenant barrier status
//   - POST   /v1/tenants/{id}/barrier/officers                               -> enrol a share holder
//   - GET    /v1/tenants/{id}/barrier/officers                               -> list holders
//   - POST   /v1/tenants/{id}/barrier/officers/revoke                        -> revoke a holder (rekey round)
//   - POST   /v1/tenants/{id}/barrier/officers/{username}/claim/begin        -> begin envelope claim
//   - POST   /v1/tenants/{id}/barrier/officers/{username}/claim/complete     -> complete envelope claim
//   - POST   /v1/tenants/{id}/barrier/quorum/begin                           -> holder quorum challenge
//   - POST   /v1/tenants/{id}/barrier/quorum/submit                          -> holder quorum submission
//   - POST   /v1/tenants/{id}/barrier/rekey/start                            -> open a rekey round
func (s *Server) handleTenantByID(w http.ResponseWriter, r *http.Request) {
	if s.tenantSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "tenant"})
		return
	}

	// Strip "/v1/tenants/" prefix; remainder is "{id}" or "{id}/sub/path".
	suffix := strings.TrimPrefix(r.URL.Path, "/v1/tenants/")
	if suffix == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "id"})
		return
	}

	// GET sub-paths: /barrier/status, /barrier/officers.
	if r.Method == http.MethodGet {
		if id, found := strings.CutSuffix(suffix, "/barrier/status"); found && id != "" {
			s.handleTenantBarrierStatus(w, r, id)
			return
		}
		if id, found := strings.CutSuffix(suffix, "/barrier/officers"); found && id != "" {
			s.handleTenantBarrierOfficerList(w, r, id)
			return
		}
	}

	// POST sub-paths.
	if r.Method == http.MethodPost {
		if id, found := strings.CutSuffix(suffix, "/quotas"); found && id != "" {
			s.handleSetTenantQuotas(w, r, id)
			return
		}
		if id, found := strings.CutSuffix(suffix, "/barrier/init"); found && id != "" {
			s.handleTenantBarrierInit(w, r, id)
			return
		}
		if id, found := strings.CutSuffix(suffix, "/barrier/unseal"); found && id != "" {
			s.handleTenantBarrierUnseal(w, r, id)
			return
		}
		if id, found := strings.CutSuffix(suffix, "/barrier/seal"); found && id != "" {
			s.handleTenantBarrierSeal(w, r, id)
			return
		}
		if id, found := strings.CutSuffix(suffix, "/barrier/quorum/begin"); found && id != "" {
			s.handleTenantBarrierQuorumBegin(w, r, id)
			return
		}
		if id, found := strings.CutSuffix(suffix, "/barrier/quorum/submit"); found && id != "" {
			s.handleTenantBarrierQuorumSubmit(w, r, id)
			return
		}
		if id, found := strings.CutSuffix(suffix, "/barrier/rekey/start"); found && id != "" {
			s.handleTenantBarrierRekeyStart(w, r, id)
			return
		}
		if id, found := strings.CutSuffix(suffix, "/barrier/officers/revoke"); found && id != "" {
			s.handleTenantBarrierOfficerRevoke(w, r, id)
			return
		}
		if id, found := strings.CutSuffix(suffix, "/barrier/officers"); found && id != "" {
			s.handleTenantBarrierOfficerRegister(w, r, id)
			return
		}
		// /v1/tenants/{id}/barrier/officers/{username}/claim/begin|complete
		// Pattern: suffix = "{id}/barrier/officers/{username}/claim/{action}"
		if idx := strings.Index(suffix, "/barrier/officers/"); idx > 0 {
			tenantID := suffix[:idx]
			officerSuffix := suffix[idx+len("/barrier/officers/"):]
			// officerSuffix = "{username}/claim/begin" or "{username}/claim/complete"
			parts := strings.SplitN(officerSuffix, "/", 3)
			if len(parts) == 3 && parts[1] == "claim" && tenantID != "" {
				username := parts[0]
				action := parts[2]
				switch action {
				case "begin":
					s.handleTenantBarrierShareClaimBegin(w, r, tenantID, username)
					return
				case "complete":
					s.handleTenantBarrierShareClaimComplete(w, r, tenantID, username)
					return
				}
			}
		}
		w.Header().Set("Allow", "GET, DELETE, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// PATCH sub-paths.
	if r.Method == http.MethodPatch {
		if id, found := strings.CutSuffix(suffix, "/entitlements"); found && id != "" {
			s.handleSetTenantEntitlements(w, r, id)
			return
		}
		w.Header().Set("Allow", "GET, DELETE, PUT, POST, PATCH")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// GET /v1/tenants/{id}, DELETE /v1/tenants/{id}, PUT /v1/tenants/{id}
	id := suffix
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

// authorizeTenantTarget re-checks perm against the tenant the URL names
// (authmw.AuthorizeTenantTarget); the route middleware checked it against the
// caller's own tenant only, which would let a tenant-local Admin act on any
// tenant. It writes the refusal and reports false when the caller may not act
// on id.
func (s *Server) authorizeTenantTarget(ctx context.Context, w http.ResponseWriter, id string, perm permission.Permission) bool {
	return s.authorizeTenantRequest(w, id, func(mw *authmw.AuthMiddleware) error {
		return mw.AuthorizeTenantTarget(ctx, id, perm)
	})
}

// authorizeTenantUpdate authorizes patch against the tenant the URL names
// (authmw.AuthorizeTenantUpdate): tenants:update for a rename, plus
// tenants:set_isolation when the patch changes the isolation tier.
func (s *Server) authorizeTenantUpdate(ctx context.Context, w http.ResponseWriter, id string, patch tenant.TenantPatch) bool {
	return s.authorizeTenantRequest(w, id, func(mw *authmw.AuthMiddleware) error {
		return mw.AuthorizeTenantUpdate(ctx, id, patch)
	})
}

// authorizeTenantRequest refuses 400 an id that cannot name a tenant
// (tenant.ValidateTenantIDShape), then runs decide against the auth
// middleware. It writes the refusal and reports false when either fails. With
// no auth middleware (unauthenticated deployment) the authorization step
// admits.
func (s *Server) authorizeTenantRequest(w http.ResponseWriter, id string, decide func(*authmw.AuthMiddleware) error) bool {
	if err := tenant.ValidateTenantIDShape(id); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return false
	}
	if s.authMW == nil {
		return true
	}
	if err := decide(s.authMW); err != nil {
		writeAuthzErrorJSON(w, err)
		return false
	}
	return true
}

// handleGetTenant handles GET /v1/tenants/{id} -- retrieves a tenant by ID.
func (s *Server) handleGetTenant(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	if !s.authorizeTenantTarget(ctx, w, id, permission.TenantsRead) {
		return
	}
	info, err := s.tenantSvc.GetTenant(ctx, id)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, tenantToResponse(info))
}

// handleDeleteTenant handles DELETE /v1/tenants/{id} -- removes a tenant.
func (s *Server) handleDeleteTenant(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	if !s.authorizeTenantTarget(ctx, w, id, permission.TenantsDelete) {
		return
	}
	if err := s.tenantSvc.DeleteTenant(ctx, id); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
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
	patch := tenant.TenantPatch{
		Name:          req.Name,
		IsolationType: req.IsolationType,
	}
	if !s.authorizeTenantUpdate(ctx, w, id, patch) {
		return
	}

	// The tenant must hold no data before its isolation type changes; the
	// tenant service enforces that for every transport (ErrTenantHasData,
	// tenant.IsolationChangeWiring).
	info, err := s.tenantSvc.UpdateTenant(ctx, id, patch)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
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
		MaxDatabases:          req.MaxDatabases,
		MaxRaftGroups:         req.MaxRaftGroups,
		MaxReplicasPerGroup:   req.MaxReplicasPerGroup,
		MaxTotalReplicas:      req.MaxTotalReplicas,
		MaxCollections:        req.MaxCollections,
		MaxKeys:               req.MaxKeys,
		MaxStorageBytes:       req.MaxStorageBytes,
		MaxDatabaseSizeBytes:  req.MaxDatabaseSizeBytes,
		MaxUsers:              req.MaxUsers,
		MaxRoles:              req.MaxRoles,
		MaxCertificates:       req.MaxCertificates,
		MaxBackups:            req.MaxBackups,
		MaxVectorDims:         req.MaxVectorDims,
		MaxTSMetrics:          req.MaxTSMetrics,
		SystemBarrierQuota:    req.SystemBarrierQuota,
		MaxDedicatedWALGroups: req.MaxDedicatedWALGroups,
	}

	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	if !s.authorizeTenantTarget(ctx, w, id, permission.TenantsSetQuotas) {
		return
	}
	info, err := s.tenantSvc.UpdateQuotas(ctx, id, q)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, tenantToResponse(info))
}

// handleSetTenantEntitlements handles PATCH /v1/tenants/{id}/entitlements --
// updates the feature entitlements for an existing tenant.
func (s *Server) handleSetTenantEntitlements(w http.ResponseWriter, r *http.Request, id string) {
	var req setTenantEntitlementsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	e := &quota.Entitlements{
		AllowedIsolationModes:     req.AllowedIsolationModes,
		AllowDedicatedRaft:        req.AllowDedicatedRaft,
		AllowVolumeStorage:        req.AllowVolumeStorage,
		AllowSystemManagedBarrier: req.AllowSystemManagedBarrier,
		AllowDedicatedWAL:         req.AllowDedicatedWAL,
		AllowedBarrierModes:       req.AllowedBarrierModes,
		AllowedVolumePools:        req.AllowedVolumePools,
	}

	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	if !s.authorizeTenantTarget(ctx, w, id, permission.TenantsSetEntitlements) {
		return
	}
	info, err := s.tenantSvc.UpdateEntitlements(ctx, id, e)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
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

// authorizeTenantBarrierInit authorizes a tenant barrier initialization via
// the shared trust-on-first-init rule (middleware.AuthorizeTenantBarrierInit
// — the single cross-transport home of the bootstrap-window semantics). When
// authMW is nil the gate is a no-op (unauthenticated deployment).
func (s *Server) authorizeTenantBarrierInit(ctx context.Context, tenantID string) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenantBarrierInit(ctx, tenantID, s.tenantSvc)
}

// authorizeTenantBarrierSealedOp authorizes the tenant barrier operations
// that must remain reachable while the tenant's IAM itself is unreachable
// (status/unseal) via the shared sealed-window rule
// (middleware.AuthorizeTenantBarrierSealedOp — the single cross-transport
// home of the fallback semantics). When authMW is nil the gate is a no-op.
func (s *Server) authorizeTenantBarrierSealedOp(ctx context.Context, tenantID string, perm permission.Permission) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeTenantBarrierSealedOp(ctx, tenantID, perm)
}

// writeAuthzErrorJSON answers a failed tenant-barrier authorization gate with
// the status the error ACTUALLY MEANS, rather than assuming every refusal is a
// permission problem.
//
// Eleven call sites on this surface each wrote http.StatusForbidden directly,
// which was survivable only while nothing but a denial ever came back out of
// the gate. go-qrdb#335 ended that: the authorization middleware now refuses a
// decision whose audit record it could not write, and *middleware.AuditError
// escapes these gates RELIABLY -- the bootstrap-window fallback's own audit
// write fails for the same reason the first one did. Answering 403 would tell
// an operator recovering a node with a full audit disk that they are not
// permitted to unseal it, and 403 is terminal: no client retries it.
//
// The fallback is StatusForbidden, not the REST middleware's StatusUnauthorized,
// and that is deliberate. These gates run AFTER authentication, so an
// unrecognised failure here means "the gate refused you", never "who are
// you" -- and it keeps the answer byte-identical to what these sites returned
// before for anything the middleware does not classify.
//
// Being a helper rather than eleven expressions is the point: eleven call
// sites each spelling out a status is how the surface drifted in the first
// place, and the next handler added here is correct without its author having
// to know any of this.
func writeAuthzErrorJSON(w http.ResponseWriter, err error) {
	writeErrorJSON(w, authmw.HTTPStatus(err, http.StatusForbidden), err)
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
	// The decoded credential is this handler's to own and to destroy. The
	// tenant service borrows it for the call and retains nothing, so a
	// deferred wipe covers every exit path including the error ones.
	defer secure.Wipe(req.Passphrase)

	ctx, cancel := context.WithTimeout(r.Context(), tenantRequestTimeout)
	defer cancel()
	if err := s.authorizeTenantBarrierInit(ctx, id); err != nil {
		writeAuthzErrorJSON(w, err)
		return
	}
	var roster *tenant.TenantBarrierShamirRoster
	if len(req.Officers) > 0 {
		roster = &tenant.TenantBarrierShamirRoster{
			Officers: holderwire.TenantBarrierRosterFrom(req.Officers),
			Policy:   seal.FactorPolicy(req.Policy),
		}
	}
	result, err := s.tenantSvc.TenantBarrierInitWithStrategy(ctx, id, req.Passphrase, req.Threshold, req.Shares, seal.StrategyID(req.Strategy), roster)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	// INV-1: share bytes are never included in initialization responses.
	// Security Officers retrieve their shares via the per-tenant claim
	// handshake. Return share_count so callers know enrollment succeeded.
	if result != nil && result.ShareCount > 0 {
		writeJSON(w, map[string]interface{}{
			"tenant_id":    result.TenantID,
			"strategy":     result.Strategy,
			"threshold":    result.Threshold,
			"total_shares": result.TotalShares,
			"share_count":  result.ShareCount,
			"holders":      holderwire.TenantBarrierHoldersTo(result.Holders),
		})
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
	if err := s.authorizeTenantBarrierSealedOp(ctx, id, permission.TenantBarrierUnseal); err != nil {
		writeAuthzErrorJSON(w, err)
		return
	}
	if err := s.tenantSvc.TenantBarrierUnseal(ctx, id, req.Key); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
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
		writeAuthzErrorJSON(w, err)
		return
	}
	if err := s.tenantSvc.TenantBarrierSeal(ctx, id); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
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
	if err := s.authorizeTenantBarrierSealedOp(ctx, id, permission.TenantBarrierStatus); err != nil {
		writeAuthzErrorJSON(w, err)
		return
	}
	status, err := s.tenantSvc.TenantBarrierStatus(ctx, id)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
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
	resp := &tenantResponse{
		ID:            info.ID,
		Name:          info.Name,
		IsolationType: info.EffectiveIsolationType(),
		VolumePool:    info.VolumePool,
		Quotas:        info.Quotas,
		Entitlements:  info.Entitlements,
		CreatedAt:     info.CreatedAt.Format(time.RFC3339),
		UpdatedAt:     info.UpdatedAt.Format(time.RFC3339),
	}
	if refusal := tenant.RefusalOf(info.Refusal); refusal != nil {
		resp.Refusal = &tenantRefusal{Code: refusal.Code, Message: refusal.Message}
	}
	return resp
}

// ---------------------------------------------------------------------------
// Per-tenant barrier Security Officer handlers (§7.3 SO design, tenant surface)
// ---------------------------------------------------------------------------
