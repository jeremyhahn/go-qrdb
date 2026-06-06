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
	"errors"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
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

	bundle, err := s.certProv.GenerateClientCert(ctx, genReq)
	if err != nil {
		writeErrorJSON(w, mapCertError(err), err)
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
		writeErrorJSON(w, mapCertError(err), err)
		return
	}

	writeJSON(w, exportCertResponse{
		PKCS12: base64.StdEncoding.EncodeToString(p12Bytes),
	})
}

// handleRevokeCert handles POST /certs/revoke -- marks a certificate
// serial as revoked. Revoked certificates are rejected during mTLS
// verification.
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

	if err := s.certProv.RevokeCert(ctx, req.Serial); err != nil {
		writeErrorJSON(w, mapCertError(err), err)
		return
	}

	writeJSON(w, okResponse{Status: "ok"})
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

	records, err := s.certProv.ListCerts(ctx, tenantID)
	if err != nil {
		writeErrorJSON(w, mapCertError(err), err)
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

// mapCertError maps cert domain errors to the appropriate HTTP status code.
func mapCertError(err error) int {
	var ve *cert.ValidationError
	var cnf *cert.CertNotFoundError
	var cr *cert.CertRevokedError
	var bs *cert.BarrierSealedError
	var ca *cert.CAKeyNotAvailableError
	var se *cert.StoreError
	var ge *cert.CertGenerateError
	var ee *cert.CertExportError

	switch {
	case errors.As(err, &ve):
		return http.StatusBadRequest
	case errors.As(err, &cnf):
		return http.StatusNotFound
	case errors.As(err, &cr):
		return http.StatusConflict
	case errors.As(err, &bs):
		return http.StatusServiceUnavailable
	case errors.As(err, &ca):
		return http.StatusServiceUnavailable
	case errors.As(err, &ge):
		return http.StatusInternalServerError
	case errors.As(err, &ee):
		return http.StatusInternalServerError
	case errors.As(err, &se):
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
