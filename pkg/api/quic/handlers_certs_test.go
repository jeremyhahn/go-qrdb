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
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// errMockCert is a sentinel error used by mock cert provider failures.
var errMockCert = errors.New("mock cert error")

// newCertServer creates a Server with the given cert provider mock.
func newCertServer(cp cert.CertProvider) *Server {
	return NewServer(nil, nil, "localhost:0", WithCertProvider(cp))
}

// fakeCertEnroll is a configurable, faithful service.CertEnrollmentServicer
// double: each method returns exactly what was configured, with no
// enrolment logic of its own for the handler tests to accidentally depend on.
type fakeCertEnroll struct {
	signFn   func(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error)
	enrollFn func(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error)
	addFn    func(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error)
	listFn   func(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error)
	removeFn func(ctx context.Context, tenantID, fingerprint string) error
	delegFn  func(ctx context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error)

	policyPutFn    func(ctx context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error)
	policyListFn   func(ctx context.Context, tenantID string) ([]delegation.Policy, error)
	policyDeleteFn func(ctx context.Context, tenantID, principal string) error
}

var _ service.CertEnrollmentServicer = (*fakeCertEnroll)(nil)

func (f *fakeCertEnroll) IssueDelegation(ctx context.Context, req *service.IssueDelegationRequest) (*service.IssuedDelegation, error) {
	return f.delegFn(ctx, req)
}

func (f *fakeCertEnroll) SignCertificate(ctx context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
	return f.signFn(ctx, req)
}

func (f *fakeCertEnroll) EnrollCertificate(ctx context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
	return f.enrollFn(ctx, req)
}

func (f *fakeCertEnroll) AddTrustAnchors(ctx context.Context, tenantID string, bundlePEM []byte) ([]trustanchor.Anchor, error) {
	return f.addFn(ctx, tenantID, bundlePEM)
}

func (f *fakeCertEnroll) ListTrustAnchors(ctx context.Context, tenantID string) ([]trustanchor.Anchor, error) {
	return f.listFn(ctx, tenantID)
}

func (f *fakeCertEnroll) RemoveTrustAnchor(ctx context.Context, tenantID, fingerprint string) error {
	return f.removeFn(ctx, tenantID, fingerprint)
}

// newCertEnrollServer creates a Server with the given cert enrolment service
// mock, and no auth middleware, matching newCertServer's posture.
func newCertEnrollServer(ce service.CertEnrollmentServicer) *Server {
	return NewServer(nil, nil, "localhost:0", WithCertEnrollment(ce))
}

// ---------------------------------------------------------------------------
// handleIssueCert
// ---------------------------------------------------------------------------

func TestHandleIssueCert_Success(t *testing.T) {
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	fake := &fakeCertEnroll{
		signFn: func(_ context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			if req.Username != "alice" {
				t.Fatalf("expected username 'alice', got %q", req.Username)
			}
			return &service.EnrolledCertificate{
				UserID:     7,
				Username:   "alice",
				TenantID:   req.TenantID,
				CertPEM:    []byte("-----BEGIN CERTIFICATE-----\nfake-cert\n-----END CERTIFICATE-----\n"),
				Serial:     "ABCDEF01",
				Thumbprint: "thumb-1",
				NotAfter:   notAfter,
			}, nil
		},
	}

	srv := newCertEnrollServer(fake)
	payload, _ := json.Marshal(issueCertWireRequest{Username: "alice", CSRPEM: "csr-pem"})

	resp, err := srv.handleIssueCert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out enrolledCertWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.UserID != 7 {
		t.Errorf("expected userID 7, got %d", out.UserID)
	}
	if out.Username != "alice" {
		t.Errorf("expected username 'alice', got %q", out.Username)
	}
	if out.Serial != "ABCDEF01" {
		t.Errorf("expected serial 'ABCDEF01', got %q", out.Serial)
	}
	if out.Thumbprint != "thumb-1" {
		t.Errorf("expected thumbprint 'thumb-1', got %q", out.Thumbprint)
	}
	if out.CertPEM == "" {
		t.Error("expected non-empty certPEM")
	}
	if !out.NotAfter.Equal(notAfter) {
		t.Errorf("expected notAfter %v, got %v", notAfter, out.NotAfter)
	}
}

