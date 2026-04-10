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
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
)

// ---------------------------------------------------------------------------
// Cert wire types
// ---------------------------------------------------------------------------

// generateCertWireRequest is the JSON wire payload for cert.Generate.
type generateCertWireRequest struct {
	Username string `json:"username"`
	TenantID string `json:"tenantID"`
	TTL      string `json:"ttl"`
}

// exportCertWireRequest is the JSON wire payload for cert.Export. The client
// must supply the PEM-encoded certificate and key received from the generate
// endpoint, as the server does not retain private keys.
type exportCertWireRequest struct {
	CertPEM    string `json:"certPEM"`
	KeyPEM     string `json:"keyPEM"`
	Passphrase string `json:"passphrase"`
}

// revokeCertWireRequest is the JSON wire payload for cert.Revoke.
type revokeCertWireRequest struct {
	Serial string `json:"serial"`
}

// listCertsWireRequest is the JSON wire payload for cert.List.
type listCertsWireRequest struct {
	TenantID string `json:"tenantID"`
}

// certBundleWireResponse is the JSON wire representation of a generated
// certificate bundle containing the PEM-encoded certificate, key, and
// serial number.
type certBundleWireResponse struct {
	CertPEM string `json:"certPEM"`
	KeyPEM  string `json:"keyPEM"`
	Serial  string `json:"serial"`
}

// exportCertWireResponse is the JSON wire representation of a PKCS#12
// export. The PKCS12 field contains the base64-encoded archive bytes.
type exportCertWireResponse struct {
	PKCS12 string `json:"pkcs12"`
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

// handleGenerateCert unmarshals a JSON generate cert request, creates a
// client certificate signed by the internal CA, and returns a serialized
// JSON certificate bundle response.
func (s *Server) handleGenerateCert(ctx context.Context, payload []byte) ([]byte, error) {
	if s.certProv == nil {
		return nil, &ServiceNotConfiguredError{Service: "cert"}
	}

	var req generateCertWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	genReq := &cert.GenerateRequest{
		Username: req.Username,
		TenantID: req.TenantID,
	}

	if req.TTL != "" {
		ttl, err := time.ParseDuration(req.TTL)
		if err != nil {
			return nil, &InvalidTTLError{Value: req.TTL, Err: err}
		}
		genReq.TTL = ttl
	}

	bundle, err := s.certProv.GenerateClientCert(ctx, genReq)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(certBundleWireResponse{
		CertPEM: string(bundle.CertPEM),
		KeyPEM:  string(bundle.KeyPEM),
		Serial:  bundle.Serial,
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleExportCert unmarshals a JSON export cert request, exports the
// certificate and private key as a PKCS#12 archive for browser import,
// and returns a serialized JSON response containing the base64-encoded
// archive.
func (s *Server) handleExportCert(ctx context.Context, payload []byte) ([]byte, error) {
	if s.certProv == nil {
		return nil, &ServiceNotConfiguredError{Service: "cert"}
	}

	var req exportCertWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	p12Bytes, err := s.certProv.ExportPKCS12(
		ctx,
		[]byte(req.CertPEM),
		[]byte(req.KeyPEM),
		req.Passphrase,
	)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(exportCertWireResponse{
		PKCS12: base64.StdEncoding.EncodeToString(p12Bytes),
	})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleRevokeCert unmarshals a JSON revoke cert request, marks the
// certificate serial as revoked, and returns a serialized status response.
// Revoked certificates are rejected during mTLS verification.
func (s *Server) handleRevokeCert(ctx context.Context, payload []byte) ([]byte, error) {
	if s.certProv == nil {
		return nil, &ServiceNotConfiguredError{Service: "cert"}
	}

	var req revokeCertWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.certProv.RevokeCert(ctx, req.Serial); err != nil {
		return nil, err
	}

	out, err := json.Marshal(statusWireResponse{Status: "ok"})
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
