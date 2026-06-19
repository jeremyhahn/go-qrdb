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
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
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

// barrierOfficerRegisterRequest is the JSON payload for
// POST /v1/barrier/officers.
type barrierOfficerRegisterRequest struct {
	Username          string `json:"username"`
	CSRPEM            string `json:"csr_pem"`
	AuthenticatorType string `json:"authenticator_type,omitempty"`
	AuthenticatorRef  string `json:"authenticator_ref,omitempty"`
}

// barrierShareClaimCompleteRequest is the JSON payload for
// POST /v1/barrier/officers/{username}/claim/complete.
type barrierShareClaimCompleteRequest struct {
	NonceHex  string `json:"nonce_hex"`
	Signature string `json:"signature"` // base64-encoded
}

// barrierShamirSealVoteRequest is the JSON payload for
// POST /v1/barrier/shamir/seal-vote.
type barrierShamirSealVoteRequest struct {
	Share string `json:"share"`
}

// restClientCertRequiredError is returned when a share-claim handler
// requires a verified mTLS client certificate but the connection carries none.
type restClientCertRequiredError struct{}

// Error returns a human-readable description.
func (e *restClientCertRequiredError) Error() string {
	return "rest: client certificate required for share claim"
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
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
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
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
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
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
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
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
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
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
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
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
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
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
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
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
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
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
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
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
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
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
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
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
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
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
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
// INV-1: share bytes are never included in initialization responses; Security
// Officers retrieve their shares via the claim handshake.
func shamirInitToResponse(info *service.ShamirInitInfo) map[string]interface{} {
	return map[string]interface{}{
		"threshold":    info.Threshold,
		"total_shares": info.TotalShares,
		"share_count":  info.ShareCount,
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
// INV-1: recovery share bytes are never included in generate responses;
// recovery officers retrieve their shares via the claim handshake.
func recoveryKeysToResponse(info *service.RecoveryKeysInfo) map[string]interface{} {
	return map[string]interface{}{
		"threshold":   info.Threshold,
		"total":       info.Total,
		"share_count": info.ShareCount,
	}
}

// rootTokenToResponse converts a RootTokenInfo to a JSON-friendly map.
func rootTokenToResponse(info *service.RootTokenInfo) map[string]interface{} {
	return map[string]interface{}{
		"token":      info.Token,
		"created_at": info.CreatedAt.Format(time.RFC3339),
	}
}

// ---------------------------------------------------------------------------
// Barrier Security Officer handlers (system barrier — §7.3 of SO design)
// ---------------------------------------------------------------------------

// restCertThumbprint computes the mTLS peer cert thumbprint from the request.
// Returns restClientCertRequiredError when no peer certificate is present.
func restCertThumbprint(r *http.Request) (string, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", &restClientCertRequiredError{}
	}
	return ceremony.CertThumbprint(r.TLS.PeerCertificates[0].Raw), nil
}

// handleBarrierOfficerRegister handles POST /v1/barrier/officers -- registers a
// Security Officer for the system barrier.
// Required permission: seal:officer_register (Admin role).
func (s *Server) handleBarrierOfficerRegister(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierOfficerRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	if req.Username == "" {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "username"})
		return
	}
	if req.CSRPEM == "" {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "csr_pem"})
		return
	}

	result, err := s.barrierSvc.BarrierOfficerRegister(r.Context(), &service.BarrierOfficerRegisterRequest{
		Username:          req.Username,
		CSRPEM:            []byte(req.CSRPEM),
		AuthenticatorType: req.AuthenticatorType,
		AuthenticatorRef:  req.AuthenticatorRef,
	})
	if err != nil {
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"cert_pem":    string(result.CertPEM),
		"share_index": result.ShareIndex,
	})
}

// handleBarrierOfficerList handles GET /v1/barrier/officers -- lists all
// enrolled Security Officers for the system barrier.
// Required permission: seal:officer_list (Admin, SO role).
func (s *Server) handleBarrierOfficerList(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	officers, err := s.barrierSvc.BarrierOfficerList(r.Context())
	if err != nil {
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"officers": barrierOfficerStatusesToResponse(officers),
	})
}