func TestHandleIssueCert_WithTTL(t *testing.T) {
	fake := &fakeCertEnroll{
		signFn: func(_ context.Context, req *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			if req.TTL != 24*time.Hour {
				t.Fatalf("expected TTL 24h, got %v", req.TTL)
			}
			return &service.EnrolledCertificate{Serial: "SER001"}, nil
		},
	}

	srv := newCertEnrollServer(fake)
	payload, _ := json.Marshal(issueCertWireRequest{Username: "bob", CSRPEM: "csr-pem", TTL: "24h"})

	resp, err := srv.handleIssueCert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var out enrolledCertWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Serial != "SER001" {
		t.Fatalf("expected serial 'SER001', got %q", out.Serial)
	}
}

func TestHandleIssueCert_InvalidTTL(t *testing.T) {
	srv := newCertEnrollServer(&fakeCertEnroll{})
	payload, _ := json.Marshal(issueCertWireRequest{Username: "carol", CSRPEM: "csr-pem", TTL: "not-a-duration"})

	_, err := srv.handleIssueCert(context.Background(), payload)
	var ttlErr *InvalidTTLError
	if !errors.As(err, &ttlErr) {
		t.Fatalf("expected InvalidTTLError, got %T: %v", err, err)
	}
}

func TestHandleIssueCert_ServiceError(t *testing.T) {
	fake := &fakeCertEnroll{
		signFn: func(context.Context, *service.SignCertificateRequest) (*service.EnrolledCertificate, error) {
			return nil, errMockCert
		},
	}

	srv := newCertEnrollServer(fake)
	payload, _ := json.Marshal(issueCertWireRequest{Username: "x", CSRPEM: "csr-pem"})

	_, err := srv.handleIssueCert(context.Background(), payload)
	if !errors.Is(err, errMockCert) {
		t.Fatalf("expected errMockCert, got %v", err)
	}
}

func TestHandleIssueCert_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(issueCertWireRequest{Username: "x", CSRPEM: "csr-pem"})

	_, err := srv.handleIssueCert(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleIssueCert_InvalidJSON(t *testing.T) {
	srv := newCertEnrollServer(&fakeCertEnroll{})

	_, err := srv.handleIssueCert(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// handleEnrollCert
// ---------------------------------------------------------------------------

func TestHandleEnrollCert_Success(t *testing.T) {
	fake := &fakeCertEnroll{
		enrollFn: func(_ context.Context, req *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
			if req.Username != "dave" {
				t.Fatalf("expected username 'dave', got %q", req.Username)
			}
			return &service.EnrolledCertificate{Username: "dave", Thumbprint: "thumb-2"}, nil
		},
	}

	srv := newCertEnrollServer(fake)
	payload, _ := json.Marshal(enrollCertWireRequest{Username: "dave", CertPEM: "leaf-pem"})

	resp, err := srv.handleEnrollCert(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out enrolledCertWireResponse
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out.Thumbprint != "thumb-2" {
		t.Fatalf("expected thumbprint 'thumb-2', got %q", out.Thumbprint)
	}
	// A bring-your-own certificate was not issued by this cluster.
	if out.Serial != "" {
		t.Errorf("expected empty serial for a BYOC enrolment, got %q", out.Serial)
	}
}

func TestHandleEnrollCert_ServiceError(t *testing.T) {
	fake := &fakeCertEnroll{
		enrollFn: func(context.Context, *service.EnrollCertificateRequest) (*service.EnrolledCertificate, error) {
			return nil, errMockCert
		},
	}

	srv := newCertEnrollServer(fake)
	payload, _ := json.Marshal(enrollCertWireRequest{Username: "x", CertPEM: "c"})

	_, err := srv.handleEnrollCert(context.Background(), payload)
	if !errors.Is(err, errMockCert) {
		t.Fatalf("expected errMockCert, got %v", err)
	}
}

func TestHandleEnrollCert_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(enrollCertWireRequest{Username: "x", CertPEM: "c"})

	_, err := srv.handleEnrollCert(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
	}
}

func TestHandleEnrollCert_InvalidJSON(t *testing.T) {
	srv := newCertEnrollServer(&fakeCertEnroll{})

	_, err := srv.handleEnrollCert(context.Background(), []byte("{bad"))
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

func TestHandleListCerts_EmptyTenantIsCallers(t *testing.T) {
	var listed string
	mock := &mockCertProvider{
		listCertsFn: func(_ context.Context, tenantID string) ([]*cert.CertRecord, error) {
			listed = tenantID
			return []*cert.CertRecord{}, nil
		},
	}

	srv := newCertServer(mock)
	payload, _ := json.Marshal(listCertsWireRequest{})

	ctx := tenant.WithTenantID(context.Background(), "acme")
	if _, err := srv.handleListCerts(ctx, payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if listed != "acme" {
		t.Fatalf("listed tenant = %q, want the caller's own %q", listed, "acme")
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
