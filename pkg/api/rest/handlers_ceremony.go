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

	// s.ceremonyCA is nil whenever this node's TLS bundle carries no CA
	// private key (BYO --tls-ca-file/--tls-cert-file/--tls-key-file --
	// ceremonyCAFromBundle's documented (nil, nil) case). That is no
	// longer refused here: ceremony.Initialize supports it directly (the
	// operator's own leaf certificate is the server identity, and the
	// bring-your-own-certificate admin-credential path needs no CA at
	// all), refusing only the two admin-credential paths that actually
	// need one to sign with (CSR-sign, generate) via the typed
	// *ceremony.AdminCredentialRequiresCAError mapErrorJSON already
	// propagates.
	//
	// admin_csr_pem/admin_cert_pem/admin_cert_pool_pem/admin_generate/secret
	// and the system SO's so_username/so_csr_pem/so_cert_pem/so_chain_pem
	// (go-qrdb#1128) round-trip through transport.CeremonyInitializeRequest/Response and
	// the holderwire.CeremonyInitFrom/To conversions below, shared by REST,
	// MCP and the embedded transport, and through the matching
	// CeremonyInitializeRequest/Response protobuf fields and
	// holderpb.CeremonyInitFromPB/ToPB, shared by gRPC, Unix (the same
	// qrdbServiceServer over a different socket) and native QUIC -- every
	// field on every transport (go-qrdb#548 B2). The nil-CA refusal this
	// handler never had was, until go-qrdb#560 review item 5, still present
	// on those other five transports, which is a DIFFERENT gap than missing
	// fields: refusing a BYO-TLS deployment's `qrdb init` outright rather
	// than forwarding a request those transports could already decode in
	// full. All six now forward ca == nil into the service layer exactly as
	// this handler does.
	//
	// The body is read into a []byte, rather than decoded straight off
	// r.Body, so a follower that CeremonyInitialize refuses with
	// *service.AdminCredentialNotLeaderError (the admin-credential step
	// must run on the system shard's leader) can replay the identical
	// request at the leader through the SAME tryForwardWrite/forwardToLeader
	// machinery every other system-shard write already uses, instead of
	// making the operator re-run `qrdb init` against another node by hand.
	body, ok := readBoundedBody(w, r, resolveMaxBodyBytes(s.maxBodyBytes))
	if !ok {
		return
	}
	var req transport.CeremonyInitializeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	// req.Secret (wire-encoded as base64, matching
	// transport.BarrierInitializeRequest.Secret's existing convention) is
	// consumed into a *secure.Buffer by CeremonyInitFrom, which this handler
	// owns and destroys once the ceremony call below has finished with it.
	opts, err := holderwire.CeremonyInitFrom(&req)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	defer opts.Secret.Destroy()

	result, err := s.ceremonySvc.CeremonyInitialize(r.Context(), s.ceremonyCA, opts)
	if err != nil {
		if s.tryForwardWrite(w, r, body, err) {
			return
		}
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	// The admin key, when the generate path produced one, is wiped from
	// this handler's own copy of result once writeJSON has already
	// serialized and written the response (go-qrdb#548) -- the response
	// itself carries an independent copy (holderwire.CeremonyInitTo's
	// bytes.Clone), so this wipe cannot reach it. Every other transport
	// now wipes the same way (go-qrdb#560 review item 7); WipeAdminKeyPEM
	// is a safe no-op when result carries none.
	defer result.WipeAdminKeyPEM()
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
