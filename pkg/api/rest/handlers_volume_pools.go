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

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// volumePoolRequestTimeout bounds every volume-pool management endpoint,
// mirroring tenantRequestTimeout.
const volumePoolRequestTimeout = 5 * time.Second

// ---------------------------------------------------------------------------
// Volume pool wire shapes (docs/volume-pools/api.md)
// ---------------------------------------------------------------------------

// volumePoolNodeCounts is the JSON form of volume.NodeHealthCounts.
type volumePoolNodeCounts struct {
	Ready       int `json:"ready"`
	Locked      int `json:"locked"`
	Unavailable int `json:"unavailable"`
	Unmapped    int `json:"unmapped"`
}

// volumePoolResponse is the JSON form of volume.PoolView: a pool plus its
// aggregate usage, tenant count and per-node health tally.
type volumePoolResponse struct {
	Name          string               `json:"name"`
	Provider      string               `json:"provider"`
	Description   string               `json:"description,omitempty"`
	CapacityBytes int64                `json:"capacityBytes"`
	ReservedBytes int64                `json:"reservedBytes"`
	UsedBytes     int64                `json:"usedBytes"`
	TenantCount   int                  `json:"tenantCount"`
	Nodes         volumePoolNodeCounts `json:"nodes"`
	Overcommit    float64              `json:"overcommit"`
	CreatedAt     string               `json:"createdAt,omitempty"`
	UpdatedAt     string               `json:"updatedAt,omitempty"`
}

