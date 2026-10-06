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
	"encoding/json"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// Cert request/response types
// ---------------------------------------------------------------------------

// issueCertRequest is the JSON payload for POST /certs/issue: a certificate
// signing request for an existing account of the caller's own tenant.
type issueCertRequest struct {
	Username string `json:"username"`
	CSRPEM   string `json:"csrPEM"`
	TTL      string `json:"ttl,omitempty"`
}

// enrollCertRequest is the JSON payload for POST /certs/enroll: a
// bring-your-own certificate (leaf first) and optional intermediates for an
// existing account of the caller's own tenant.
type enrollCertRequest struct {
	Username string `json:"username"`
	CertPEM  string `json:"certPEM"`
	ChainPEM string `json:"chainPEM,omitempty"`
}

// enrolledCertResponse is a certificate now bound to an account. There is no
// key: the requester keeps its own.
type enrolledCertResponse struct {
	UserID     uint64    `json:"userID,string"`
	Username   string    `json:"username"`
	TenantID   string    `json:"tenantID"`
	CertPEM    string    `json:"certPEM"`
	Serial     string    `json:"serial,omitempty"`
	Thumbprint string    `json:"thumbprint"`
	NotAfter   time.Time `json:"notAfter"`
}

func enrolledToResponse(e *service.EnrolledCertificate) enrolledCertResponse {
	return enrolledCertResponse{
		UserID:     e.UserID,
		Username:   e.Username,
		TenantID:   e.TenantID,
		CertPEM:    string(e.CertPEM),
		Serial:     e.Serial,
		Thumbprint: e.Thumbprint,
		NotAfter:   e.NotAfter,
	}
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
// because two of these verbs name their target in the request: the tenantID
// in the list query and, for revoke, a serial that resolves to a tenant only
// by looking it up. Issue and enroll name no tenant: they act on the caller's
// own (see handlers_certs' callerTenant).
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

// callerTenant is the tenant every certificate enrolment verb acts on: the
// caller's own. The certs verbs are ScopeSelfStrict, so no request names
// another.
func callerTenant(ctx context.Context) string {
	return tenantid.Normalize(tenant.FromContext(ctx))
}

// handleIssueCert handles POST /certs/issue -- issues a certificate from a
// CSR for an EXISTING account of the caller's own tenant, signed by that
// tenant's own Key B CA, and binds it to the account. The requester keeps its
// private key; only the certificate is returned.
func (s *Server) handleIssueCert(w http.ResponseWriter, r *http.Request) {
	if s.certEnrollment == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "cert enrollment"})
		return
	}
	var req issueCertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	var ttl time.Duration
	if req.TTL != "" {
		d, err := time.ParseDuration(req.TTL)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "ttl", Err: err})
			return
		}
		ttl = d
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	tenantID := callerTenant(ctx)
	if err := s.authorizeCertTenant(ctx, tenantID, permission.CertsIssue); err != nil {
		writeAuthzErrorJSON(w, err)
		return
	}
	enrolled, err := s.certEnrollment.SignCertificate(ctx, &service.SignCertificateRequest{
		TenantID: tenantID,
		Username: req.Username,
		CSRPEM:   []byte(req.CSRPEM),
		TTL:      ttl,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(enrolledToResponse(enrolled))
}

// handleEnrollCert handles POST /certs/enroll -- binds a bring-your-own
// certificate to an EXISTING account of the caller's own tenant, after
// verifying it chains to one of the tenant's registered trust anchors and
// authenticates as exactly that account.
func (s *Server) handleEnrollCert(w http.ResponseWriter, r *http.Request) {
	if s.certEnrollment == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "cert enrollment"})
		return
	}
	var req enrollCertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	tenantID := callerTenant(ctx)
	if err := s.authorizeCertTenant(ctx, tenantID, permission.CertsEnroll); err != nil {
		writeAuthzErrorJSON(w, err)
		return
	}
	enrolled, err := s.certEnrollment.EnrollCertificate(ctx, &service.EnrollCertificateRequest{
		TenantID: tenantID,
		Username: req.Username,
		CertPEM:  []byte(req.CertPEM),
		ChainPEM: []byte(req.ChainPEM),
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(enrolledToResponse(enrolled))
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
