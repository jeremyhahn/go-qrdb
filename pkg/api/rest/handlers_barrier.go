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
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Barrier request types (REST-local, unexported)
// ---------------------------------------------------------------------------

// barrierInitializeRequest is the JSON payload for POST /v1/barrier/initialize.
type barrierInitializeRequest struct {
	Secret string `json:"secret"`
}

// barrierUnsealRequest is the JSON payload for POST /v1/barrier/unseal.
type barrierUnsealRequest struct {
	Secret string `json:"secret"`
}

// barrierShamirInitializeRequest is the JSON payload for
// POST /v1/barrier/shamir/initialize.
type barrierShamirInitializeRequest struct {
	Secret      string `json:"secret"`
	Threshold   int    `json:"threshold"`
	TotalShares int    `json:"total_shares"`
}

// barrierShamirUnsealShareRequest is the JSON payload for
// POST /v1/barrier/shamir/unseal-share.
type barrierShamirUnsealShareRequest struct {
	Share string `json:"share"`
}

// barrierShamirUnsealSharesRequest is the JSON payload for
// POST /v1/barrier/shamir/unseal-shares.
type barrierShamirUnsealSharesRequest struct {
	Shares []string `json:"shares"`
}

// barrierRekeyRequest is the JSON payload for POST /v1/barrier/rekey.
type barrierRekeyRequest struct {
	Threshold int `json:"threshold"`
	Total     int `json:"total"`
}

// barrierRecoveryGenerateRequest is the JSON payload for
// POST /v1/barrier/recovery/generate.
type barrierRecoveryGenerateRequest struct {
	Threshold int `json:"threshold"`
	Total     int `json:"total"`
}

// barrierRecoveryRecoverRequest is the JSON payload for
// POST /v1/barrier/recovery/recover.
type barrierRecoveryRecoverRequest struct {
	Keys []string `json:"keys"`
}

// barrierRootTokenRequest is the JSON payload for
// POST /v1/barrier/root-token.
type barrierRootTokenRequest struct {
	Shares []string `json:"shares"`
}

// ---------------------------------------------------------------------------
// Barrier handlers
// ---------------------------------------------------------------------------

// handleBarrierInitialize handles POST /v1/barrier/initialize -- initializes
// the barrier with a secret credential.
func (s *Server) handleBarrierInitialize(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierInitializeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if err := s.barrierSvc.BarrierInitialize(r.Context(), req.Secret); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBarrierUnseal handles POST /v1/barrier/unseal -- unseals the barrier
// with a secret credential.
func (s *Server) handleBarrierUnseal(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierUnsealRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if err := s.barrierSvc.BarrierUnseal(r.Context(), req.Secret); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBarrierSeal handles POST /v1/barrier/seal -- transitions the barrier
// to the sealed state.
func (s *Server) handleBarrierSeal(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	if err := s.barrierSvc.BarrierSeal(r.Context()); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBarrierStatus handles GET /v1/barrier/status -- returns the current
// barrier status including sealed state, strategy, and initialization time.
func (s *Server) handleBarrierStatus(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	info, err := s.barrierSvc.BarrierStatus(r.Context())
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, barrierStatusToResponse(info))
}

// handleBarrierShamirInitialize handles POST /v1/barrier/shamir/initialize --
// initializes the barrier with Shamir secret sharing, splitting the root key
// into threshold-of-totalShares shares.
func (s *Server) handleBarrierShamirInitialize(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierShamirInitializeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	result, err := s.barrierSvc.BarrierInitializeShamir(r.Context(), req.Secret, req.Threshold, req.TotalShares)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, shamirInitToResponse(result))
}

// handleBarrierShamirUnsealShare handles POST /v1/barrier/shamir/unseal-share --
// submits a single Shamir share toward the quorum threshold.
func (s *Server) handleBarrierShamirUnsealShare(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierShamirUnsealShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	progress, err := s.barrierSvc.BarrierUnsealWithShare(r.Context(), req.Share)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, unsealShareProgressToResponse(progress))
}

// handleBarrierShamirUnsealShares handles POST /v1/barrier/shamir/unseal-shares --
// unseals the barrier using a complete set of Shamir shares in a single call.
func (s *Server) handleBarrierShamirUnsealShares(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierShamirUnsealSharesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if err := s.barrierSvc.BarrierUnsealWithShares(r.Context(), req.Shares); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBarrierShamirSharesByPath routes requests to /v1/barrier/shamir/shares
// with optional path parameters. When no index is present, it dispatches to
// list (GET) or delete-all (DELETE). When an index is present, it dispatches
// to delete-by-index (DELETE).
func (s *Server) handleBarrierShamirSharesByPath(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	remainder := strings.TrimPrefix(r.URL.Path, "/v1/barrier/shamir/shares/")
	if remainder == "" || remainder == r.URL.Path {
		// No index in path -- dispatch based on method.
		switch r.Method {
		case http.MethodGet:
			s.handleBarrierShamirListShares(w, r)
		case http.MethodDelete:
			s.handleBarrierShamirDeleteAllShares(w, r)
		default:
			w.Header().Set("Allow", "GET, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// Parse index from path.
	index, err := strconv.Atoi(remainder)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "index", Err: err})
		return
	}

	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.handleBarrierShamirDeleteShare(w, r, index)
}

// handleBarrierShamirListShares handles GET /v1/barrier/shamir/shares --
// returns information about the persisted Shamir shares including count,
// threshold, and total.
func (s *Server) handleBarrierShamirListShares(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	info, err := s.barrierSvc.BarrierShamirListShares(r.Context())
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, shamirSharesToResponse(info))
}