// volumePoolNodeReportResponse is the JSON form of volume.NodeReportRow.
type volumePoolNodeReportResponse struct {
	NodeID     string `json:"nodeId"`
	State      string `json:"state"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
	FreeBytes  int64  `json:"freeBytes,omitempty"`
	Detail     string `json:"detail,omitempty"`
	ReportedAt string `json:"reportedAt,omitempty"`
}

// volumePoolDetailResponse is the JSON form of volume.PoolDetail.
type volumePoolDetailResponse struct {
	volumePoolResponse
	NodeReports []volumePoolNodeReportResponse `json:"nodeReports"`
}

// listVolumePoolsResponse is the JSON payload for GET /v1/volume-pools/list.
type listVolumePoolsResponse struct {
	Pools []volumePoolResponse `json:"pools"`
}

// createVolumePoolRequest is the JSON payload for POST /v1/volume-pools.
type createVolumePoolRequest struct {
	Name          string  `json:"name"`
	Provider      string  `json:"provider,omitempty"`
	Description   string  `json:"description,omitempty"`
	CapacityBytes int64   `json:"capacityBytes,omitempty"`
	Overcommit    float64 `json:"overcommit,omitempty"`
}

// updateVolumePoolRequest is the JSON payload for PUT
// /v1/volume-pools/{pool}. Absent fields are unchanged; provider is
// immutable and has no field here.
type updateVolumePoolRequest struct {
	Description   *string  `json:"description,omitempty"`
	CapacityBytes *int64   `json:"capacityBytes,omitempty"`
	Overcommit    *float64 `json:"overcommit,omitempty"`
}

// volumePoolTenantResponse is the JSON form of volume.Assignment, as one
// row of a pool's tenant listing.
type volumePoolTenantResponse struct {
	TenantID      string `json:"tenantId"`
	ReservedBytes int64  `json:"reservedBytes"`
}

// listVolumePoolTenantsResponse is one page of a pool's assigned tenants.
type listVolumePoolTenantsResponse struct {
	Tenants       []volumePoolTenantResponse `json:"tenants"`
	NextPageToken string                     `json:"next_page_token,omitempty"`
}

func volumePoolToResponse(p *volume.PoolView) volumePoolResponse {
	resp := volumePoolResponse{
		Name:          p.Name,
		Provider:      p.Provider,
		Description:   p.Description,
		CapacityBytes: p.CapacityBytes,
		ReservedBytes: p.ReservedBytes,
		UsedBytes:     p.UsedBytes,
		TenantCount:   p.TenantCount,
		Nodes: volumePoolNodeCounts{
			Ready:       p.NodeCounts.Ready,
			Locked:      p.NodeCounts.Locked,
			Unavailable: p.NodeCounts.Unavailable,
			Unmapped:    p.NodeCounts.Unmapped,
		},
		Overcommit: p.Overcommit,
	}
	if !p.CreatedAt.IsZero() {
		resp.CreatedAt = p.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !p.UpdatedAt.IsZero() {
		resp.UpdatedAt = p.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return resp
}

// volumePoolFromCreated adapts CreatePool's bare *volume.Pool (no aggregate
// view yet -- a freshly created pool has no tenants, usage or node reports)
// into the same response shape as the list/get rows.
func volumePoolFromCreated(p *volume.Pool) volumePoolResponse {
	return volumePoolToResponse(&volume.PoolView{Pool: *p})
}

// ---------------------------------------------------------------------------
// Volume pool handlers
// ---------------------------------------------------------------------------

// handleListVolumePools handles GET /v1/volume-pools/list.
func (s *Server) handleListVolumePools(w http.ResponseWriter, r *http.Request) {
	if s.volumeSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "volume"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), volumePoolRequestTimeout)
	defer cancel()
	pools, err := s.volumeSvc.ListPools(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	resp := listVolumePoolsResponse{Pools: make([]volumePoolResponse, len(pools))}
	for i := range pools {
		resp.Pools[i] = volumePoolToResponse(&pools[i])
	}
	writeJSON(w, resp)
}

// handleCreateVolumePool handles POST /v1/volume-pools.
func (s *Server) handleCreateVolumePool(w http.ResponseWriter, r *http.Request) {
	if s.volumeSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "volume"})
		return
	}
	var req createVolumePoolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), volumePoolRequestTimeout)
	defer cancel()
	pool, err := s.volumeSvc.CreatePool(ctx, volume.CreatePoolRequest{
		Name:          req.Name,
		Provider:      req.Provider,
		Description:   req.Description,
		CapacityBytes: req.CapacityBytes,
		Overcommit:    req.Overcommit,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(volumePoolFromCreated(pool))
}

// handleVolumePoolByName dispatches volume-pool operations that carry the
// pool name in the URL path.
//
// Routes:
//   - GET    /v1/volume-pools/{pool}          -> inspect a pool
//   - PUT    /v1/volume-pools/{pool}          -> update a pool
//   - DELETE /v1/volume-pools/{pool}          -> delete a pool
//   - GET    /v1/volume-pools/{pool}/tenants  -> list a pool's tenants
func (s *Server) handleVolumePoolByName(w http.ResponseWriter, r *http.Request) {
	if s.volumeSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "volume"})
		return
	}
	name := r.PathValue("pool")
	if name == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "pool"})
		return
	}

	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/tenants") {
		s.handleListVolumePoolTenants(w, r, name)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGetVolumePool(w, r, name)
	case http.MethodPut:
		s.handleUpdateVolumePool(w, r, name)
	case http.MethodDelete:
		s.handleDeleteVolumePool(w, r, name)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGetVolumePool(w http.ResponseWriter, r *http.Request, name string) {
	ctx, cancel := context.WithTimeout(r.Context(), volumePoolRequestTimeout)
	defer cancel()
	detail, err := s.volumeSvc.GetPool(ctx, name)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	resp := volumePoolDetailResponse{
		volumePoolResponse: volumePoolToResponse(&detail.PoolView),
		NodeReports:        make([]volumePoolNodeReportResponse, len(detail.NodeReports)),
	}
	for i, nr := range detail.NodeReports {
		row := volumePoolNodeReportResponse{
			NodeID: nr.NodeID,
			State:  nr.State,
		}
		if nr.State != volume.NodeStateNoReport {
			row.SizeBytes = nr.SizeBytes
			row.FreeBytes = nr.FreeBytes
			row.Detail = nr.Detail
		}
		if !nr.ReportedAt.IsZero() {
			row.ReportedAt = nr.ReportedAt.UTC().Format(time.RFC3339)
		}
		resp.NodeReports[i] = row
	}
	writeJSON(w, resp)
}

func (s *Server) handleUpdateVolumePool(w http.ResponseWriter, r *http.Request, name string) {
	var req updateVolumePoolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), volumePoolRequestTimeout)
	defer cancel()
	pool, err := s.volumeSvc.UpdatePool(ctx, name, volume.UpdatePoolRequest{
		Description:   req.Description,
		CapacityBytes: req.CapacityBytes,
		Overcommit:    req.Overcommit,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, volumePoolFromCreated(pool))
}

func (s *Server) handleDeleteVolumePool(w http.ResponseWriter, r *http.Request, name string) {
	ctx, cancel := context.WithTimeout(r.Context(), volumePoolRequestTimeout)
	defer cancel()
	if err := s.volumeSvc.DeletePool(ctx, name); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListVolumePoolTenants(w http.ResponseWriter, r *http.Request, name string) {
	pageSize := 0
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &volume.InvalidPageRequestError{Field: "page_size", Reason: "not an integer"})
			return
		}
		pageSize = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), volumePoolRequestTimeout)
	defer cancel()
	page, err := s.volumeSvc.ListAssignments(ctx, name, r.URL.Query().Get("page_token"), pageSize)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	resp := listVolumePoolTenantsResponse{
		Tenants:       make([]volumePoolTenantResponse, len(page.Assignments)),
		NextPageToken: page.NextPageToken,
	}
	for i, a := range page.Assignments {
		resp.Tenants[i] = volumePoolTenantResponse{TenantID: a.TenantID, ReservedBytes: a.ReservedBytes}
	}
	writeJSON(w, resp)
}
