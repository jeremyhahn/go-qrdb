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

//go:build !integration

package rest

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockCeremonyService (local to this file)
// ---------------------------------------------------------------------------

type coverageCeremonyService struct {
	initResult       *service.CeremonyInitResult
	initErr          error
	statusResult     *service.CeremonyStatusInfo
	statusErr        error
	beginResult      *service.CeremonyChallenge
	beginErr         error
	completeResult   *service.CeremonyClaimCertResult
	completeErr      error
	claimShareResult []byte
	claimShareErr    error
	signCSRResult    *service.CeremonySignCSRResult
	signCSRErr       error
}

var _ service.CeremonyServicer = (*coverageCeremonyService)(nil)

func newCoverageCeremonyService() *coverageCeremonyService {
	return &coverageCeremonyService{
		initResult: &service.CeremonyInitResult{
			SPKIPin:    "pin123",
			CACertPEM:  []byte("ca-cert"),
			TLSCertPEM: []byte("tls-cert"),
			SOCertPEM:  []byte("so-cert"),
			SOKeyPEM:   []byte("so-key"),
			State:      "initialized",
			ShareCount: 3,
		},
		statusResult:     &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "pin123"},
		beginResult:      &service.CeremonyChallenge{NonceHex: "deadbeef", ExpiresAt: time.Now().Add(time.Minute)},
		completeResult:   &service.CeremonyClaimCertResult{CertPEM: []byte("officer-cert"), CACertPEM: []byte("ca-cert")},
		claimShareResult: []byte("shamir-share-bytes"),
		signCSRResult:    &service.CeremonySignCSRResult{CertPEM: []byte("signed-cert")},
	}
}

func (m *coverageCeremonyService) CeremonyInitialize(_ context.Context, _ ceremony.CeremonyCA, _ *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	return m.initResult, m.initErr
}
func (m *coverageCeremonyService) CeremonyStatus(_ context.Context) (*service.CeremonyStatusInfo, error) {
	return m.statusResult, m.statusErr
}
func (m *coverageCeremonyService) CeremonyBeginClaimCert(_ context.Context, _ string) (*service.CeremonyChallenge, error) {
	return m.beginResult, m.beginErr
}
func (m *coverageCeremonyService) CeremonyCompleteClaimCert(_ context.Context, _, _ string, _ []byte) (*service.CeremonyClaimCertResult, error) {
	return m.completeResult, m.completeErr
}
func (m *coverageCeremonyService) CeremonyClaimShare(_ context.Context, _ string) ([]byte, error) {
	return m.claimShareResult, m.claimShareErr
}
func (m *coverageCeremonyService) CeremonySignCSR(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
	return m.signCSRResult, m.signCSRErr
}
func (m *coverageCeremonyService) CeremonyClaimShareBegin(_ context.Context, _ string) (*service.CeremonyCustodianChallenge, error) {
	return nil, nil
}
func (m *coverageCeremonyService) CeremonyClaimShareComplete(_ context.Context, _, _ string, _ []byte) (*custodian.Custodian, error) {
	return nil, nil
}
func (m *coverageCeremonyService) CeremonySubmitUnsealShare(_ context.Context, _, _ string, _, _ []byte) (*service.CeremonyUnsealShareResult, error) {
	return nil, nil
}