// handleBarrierOfficerClaimStatus handles GET /v1/barrier/officers/claim-status --
// returns a per-officer claim status snapshot for the system barrier.
// Required permission: seal:officer_list (Admin, SO role).
func (s *Server) handleBarrierOfficerClaimStatus(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	officers, err := s.barrierSvc.BarrierOfficerClaimStatus(r.Context())
	if err != nil {
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"officers": barrierOfficerStatusesToResponse(officers),
	})
}

// handleBarrierShareClaimBegin handles the per-username claim begin routes:
//
//	POST /v1/barrier/officers/{username}/claim/begin
//
// It issues a challenge nonce to the named Security Officer.
// Required permission: ceremony:claim_share_begin (SO role, identity-gated).
func (s *Server) handleBarrierShareClaimBegin(w http.ResponseWriter, r *http.Request, username string) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	thumbprint, err := restCertThumbprint(r)
	if err != nil {
		writeErrorJSON(w, http.StatusUnauthorized, err)
		return
	}

	challenge, err := s.barrierSvc.BarrierShareClaimBegin(r.Context(), username, thumbprint)
	if err != nil {
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"nonce_hex":  challenge.NonceHex,
		"expires_at": challenge.ExpiresAt.Format(time.RFC3339),
	})
}

// handleBarrierShareClaimComplete handles the per-username claim complete routes:
//
//	POST /v1/barrier/officers/{username}/claim/complete
//
// It verifies the officer's nonce signature and returns the share bytes exactly
// once via the officer's own mTLS session (INV-1, INV-11).
// Required permission: ceremony:claim_share_complete (SO role, identity-gated).
func (s *Server) handleBarrierShareClaimComplete(w http.ResponseWriter, r *http.Request, username string) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	thumbprint, err := restCertThumbprint(r)
	if err != nil {
		writeErrorJSON(w, http.StatusUnauthorized, err)
		return
	}

	var req barrierShareClaimCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	if req.NonceHex == "" {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "nonce_hex"})
		return
	}
	if req.Signature == "" {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "signature"})
		return
	}

	sigBytes, decErr := base64.StdEncoding.DecodeString(req.Signature)
	if decErr != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "signature"})
		return
	}

	result, err := s.barrierSvc.BarrierShareClaimComplete(r.Context(), username, req.NonceHex, sigBytes, thumbprint)
	if err != nil {
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
		return
	}

	// INV-11: share_bytes appears only here, in the claiming officer's own session.
	writeJSON(w, map[string]interface{}{
		"share_index": result.ShareIndex,
		"share_bytes": base64.StdEncoding.EncodeToString(result.ShareBytes),
	})
}

// handleBarrierShamirSealVote handles POST /v1/barrier/shamir/seal-vote --
// submits a Shamir share as a seal vote. When the quorum threshold is met the
// barrier is sealed automatically.
// Required permission: seal:submit_seal_vote (SO role).
func (s *Server) handleBarrierShamirSealVote(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierShamirSealVoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	if req.Share == "" {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "share"})
		return
	}

	progress, err := s.barrierSvc.BarrierShamirSealVote(r.Context(), req.Share)
	if err != nil {
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"required":  progress.Required,
		"submitted": progress.Submitted,
		"complete":  progress.Complete,
	})
}

// handleBarrierRecoveryOfficerRegister handles POST /v1/barrier/recovery/officers --
// registers a Security Officer for recovery key distribution.
// Required permission: seal:officer_register (Admin role).
func (s *Server) handleBarrierRecoveryOfficerRegister(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	var req barrierOfficerRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	if req.Username == "" {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "username"})
		return
	}
	if req.CSRPEM == "" {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "csr_pem"})
		return
	}

	result, err := s.barrierSvc.BarrierRecoveryOfficerRegister(r.Context(), &service.BarrierOfficerRegisterRequest{
		Username:          req.Username,
		CSRPEM:            []byte(req.CSRPEM),
		AuthenticatorType: req.AuthenticatorType,
		AuthenticatorRef:  req.AuthenticatorRef,
	})
	if err != nil {
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"cert_pem":    string(result.CertPEM),
		"share_index": result.ShareIndex,
	})
}

