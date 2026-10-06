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

// retireJoinIdentityRequestTimeout bounds the retire-identity endpoint: a
// single replicated-store write, never a shard snapshot or a network
// fetch, so it shares the tighter control-plane budget rather than
// backupRequestTimeout.
const retireJoinIdentityRequestTimeout = 10 * time.Second

// retireJoinIdentityRequest is the JSON payload for POST
// /v1/cluster/join-identities/retire. Reason is an optional, free-text
// operator note carried for audit only.
type retireJoinIdentityRequest struct {
	NodeID string `json:"node_id"`
	Reason string `json:"reason,omitempty"`
}

// handleRetireJoinIdentity handles POST /v1/cluster/join-identities/retire:
// permanently refuses any future admission for the admin-named node
// identity. permission.ClusterJoinIdentityRetire (ScopeCross,
// platform-only) gates this route -- see registerRoutes -- so only an
// identity of the system tenant reaches this handler at all.
func (s *Server) handleRetireJoinIdentity(w http.ResponseWriter, r *http.Request) {
	retirer, ok := s.adminSvc.(service.JoinIdentityRetirer)
	if !ok {
		writeErrorJSON(w, http.StatusNotImplemented, &service.ClusterJoinTokenNotConfiguredError{})
		return
	}

	var req retireJoinIdentityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), retireJoinIdentityRequestTimeout)
	defer cancel()
	if err := retirer.RetireJoinIdentity(ctx, req.NodeID, req.Reason); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
}
