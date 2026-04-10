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

// ---------------------------------------------------------------------------
// Ceremony handlers
// ---------------------------------------------------------------------------

// handleCeremonyInitialize handles POST /v1/ceremony/initialize -- performs
// the initialization ceremony using the server's pre-configured CeremonyCA,
// issuing TLS and SO certificates and optionally starting M-of-N enrollment.
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

	result, err := s.ceremonySvc.CeremonyInitialize(r.Context(), s.ceremonyCA)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, ceremonyInitToResponse(result))
}

// handleCeremonyStatus handles GET /v1/ceremony/status -- returns the
// current ceremony state and SPKI pin.
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

	writeJSON(w, map[string]interface{}{
		"share": base64.StdEncoding.EncodeToString(share),
	})
}

// handleCeremonySignCSR handles POST /v1/ceremony/sign-csr -- signs a CSR
// during the ceremony for an officer or service, returning the issued
// certificate PEM.
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
