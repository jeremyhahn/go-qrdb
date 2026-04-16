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
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
)

// ---------------------------------------------------------------------------
// Lease request/response types
// ---------------------------------------------------------------------------

// leaseGrantRequest is the JSON payload for POST /lease/grant.
type leaseGrantRequest struct {
	TTL int64 `json:"ttl"`
}

// leaseIDRequest is the JSON payload for POST /lease/revoke and
// POST /lease/keepalive.
type leaseIDRequest struct {
	LeaseID int64 `json:"lease_id"`
}

// leaseResponse is the JSON representation of a lease.
type leaseResponse struct {
	ID           int64    `json:"id"`
	TTL          int64    `json:"ttl"`
	GrantedTTL   int64    `json:"granted_ttl"`
	RemainingTTL int64    `json:"remaining_ttl"`
	Keys         []string `json:"keys"`
}

// leasesResponse is the JSON payload returned by GET /lease/list.
type leasesResponse struct {
	Leases []*leaseResponse `json:"leases"`
}

// ---------------------------------------------------------------------------
// Lease handlers
// ---------------------------------------------------------------------------

// handleLeaseGrant handles POST /lease/grant -- creates a new lease with
// the specified TTL in seconds.
func (s *Server) handleLeaseGrant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	leaseSvc := s.leaseSvcFor(w, ctx)
	if leaseSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "lease"})
		}
		return
	}

	var req leaseGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	l, err := leaseSvc.Grant(ctx, req.TTL)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, leaseToResponse(l))
}

// handleLeaseRevoke handles POST /lease/revoke -- immediately expires a
// lease and deletes all attached keys.
func (s *Server) handleLeaseRevoke(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	leaseSvc := s.leaseSvcFor(w, ctx)
	if leaseSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "lease"})
		}
		return
	}

	var req leaseIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if err := leaseSvc.Revoke(ctx, req.LeaseID); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}

// handleLeaseKeepAlive handles POST /lease/keepalive -- resets the TTL on
// an existing lease, preventing it from expiring.
func (s *Server) handleLeaseKeepAlive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	leaseSvc := s.leaseSvcFor(w, ctx)
	if leaseSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "lease"})
		}
		return
	}

	var req leaseIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	l, err := leaseSvc.KeepAlive(ctx, req.LeaseID)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, leaseToResponse(l))
}

// handleLeaseTimeToLive handles GET /lease/ttl -- returns the current state
// of a lease including its remaining TTL and attached keys.
// Query parameter: lease_id (required).
func (s *Server) handleLeaseTimeToLive(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	leaseSvc := s.leaseSvcFor(w, ctx)
	if leaseSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "lease"})
		}
		return
	}

	leaseIDStr := r.URL.Query().Get("lease_id")
	if leaseIDStr == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "lease_id"})
		return
	}

	leaseID, err := strconv.ParseInt(leaseIDStr, 10, 64)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "lease_id", Err: err})
		return
	}

	l, ttlErr := leaseSvc.TimeToLive(ctx, leaseID)
	if ttlErr != nil {
		writeErrorJSON(w, mapHTTPError(ttlErr), ttlErr)
		return
	}

	writeJSON(w, leaseToResponse(l))
}

// handleLeaseList handles GET /lease/list -- returns all active leases.
func (s *Server) handleLeaseList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	leaseSvc := s.leaseSvcFor(w, ctx)
	if leaseSvc == nil {
		if s.serviceRegistry == nil {
			writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "lease"})
		}
		return
	}

	leases, err := leaseSvc.Leases(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	resp := make([]*leaseResponse, len(leases))
	for i, l := range leases {
		resp[i] = leaseToResponse(l)
	}

	writeJSON(w, leasesResponse{Leases: resp})
}

// ---------------------------------------------------------------------------
// Lease conversion helpers
// ---------------------------------------------------------------------------

// leaseToResponse converts a lease.Lease to its JSON response representation.
func leaseToResponse(l *lease.Lease) *leaseResponse {
	if l == nil {
		return nil
	}

	keys := make([]string, len(l.Keys))
	for i, k := range l.Keys {
		keys[i] = string(k)
	}

	return &leaseResponse{
		ID:           l.ID,
		TTL:          l.TTL,
		GrantedTTL:   l.GrantedTTL,
		RemainingTTL: l.RemainingTTL,
		Keys:         keys,
	}
}