func (m *coverageCeremonyService) CeremonyEnrollExternalOfficer(_ context.Context, _ *service.CeremonyEnrollExternalOfficerRequest) (*service.CeremonyEnrollExternalOfficerResponse, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// coverageCeremonyCA implements ceremony.CeremonyCA for testing.
// ---------------------------------------------------------------------------

type coverageCeremonyCA struct{}

var _ ceremony.CeremonyCA = (*coverageCeremonyCA)(nil)

func (m *coverageCeremonyCA) SignCSR(_ context.Context, _ []byte, _ *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, nil
}
func (m *coverageCeremonyCA) CACertificate() (*x509.Certificate, error) {
	return nil, nil
}
func (m *coverageCeremonyCA) CABundle() ([]byte, error) {
	return []byte("ca-bundle"), nil
}
func (m *coverageCeremonyCA) IssueCertificate(_ context.Context, _ *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return &ceremony.IssuedCert{
		CertPEM: []byte("cert-pem"),
		KeyPEM:  []byte("key-pem"),
	}, nil
}

// ---------------------------------------------------------------------------
// coverageCredentialService implements service.CredentialServicer for testing.
// ---------------------------------------------------------------------------

type coverageCredentialService struct {
	submitFn          func(ctx context.Context, name string, value []byte) error
	strategyFn        func() string
	deleteFn          func(ctx context.Context, name string) error
	listFn            func(ctx context.Context) ([]string, error)
	rotateFn          func(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)
	retrieveVersionFn func(ctx context.Context, name string, version uint64) ([]byte, error)
	listVersionsFn    func(ctx context.Context, name string) ([]credentials.CredentialVersion, error)
	gcFn              func(ctx context.Context, name string) (int, error)
}

var _ service.CredentialServicer = (*coverageCredentialService)(nil)

func newCoverageCredentialService() *coverageCredentialService {
	return &coverageCredentialService{
		strategyFn: func() string { return "manual" },
	}
}

func (m *coverageCredentialService) CredentialSubmit(ctx context.Context, name string, value []byte) error {
	if m.submitFn != nil {
		return m.submitFn(ctx, name, value)
	}
	return nil
}
func (m *coverageCredentialService) CredentialStrategy() string {
	if m.strategyFn != nil {
		return m.strategyFn()
	}
	return "manual"
}
func (m *coverageCredentialService) CredentialDelete(ctx context.Context, name string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, name)
	}
	return nil
}
func (m *coverageCredentialService) CredentialStore(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (m *coverageCredentialService) CredentialRetrieve(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}
func (m *coverageCredentialService) CredentialList(ctx context.Context) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []string{}, nil
}
func (m *coverageCredentialService) CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	if m.rotateFn != nil {
		return m.rotateFn(ctx, name, opts)
	}
	return &credentials.RotateResult{Name: name, PreviousVersion: 0, NewVersion: 1}, nil
}
func (m *coverageCredentialService) CredentialRetrieveVersion(ctx context.Context, name string, version uint64) ([]byte, error) {
	if m.retrieveVersionFn != nil {
		return m.retrieveVersionFn(ctx, name, version)
	}
	return nil, nil
}
func (m *coverageCredentialService) CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error) {
	if m.listVersionsFn != nil {
		return m.listVersionsFn(ctx, name)
	}
	return nil, nil
}
func (m *coverageCredentialService) CredentialGCExpired(ctx context.Context, name string) (int, error) {
	if m.gcFn != nil {
		return m.gcFn(ctx, name)
	}
	return 0, nil
}

// ---------------------------------------------------------------------------
// mockDatabaseServiceWithEtcd overrides UpdateEtcd to return a non-nil
// DatabaseInfo, avoiding nil dereference in databaseInfoToResponse.
// ---------------------------------------------------------------------------

type mockDatabaseServiceWithEtcd struct {
	mockDatabaseService
	updateEtcdResult *database.DatabaseInfo
}

func (m *mockDatabaseServiceWithEtcd) UpdateEtcd(_ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.updateEtcdResult, nil
}

// UpdateEtcdForTenant overrides the embedded mock's promoted method so it
// routes to this type's UpdateEtcd (Go embedding is not virtual dispatch).
func (m *mockDatabaseServiceWithEtcd) UpdateEtcdForTenant(_ context.Context, name string, cfg *database.EtcdConfig) (*database.DatabaseInfo, error) {
	return m.UpdateEtcd(name, cfg)
}

// ---------------------------------------------------------------------------
// SQLQueryError – Error and Unwrap
// ---------------------------------------------------------------------------

func TestSQLQueryError_ErrorAndUnwrap(t *testing.T) {
	inner := &service.KeyNotFoundError{Key: "x"}
	e := &SQLQueryError{Query: "SELECT 1", Err: inner}

	msg := e.Error()
	if !strings.Contains(msg, "sql query failed") {
		t.Errorf("Error() = %q, want contains 'sql query failed'", msg)
	}

	unwrapped := e.Unwrap()
	if unwrapped != inner {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, inner)
	}
}

