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
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
)

// errMockCert is a sentinel error used by mock cert provider failures.
var errMockCert = errors.New("mock cert error")

// newCertServer creates a Server with the given cert provider mock.
func newCertServer(cp cert.CertProvider) *Server {
	return NewServer(nil, nil, "localhost:0", WithCertProvider(cp))
}

// ---------------------------------------------------------------------------
// handleGenerateCert
// ---------------------------------------------------------------------------

func TestHandleGenerateCert_Success(t *testing.T) {
	mock := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error) {
			if req.Username != "alice" {
				t.Fatalf("expected username 'alice', got %q", req.Username)
			}
			if req.TenantID != "tenant-1" {
				t.Fatalf("expected tenant 'tenant-1', got %q", req.TenantID)
			}
			return &cert.CertBundle{
				CertPEM: []byte("-----BEGIN CERTIFICATE-----\nfake-cert\n-----END CERTIFICATE-----\n"),
				KeyPEM:  []byte("-----BEGIN EC PRIVATE KEY-----\nfake-key\n-----END EC PRIVATE KEY-----\n"),
				Serial:  "ABCDEF01",
			}, nil
		},
	}

	srv := newCertServer(mock)
	payload, _ := json.Marshal(generateCertWireRequest{
		Username: "alice",
		TenantID: "tenant-1",
	})

	resp, err := srv.handleGenerateCert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out certBundleWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Serial != "ABCDEF01" {
		t.Fatalf("expected serial 'ABCDEF01', got %q", out.Serial)
	}
	if out.CertPEM == "" {
		t.Fatal("expected non-empty certPEM")
	}
	if out.KeyPEM == "" {
		t.Fatal("expected non-empty keyPEM")
	}
}

func TestHandleGenerateCert_WithTTL(t *testing.T) {
	mock := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, req *cert.GenerateRequest) (*cert.CertBundle, error) {
			if req.TTL != 24*time.Hour {
				t.Fatalf("expected TTL 24h, got %v", req.TTL)
			}
			return &cert.CertBundle{
				CertPEM: []byte("cert"),
				KeyPEM:  []byte("key"),
				Serial:  "SER001",
			}, nil
		},
	}

	srv := newCertServer(mock)
	payload, _ := json.Marshal(generateCertWireRequest{
		Username: "bob",
		TenantID: "tenant-1",
		TTL:      "24h",
	})

	resp, err := srv.handleGenerateCert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out certBundleWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Serial != "SER001" {
		t.Fatalf("expected serial 'SER001', got %q", out.Serial)
	}
}

func TestHandleGenerateCert_InvalidTTL(t *testing.T) {
	mock := &mockCertProvider{}
	srv := newCertServer(mock)
	payload, _ := json.Marshal(generateCertWireRequest{
		Username: "carol",
		TenantID: "tenant-1",
		TTL:      "not-a-duration",
	})

	_, err := srv.handleGenerateCert(context.Background(), payload)
	var ttlErr *InvalidTTLError
	if !errors.As(err, &ttlErr) {
		t.Fatalf("expected InvalidTTLError, got %T: %v", err, err)
	}
}

func TestHandleGenerateCert_ServiceError(t *testing.T) {
	mock := &mockCertProvider{
		generateClientCertFn: func(_ context.Context, _ *cert.GenerateRequest) (*cert.CertBundle, error) {
			return nil, errMockCert
		},
	}

	srv := newCertServer(mock)
	payload, _ := json.Marshal(generateCertWireRequest{Username: "x"})

	_, err := srv.handleGenerateCert(context.Background(), payload)
	if !errors.Is(err, errMockCert) {
		t.Fatalf("expected errMockCert, got %v", err)
	}
}

