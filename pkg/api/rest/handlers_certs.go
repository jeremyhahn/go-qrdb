// Copyright (c) 2026 Jeremy Hahn
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
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// ---------------------------------------------------------------------------
// Cert request/response types
// ---------------------------------------------------------------------------

// generateCertRequest is the JSON payload for POST /certs/generate.
type generateCertRequest struct {
	Username string `json:"username"`
	TenantID string `json:"tenantID"`
	TTL      string `json:"ttl"`
}

// exportCertRequest is the JSON payload for POST /certs/export.
// The client must supply the PEM-encoded certificate and key received
// from the generate endpoint, as the server does not retain private keys.
type exportCertRequest struct {
	CertPEM    string `json:"certPEM"`
	KeyPEM     string `json:"keyPEM"`
	Passphrase string `json:"passphrase"`
}

// revokeCertRequest is the JSON payload for POST /certs/revoke.
type revokeCertRequest struct {
	Serial string `json:"serial"`
}

// revokeCertResponse is the JSON representation of a revocation outcome.
// RecordFound distinguishes a certificate this node holds a record for from
// a deny-by-serial revocation of one it does not -- both are successes. See
// go-qrdb#287.
type revokeCertResponse struct {
	Status      string `json:"status"`
	RecordFound bool   `json:"recordFound"`
}

// certBundleResponse is the JSON representation of a generated certificate
// bundle containing the PEM-encoded certificate, key, and serial number.
type certBundleResponse struct {
	CertPEM string `json:"certPEM"`
	KeyPEM  string `json:"keyPEM"`
	Serial  string `json:"serial"`
}

// exportCertResponse is the JSON representation of a PKCS#12 export.
// The PKCS12 field contains the base64-encoded archive bytes.
type exportCertResponse struct {
	PKCS12 string `json:"pkcs12"`
}

// listCertsResponse wraps a slice of certRecordResponse so the REST
// endpoint returns an object with a "certs" key instead of a bare array.
type listCertsResponse struct {
	Certs []*certRecordResponse `json:"certs"`
}

// certRecordResponse is the JSON representation of an issued certificate
// record.
type certRecordResponse struct {
	Serial    string `json:"serial"`
	Username  string `json:"username"`
	TenantID  string `json:"tenantID"`
	IssuedAt  string `json:"issuedAt"`
	ExpiresAt string `json:"expiresAt"`
	Revoked   bool   `json:"revoked"`
}

// ---------------------------------------------------------------------------
// Cert tenancy gates
//
// The route permission on each /certs/* entry is checked against the CALLER's
// own tenant. It cannot say which tenant's certificates the call reaches,
// because three of these four verbs name their target in the request: the
// tenantID in the generate body, the tenantID in the list query, and, for
// revoke, a serial that resolves to a tenant only by looking it up.
//
// So each handler asks a second question through the shared gate in
// pkg/auth/middleware -- the one cross-transport home of the rule, which the
// gRPC service calls too.
//
// When no auth middleware is configured the gates are no-ops, mirroring every
// other handler-level gate on this server (see authorizeTenantBarrierOp).
// ---------------------------------------------------------------------------

// authorizeCertTenant gates a certificate operation on the tenant the REQUEST
// names, rather than on the tenant the caller happens to be in.
func (s *Server) authorizeCertTenant(ctx context.Context, tenantID string, perm permission.Permission) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeCertTenant(ctx, tenantID, perm)
}

// authorizeCertRevoke gates a revocation on the tenant that owns the serial,
// falling back to the CA-operator authority when the serial owns no record
// here (deny-by-serial).
func (s *Server) authorizeCertRevoke(ctx context.Context, serial string) error {
	if s.authMW == nil {
		return nil
	}
	return s.authMW.AuthorizeCertRevoke(ctx, serial, s.certProv)
}

// ---------------------------------------------------------------------------
// Cert handlers
// ---------------------------------------------------------------------------