// ---------------------------------------------------------------------------
// Server options: WithEmbedding, WithRAG, WithOnEtcdUpdated,
//                WithCeremonyService, WithCredentialService, WithCeremonyCA
// ---------------------------------------------------------------------------

func TestServerOption_WithEmbedding(t *testing.T) {
	embSvc := &mockEmbeddingService{}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithEmbedding(embSvc))
	if s.embeddingSvc != embSvc {
		t.Error("embeddingSvc not set")
	}
}

func TestServerOption_WithRAG(t *testing.T) {
	ragSvc := &mockRAGService{}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithRAG(ragSvc))
	if s.ragSvc != ragSvc {
		t.Error("ragSvc not set")
	}
}

func TestServerOption_WithOnEtcdUpdated(t *testing.T) {
	called := false
	fn := func(_ string, _ *database.EtcdConfig) error {
		called = true
		return nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithOnEtcdUpdated(fn))
	if s.onEtcdUpdated == nil {
		t.Fatal("onEtcdUpdated not set")
	}
	_ = s.onEtcdUpdated("db", &database.EtcdConfig{})
	if !called {
		t.Error("onEtcdUpdated callback not called")
	}
}

func TestServerOption_WithCeremonyService(t *testing.T) {
	svc := newCoverageCeremonyService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithCeremonyService(svc))
	if s.ceremonySvc != svc {
		t.Error("ceremonySvc not set")
	}
}

func TestServerOption_WithCredentialService(t *testing.T) {
	svc := newCoverageCredentialService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithCredentialService(svc))
	if s.credentialSvc != svc {
		t.Error("credentialSvc not set")
	}
}

func TestServerOption_WithCeremonyCA(t *testing.T) {
	ca := &coverageCeremonyCA{}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithCeremonyCA(ca))
	if s.ceremonyCA != ca {
		t.Error("ceremonyCA not set")
	}
}

// ---------------------------------------------------------------------------
// Server.Stop with nil server (no panic)
// ---------------------------------------------------------------------------

func TestServer_Stop_NilServer(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	// Must not panic.
	s.Stop()
}

// ---------------------------------------------------------------------------
// Ceremony handlers: nil service → 501 Not Implemented
// ---------------------------------------------------------------------------

