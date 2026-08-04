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
	"net/http"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Ceremony request types (REST-local, unexported)
// ---------------------------------------------------------------------------

// ceremonyInitializeRequest is the JSON payload for POST /v1/ceremony/initialize.
type ceremonyInitializeRequest struct {
	SOPin                  string                  `json:"so_pin"`
	UserPin                string                  `json:"user_pin"`
	CredentialSealStrategy string                  `json:"credential_seal_strategy,omitempty"`
	Threshold              int                     `json:"threshold"`
	Officers               []ceremonyOfficerConfig `json:"officers,omitempty"`
	Hostname               string                  `json:"hostname,omitempty"`
	SANs                   []string                `json:"sans,omitempty"`
}

// ceremonyOfficerConfig describes an officer participating in the ceremony.
type ceremonyOfficerConfig struct {
	Username string `json:"username"`
	CSRPEM   string `json:"csr_pem"`
}

// claimCertBeginRequest is the JSON payload for
// POST /v1/ceremony/claim-cert/begin.
type claimCertBeginRequest struct {
	Username string `json:"username"`
}

// claimCertCompleteRequest is the JSON payload for
// POST /v1/ceremony/claim-cert/complete.
type claimCertCompleteRequest struct {
	Username  string `json:"username"`
	NonceHex  string `json:"nonce_hex"`
	Signature string `json:"signature"` // base64-encoded
}

// claimShareRequest is the JSON payload for POST /v1/ceremony/claim-share.
type claimShareRequest struct {
	Username string `json:"username"`
}

// signCSRRequest is the JSON payload for POST /v1/ceremony/sign-csr.
type signCSRRequest struct {
	Username string `json:"username"`
	SOPin    string `json:"so_pin"`
	CSRPEM   string `json:"csr_pem"`
	Role     string `json:"role"`
}

// enrollExternalOfficerRequest is the JSON payload for
// POST /v1/init/enroll-external-officer.
type enrollExternalOfficerRequest struct {
	Username   string `json:"username"`
	CertPEM    string `json:"cert_pem,omitempty"`
	PubKeyPEM  string `json:"pubkey_pem,omitempty"`
	ShareIndex int    `json:"share_index"`
	AuthFactor string `json:"auth_factor"`
	ShareBytes []byte `json:"share_bytes,omitempty"`
}

// enrollExternalOfficerResponse is the JSON response for
// POST /v1/init/enroll-external-officer.
type enrollExternalOfficerResponse struct {
	SealedShare string `json:"sealed_share,omitempty"`
}

// ---------------------------------------------------------------------------
// Ceremony handlers
// ---------------------------------------------------------------------------

// handleCeremonyInitialize handles POST /v1/ceremony/initialize -- performs
// the initialization ceremony using the server's pre-configured CeremonyCA,
// issuing TLS and SO certificates and optionally starting M-of-N enrollment.
// Required permission: ceremony:initialize (SO role only).
func (s *Server) handleCeremonyInitialize(w http.ResponseWriter, r *http.Request) {
	if s.ceremonySvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "ceremony"})
		return
	}

	var req ceremonyInitializeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	result, err := s.ceremonySvc.CeremonyInitialize(r.Context(), s.ceremonyCA, &service.CeremonyInitializeOptions{
		SOPin:                  req.SOPin,
		UserPin:                req.UserPin,
		CredentialSealStrategy: req.CredentialSealStrategy,
		Threshold:              req.Threshold,
		Officers:               ceremonyOfficerConfigsFromRequest(req.Officers),
		Hostname:               req.Hostname,
		SANs:                   req.SANs,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, ceremonyInitToResponse(result))
}

// ceremonyOfficerConfigsFromRequest converts the REST-local officer
// configuration wire shape to the service-layer type. Returns nil when
// officers is empty so the service layer's "non-empty overrides the
// configured value" precedence rule works unchanged.
func ceremonyOfficerConfigsFromRequest(officers []ceremonyOfficerConfig) []service.CeremonyOfficerConfig {
	if len(officers) == 0 {
		return nil
	}
	out := make([]service.CeremonyOfficerConfig, len(officers))
	for i, o := range officers {
		out[i] = service.CeremonyOfficerConfig{Username: o.Username, CSRPEM: o.CSRPEM}
	}
	return out
}

// handleCeremonyStatus handles GET /v1/ceremony/status -- returns the
// current ceremony state and SPKI pin.
// Required permission: ceremony:status (SO, Admin, Operator, Auditor).
func (s *Server) handleCeremonyStatus(w http.ResponseWriter, r *http.Request) {
	if s.ceremonySvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "ceremony"})
		return
	}

	info, err := s.ceremonySvc.CeremonyStatus(r.Context())
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, ceremonyStatusToResponse(info))
}

// handleCeremonyClaimCertBegin handles POST /v1/ceremony/claim-cert/begin --
// starts the certificate claim handshake for an officer by issuing a
// challenge nonce.
// Required permission: ceremony:claim_cert (enrolled officer / SO).
func (s *Server) handleCeremonyClaimCertBegin(w http.ResponseWriter, r *http.Request) {
	if s.ceremonySvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "ceremony"})
		return
	}

	var req claimCertBeginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	challenge, err := s.ceremonySvc.CeremonyBeginClaimCert(r.Context(), req.Username)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, ceremonyChallengeToResponse(challenge))
}

