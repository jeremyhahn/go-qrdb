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
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// unsealDatabaseRequest is the JSON payload for POST /v1/databases/{name}/unseal.
type unsealDatabaseRequest struct {
	Secret string `json:"secret"`
}

// databaseBarrierStatusResponse is the JSON representation of a database
// barrier's status.
type databaseBarrierStatusResponse struct {
	Sealed         bool                `json:"sealed"`
	Strategy       string              `json:"strategy"`
	HardwareBacked bool                `json:"hardware_backed"`
	InitializedAt  string              `json:"initialized_at,omitempty"`
	ClusterSeal    *clusterseal.Report `json:"cluster_seal"`
}

// handleSealDatabase handles POST /v1/databases/{name}/seal -- seals the
// database's encryption barrier. Returns 204 No Content on success.
func (s *Server) handleSealDatabase(w http.ResponseWriter, r *http.Request, name string) {
	if err := s.databaseSvc.SealDatabase(r.Context(), name); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUnsealDatabase handles POST /v1/databases/{name}/unseal -- unseals the
// database's encryption barrier with the supplied secret. Returns 204 No
// Content on success.
func (s *Server) handleUnsealDatabase(w http.ResponseWriter, r *http.Request, name string) {
	var req unsealDatabaseRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
			return
		}
	}
	creds, err := seal.CredentialsFor(r.Context(), []byte(req.Secret))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	defer seal.DestroyCredentials(creds)
	if err := s.databaseSvc.UnsealDatabase(r.Context(), name, creds); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDatabaseBarrierStatus handles GET /v1/databases/{name}/barrier/status --
// reports the sealed/initialized state and active strategy metadata.
func (s *Server) handleDatabaseBarrierStatus(w http.ResponseWriter, r *http.Request, name string) {
	st, err := s.databaseSvc.DatabaseBarrierStatus(r.Context(), name)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	resp := databaseBarrierStatusResponse{
		Sealed:         st.Sealed,
		Strategy:       string(st.Strategy),
		HardwareBacked: st.HardwareBacked,
		ClusterSeal:    clusterSealOrInactive(st.ClusterSeal),
	}
	if !st.InitializedAt.IsZero() {
		resp.InitializedAt = st.InitializedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	writeJSON(w, resp)
}