func TestHandleGenerateCert_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(generateCertWireRequest{Username: "x"})

	_, err := srv.handleGenerateCert(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleGenerateCert_InvalidJSON(t *testing.T) {
	mock := &mockCertProvider{}
	srv := newCertServer(mock)

	_, err := srv.handleGenerateCert(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleExportCert
// ---------------------------------------------------------------------------

func TestHandleExportCert_Success(t *testing.T) {
	expectedP12 := []byte("pkcs12-binary-data")
	mock := &mockCertProvider{
		exportPKCS12Fn: func(_ context.Context, certPEM, keyPEM []byte, passphrase string) ([]byte, error) {
			if string(certPEM) != "fake-cert" {
				t.Fatalf("expected certPEM 'fake-cert', got %q", string(certPEM))
			}
			if string(keyPEM) != "fake-key" {
				t.Fatalf("expected keyPEM 'fake-key', got %q", string(keyPEM))
			}
			if passphrase != "secret123" {
				t.Fatalf("expected passphrase 'secret123', got %q", passphrase)
			}
			return expectedP12, nil
		},
	}

	srv := newCertServer(mock)
	payload, _ := json.Marshal(exportCertWireRequest{
		CertPEM:    "fake-cert",
		KeyPEM:     "fake-key",
		Passphrase: "secret123",
	})

	resp, err := srv.handleExportCert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out exportCertWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(out.PKCS12)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if string(decoded) != string(expectedP12) {
		t.Fatalf("expected decoded PKCS12 %q, got %q", string(expectedP12), string(decoded))
	}
}

func TestHandleExportCert_ServiceError(t *testing.T) {
	mock := &mockCertProvider{
		exportPKCS12Fn: func(_ context.Context, _, _ []byte, _ string) ([]byte, error) {
			return nil, errMockCert
		},
	}

	srv := newCertServer(mock)
	payload, _ := json.Marshal(exportCertWireRequest{CertPEM: "c", KeyPEM: "k"})

	_, err := srv.handleExportCert(context.Background(), payload)
	if !errors.Is(err, errMockCert) {
		t.Fatalf("expected errMockCert, got %v", err)
	}
}

func TestHandleExportCert_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(exportCertWireRequest{CertPEM: "c", KeyPEM: "k"})

	_, err := srv.handleExportCert(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleExportCert_InvalidJSON(t *testing.T) {
	mock := &mockCertProvider{}
	srv := newCertServer(mock)

	_, err := srv.handleExportCert(context.Background(), []byte("invalid"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleRevokeCert
// ---------------------------------------------------------------------------

func TestHandleRevokeCert_Success(t *testing.T) {
	mock := &mockCertProvider{
		revokeCertFn: func(_ context.Context, serial string) (*cert.RevokeResult, error) {
			if serial != "SER-REVOKE" {
				t.Fatalf("expected serial 'SER-REVOKE', got %q", serial)
			}
			return &cert.RevokeResult{Serial: serial, Outcome: cert.RevokeOutcomeRecordUpdated}, nil
		},
	}

	srv := newCertServer(mock)
	payload, _ := json.Marshal(revokeCertWireRequest{Serial: "SER-REVOKE"})

	resp, err := srv.handleRevokeCert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out revokeCertWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Status != "ok" {
		t.Fatalf("expected status 'ok', got %q", out.Status)
	}
	if !out.RecordFound {
		t.Fatal("RecordFound should be true")
	}
}

func TestHandleRevokeCert_DenyBySerial(t *testing.T) {
	mock := &mockCertProvider{
		revokeCertFn: func(_ context.Context, serial string) (*cert.RevokeResult, error) {
			return &cert.RevokeResult{Serial: serial, Outcome: cert.RevokeOutcomeDenyBySerial}, nil
		},
	}

	srv := newCertServer(mock)
	payload, _ := json.Marshal(revokeCertWireRequest{Serial: "peer-issued"})

	resp, err := srv.handleRevokeCert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out revokeCertWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.RecordFound {
		t.Fatal("RecordFound should be false for a deny-by-serial revocation")
	}
}

func TestHandleRevokeCert_ServiceError(t *testing.T) {
	mock := &mockCertProvider{
		revokeCertFn: func(_ context.Context, _ string) (*cert.RevokeResult, error) {
			return nil, errMockCert
		},
	}

	srv := newCertServer(mock)
	payload, _ := json.Marshal(revokeCertWireRequest{Serial: "bad"})

	_, err := srv.handleRevokeCert(context.Background(), payload)
	if !errors.Is(err, errMockCert) {
		t.Fatalf("expected errMockCert, got %v", err)
	}
}

func TestHandleRevokeCert_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(revokeCertWireRequest{Serial: "x"})

	_, err := srv.handleRevokeCert(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleRevokeCert_InvalidJSON(t *testing.T) {
	mock := &mockCertProvider{}
	srv := newCertServer(mock)

	_, err := srv.handleRevokeCert(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleListCerts
// ---------------------------------------------------------------------------

func TestHandleListCerts_Success(t *testing.T) {
	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	mock := &mockCertProvider{
		listCertsFn: func(_ context.Context, tenantID string) ([]*cert.CertRecord, error) {
			if tenantID != "tenant-1" {
				t.Fatalf("expected tenant 'tenant-1', got %q", tenantID)
			}
			return []*cert.CertRecord{
				{
					Serial:    "SER001",
					Username:  "alice",
					TenantID:  "tenant-1",
					IssuedAt:  issued,
					ExpiresAt: expires,
					Revoked:   false,
				},
				{
					Serial:    "SER002",
					Username:  "bob",
					TenantID:  "tenant-1",
					IssuedAt:  issued,
					ExpiresAt: expires,
					Revoked:   true,
				},
			}, nil
		},
	}

	srv := newCertServer(mock)
	payload, _ := json.Marshal(listCertsWireRequest{TenantID: "tenant-1"})

	resp, err := srv.handleListCerts(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out []*certRecordWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 records, got %d", len(out))
	}
	if out[0].Serial != "SER001" {
		t.Fatalf("expected serial 'SER001', got %q", out[0].Serial)
	}
	if out[0].Username != "alice" {
		t.Fatalf("expected username 'alice', got %q", out[0].Username)
	}
	if out[0].Revoked {
		t.Fatal("expected first cert not revoked")
	}
	if !out[1].Revoked {
		t.Fatal("expected second cert revoked")
	}
	// Verify time formatting.
	expectedIssuedAt := issued.Format(time.RFC3339)
	if out[0].IssuedAt != expectedIssuedAt {
		t.Fatalf("expected issuedAt %q, got %q", expectedIssuedAt, out[0].IssuedAt)
	}
	expectedExpiresAt := expires.Format(time.RFC3339)
	if out[0].ExpiresAt != expectedExpiresAt {
		t.Fatalf("expected expiresAt %q, got %q", expectedExpiresAt, out[0].ExpiresAt)
	}
}

func TestHandleListCerts_EmptyResult(t *testing.T) {
	mock := &mockCertProvider{
		listCertsFn: func(_ context.Context, _ string) ([]*cert.CertRecord, error) {
			return []*cert.CertRecord{}, nil
		},
	}

	srv := newCertServer(mock)
	payload, _ := json.Marshal(listCertsWireRequest{TenantID: "empty-tenant"})

	resp, err := srv.handleListCerts(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out []*certRecordWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected 0 records, got %d", len(out))
	}
}

func TestHandleListCerts_ServiceError(t *testing.T) {
	mock := &mockCertProvider{
		listCertsFn: func(_ context.Context, _ string) ([]*cert.CertRecord, error) {
			return nil, errMockCert
		},
	}

	srv := newCertServer(mock)
	payload, _ := json.Marshal(listCertsWireRequest{TenantID: "t"})

	_, err := srv.handleListCerts(context.Background(), payload)
	if !errors.Is(err, errMockCert) {
		t.Fatalf("expected errMockCert, got %v", err)
	}
}

func TestHandleListCerts_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(listCertsWireRequest{TenantID: "t"})

	_, err := srv.handleListCerts(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleListCerts_InvalidJSON(t *testing.T) {
	mock := &mockCertProvider{}
	srv := newCertServer(mock)

	_, err := srv.handleListCerts(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// InvalidTTLError
// ---------------------------------------------------------------------------

func TestInvalidTTLError_Error(t *testing.T) {
	inner := errors.New("time: invalid duration")
	e := &InvalidTTLError{Value: "not-a-duration", Err: inner}
	msg := e.Error()
	if msg != "quic: invalid TTL duration not-a-duration: time: invalid duration" {
		t.Fatalf("unexpected error message: %q", msg)
	}
}

func TestInvalidTTLError_Unwrap(t *testing.T) {
	inner := errors.New("parse error")
	e := &InvalidTTLError{Value: "bad", Err: inner}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return the underlying error")
	}
}

func TestInvalidTTLError_ErrorsAs(t *testing.T) {
	inner := errors.New("invalid")
	e := &InvalidTTLError{Value: "xyz", Err: inner}
	var target *InvalidTTLError
	if !errors.As(e, &target) {
		t.Fatal("errors.As failed to match *InvalidTTLError")
	}
	if target.Value != "xyz" {
		t.Fatalf("expected Value 'xyz', got %q", target.Value)
	}
}

// ---------------------------------------------------------------------------
// certRecordToWireResponse
// ---------------------------------------------------------------------------

func TestCertRecordToWireResponse_NilInput(t *testing.T) {
	result := certRecordToWireResponse(nil)
	if result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestCertRecordToWireResponse_AllFields(t *testing.T) {
	issued := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	expires := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	rec := &cert.CertRecord{
		Serial:    "SER-123",
		Username:  "testuser",
		TenantID:  "tenant-x",
		IssuedAt:  issued,
		ExpiresAt: expires,
		Revoked:   true,
	}
	result := certRecordToWireResponse(rec)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Serial != "SER-123" {
		t.Fatalf("expected serial 'SER-123', got %q", result.Serial)
	}
	if result.Username != "testuser" {
		t.Fatalf("expected username 'testuser', got %q", result.Username)
	}
	if result.TenantID != "tenant-x" {
		t.Fatalf("expected tenantID 'tenant-x', got %q", result.TenantID)
	}
	if !result.Revoked {
		t.Fatal("expected revoked to be true")
	}
	expectedIssuedAt := issued.Format(time.RFC3339)
	if result.IssuedAt != expectedIssuedAt {
		t.Fatalf("expected issuedAt %q, got %q", expectedIssuedAt, result.IssuedAt)
	}
	expectedExpiresAt := expires.Format(time.RFC3339)
	if result.ExpiresAt != expectedExpiresAt {
		t.Fatalf("expected expiresAt %q, got %q", expectedExpiresAt, result.ExpiresAt)
	}
}
