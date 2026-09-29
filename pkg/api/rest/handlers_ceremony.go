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

	"github.com/jeremyhahn/go-qrdb/pkg/api/holderwire"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Ceremony request types (REST-local, unexported)
// ---------------------------------------------------------------------------

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
// the initialization ceremony using the server's pre-configured CeremonyCA:
// it provisions the SO and User PINs, issues the TLS certificate and, for a
// threshold of two or more, enrols the initial share holders and initializes
// Shamir custody over them.
// Required permission: ceremony:initialize (SO role only).
func (s *Server) handleCeremonyInitialize(w http.ResponseWriter, r *http.Request) {
	if s.ceremonySvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "ceremony"})
		return
	}

	var req transport.CeremonyInitializeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	result, err := s.ceremonySvc.CeremonyInitialize(r.Context(), s.ceremonyCA, holderwire.CeremonyInitFrom(&req))
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, holderwire.CeremonyInitTo(result))
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

// ---------------------------------------------------------------------------
// Service-to-transport type mapping helpers
// ---------------------------------------------------------------------------

// ceremonyStatusToResponse converts a CeremonyStatusInfo to a JSON-friendly map.
func ceremonyStatusToResponse(info *service.CeremonyStatusInfo) map[string]interface{} {
	return map[string]interface{}{
		"state":    info.State,
		"spki_pin": info.SPKIPin,
	}
}

// ceremonySignCSRToResponse converts a CeremonySignCSRResult to a
// JSON-friendly map.
func ceremonySignCSRToResponse(r *service.CeremonySignCSRResult) map[string]interface{} {
	return map[string]interface{}{
		"cert_pem": string(r.CertPEM),
	}
}
