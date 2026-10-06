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

package quic

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// Cert wire types
// ---------------------------------------------------------------------------

// issueCertWireRequest is the JSON wire payload for cert.Issue: a CSR for an
// existing account of the caller's own tenant.
type issueCertWireRequest struct {
	Username string `json:"username"`
	CSRPEM   string `json:"csrPEM"`
	TTL      string `json:"ttl,omitempty"`
}

// enrollCertWireRequest is the JSON wire payload for cert.Enroll: a
// bring-your-own certificate (leaf first) and optional intermediates.
type enrollCertWireRequest struct {
	Username string `json:"username"`
	CertPEM  string `json:"certPEM"`
	ChainPEM string `json:"chainPEM,omitempty"`
}

// enrolledCertWireResponse is a certificate now bound to an account. There is
// no key: the requester keeps its own.
type enrolledCertWireResponse struct {
	UserID     uint64    `json:"userID,string"`
	Username   string    `json:"username"`
	TenantID   string    `json:"tenantID"`
	CertPEM    string    `json:"certPEM"`
	Serial     string    `json:"serial,omitempty"`
	Thumbprint string    `json:"thumbprint"`
	NotAfter   time.Time `json:"notAfter"`
}

// revokeCertWireRequest is the JSON wire payload for cert.Revoke.
type revokeCertWireRequest struct {
	Serial string `json:"serial"`
}

// revokeCertWireResponse is the JSON wire representation of a revocation
// outcome. RecordFound distinguishes a certificate this node holds a record
// for from a deny-by-serial revocation of one it does not -- both are
// successes.
type revokeCertWireResponse struct {
	Status      string `json:"status"`
	RecordFound bool   `json:"recordFound"`
}

// listCertsWireRequest is the JSON wire payload for cert.List.
type listCertsWireRequest struct {
	TenantID string `json:"tenantID"`
}