// handleCeremonyClaimCertComplete handles POST /v1/ceremony/claim-cert/complete --
// completes the certificate claim handshake by verifying the officer's nonce
// signature and returning the issued certificate.
// Required permission: ceremony:claim_cert (enrolled officer / SO).
func (s *Server) handleCeremonyClaimCertComplete(w http.ResponseWriter, r *http.Request) {
	if s.ceremonySvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "ceremony"})
		return
	}

	var req claimCertCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	sig, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	result, err := s.ceremonySvc.CeremonyCompleteClaimCert(r.Context(), req.Username, req.NonceHex, sig)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, ceremonyClaimCertToResponse(result))
}

// handleCeremonyClaimShare handles POST /v1/ceremony/claim-share -- retrieves
// the Shamir share for the given officer username.
// Required permission: ceremony:claim_share_complete (enrolled Custodian).
func (s *Server) handleCeremonyClaimShare(w http.ResponseWriter, r *http.Request) {
	if s.ceremonySvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "ceremony"})
		return
	}

	var req claimShareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	share, err := s.ceremonySvc.CeremonyClaimShare(r.Context(), req.Username)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	// share is already JSON text (a marshaled shamir.Share envelope, per
	// pkg/ceremony's sealedShareStore) -- not raw secret bytes needing
	// base64. The response key and encoding must match
	// transport.ClaimShareResponse{ShareJSON string `json:"share_json"`},
	// the shared SDK type, and the gRPC/QUIC/embedded transports, which all
	// emit the share bytes verbatim as a string. A prior {"share":
	// base64(...)} shape here left every REST-transport caller's ShareJSON
	// field empty (wrong key) even when the CSR-derived guess landed on the
	// right shape.
	writeJSON(w, map[string]interface{}{
		"share_json": string(share),
	})
}

// handleCeremonySignCSR handles POST /v1/ceremony/sign-csr -- signs a CSR
// during the ceremony for an officer or service, returning the issued
// certificate PEM.
// Required permission: ceremony:sign_csr (SO role only).
func (s *Server) handleCeremonySignCSR(w http.ResponseWriter, r *http.Request) {
	if s.ceremonySvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "ceremony"})
		return
	}

	var req signCSRRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	result, err := s.ceremonySvc.CeremonySignCSR(r.Context(), req.Username, req.SOPin, req.CSRPEM, req.Role)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, ceremonySignCSRToResponse(result))
}

// handleCeremonyEnrollExternalOfficer handles
// POST /v1/init/enroll-external-officer — admin-driven enrollment of an
// officer whose key material lives outside go-qrdb.
// Required permission: ceremony:enroll_external_officer (SO role only).
func (s *Server) handleCeremonyEnrollExternalOfficer(w http.ResponseWriter, r *http.Request) {
	if s.ceremonySvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "ceremony"})
		return
	}

	var req enrollExternalOfficerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	resp, err := s.ceremonySvc.CeremonyEnrollExternalOfficer(r.Context(), &service.CeremonyEnrollExternalOfficerRequest{
		Username:   req.Username,
		CertPEM:    req.CertPEM,
		PubKeyPEM:  req.PubKeyPEM,
		ShareIndex: req.ShareIndex,
		AuthFactor: req.AuthFactor,
		ShareBytes: req.ShareBytes,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, enrollExternalOfficerResponse{SealedShare: resp.SealedShare})
}

// ---------------------------------------------------------------------------
// Service-to-transport type mapping helpers
// ---------------------------------------------------------------------------

// ceremonyInitToResponse converts a CeremonyInitResult to a JSON-friendly map.
func ceremonyInitToResponse(r *service.CeremonyInitResult) map[string]interface{} {
	return map[string]interface{}{
		"spki_pin":     r.SPKIPin,
		"ca_cert_pem":  string(r.CACertPEM),
		"tls_cert_pem": string(r.TLSCertPEM),
		"so_cert_pem":  string(r.SOCertPEM),
		"so_key_pem":   string(r.SOKeyPEM),
		"state":        r.State,
		"share_count":  r.ShareCount,
	}
}

// ceremonyStatusToResponse converts a CeremonyStatusInfo to a JSON-friendly map.
func ceremonyStatusToResponse(info *service.CeremonyStatusInfo) map[string]interface{} {
	return map[string]interface{}{
		"state":    info.State,
		"spki_pin": info.SPKIPin,
	}
}

// ceremonyChallengeToResponse converts a CeremonyChallenge to a JSON-friendly map.
func ceremonyChallengeToResponse(c *service.CeremonyChallenge) map[string]interface{} {
	return map[string]interface{}{
		"nonce_hex":  c.NonceHex,
		"expires_at": c.ExpiresAt.Format(time.RFC3339),
	}
}

// ceremonyClaimCertToResponse converts a CeremonyClaimCertResult to a
// JSON-friendly map.
func ceremonyClaimCertToResponse(r *service.CeremonyClaimCertResult) map[string]interface{} {
	return map[string]interface{}{
		"cert_pem":    string(r.CertPEM),
		"ca_cert_pem": string(r.CACertPEM),
	}
}

// ceremonySignCSRToResponse converts a CeremonySignCSRResult to a
// JSON-friendly map.
func ceremonySignCSRToResponse(r *service.CeremonySignCSRResult) map[string]interface{} {
	return map[string]interface{}{
		"cert_pem": string(r.CertPEM),
	}
}
