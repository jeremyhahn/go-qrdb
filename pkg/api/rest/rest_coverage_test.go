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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// mockCeremonyService (local to this file)
// ---------------------------------------------------------------------------

type coverageCeremonyService struct {
	initResult       *service.CeremonyInitResult
	initErr          error
	capturedInitOpts *service.CeremonyInitializeOptions
	statusResult     *service.CeremonyStatusInfo
	statusErr        error
	beginErr         error
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
			State:      "initialized",
			ShareCount: 3,
		},
		statusResult:     &service.CeremonyStatusInfo{State: "initialized", SPKIPin: "pin123"},
		claimShareResult: []byte("shamir-share-bytes"),
		signCSRResult:    &service.CeremonySignCSRResult{CertPEM: []byte("signed-cert")},
	}
}

func (m *coverageCeremonyService) CeremonyInitialize(_ context.Context, _ ceremony.CeremonyCA, opts *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	m.capturedInitOpts = opts
	return m.initResult, m.initErr
}
func (m *coverageCeremonyService) CeremonyStatus(_ context.Context) (*service.CeremonyStatusInfo, error) {
	return m.statusResult, m.statusErr
}
func (m *coverageCeremonyService) CeremonySignCSR(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
	return m.signCSRResult, m.signCSRErr
}

// ---------------------------------------------------------------------------
// coverageCeremonyCA implements ceremony.CeremonyCA for testing.
// ---------------------------------------------------------------------------

type coverageCeremonyCA struct{}

var _ ceremony.CeremonyCA = (*coverageCeremonyCA)(nil)