// certRecordWireResponse is the JSON wire representation of an issued
// certificate record.
type certRecordWireResponse struct {
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

// ---------------------------------------------------------------------------
// Cert tenancy gates
//
// The QUIC dispatcher authorizes each opcode's declared permission against the
// CALLER's tenant. It cannot authorize the tenant the REQUEST names, and three
// of these four verbs name theirs. The gates below ask that second question
// through the shared home of the rule (pkg/auth/middleware/cert_authz.go),
// which the REST and gRPC surfaces call too.
//
// This transport matters as much as the others: it reaches the SAME
// process-wide CertProvider, so a gate on REST alone would be a gate with a
// door beside it.
//
// With no auth middleware wired the gates are no-ops.
// ---------------------------------------------------------------------------

// authorizeCertTenant gates a certificate operation on the tenant the request
// names rather than on the tenant the caller happens to be in.
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

// callerTenant is the tenant every certificate enrolment verb acts on: the
// caller's own. The certs verbs are ScopeSelfStrict, so no request names
// another.
func callerTenant(ctx context.Context) string {
	return tenantid.Normalize(tenant.FromContext(ctx))
}

func marshalEnrolled(e *service.EnrolledCertificate) ([]byte, error) {
	out, err := json.Marshal(enrolledCertWireResponse{
		UserID:     e.UserID,
		Username:   e.Username,
		TenantID:   e.TenantID,
		CertPEM:    string(e.CertPEM),
		Serial:     e.Serial,
		Thumbprint: e.Thumbprint,
		NotAfter:   e.NotAfter,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleIssueCert issues a certificate from a CSR for an existing account of
// the caller's own tenant, signed by that tenant's own Key B CA, and binds
// it. The requester keeps its private key.
func (s *Server) handleIssueCert(ctx context.Context, payload []byte) ([]byte, error) {
	if s.certEnroll == nil {
		return nil, &ServiceNotConfiguredError{Service: "cert enrollment"}
	}
	var req issueCertWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	var ttl time.Duration
	if req.TTL != "" {
		d, err := time.ParseDuration(req.TTL)
		if err != nil {
			return nil, &InvalidTTLError{Value: req.TTL, Err: err}
		}
		ttl = d
	}
	tenantID := callerTenant(ctx)
	if err := s.authorizeCertTenant(ctx, tenantID, permission.CertsIssue); err != nil {
		return nil, err
	}
	enrolled, err := s.certEnroll.SignCertificate(ctx, &service.SignCertificateRequest{
		TenantID: tenantID,
		Username: req.Username,
		CSRPEM:   []byte(req.CSRPEM),
		TTL:      ttl,
	})
	if err != nil {
		return nil, err
	}
	return marshalEnrolled(enrolled)
}

// handleEnrollCert binds a bring-your-own certificate to an existing account
// of the caller's own tenant, verified against the tenant's trust anchors.
func (s *Server) handleEnrollCert(ctx context.Context, payload []byte) ([]byte, error) {
	if s.certEnroll == nil {
		return nil, &ServiceNotConfiguredError{Service: "cert enrollment"}
	}
	var req enrollCertWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	tenantID := callerTenant(ctx)
	if err := s.authorizeCertTenant(ctx, tenantID, permission.CertsEnroll); err != nil {
		return nil, err
	}
	enrolled, err := s.certEnroll.EnrollCertificate(ctx, &service.EnrollCertificateRequest{
		TenantID: tenantID,
		Username: req.Username,
		CertPEM:  []byte(req.CertPEM),
		ChainPEM: []byte(req.ChainPEM),
	})
	if err != nil {
		return nil, err
	}
	return marshalEnrolled(enrolled)
}

// handleRevokeCert unmarshals a JSON revoke cert request, marks the
// certificate serial as revoked, and returns a serialized status response.
// Enforcement is on the authentication path, per request -- which matters most
// here: a QUIC client holds one long-lived connection, so a handshake-time
// check would leave a revoked peer authorised for the life of that connection.
//
// The response's recordFound field reports whether a local record matched,
// distinguishing a mistyped serial from a deliberate deny-by-serial
// revocation without either being reported as an error -- both are
// successes.
func (s *Server) handleRevokeCert(ctx context.Context, payload []byte) ([]byte, error) {
	if s.certProv == nil {
		return nil, &ServiceNotConfiguredError{Service: "cert"}
	}

	var req revokeCertWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.authorizeCertRevoke(ctx, req.Serial); err != nil {
		return nil, err
	}

	result, err := s.certProv.RevokeCert(ctx, req.Serial)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(revokeCertWireResponse{Status: "ok", RecordFound: result.RecordFound()})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleListCerts unmarshals a JSON list certs request, retrieves all
// issued certificate records for the specified tenant, and returns a
// serialized JSON response.
func (s *Server) handleListCerts(ctx context.Context, payload []byte) ([]byte, error) {
	if s.certProv == nil {
		return nil, &ServiceNotConfiguredError{Service: "cert"}
	}

	var req listCertsWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.authorizeCertTenant(ctx, req.TenantID, permission.CertsList); err != nil {
		return nil, err
	}

	records, err := s.certProv.ListCerts(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}

	resp := make([]*certRecordWireResponse, len(records))
	for i, rec := range records {
		resp[i] = certRecordToWireResponse(rec)
	}

	out, err := json.Marshal(resp)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Cert conversion helpers
// ---------------------------------------------------------------------------

// certRecordToWireResponse converts a cert.CertRecord domain object to its
// JSON wire representation.
func certRecordToWireResponse(rec *cert.CertRecord) *certRecordWireResponse {
	if rec == nil {
		return nil
	}

	return &certRecordWireResponse{
		Serial:    rec.Serial,
		Username:  rec.Username,
		TenantID:  rec.TenantID,
		IssuedAt:  rec.IssuedAt.Format(time.RFC3339),
		ExpiresAt: rec.ExpiresAt.Format(time.RFC3339),
		Revoked:   rec.Revoked,
	}
}

// ---------------------------------------------------------------------------
// Cert error types
// ---------------------------------------------------------------------------

// InvalidTTLError is returned when a TTL duration string cannot be parsed.
type InvalidTTLError struct {
	Value string
	Err   error
}

// Error returns a human-readable description.
func (e *InvalidTTLError) Error() string {
	return "quic: invalid TTL duration " + e.Value + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *InvalidTTLError) Unwrap() error {
	return e.Err
}