// handleBarrierShamirDeleteShare handles DELETE /v1/barrier/shamir/shares/{index} --
// removes a single Shamir share by its index.
func (s *Server) handleBarrierShamirDeleteShare(w http.ResponseWriter, r *http.Request, index int) {
	if err := s.barrierSvc.BarrierShamirDeleteShare(r.Context(), index); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBarrierShamirDeleteAllShares handles DELETE /v1/barrier/shamir/shares --
// removes all persisted Shamir shares.
func (s *Server) handleBarrierShamirDeleteAllShares(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	if err := s.barrierSvc.BarrierShamirDeleteAllShares(r.Context()); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBarrierShamirVerify handles POST /v1/barrier/shamir/verify --
// validates the consistency of persisted Shamir shares.
func (s *Server) handleBarrierShamirVerify(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	if err := s.barrierSvc.BarrierShamirVerify(r.Context()); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBarrierRekey handles POST /v1/barrier/rekey -- generates new Shamir
// shares for the existing root key with the specified threshold and total.
func (s *Server) handleBarrierRekey(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierRekeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	result, err := s.barrierSvc.BarrierRekey(r.Context(), req.Threshold, req.Total)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, shamirInitToResponse(result))
}

// handleBarrierRecoveryGenerate handles POST /v1/barrier/recovery/generate --
// generates a set of Shamir recovery shares from the root key.
func (s *Server) handleBarrierRecoveryGenerate(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierRecoveryGenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	result, err := s.barrierSvc.BarrierGenerateRecoveryKeys(r.Context(), req.Threshold, req.Total)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, recoveryKeysToResponse(result))
}

// handleBarrierRecoveryRecover handles POST /v1/barrier/recovery/recover --
// unseals the barrier using recovery shares.
func (s *Server) handleBarrierRecoveryRecover(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierRecoveryRecoverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	if err := s.barrierSvc.BarrierRecoverWithKeys(r.Context(), req.Keys); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBarrierRecoveryDelete handles DELETE /v1/barrier/recovery -- removes
// recovery key metadata from storage.
func (s *Server) handleBarrierRecoveryDelete(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	if err := s.barrierSvc.BarrierDeleteRecoveryKeys(r.Context()); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBarrierRecoveryStatus handles GET /v1/barrier/recovery/status --
// reports whether recovery key metadata exists in storage.
func (s *Server) handleBarrierRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	hasKeys, err := s.barrierSvc.BarrierHasRecoveryKeys(r.Context())
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"has_recovery_keys": hasKeys,
	})
}

// handleBarrierRootToken handles POST /v1/barrier/root-token -- creates a
// new root token by proving knowledge of the master key through Shamir shares.
func (s *Server) handleBarrierRootToken(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierRootTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	token, err := s.barrierSvc.BarrierGenerateRootToken(r.Context(), req.Shares)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, rootTokenToResponse(token))
}

// ---------------------------------------------------------------------------
// Service-to-transport type mapping helpers
// ---------------------------------------------------------------------------

// barrierStatusToResponse converts a BarrierStatusInfo to a JSON-friendly
// map. The initialized_at field is omitted when zero.
func barrierStatusToResponse(info *service.BarrierStatusInfo) map[string]interface{} {
	resp := map[string]interface{}{
		"sealed":          info.Sealed,
		"strategy":        info.Strategy,
		"hardware_backed": info.HardwareBacked,
	}
	if !info.InitializedAt.IsZero() {
		resp["initialized_at"] = info.InitializedAt.Format(time.RFC3339)
	}
	return resp
}

// shamirInitToResponse converts a ShamirInitInfo to a JSON-friendly map.
func shamirInitToResponse(info *service.ShamirInitInfo) map[string]interface{} {
	return map[string]interface{}{
		"shares":       info.Shares,
		"threshold":    info.Threshold,
		"total_shares": info.TotalShares,
	}
}

// unsealShareProgressToResponse converts an UnsealShareProgress to a
// JSON-friendly map.
func unsealShareProgressToResponse(info *service.UnsealShareProgress) map[string]interface{} {
	return map[string]interface{}{
		"required":  info.Required,
		"submitted": info.Submitted,
		"complete":  info.Complete,
	}
}

// shamirSharesToResponse converts a ShamirSharesInfo to a JSON-friendly map.
func shamirSharesToResponse(info *service.ShamirSharesInfo) map[string]interface{} {
	return map[string]interface{}{
		"count":     info.Count,
		"threshold": info.Threshold,
		"total":     info.Total,
	}
}

// recoveryKeysToResponse converts a RecoveryKeysInfo to a JSON-friendly map.
func recoveryKeysToResponse(info *service.RecoveryKeysInfo) map[string]interface{} {
	return map[string]interface{}{
		"keys":      info.Keys,
		"threshold": info.Threshold,
		"total":     info.Total,
	}
}

// rootTokenToResponse converts a RootTokenInfo to a JSON-friendly map.
func rootTokenToResponse(info *service.RootTokenInfo) map[string]interface{} {
	return map[string]interface{}{
		"token":      info.Token,
		"created_at": info.CreatedAt.Format(time.RFC3339),
	}
}