func TestHandleCeremonyInitialize_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCeremonyInitialize_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()),
		WithCeremonyCA(&coverageCeremonyCA{}))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCeremonyInitialize_Success(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()),
		WithCeremonyCA(&coverageCeremonyCA{}))
	body := `{"so_pin":"pin","user_pin":"upin","threshold":2}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleCeremonyInitialize_ServiceError(t *testing.T) {
	svc := newCoverageCeremonyService()
	svc.initErr = &service.KeyNotFoundError{Key: "k"}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(svc),
		WithCeremonyCA(&coverageCeremonyCA{}))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleCeremonyStatus_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/v1/ceremony/status", nil)
	rec := httptest.NewRecorder()
	s.handleCeremonyStatus(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCeremonyStatus_Success(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()))
	req := httptest.NewRequest(http.MethodGet, "/v1/ceremony/status", nil)
	rec := httptest.NewRecorder()
	s.handleCeremonyStatus(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleCeremonyStatus_ServiceError(t *testing.T) {
	svc := newCoverageCeremonyService()
	svc.statusErr = &service.KeyNotFoundError{Key: "k"}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/ceremony/status", nil)
	rec := httptest.NewRecorder()
	s.handleCeremonyStatus(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleCeremonyClaimCertBegin_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-cert/begin", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyClaimCertBegin(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCeremonyClaimCertBegin_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-cert/begin", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	s.handleCeremonyClaimCertBegin(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCeremonyClaimCertBegin_Success(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-cert/begin", strings.NewReader(`{"username":"alice"}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyClaimCertBegin(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleCeremonyClaimCertBegin_ServiceError(t *testing.T) {
	svc := newCoverageCeremonyService()
	svc.beginErr = &service.KeyNotFoundError{Key: "k"}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-cert/begin", strings.NewReader(`{"username":"alice"}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyClaimCertBegin(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleCeremonyClaimCertComplete_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-cert/complete", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyClaimCertComplete(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCeremonyClaimCertComplete_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-cert/complete", strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	s.handleCeremonyClaimCertComplete(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCeremonyClaimCertComplete_BadSignatureEncoding(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()))
	body := `{"username":"alice","nonce_hex":"aa","signature":"!!not-base64!!"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-cert/complete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCeremonyClaimCertComplete(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCeremonyClaimCertComplete_Success(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()))
	sig := base64.StdEncoding.EncodeToString([]byte("signature"))
	body := `{"username":"alice","nonce_hex":"deadbeef","signature":"` + sig + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-cert/complete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCeremonyClaimCertComplete(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleCeremonyClaimCertComplete_ServiceError(t *testing.T) {
	svc := newCoverageCeremonyService()
	svc.completeErr = &service.KeyNotFoundError{Key: "k"}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(svc))
	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	body := `{"username":"alice","nonce_hex":"aa","signature":"` + sig + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-cert/complete", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCeremonyClaimCertComplete(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleCeremonyClaimShare_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyClaimShare(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCeremonyClaimShare_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share", strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	s.handleCeremonyClaimShare(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCeremonyClaimShare_Success(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share", strings.NewReader(`{"username":"alice"}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyClaimShare(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleCeremonyClaimShare_ServiceError(t *testing.T) {
	svc := newCoverageCeremonyService()
	svc.claimShareErr = &service.KeyNotFoundError{Key: "k"}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/claim-share", strings.NewReader(`{"username":"alice"}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyClaimShare(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleCeremonySignCSR_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/sign-csr", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleCeremonySignCSR(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCeremonySignCSR_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/sign-csr", strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	s.handleCeremonySignCSR(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCeremonySignCSR_Success(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()))
	body := `{"username":"alice","so_pin":"pin","csr_pem":"csr","role":"officer"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/sign-csr", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCeremonySignCSR(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleCeremonySignCSR_ServiceError(t *testing.T) {
	svc := newCoverageCeremonyService()
	svc.signCSRErr = &service.KeyNotFoundError{Key: "k"}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(svc))
	body := `{"username":"alice","so_pin":"pin","csr_pem":"csr","role":"officer"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/sign-csr", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCeremonySignCSR(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Ceremony response converter functions
// ---------------------------------------------------------------------------

func TestCeremonyInitToResponse(t *testing.T) {
	r := &service.CeremonyInitResult{
		SPKIPin:    "pin",
		CACertPEM:  []byte("ca"),
		TLSCertPEM: []byte("tls"),
		SOCertPEM:  []byte("so"),
		SOKeyPEM:   []byte("key"),
		State:      "initialized",
		ShareCount: 2,
	}
	m := ceremonyInitToResponse(r)
	if m["spki_pin"] != "pin" {
		t.Errorf("spki_pin = %v", m["spki_pin"])
	}
	if m["share_count"] != 2 {
		t.Errorf("share_count = %v", m["share_count"])
	}
}

func TestCeremonyStatusToResponse(t *testing.T) {
	info := &service.CeremonyStatusInfo{State: "complete", SPKIPin: "abc"}
	m := ceremonyStatusToResponse(info)
	if m["state"] != "complete" {
		t.Errorf("state = %v", m["state"])
	}
	if m["spki_pin"] != "abc" {
		t.Errorf("spki_pin = %v", m["spki_pin"])
	}
}

func TestCeremonyChallengeToResponse(t *testing.T) {
	expiry := time.Now().Add(time.Minute)
	c := &service.CeremonyChallenge{NonceHex: "ff00", ExpiresAt: expiry}
	m := ceremonyChallengeToResponse(c)
	if m["nonce_hex"] != "ff00" {
		t.Errorf("nonce_hex = %v", m["nonce_hex"])
	}
	if m["expires_at"] == "" {
		t.Error("expires_at should not be empty")
	}
}

func TestCeremonyClaimCertToResponse(t *testing.T) {
	r := &service.CeremonyClaimCertResult{CertPEM: []byte("cert"), CACertPEM: []byte("ca")}
	m := ceremonyClaimCertToResponse(r)
	if m["cert_pem"] != "cert" {
		t.Errorf("cert_pem = %v", m["cert_pem"])
	}
	if m["ca_cert_pem"] != "ca" {
		t.Errorf("ca_cert_pem = %v", m["ca_cert_pem"])
	}
}

func TestCeremonySignCSRToResponse(t *testing.T) {
	r := &service.CeremonySignCSRResult{CertPEM: []byte("signed")}
	m := ceremonySignCSRToResponse(r)
	if m["cert_pem"] != "signed" {
		t.Errorf("cert_pem = %v", m["cert_pem"])
	}
}

// ---------------------------------------------------------------------------
// Credential handlers
// ---------------------------------------------------------------------------

func TestHandleCredentialSubmit_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/submit", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleCredentialSubmit(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCredentialSubmit_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(newCoverageCredentialService()))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/submit", strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	s.handleCredentialSubmit(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCredentialSubmit_Success(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(newCoverageCredentialService()))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/submit", strings.NewReader(`{"name":"apikey","value":"secret"}`))
	rec := httptest.NewRecorder()
	s.handleCredentialSubmit(rec, req)
	assertStatus(t, rec, http.StatusNoContent)
}

func TestHandleCredentialSubmit_ServiceError(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.submitFn = func(_ context.Context, _ string, _ []byte) error {
		return &service.KeyNotFoundError{Key: "k"}
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/submit", strings.NewReader(`{"name":"k","value":"v"}`))
	rec := httptest.NewRecorder()
	s.handleCredentialSubmit(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleCredentialStrategy_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/strategy", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialStrategy(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCredentialStrategy_Success(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(newCoverageCredentialService()))
	req := httptest.NewRequest(http.MethodGet, "/v1/credentials/strategy", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialStrategy(rec, req)
	assertStatus(t, rec, http.StatusOK)
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["strategy"] != "manual" {
		t.Errorf("strategy = %q, want 'manual'", resp["strategy"])
	}
}

func TestHandleCredentialDelete_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodDelete, "/v1/credentials/mykey", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialDelete(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCredentialDelete_Success(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(newCoverageCredentialService()))
	req := httptest.NewRequest(http.MethodDelete, "/v1/credentials/mykey", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialDelete(rec, req)
	assertStatus(t, rec, http.StatusNoContent)
}

func TestHandleCredentialDelete_ServiceError(t *testing.T) {
	svc := newCoverageCredentialService()
	svc.deleteFn = func(_ context.Context, _ string) error {
		return &service.KeyNotFoundError{Key: "k"}
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCredentialService(svc))
	req := httptest.NewRequest(http.MethodDelete, "/v1/credentials/mykey", nil)
	rec := httptest.NewRecorder()
	s.handleCredentialDelete(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// handleDatabaseByName sub-routes: etcd, raft, barrier, connection
// ---------------------------------------------------------------------------

func TestHandleDatabaseByName_UpdateEtcd_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithDatabaseService(newMockDatabaseService()))
	req := httptest.NewRequest(http.MethodPatch, "/v1/databases/mydb/etcd", strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUpdateDatabaseEtcd_WithCallback(t *testing.T) {
	called := false

	// Provide a dbSvc that returns a non-nil info to avoid nil dereference.
	dbSvc := &mockDatabaseServiceWithEtcd{
		updateEtcdResult: &database.DatabaseInfo{Name: "mydb", Engine: database.EngineKV, Backend: database.BackendPebble},
	}
	fn := func(_ string, _ *database.EtcdConfig) error {
		called = true
		return nil
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithDatabaseService(dbSvc),
		WithOnEtcdUpdated(fn))
	body := `{"enabled":true,"listen_addr":"localhost:2379"}`
	req := httptest.NewRequest(http.MethodPatch, "/v1/databases/mydb/etcd", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleUpdateDatabaseEtcd(rec, req, "mydb")
	if !called {
		t.Error("onEtcdUpdated callback not invoked")
	}
}

func TestHandleDatabaseByName_UpdateRaft_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithDatabaseService(newMockDatabaseService()))
	req := httptest.NewRequest(http.MethodPatch, "/v1/databases/mydb/raft", strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleDatabaseByName_UpdateBarrier_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithDatabaseService(newMockDatabaseService()))
	req := httptest.NewRequest(http.MethodPatch, "/v1/databases/mydb/barrier", strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleDatabaseByName_UpdateConnection_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithDatabaseService(newMockDatabaseService()))
	req := httptest.NewRequest(http.MethodPatch, "/v1/databases/mydb/connection", strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// handleIndexList
// ---------------------------------------------------------------------------

func TestHandleIndexList_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/index/list", nil)
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleIndexList_Success(t *testing.T) {
	svc := &mockIndexService{}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithIndex(svc))
	req := httptest.NewRequest(http.MethodGet, "/index/list", nil)
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)
}

func TestHandleIndexList_ServiceError(t *testing.T) {
	svc := &mockIndexService{
		listIndexesFn: func(_ context.Context) ([]string, error) {
			return nil, &service.IndexNotFoundError{Name: "idx"}
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithIndex(svc))
	req := httptest.NewRequest(http.MethodGet, "/index/list", nil)
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// writeJSON / writeErrorJSON
// ---------------------------------------------------------------------------

func TestWriteJSON_SetsContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, map[string]string{"key": "value"})
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestWriteErrorJSON_SetsStatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErrorJSON(rec, http.StatusBadRequest, &service.KeyNotFoundError{Key: "k"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] == "" {
		t.Error("expected non-empty error field")
	}
}

// ---------------------------------------------------------------------------
// mapHTTPError – collection error branches not covered elsewhere
// ---------------------------------------------------------------------------

func TestMapHTTPError_CollectionErrors(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{&service.CollectionAlreadyExistsError{Database: "db", Collection: "c"}, http.StatusConflict},
		{&service.CollectionNotFoundError{Database: "db", Collection: "c"}, http.StatusNotFound},
		{&service.CollectionEmptyNameError{}, http.StatusBadRequest},
		{&service.CollectionEmptyDatabaseError{}, http.StatusBadRequest},
		{&service.CollectionInvalidIDStrategyError{}, http.StatusBadRequest},
		{&service.CollectionMissingFieldHashError{}, http.StatusBadRequest},
		{&service.CollectionDocumentNotFoundError{}, http.StatusNotFound},
		{&service.CollectionEmptyDocumentError{}, http.StatusBadRequest},
		{&service.CollectionInvalidDocumentIDError{}, http.StatusBadRequest},
		{&service.CollectionMetadataError{}, http.StatusInternalServerError},
	}
	for _, tc := range tests {
		got := mapHTTPError(tc.err)
		if got != tc.want {
			t.Errorf("mapHTTPError(%T) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// forwardToLeader – loop detection
// ---------------------------------------------------------------------------

func TestForwardToLeader_LoopDetection(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPut, "/kv/key", strings.NewReader(`{}`))
	req.Header.Set(headerForwardedBy, "some-node")
	rec := httptest.NewRecorder()
	s.forwardToLeader(rec, req, "http://leader", "key", nil)
	assertStatus(t, rec, http.StatusLoopDetected)
}

// ---------------------------------------------------------------------------
// databaseInfoToResponse with Raft and Barrier sub-configs present
// ---------------------------------------------------------------------------

func TestDatabaseInfoToResponse_WithRaftAndBarrierConfig(t *testing.T) {
	info := &database.DatabaseInfo{
		Name:    "mydb",
		Engine:  database.EngineKV,
		Backend: database.BackendPebble,
		Status:  database.StatusOpen,
		Raft: &database.DatabaseRaftConfig{
			ShardID:   1,
			ReplicaID: 1,
		},
		Barrier: &database.DatabaseBarrierConfig{
			Enabled:        true,
			SharesRequired: 2,
			SharesTotal:    3,
		},
	}
	resp := databaseInfoToResponse(info)
	if resp.Name != "mydb" {
		t.Errorf("name = %q, want 'mydb'", resp.Name)
	}
	if resp.Raft == nil {
		t.Error("raft config should not be nil")
	}
	if resp.Barrier == nil {
		t.Error("barrier config should not be nil")
	}
}