// handleGenerateCert handles POST /certs/generate -- creates a client
// certificate signed by the internal CA. The certificate PEM, private key
// PEM, and serial number are returned to the caller.
func (s *Server) handleGenerateCert(w http.ResponseWriter, r *http.Request) {
	if s.certProv == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "cert"})
		return
	}

	var req generateCertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	genReq := &cert.GenerateRequest{
		Username: req.Username,
		TenantID: req.TenantID,
	}

	if req.TTL != "" {
		ttl, err := time.ParseDuration(req.TTL)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "ttl", Err: err})
			return
		}
		genReq.TTL = ttl
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	// The body's tenantID becomes the certificate's Subject OU, and the mTLS
	// authenticator derives an identity's tenant from exactly that OU. An
	// unchecked cross-tenant mint therefore does not merely misfile a record
	// -- it hands the caller a CA-signed credential for another tenant.
	if err := s.authorizeCertTenant(ctx, genReq.TenantID, permission.CertsGenerate); err != nil {
		writeAuthzErrorJSON(w, err)
		return
	}

	bundle, err := s.certProv.GenerateClientCert(ctx, genReq)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(certBundleResponse{
		CertPEM: string(bundle.CertPEM),
		KeyPEM:  string(bundle.KeyPEM),
		Serial:  bundle.Serial,
	})
}

// handleExportCert handles POST /certs/export -- exports a certificate
// and private key as a PKCS#12 archive for browser import. The client
// must supply the PEM-encoded certificate and key originally returned
// by the generate endpoint, since the server does not retain private keys.
//
// NO TENANCY GATE, deliberately, and this is the one certs verb that needs
// none. It reads no server-held certificate material and names no tenant: the
// caller supplies both the certificate and the private key, so the archive it
// gets back contains only what it already had. There is nothing here for a
// tenancy check to protect. The revocation check inside ExportPKCS12 is an
// operator signal, not a containment control, for the same reason.
func (s *Server) handleExportCert(w http.ResponseWriter, r *http.Request) {
	if s.certProv == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "cert"})
		return
	}

	var req exportCertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	p12Bytes, err := s.certProv.ExportPKCS12(
		ctx,
		[]byte(req.CertPEM),
		[]byte(req.KeyPEM),
		req.Passphrase,
	)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, exportCertResponse{
		PKCS12: base64.StdEncoding.EncodeToString(p12Bytes),
	})
}

// handleRevokeCert handles POST /certs/revoke -- marks a certificate serial as
// revoked. Enforcement is on the authentication path of every transport, on
// every request (pkg/auth.RevocationChecker), so a revoked peer is refused
// even on an already-established connection.
//
// A serial this node holds no record for is still revoked -- the marker is
// written -- and the response is 200, because that is a real CA operation
// (revoking a peer-issued certificate). The response's recordFound field
// lets the caller tell that case apart from a mistyped serial without
// either being reported as an error -- both are successes. See go-qrdb#287.
func (s *Server) handleRevokeCert(w http.ResponseWriter, r *http.Request) {
	if s.certProv == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "cert"})
		return
	}

	var req revokeCertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := s.authorizeCertRevoke(ctx, req.Serial); err != nil {
		writeAuthzErrorJSON(w, err)
		return
	}

	result, err := s.certProv.RevokeCert(ctx, req.Serial)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	writeJSON(w, revokeCertResponse{Status: "ok", RecordFound: result.RecordFound()})
}

// handleListCerts handles GET /certs/list -- returns all issued certificate
// records for the specified tenant.
// Query parameter: tenantID (required).
func (s *Server) handleListCerts(w http.ResponseWriter, r *http.Request) {
	if s.certProv == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "cert"})
		return
	}

	tenantID := r.URL.Query().Get("tenantID")
	if tenantID == "" {
		writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{Param: "tenantID"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	if err := s.authorizeCertTenant(ctx, tenantID, permission.CertsList); err != nil {
		writeAuthzErrorJSON(w, err)
		return
	}

	records, err := s.certProv.ListCerts(ctx, tenantID)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	certs := make([]*certRecordResponse, len(records))
	for i, rec := range records {
		certs[i] = certRecordToResponse(rec)
	}

	writeJSON(w, listCertsResponse{Certs: certs})
}

// ---------------------------------------------------------------------------
// Cert conversion and error mapping
// ---------------------------------------------------------------------------

// certRecordToResponse converts a cert.CertRecord to its JSON response
// representation.
func certRecordToResponse(rec *cert.CertRecord) *certRecordResponse {
	if rec == nil {
		return nil
	}

	return &certRecordResponse{
		Serial:    rec.Serial,
		Username:  rec.Username,
		TenantID:  rec.TenantID,
		IssuedAt:  rec.IssuedAt.Format(time.RFC3339),
		ExpiresAt: rec.ExpiresAt.Format(time.RFC3339),
		Revoked:   rec.Revoked,
	}
}