func (m *coverageCeremonyCA) SignCSR(_ context.Context, _ []byte, _ *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, nil
}
func (m *coverageCeremonyCA) CACertificate(context.Context) (*x509.Certificate, error) {
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
	storeFn           func(ctx context.Context, name string, value []byte) error
	retrieveFn        func(ctx context.Context, name string) ([]byte, error)
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
func (m *coverageCredentialService) CredentialStore(ctx context.Context, name string, value []byte) error {
	if m.storeFn != nil {
		return m.storeFn(ctx, name, value)
	}
	return nil
}
func (m *coverageCredentialService) CredentialRetrieve(ctx context.Context, name string) ([]byte, error) {
	if m.retrieveFn != nil {
		return m.retrieveFn(ctx, name)
	}
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
// DatabaseScopeConflictError – Error
// ---------------------------------------------------------------------------

func TestDatabaseScopeConflictError_Error(t *testing.T) {
	e := &DatabaseScopeConflictError{Body: "bodydb", Header: "headerdb"}

	msg := e.Error()
	if !strings.Contains(msg, "bodydb") || !strings.Contains(msg, "headerdb") {
		t.Errorf("Error() = %q, want both database names", msg)
	}
	if !strings.Contains(msg, "scope conflict") {
		t.Errorf("Error() = %q, want contains 'scope conflict'", msg)
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

// TestHandleCeremonyInitialize_ForwardsAllFields is the regression guard for
// the bug where handleCeremonyInitialize only forwarded SOPin/UserPin into
// service.CeremonyInitializeOptions, silently dropping
// CredentialSealStrategy, Threshold, Officers, Hostname, and SANs from the
// wire request.
func TestHandleCeremonyInitialize_ForwardsAllFields(t *testing.T) {
	svc := newCoverageCeremonyService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(svc),
		WithCeremonyCA(&coverageCeremonyCA{}))

	body := `{
		"so_pin": "pin",
		"user_pin": "upin",
		"credential_seal_strategy": "shamir",
		"threshold": 2,
		"officers": [
			{"username": "officer-1", "csr_pem": "-----BEGIN CERTIFICATE REQUEST-----\ncsr1\n-----END CERTIFICATE REQUEST-----\n"},
			{"username": "officer-2", "csr_pem": "-----BEGIN CERTIFICATE REQUEST-----\ncsr2\n-----END CERTIFICATE REQUEST-----\n"}
		],
		"hostname": "override.example",
		"sans": ["alt1.example", "alt2.example"]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)
	assertStatus(t, rec, http.StatusOK)

	got := svc.capturedInitOpts
	if got == nil {
		t.Fatal("expected CeremonyInitialize to be called with non-nil opts")
	}
	if got.SOPin != "pin" || got.UserPin != "upin" {
		t.Errorf("SOPin/UserPin not forwarded: %+v", got)
	}
	if got.CredentialSealStrategy != "shamir" {
		t.Errorf("CredentialSealStrategy = %q, want %q", got.CredentialSealStrategy, "shamir")
	}
	if got.Threshold != 2 {
		t.Errorf("Threshold = %d, want 2", got.Threshold)
	}
	if got.Hostname != "override.example" {
		t.Errorf("Hostname = %q, want %q", got.Hostname, "override.example")
	}
	if len(got.SANs) != 2 || got.SANs[0] != "alt1.example" || got.SANs[1] != "alt2.example" {
		t.Errorf("SANs = %v, want [alt1.example alt2.example]", got.SANs)
	}
	if len(got.Officers) != 2 {
		t.Fatalf("Officers = %v, want 2 entries", got.Officers)
	}
	if got.Officers[0].Username != "officer-1" || got.Officers[0].CSRPEM == "" {
		t.Errorf("Officers[0] = %+v", got.Officers[0])
	}
	if got.Officers[1].Username != "officer-2" || got.Officers[1].CSRPEM == "" {
		t.Errorf("Officers[1] = %+v", got.Officers[1])
	}
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

// TestHandleCeremonyInitialize_SOPINRefusalPropagates proves REST surfaces a
// SO-PIN refusal from the service layer as 401 Unauthorized, never as 200
// OK, so a caller retrying the admin-credential step over REST without the
// correct SO PIN cannot reach ReissueAdminCredential's side effects
// (go-qrdb#560 review B1, which named REST's awaiting-mode passthrough as
// one of three a caller could otherwise use to bypass the gate).
func TestHandleCeremonyInitialize_SOPINRefusalPropagates(t *testing.T) {
	svc := newCoverageCeremonyService()
	svc.initErr = pin.ErrPINInvalid
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(svc),
		WithCeremonyCA(&coverageCeremonyCA{}))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize", strings.NewReader(`{"admin_generate":true}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)
	assertStatus(t, rec, http.StatusUnauthorized)
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
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/submit", strings.NewReader(`{"name":"apikey","value":"c2VjcmV0"}`))
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
	req := httptest.NewRequest(http.MethodPost, "/v1/credentials/submit", strings.NewReader(`{"name":"k","value":"dg=="}`))
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
		{&service.CollectionInvalidNameError{Name: "a#b", Reason: "x"}, http.StatusBadRequest},
		{&service.CollectionReservedFieldError{Field: "id", Reason: "x"}, http.StatusBadRequest},
		{&service.CollectionEmptyDatabaseError{}, http.StatusBadRequest},
		{&service.CollectionInvalidIDStrategyError{}, http.StatusBadRequest},
		{&service.CollectionMissingFieldHashError{}, http.StatusBadRequest},
		{&service.CollectionDocumentNotFoundError{}, http.StatusNotFound},
		// The insert-refused-an-overwrite case (go-qrdb#447) is a conflict,
		// not a bad request: the body is well-formed and the caller may
		// retry it under a different id.
		{&service.CollectionDocumentExistsError{Database: "db", Collection: "c", ID: 7, Explicit: true}, http.StatusConflict},
		{&service.CollectionDocumentExistsError{Database: "db", Collection: "c", ID: 7}, http.StatusConflict},
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

// TestMapHTTPError_SealedTenant covers a review finding: mapHTTPError (the
// general classifier every IAM-touching handler shares) recognized
// tenant.ErrTenantSealed -- what StoreResolver.Resolve returns when a
// barrier-isolated tenant is ALREADY sealed -- but not seal.ErrSealed, the
// distinct sentinel a resolved store's own Get/Put return when the barrier
// seals AFTER Resolve succeeded but before the store operation runs. Both
// must classify identically: both are the same "retry after unseal"
// condition from the caller's point of view (go-qrdb#464 round 6).
func TestMapHTTPError_SealedTenant(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"tenant.ErrTenantSealed", tenant.ErrTenantSealed},
		{"seal.ErrSealed", seal.ErrSealed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := mapHTTPError(tc.err); got != http.StatusServiceUnavailable {
				t.Errorf("mapHTTPError(%v) = %d, want %d", tc.err, got, http.StatusServiceUnavailable)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// forwardToLeader – loop detection
// ---------------------------------------------------------------------------

func TestForwardToLeader_LoopDetection(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPut, "/kv/key", strings.NewReader(`{}`))
	// maxForwardHops hops already taken -- one more forward must be rejected.
	req.Header.Set(headerForwardHops, strconv.Itoa(maxForwardHops))
	rec := httptest.NewRecorder()
	s.forwardToLeader(rec, req, "http://leader", nil)
	assertStatus(t, rec, http.StatusLoopDetected)
}