// handleBarrierRecoveryShareClaimBegin handles the per-username recovery claim
// begin route:
//
//	POST /v1/barrier/recovery/officers/{username}/claim/begin
//
// It issues a challenge nonce to the named recovery officer.
// Required permission: ceremony:claim_share_begin (recovery SO role, identity-gated).
func (s *Server) handleBarrierRecoveryShareClaimBegin(w http.ResponseWriter, r *http.Request, username string) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	thumbprint, err := restCertThumbprint(r)
	if err != nil {
		writeErrorJSON(w, http.StatusUnauthorized, err)
		return
	}

	challenge, err := s.barrierSvc.BarrierRecoveryShareClaimBegin(r.Context(), username, thumbprint)
	if err != nil {
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
		return
	}

	writeJSON(w, map[string]interface{}{
		"nonce_hex":  challenge.NonceHex,
		"expires_at": challenge.ExpiresAt.Format(time.RFC3339),
	})
}

// handleBarrierRecoveryShareClaimComplete handles the per-username recovery
// claim complete route:
//
//	POST /v1/barrier/recovery/officers/{username}/claim/complete
//
// It verifies the recovery officer's nonce signature and returns the recovery
// share bytes exactly once (INV-1, INV-11).
// Required permission: ceremony:claim_share_complete (recovery SO role, identity-gated).
func (s *Server) handleBarrierRecoveryShareClaimComplete(w http.ResponseWriter, r *http.Request, username string) {
	if s.barrierSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "barrier"})
		return
	}

	thumbprint, err := restCertThumbprint(r)
	if err != nil {
		writeErrorJSON(w, http.StatusUnauthorized, err)
		return
	}

	var req barrierShareClaimCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	if req.NonceHex == "" {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "nonce_hex"})
		return
	}
	if req.Signature == "" {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "signature"})
		return
	}

	sigBytes, decErr := base64.StdEncoding.DecodeString(req.Signature)
	if decErr != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ValidationError{Field: "signature"})
		return
	}

	result, err := s.barrierSvc.BarrierRecoveryShareClaimComplete(r.Context(), username, req.NonceHex, sigBytes, thumbprint)
	if err != nil {
		writeErrorJSON(w, mapBarrierCeremonyHTTPError(err), err)
		return
	}

	// INV-11: share_bytes appears only here, in the claiming officer's own session.
	writeJSON(w, map[string]interface{}{
		"share_index": result.ShareIndex,
		"share_bytes": base64.StdEncoding.EncodeToString(result.ShareBytes),
	})
}

// ---------------------------------------------------------------------------
// Barrier officer sub-handler dispatch (path-param routes)
// ---------------------------------------------------------------------------

// handleBarrierOfficerByUsername dispatches routes under:
//
//	/v1/barrier/officers/{username}/claim/begin|complete
//	/v1/barrier/recovery/officers/{username}/claim/begin|complete
//
// The caller passes the trimmed path suffix after the base prefix so the
// handler can extract the username and the terminal sub-path segment.
func (s *Server) handleBarrierOfficersByPath(w http.ResponseWriter, r *http.Request) {
	const officersPrefix = "/v1/barrier/officers/"
	const recoveryOfficersPrefix = "/v1/barrier/recovery/officers/"

	switch {
	case strings.HasPrefix(r.URL.Path, officersPrefix):
		suffix := strings.TrimPrefix(r.URL.Path, officersPrefix)
		s.dispatchOfficerClaimPath(w, r, suffix, false)
	case strings.HasPrefix(r.URL.Path, recoveryOfficersPrefix):
		suffix := strings.TrimPrefix(r.URL.Path, recoveryOfficersPrefix)
		s.dispatchOfficerClaimPath(w, r, suffix, true)
	default:
		writeErrorJSON(w, http.StatusNotFound, &MissingParameterError{Param: "username"})
	}
}

