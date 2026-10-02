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

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// nodeCompactionRequest is the request body of PUT /admin/node-compaction:
// the platform's node-keyspace compaction policy. An absent compaction turns
// the policy off.
type nodeCompactionRequest struct {
	Compaction *compactionConfigJSON `json:"compaction"`
}

// nodeCompactionResponse is the response body of GET and PUT
// /admin/node-compaction: the policy in force, absent when there is none.
type nodeCompactionResponse struct {
	Compaction *transport.DatabaseCompactionConfig `json:"compaction,omitempty"`
}

// handleGetNodeCompaction handles GET /admin/node-compaction. The route
// requires admin:node_compaction, which is ScopeCross: system tenant only.
func (s *Server) handleGetNodeCompaction(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	policy, err := s.adminSvc.NodeCompaction(ctx)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, nodeCompactionResponse{Compaction: dbwire.Compaction(policy)})
}

// handleSetNodeCompaction handles PUT /admin/node-compaction: it sets the
// platform's node-keyspace compaction policy and answers with the policy
// now in force. The route requires admin:node_compaction (ScopeCross).
func (s *Server) handleSetNodeCompaction(w http.ResponseWriter, r *http.Request) {
	var req nodeCompactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	policy := compactionConfigFromJSON(req.Compaction)
	if err := s.adminSvc.SetNodeCompaction(ctx, policy); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, nodeCompactionResponse{Compaction: dbwire.Compaction(policy.Normalized())})
}
