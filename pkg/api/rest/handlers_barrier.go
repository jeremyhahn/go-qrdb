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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Barrier request types (REST-local, unexported)
// ---------------------------------------------------------------------------

// barrierInitializeRequest is the JSON payload for POST /v1/barrier/initialize.
type barrierInitializeRequest struct {
	Secret  string `json:"secret"`
	SOPin   string `json:"so_pin,omitempty"`
	UserPin string `json:"user_pin,omitempty"`
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
	SOPin       string `json:"so_pin,omitempty"`
	UserPin     string `json:"user_pin,omitempty"`
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
	Threshold int    `json:"threshold"`
	Total     int    `json:"total"`
	SOPin     string `json:"so_pin,omitempty"`
}

// barrierRecoveryGenerateRequest is the JSON payload for
// POST /v1/barrier/recovery/generate.
type barrierRecoveryGenerateRequest struct {
	Threshold int    `json:"threshold"`
	Total     int    `json:"total"`
	SOPin     string `json:"so_pin,omitempty"`
}

// barrierRecoveryRecoverRequest is the JSON payload for
// POST /v1/barrier/recovery/recover.
type barrierRecoveryRecoverRequest struct {
	Keys []string `json:"keys"`
}

// barrierSealRequest is the JSON payload for POST /v1/barrier/seal.
// When the request body is empty or absent the server treats SOPin as
// the empty string; verification is enforced only when a pin.PINBackend
// is configured AND has a SO PIN set.
type barrierSealRequest struct {
	SOPin string `json:"so_pin,omitempty"`
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
// Required permission: seal:bootstrap (SO role).
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

	// Apply optional SO PIN / User PIN to the pin.PINBackend before
	// initializing the barrier. When the backend is nil the call is a
	// no-op so dev-mode flows continue to work.
	if err := applyBarrierInitializePINs(s.pinBackend, req.SOPin, req.UserPin); err != nil {
		writeErrorJSON(w, mapPINHTTPError(err), err)
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
// Required permission: seal:unseal (SO, Admin, Operator).
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
// to the sealed state. When a request body is present it is decoded into a
// barrierSealRequest carrying an optional SO PIN; when absent the handler
// treats SOPin as empty and verification is a no-op.
// Required permission: seal:seal (SO, Admin, Operator).
func (s *Server) handleBarrierSeal(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	// Decode the optional SO PIN body. An empty body is permitted —
	// json.Decoder.Decode returns io.EOF which we treat as a no-op.
	var req barrierSealRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
			return
		}
	}

	if err := verifyBarrierSOPIN(s.pinBackend, req.SOPin); err != nil {
		writeErrorJSON(w, mapPINHTTPError(err), err)
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
// Required permission: seal:status (SO, Admin, Operator, Auditor).
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
// Required permission: seal:initialize_shamir (SO role only).
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

	// Apply optional SO PIN / User PIN to the pin.PINBackend before
	// initializing the barrier (no-op when no backend is configured).
	if err := applyBarrierInitializePINs(s.pinBackend, req.SOPin, req.UserPin); err != nil {
		writeErrorJSON(w, mapPINHTTPError(err), err)
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
// Required permission: barrier:unseal-share (Custodian role only).
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
// Required permission: barrier:unseal-share (Custodian role only).
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

// handleBarrierRekey handles POST /v1/barrier/rekey -- generates new Shamir
// shares for the existing root key with the specified threshold and total.
// Required permission: seal:rekey (SO role only).
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

	// Verify SO PIN before performing the rekey administrative operation.
	// Enforced only when a PINBackend is configured AND has a SO PIN set.
	if err := verifyBarrierSOPIN(s.pinBackend, req.SOPin); err != nil {
		writeErrorJSON(w, mapPINHTTPError(err), err)
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
// Required permission: seal:generate_recovery (SO role only).
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

	// Verify SO PIN before generating recovery keys (no-op when no
	// PINBackend is configured or no SO PIN has been set).
	if err := verifyBarrierSOPIN(s.pinBackend, req.SOPin); err != nil {
		writeErrorJSON(w, mapPINHTTPError(err), err)
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
// Required permission: seal:recover_with_keys (SO, Admin).
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
// Required permission: seal:generate_recovery (SO role only).
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
// Required permission: seal:status (SO, Admin, Operator, Auditor).
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
// Required permission: seal:bootstrap (SO role only).
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
// map. The initialized_at field is omitted when zero. The shamir_threshold
// and shamir_total fields are included only when the strategy is shamir and
// the values are non-zero.
func barrierStatusToResponse(info *service.BarrierStatusInfo) map[string]interface{} {
	resp := map[string]interface{}{
		"sealed":          info.Sealed,
		"strategy":        info.Strategy,
		"hardware_backed": info.HardwareBacked,
	}
	if !info.InitializedAt.IsZero() {
		resp["initialized_at"] = info.InitializedAt.Format(time.RFC3339)
	}
	if info.ShamirThreshold > 0 {
		resp["shamir_threshold"] = info.ShamirThreshold
	}
	if info.ShamirTotal > 0 {
		resp["shamir_total"] = info.ShamirTotal
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