// dispatchOfficerClaimPath handles the {username}/claim/begin and
// {username}/claim/complete suffix patterns for both unseal and recovery
// officer claim routes.
func (s *Server) dispatchOfficerClaimPath(w http.ResponseWriter, r *http.Request, suffix string, recovery bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// suffix is "{username}/claim/begin" or "{username}/claim/complete"
	parts := strings.SplitN(suffix, "/", 3)
	if len(parts) < 3 || parts[1] != "claim" {
		writeErrorJSON(w, http.StatusNotFound, &MissingParameterError{Param: "username"})
		return
	}
	username := parts[0]
	action := parts[2]

	if recovery {
		switch action {
		case "begin":
			s.handleBarrierRecoveryShareClaimBegin(w, r, username)
		case "complete":
			s.handleBarrierRecoveryShareClaimComplete(w, r, username)
		default:
			writeErrorJSON(w, http.StatusNotFound, &MissingParameterError{Param: "action"})
		}
		return
	}

	switch action {
	case "begin":
		s.handleBarrierShareClaimBegin(w, r, username)
	case "complete":
		s.handleBarrierShareClaimComplete(w, r, username)
	default:
		writeErrorJSON(w, http.StatusNotFound, &MissingParameterError{Param: "action"})
	}
}

// ---------------------------------------------------------------------------
// Barrier ceremony error mapping
// ---------------------------------------------------------------------------

// mapBarrierCeremonyHTTPError maps ceremony and seal errors to HTTP status
// codes. Errors not recognized here fall through to the generic service
// mapper, which returns 500 Internal Server Error for unknown errors.
func mapBarrierCeremonyHTTPError(err error) int {
	switch {
	case errors.Is(err, ceremony.ErrCertThumbprintMismatch):
		return http.StatusForbidden
	case errors.Is(err, ceremony.ErrOfficerNotFound):
		return http.StatusNotFound
	case errors.Is(err, ceremony.ErrShareAlreadyClaimed):
		return http.StatusConflict
	case errors.Is(err, ceremony.ErrNoPendingShare):
		return http.StatusNotFound
	case errors.Is(err, ceremony.ErrInsufficientOfficers):
		return http.StatusUnprocessableEntity
	case errors.Is(err, seal.ErrShamirBarrierRequiresQuorum):
		return http.StatusUnprocessableEntity
	case errors.Is(err, seal.ErrSealVoteVerificationFailed):
		return http.StatusForbidden
	case errors.Is(err, seal.ErrUnsealKeyMismatch):
		return http.StatusForbidden
	case errors.Is(err, seal.ErrWrappedRootKeyCorrupt):
		return http.StatusInternalServerError
	case errors.Is(err, seal.ErrAlreadyUnsealed):
		return http.StatusConflict
	case errors.Is(err, seal.ErrAlreadyInitialized):
		return http.StatusConflict
	case errors.Is(err, seal.ErrNotInitialized):
		return http.StatusConflict
	case errors.Is(err, seal.ErrInvalidCredentials):
		return http.StatusForbidden
	case errors.Is(err, seal.ErrDecryptionFailed):
		return http.StatusForbidden
	case errors.Is(err, service.ErrCeremonyNotConfigured):
		return http.StatusUnprocessableEntity
	default:
		return mapHTTPError(err)
	}
}

// barrierOfficerStatusesToResponse converts a slice of BarrierOfficerStatus to
// a JSON-serializable representation.
func barrierOfficerStatusesToResponse(officers []*service.BarrierOfficerStatus) []map[string]interface{} {
	out := make([]map[string]interface{}, len(officers))
	for i, o := range officers {
		m := map[string]interface{}{
			"username":        o.Username,
			"cert_thumbprint": o.CertThumbprint,
			"share_index":     o.ShareIndex,
			"auth_factor":     o.AuthFactor,
			"share_pending":   o.SharePending,
		}
		if !o.EnrolledAt.IsZero() {
			m["enrolled_at"] = o.EnrolledAt.Format(time.RFC3339)
		}
		if !o.ClaimedAt.IsZero() {
			m["claimed_at"] = o.ClaimedAt.Format(time.RFC3339)
		}
		out[i] = m
	}
	return out
}
