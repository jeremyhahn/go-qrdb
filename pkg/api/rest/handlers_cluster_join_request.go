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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// joinRequestTimeout bounds the approval-queue endpoints: replicated-store
// reads and writes, never a shard snapshot or a network fetch, so they
// share the same tighter control-plane budget mintJoinTokenRequestTimeout
// does.
const joinRequestTimeout = 10 * time.Second

// pendingJoinRequestResponse is the JSON shape of one queued CSR -- the
// wire counterpart of service.PendingJoinRequest.
type pendingJoinRequestResponse struct {
	ID          string    `json:"id"`
	NodeURI     string    `json:"node_uri,omitempty"`
	ReplicaID   uint64    `json:"replica_id,omitempty"`
	Features    []string  `json:"features,omitempty"`
	SubmittedAt time.Time `json:"submitted_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// listPendingJoinRequestsResponse is the JSON payload for GET
// /v1/cluster/join-requests.
type listPendingJoinRequestsResponse struct {
	Requests []pendingJoinRequestResponse `json:"requests"`
}

// approveJoinRequestRequest is the JSON payload for POST
// /v1/cluster/join-requests/approve.
type approveJoinRequestRequest struct {
	ID     string `json:"id"`
	NodeID string `json:"node_id"`
}

// rejectJoinRequestRequest is the JSON payload for POST
// /v1/cluster/join-requests/reject.
type rejectJoinRequestRequest struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// handleListPendingJoinRequests handles GET /v1/cluster/join-requests:
// lists tokenless member-side CSR join requests awaiting an operator's
// decision. permission.ClusterJoinToken (ScopeCross, platform-only) gates
// this route -- see registerRoutes -- the same whole-cluster admission
// decision minting a join token already requires.
func (s *Server) handleListPendingJoinRequests(w http.ResponseWriter, r *http.Request) {
	reviewer, ok := s.adminSvc.(service.JoinRequestReviewer)
	if !ok {
		writeErrorJSON(w, http.StatusNotImplemented, &service.ClusterJoinTokenNotConfiguredError{})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), joinRequestTimeout)
	defer cancel()
	pending, err := reviewer.ListPendingJoinRequests(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	resp := listPendingJoinRequestsResponse{Requests: make([]pendingJoinRequestResponse, 0, len(pending))}
	for _, p := range pending {
		resp.Requests = append(resp.Requests, pendingJoinRequestResponse{
			ID:          p.ID,
			NodeURI:     p.NodeURI,
			ReplicaID:   p.ReplicaID,
			Features:    p.Features,
			SubmittedAt: p.SubmittedAt,
			ExpiresAt:   p.ExpiresAt,
		})
	}
	writeJSON(w, resp)
}

// handleApproveJoinRequest handles POST /v1/cluster/join-requests/approve:
// binds a queued CSR to an operator-chosen node identity. The leaf is
// signed the next time the joiner's own retry reaches the join listener,
// not by this call -- see pkg/clusterenroll.ApproveJoinRequest.
func (s *Server) handleApproveJoinRequest(w http.ResponseWriter, r *http.Request) {
	reviewer, ok := s.adminSvc.(service.JoinRequestReviewer)
	if !ok {
		writeErrorJSON(w, http.StatusNotImplemented, &service.ClusterJoinTokenNotConfiguredError{})
		return
	}

	var req approveJoinRequestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), joinRequestTimeout)
	defer cancel()
	if err := reviewer.ApproveJoinRequest(ctx, req.ID, req.NodeID); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, okResponse{Status: "ok"})
}

// handleRejectJoinRequest handles POST /v1/cluster/join-requests/reject:
// records a terminal refusal of a queued CSR.
func (s *Server) handleRejectJoinRequest(w http.ResponseWriter, r *http.Request) {
	reviewer, ok := s.adminSvc.(service.JoinRequestReviewer)
	if !ok {
		writeErrorJSON(w, http.StatusNotImplemented, &service.ClusterJoinTokenNotConfiguredError{})
		return
	}

	var req rejectJoinRequestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), joinRequestTimeout)
	defer cancel()
	if err := reviewer.RejectJoinRequest(ctx, req.ID, req.Reason); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, okResponse{Status: "ok"})
}
