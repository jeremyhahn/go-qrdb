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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package mcp

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: CeremonyServicer
// ---------------------------------------------------------------------------

type mockCeremonyService struct {
	initializeFn        func(ctx context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error)
	beginClaimCertFn    func(ctx context.Context, username string) (*service.CeremonyChallenge, error)
	completeClaimCertFn func(ctx context.Context, username, nonceHex string, signature []byte) (*service.CeremonyClaimCertResult, error)
	claimShareFn        func(ctx context.Context, username string) ([]byte, error)
	signCSRFn           func(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error)
	statusFn            func(ctx context.Context) (*service.CeremonyStatusInfo, error)
}

func newMockCeremonyService() *mockCeremonyService {
	return &mockCeremonyService{}
}

func (m *mockCeremonyService) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, _ *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	if m.initializeFn != nil {
		return m.initializeFn(ctx, ca)
	}
	return &service.CeremonyInitResult{
		SPKIPin:    "abc123pin",
		State:      "initialized",
		ShareCount: 3,
	}, nil
}

func (m *mockCeremonyService) CeremonyBeginClaimCert(ctx context.Context, username string) (*service.CeremonyChallenge, error) {
	if m.beginClaimCertFn != nil {
		return m.beginClaimCertFn(ctx, username)
	}
	return &service.CeremonyChallenge{
		NonceHex:  "deadbeef1234",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}, nil
}

func (m *mockCeremonyService) CeremonyCompleteClaimCert(ctx context.Context, username, nonceHex string, signature []byte) (*service.CeremonyClaimCertResult, error) {
	if m.completeClaimCertFn != nil {
		return m.completeClaimCertFn(ctx, username, nonceHex, signature)
	}
	return &service.CeremonyClaimCertResult{
		CertPEM:   []byte("-----BEGIN CERTIFICATE-----\nfake-cert\n-----END CERTIFICATE-----"),
		CACertPEM: []byte("-----BEGIN CERTIFICATE-----\nfake-ca\n-----END CERTIFICATE-----"),
	}, nil
}

func (m *mockCeremonyService) CeremonyClaimShare(ctx context.Context, username string) ([]byte, error) {
	if m.claimShareFn != nil {
		return m.claimShareFn(ctx, username)
	}
	return []byte("shamir-share-data-for-" + username), nil
}

func (m *mockCeremonyService) CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error) {
	if m.signCSRFn != nil {
		return m.signCSRFn(ctx, username, soPin, csrPEM, role)
	}
	return &service.CeremonySignCSRResult{
		CertPEM: []byte("-----BEGIN CERTIFICATE-----\nsigned-cert\n-----END CERTIFICATE-----"),
	}, nil
}

func (m *mockCeremonyService) CeremonyStatus(ctx context.Context) (*service.CeremonyStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.CeremonyStatusInfo{
		State:   "initialized",
		SPKIPin: "abc123pin",
	}, nil
}

// CeremonyClaimShareBegin is a stub satisfying the Phase 4 custodian-ceremony
// extension of service.CeremonyServicer. The MCP credential tests do not
// exercise factor-based claim flows.
func (m *mockCeremonyService) CeremonyClaimShareBegin(_ context.Context, _ string) (*service.CeremonyCustodianChallenge, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

// CeremonyClaimShareComplete is a stub for the custodian claim-complete flow.
func (m *mockCeremonyService) CeremonyClaimShareComplete(_ context.Context, _, _ string, _ []byte) (*custodian.Custodian, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

// CeremonySubmitUnsealShare is a stub for custodian unseal-share submission.
func (m *mockCeremonyService) CeremonySubmitUnsealShare(_ context.Context, _, _ string, _, _ []byte) (*service.CeremonyUnsealShareResult, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

// CeremonyEnrollExternalOfficer is a stub for external-officer enrollment.
func (m *mockCeremonyService) CeremonyEnrollExternalOfficer(_ context.Context, _ *service.CeremonyEnrollExternalOfficerRequest) (*service.CeremonyEnrollExternalOfficerResponse, error) {
	return nil, fmt.Errorf("not implemented in mock")
}

// ---------------------------------------------------------------------------
// Mock: CeremonyCA
// ---------------------------------------------------------------------------

type mockCeremonyCA struct{}

func (m *mockCeremonyCA) SignCSR(_ context.Context, _ []byte, _ *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, nil
}

func (m *mockCeremonyCA) CACertificate() (*x509.Certificate, error) {
	return nil, nil
}

func (m *mockCeremonyCA) CABundle() ([]byte, error) {
	return []byte("-----BEGIN CERTIFICATE-----\nca-bundle\n-----END CERTIFICATE-----"), nil
}

func (m *mockCeremonyCA) IssueCertificate(_ context.Context, _ *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return &ceremony.IssuedCert{
		CertPEM: []byte("-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----"),
		KeyPEM:  []byte("-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----"),
	}, nil
}

// ---------------------------------------------------------------------------
// Mock: CredentialServicer
// ---------------------------------------------------------------------------

type mockCredentialService struct {
	submitFn          func(ctx context.Context, name string, value []byte) error
	strategyFn        func() string
	deleteFn          func(ctx context.Context, name string) error
	storeFn           func(ctx context.Context, name string, value []byte) error
	retrieveFn        func(ctx context.Context, name string) ([]byte, error)
	listFn            func(ctx context.Context) ([]string, error)
	rotateFn          func(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)
	retrieveVersionFn func(ctx context.Context, name string, version uint64) ([]byte, error)
	listVersionsFn    func(ctx context.Context, name string) ([]credentials.CredentialVersion, error)
	gcExpiredFn       func(ctx context.Context, name string) (int, error)
}

func newMockCredentialService() *mockCredentialService {
	return &mockCredentialService{}
}

func (m *mockCredentialService) CredentialStore(ctx context.Context, name string, value []byte) error {
	if m.storeFn != nil {
		return m.storeFn(ctx, name, value)
	}
	return nil
}

func (m *mockCredentialService) CredentialRetrieve(ctx context.Context, name string) ([]byte, error) {
	if m.retrieveFn != nil {
		return m.retrieveFn(ctx, name)
	}
	return []byte("credential-value"), nil
}

func (m *mockCredentialService) CredentialSubmit(ctx context.Context, name string, value []byte) error {
	if m.submitFn != nil {
		return m.submitFn(ctx, name, value)
	}
	return nil
}

func (m *mockCredentialService) CredentialDelete(ctx context.Context, name string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, name)
	}
	return nil
}

func (m *mockCredentialService) CredentialStrategy() string {
	if m.strategyFn != nil {
		return m.strategyFn()
	}
	return "manual"
}

func (m *mockCredentialService) CredentialList(ctx context.Context) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []string{"alpha", "beta"}, nil
}

func (m *mockCredentialService) CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	if m.rotateFn != nil {
		return m.rotateFn(ctx, name, opts)
	}
	res := &credentials.RotateResult{
		Name:            name,
		PreviousVersion: 1,
		NewVersion:      2,
	}
	if opts.GracePeriod > 0 {
		res.GraceUntil = time.Unix(1_700_000_000, 0).Add(opts.GracePeriod)
	}
	return res, nil
}

func (m *mockCredentialService) CredentialRetrieveVersion(ctx context.Context, name string, version uint64) ([]byte, error) {
	if m.retrieveVersionFn != nil {
		return m.retrieveVersionFn(ctx, name, version)
	}
	return []byte("version-value"), nil
}

func (m *mockCredentialService) CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error) {
	if m.listVersionsFn != nil {
		return m.listVersionsFn(ctx, name)
	}
	return []credentials.CredentialVersion{
		{Name: name, Version: 2, IsCurrent: true, CreatedAt: time.Unix(1_700_000_100, 0)},
		{Name: name, Version: 1, IsCurrent: false, CreatedAt: time.Unix(1_700_000_000, 0), GraceUntil: time.Unix(1_700_003_600, 0)},
	}, nil
}

func (m *mockCredentialService) CredentialGCExpired(ctx context.Context, name string) (int, error) {
	if m.gcExpiredFn != nil {
		return m.gcExpiredFn(ctx, name)
	}
	return 0, nil
}

// Compile-time assertion that the mock satisfies the full CredentialServicer
// interface, including the versioned-rotation surface.
var _ service.CredentialServicer = (*mockCredentialService)(nil)

// ---------------------------------------------------------------------------
// ceremonyTools registration
// ---------------------------------------------------------------------------

func TestCeremonyTools_Registration(t *testing.T) {
	svc := newMockCeremonyService()
	ca := &mockCeremonyCA{}
	tools := ceremonyTools(svc, ca)

	expectedNames := []string{
		"qrdb_ceremony_initialize",
		"qrdb_ceremony_status",
		"qrdb_ceremony_claim_cert_begin",
		"qrdb_ceremony_claim_cert_complete",
		"qrdb_ceremony_claim_share",
		"qrdb_ceremony_sign_csr",
		"qrdb_ceremony_claim_share_begin",
		"qrdb_ceremony_claim_share_complete",
		"qrdb_ceremony_submit_unseal_share",
	}

	if len(tools) != len(expectedNames) {
		t.Fatalf("expected %d tools, got %d", len(expectedNames), len(tools))
	}

	for i, name := range expectedNames {
		if tools[i].Tool.Name != name {
			t.Errorf("tool[%d]: got %q, want %q", i, tools[i].Tool.Name, name)
		}
	}
}

func TestServer_WithCeremonyService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	ceremonySvc := newMockCeremonyService()
	ca := &mockCeremonyCA{}

	s := NewServer(kvSvc, adminSvc, ":0",
		WithCeremonyService(ceremonySvc),
		WithCeremonyCA(ca),
	)

	if s.ceremonySvc != ceremonySvc {
		t.Error("ceremony service not set on server")
	}
	if s.ceremonyCA != ca {
		t.Error("ceremony CA not set on server")
	}

	registeredTools := s.MCPServer().ListTools()
	if _, ok := registeredTools["qrdb_ceremony_initialize"]; !ok {
		t.Error("qrdb_ceremony_initialize not registered")
	}
	if _, ok := registeredTools["qrdb_ceremony_status"]; !ok {
		t.Error("qrdb_ceremony_status not registered")
	}
}

// ---------------------------------------------------------------------------
// handleCeremonyInitialize
// ---------------------------------------------------------------------------

func TestHandleCeremonyInitialize_NilCA(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonyInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nil CA")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_ceremony_initialize" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_ceremony_initialize")
	}
}

func TestHandleCeremonyInitialize_Success(t *testing.T) {
	svc := newMockCeremonyService()
	ca := &mockCeremonyCA{}
	handler := handleCeremonyInitialize(svc, ca)

	req := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCeremonyInitialize: %v", err)
	}

	text := resultText(t, result)
	var info service.CeremonyInitResult
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.SPKIPin != "abc123pin" {
		t.Errorf("spki_pin: got %q, want %q", info.SPKIPin, "abc123pin")
	}
	if info.State != "initialized" {
		t.Errorf("state: got %q, want %q", info.State, "initialized")
	}
}

func TestHandleCeremonyInitialize_ServiceError(t *testing.T) {
	svc := newMockCeremonyService()
	svc.initializeFn = func(_ context.Context, _ ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
		return nil, errors.New("ceremony already completed")
	}
	ca := &mockCeremonyCA{}
	handler := handleCeremonyInitialize(svc, ca)

	req := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCeremonyStatus
// ---------------------------------------------------------------------------

func TestHandleCeremonyStatus_Success(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonyStatus(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_status", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCeremonyStatus: %v", err)
	}

	text := resultText(t, result)
	var info service.CeremonyStatusInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.State != "initialized" {
		t.Errorf("state: got %q, want %q", info.State, "initialized")
	}
	if info.SPKIPin != "abc123pin" {
		t.Errorf("spki_pin: got %q, want %q", info.SPKIPin, "abc123pin")
	}
}

func TestHandleCeremonyStatus_ServiceError(t *testing.T) {
	svc := newMockCeremonyService()
	svc.statusFn = func(_ context.Context) (*service.CeremonyStatusInfo, error) {
		return nil, errors.New("status unavailable")
	}
	handler := handleCeremonyStatus(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_status", map[string]any{})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_ceremony_status" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_ceremony_status")
	}
}

// ---------------------------------------------------------------------------
// handleCeremonyClaimCertBegin
// ---------------------------------------------------------------------------

func TestHandleCeremonyClaimCertBegin_Success(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonyClaimCertBegin(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_claim_cert_begin", map[string]any{
		"username": "alice",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCeremonyClaimCertBegin: %v", err)
	}

	text := resultText(t, result)
	var challenge service.CeremonyChallenge
	if jsonErr := json.Unmarshal([]byte(text), &challenge); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if challenge.NonceHex != "deadbeef1234" {
		t.Errorf("nonce_hex: got %q, want %q", challenge.NonceHex, "deadbeef1234")
	}
}

func TestHandleCeremonyClaimCertBegin_MissingUsername(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonyClaimCertBegin(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_claim_cert_begin", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing username")
	}
}

func TestHandleCeremonyClaimCertBegin_ServiceError(t *testing.T) {
	svc := newMockCeremonyService()
	svc.beginClaimCertFn = func(_ context.Context, _ string) (*service.CeremonyChallenge, error) {
		return nil, errors.New("user not found")
	}
	handler := handleCeremonyClaimCertBegin(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_claim_cert_begin", map[string]any{
		"username": "unknown",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCeremonyClaimCertComplete
// ---------------------------------------------------------------------------

func TestHandleCeremonyClaimCertComplete_Success(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonyClaimCertComplete(svc)

	// Base64-encoded dummy signature: "signature-bytes"
	sigB64 := "c2lnbmF0dXJlLWJ5dGVz"

	req := buildCallToolRequest(t, "qrdb_ceremony_claim_cert_complete", map[string]any{
		"username":  "alice",
		"nonce_hex": "deadbeef",
		"signature": sigB64,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCeremonyClaimCertComplete: %v", err)
	}

	text := resultText(t, result)
	var claimResult service.CeremonyClaimCertResult
	if jsonErr := json.Unmarshal([]byte(text), &claimResult); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(claimResult.CertPEM) == 0 {
		t.Error("expected non-empty CertPEM")
	}
}

func TestHandleCeremonyClaimCertComplete_MissingUsername(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonyClaimCertComplete(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_claim_cert_complete", map[string]any{
		"nonce_hex": "deadbeef",
		"signature": "c2lnbmF0dXJlLWJ5dGVz",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing username")
	}
}

func TestHandleCeremonyClaimCertComplete_MissingNonce(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonyClaimCertComplete(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_claim_cert_complete", map[string]any{
		"username":  "alice",
		"signature": "c2lnbmF0dXJlLWJ5dGVz",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing nonce_hex")
	}
}

func TestHandleCeremonyClaimCertComplete_MissingSignature(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonyClaimCertComplete(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_claim_cert_complete", map[string]any{
		"username":  "alice",
		"nonce_hex": "deadbeef",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing signature")
	}
}

func TestHandleCeremonyClaimCertComplete_InvalidBase64(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonyClaimCertComplete(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_claim_cert_complete", map[string]any{
		"username":  "alice",
		"nonce_hex": "deadbeef",
		"signature": "!!!invalid-base64!!!",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid base64 signature")
	}
}

func TestHandleCeremonyClaimCertComplete_ServiceError(t *testing.T) {
	svc := newMockCeremonyService()
	svc.completeClaimCertFn = func(_ context.Context, _, _ string, _ []byte) (*service.CeremonyClaimCertResult, error) {
		return nil, errors.New("signature verification failed")
	}
	handler := handleCeremonyClaimCertComplete(svc)

	sigB64 := "c2lnbmF0dXJlLWJ5dGVz"
	req := buildCallToolRequest(t, "qrdb_ceremony_claim_cert_complete", map[string]any{
		"username":  "alice",
		"nonce_hex": "deadbeef",
		"signature": sigB64,
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCeremonyClaimShare
// ---------------------------------------------------------------------------

func TestHandleCeremonyClaimShare_Success(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonyClaimShare(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_claim_share", map[string]any{
		"username": "alice",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCeremonyClaimShare: %v", err)
	}

	text := resultText(t, result)
	var resp struct {
		Share string `json:"share"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Share == "" {
		t.Error("expected non-empty share")
	}
}

func TestHandleCeremonyClaimShare_MissingUsername(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonyClaimShare(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_claim_share", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing username")
	}
}

func TestHandleCeremonyClaimShare_ServiceError(t *testing.T) {
	svc := newMockCeremonyService()
	svc.claimShareFn = func(_ context.Context, _ string) ([]byte, error) {
		return nil, errors.New("share not assigned")
	}
	handler := handleCeremonyClaimShare(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_claim_share", map[string]any{
		"username": "bob",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCeremonySignCSR
// ---------------------------------------------------------------------------

func TestHandleCeremonySignCSR_Success(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonySignCSR(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_sign_csr", map[string]any{
		"username": "alice",
		"so_pin":   "secret-pin",
		"csr_pem":  "-----BEGIN CERTIFICATE REQUEST-----\nfake\n-----END CERTIFICATE REQUEST-----",
		"role":     "security-officer",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCeremonySignCSR: %v", err)
	}

	text := resultText(t, result)
	var signResult service.CeremonySignCSRResult
	if jsonErr := json.Unmarshal([]byte(text), &signResult); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(signResult.CertPEM) == 0 {
		t.Error("expected non-empty CertPEM")
	}
}

func TestHandleCeremonySignCSR_MissingUsername(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonySignCSR(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_sign_csr", map[string]any{
		"so_pin":  "secret-pin",
		"csr_pem": "-----BEGIN CERTIFICATE REQUEST-----\nfake\n-----END CERTIFICATE REQUEST-----",
		"role":    "security-officer",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing username")
	}
}

func TestHandleCeremonySignCSR_MissingSoPin(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonySignCSR(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_sign_csr", map[string]any{
		"username": "alice",
		"csr_pem":  "-----BEGIN CERTIFICATE REQUEST-----\nfake\n-----END CERTIFICATE REQUEST-----",
		"role":     "security-officer",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing so_pin")
	}
}

func TestHandleCeremonySignCSR_MissingCSRPEM(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonySignCSR(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_sign_csr", map[string]any{
		"username": "alice",
		"so_pin":   "secret-pin",
		"role":     "security-officer",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing csr_pem")
	}
}

func TestHandleCeremonySignCSR_MissingRole(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonySignCSR(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_sign_csr", map[string]any{
		"username": "alice",
		"so_pin":   "secret-pin",
		"csr_pem":  "-----BEGIN CERTIFICATE REQUEST-----\nfake\n-----END CERTIFICATE REQUEST-----",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing role")
	}
}

func TestHandleCeremonySignCSR_ServiceError(t *testing.T) {
	svc := newMockCeremonyService()
	svc.signCSRFn = func(_ context.Context, _, _, _, _ string) (*service.CeremonySignCSRResult, error) {
		return nil, errors.New("signing failed")
	}
	handler := handleCeremonySignCSR(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_sign_csr", map[string]any{
		"username": "alice",
		"so_pin":   "secret-pin",
		"csr_pem":  "-----BEGIN CERTIFICATE REQUEST-----\nfake\n-----END CERTIFICATE REQUEST-----",
		"role":     "security-officer",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// credentialTools registration
// ---------------------------------------------------------------------------

func TestCredentialTools_Registration(t *testing.T) {
	svc := newMockCredentialService()
	tools := credentialTools(svc)

	expectedNames := []string{
		"qrdb_credential_submit",
		"qrdb_credential_strategy",
		"qrdb_credential_delete",
		"qrdb_credential_list",
		"qrdb_credential_rotate",
		"qrdb_credential_retrieve_version",
		"qrdb_credential_list_versions",
		"qrdb_credential_gc_expired",
		"qrdb_credential_store",
		"qrdb_credential_retrieve",
	}

	if len(tools) != len(expectedNames) {
		t.Fatalf("expected %d tools, got %d", len(expectedNames), len(tools))
	}

	for i, name := range expectedNames {
		if tools[i].Tool.Name != name {
			t.Errorf("tool[%d]: got %q, want %q", i, tools[i].Tool.Name, name)
		}
	}
}

func TestServer_WithCredentialService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	credSvc := newMockCredentialService()

	s := NewServer(kvSvc, adminSvc, ":0", WithCredentialService(credSvc))

	if s.credentialSvc != credSvc {
		t.Error("credential service not set on server")
	}

	registeredTools := s.MCPServer().ListTools()
	for _, name := range []string{
		"qrdb_credential_submit",
		"qrdb_credential_strategy",
		"qrdb_credential_delete",
		"qrdb_credential_list",
		"qrdb_credential_rotate",
		"qrdb_credential_retrieve_version",
		"qrdb_credential_list_versions",
		"qrdb_credential_gc_expired",
		"qrdb_credential_store",
		"qrdb_credential_retrieve",
	} {
		if _, ok := registeredTools[name]; !ok {
			t.Errorf("%s not registered", name)
		}
	}
}

// ---------------------------------------------------------------------------
// handleCredentialSubmit
// ---------------------------------------------------------------------------

func TestHandleCredentialSubmit_Success(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialSubmit(svc)

	req := buildCallToolRequest(t, "qrdb_credential_submit", map[string]any{
		"name":  "db-password",
		"value": "secret123",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialSubmit: %v", err)
	}

	text := resultText(t, result)
	if text != "credential submitted" {
		t.Errorf("expected 'credential submitted', got %q", text)
	}
}

func TestHandleCredentialSubmit_MissingName(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialSubmit(svc)

	req := buildCallToolRequest(t, "qrdb_credential_submit", map[string]any{
		"value": "secret123",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleCredentialSubmit_MissingValue(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialSubmit(svc)

	req := buildCallToolRequest(t, "qrdb_credential_submit", map[string]any{
		"name": "db-password",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing value")
	}
}

func TestHandleCredentialSubmit_ServiceError(t *testing.T) {
	svc := newMockCredentialService()
	svc.submitFn = func(_ context.Context, _ string, _ []byte) error {
		return errors.New("submit failed")
	}
	handler := handleCredentialSubmit(svc)

	req := buildCallToolRequest(t, "qrdb_credential_submit", map[string]any{
		"name":  "db-password",
		"value": "secret123",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_credential_submit" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_credential_submit")
	}
}

// ---------------------------------------------------------------------------
// handleCredentialStrategy
// ---------------------------------------------------------------------------

func TestHandleCredentialStrategy_Success(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialStrategy(svc)

	req := buildCallToolRequest(t, "qrdb_credential_strategy", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialStrategy: %v", err)
	}

	text := resultText(t, result)
	var resp struct {
		Strategy string `json:"strategy"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Strategy != "manual" {
		t.Errorf("strategy: got %q, want %q", resp.Strategy, "manual")
	}
}

func TestHandleCredentialStrategy_BarrierStrategy(t *testing.T) {
	svc := newMockCredentialService()
	svc.strategyFn = func() string { return "barrier" }
	handler := handleCredentialStrategy(svc)

	req := buildCallToolRequest(t, "qrdb_credential_strategy", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialStrategy: %v", err)
	}

	text := resultText(t, result)
	var resp struct {
		Strategy string `json:"strategy"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Strategy != "barrier" {
		t.Errorf("strategy: got %q, want %q", resp.Strategy, "barrier")
	}
}

// ---------------------------------------------------------------------------
// handleCredentialDelete
// ---------------------------------------------------------------------------

func TestHandleCredentialDelete_Success(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialDelete(svc)

	req := buildCallToolRequest(t, "qrdb_credential_delete", map[string]any{
		"name": "db-password",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialDelete: %v", err)
	}

	text := resultText(t, result)
	if text != "credential deleted" {
		t.Errorf("expected 'credential deleted', got %q", text)
	}
}

func TestHandleCredentialDelete_MissingName(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialDelete(svc)

	req := buildCallToolRequest(t, "qrdb_credential_delete", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleCredentialDelete_ServiceError(t *testing.T) {
	svc := newMockCredentialService()
	svc.deleteFn = func(_ context.Context, _ string) error {
		return errors.New("credential not found")
	}
	handler := handleCredentialDelete(svc)

	req := buildCallToolRequest(t, "qrdb_credential_delete", map[string]any{
		"name": "nonexistent",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_credential_delete" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_credential_delete")
	}
}

// ---------------------------------------------------------------------------
// handleCredentialList
// ---------------------------------------------------------------------------

func TestHandleCredentialList_Success(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialList(svc)

	req := buildCallToolRequest(t, "qrdb_credential_list", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialList: %v", err)
	}

	text := resultText(t, result)
	var resp struct {
		Names []string `json:"names"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(resp.Names) != 2 || resp.Names[0] != "alpha" || resp.Names[1] != "beta" {
		t.Errorf("names: got %v, want [alpha beta]", resp.Names)
	}
}

func TestHandleCredentialList_Empty(t *testing.T) {
	svc := newMockCredentialService()
	svc.listFn = func(_ context.Context) ([]string, error) {
		return nil, nil
	}
	handler := handleCredentialList(svc)

	req := buildCallToolRequest(t, "qrdb_credential_list", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialList: %v", err)
	}

	text := resultText(t, result)
	var resp struct {
		Names []string `json:"names"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Names == nil {
		t.Error("expected non-nil names slice (even if empty)")
	}
	if len(resp.Names) != 0 {
		t.Errorf("names: got %d entries, want 0", len(resp.Names))
	}
}

func TestHandleCredentialList_ServiceError(t *testing.T) {
	svc := newMockCredentialService()
	svc.listFn = func(_ context.Context) ([]string, error) {
		return nil, errors.New("enumeration unavailable")
	}
	handler := handleCredentialList(svc)

	req := buildCallToolRequest(t, "qrdb_credential_list", map[string]any{})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_credential_list" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_credential_list")
	}
}

// ---------------------------------------------------------------------------
// handleCredentialRotate
// ---------------------------------------------------------------------------

func TestHandleCredentialRotate_SuccessNoGrace(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialRotate(svc)

	req := buildCallToolRequest(t, "qrdb_credential_rotate", map[string]any{
		"name":      "db-password",
		"new_value": "new-secret",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialRotate: %v", err)
	}

	text := resultText(t, result)
	var resp struct {
		Name            string `json:"name"`
		PreviousVersion uint64 `json:"previous_version"`
		NewVersion      uint64 `json:"new_version"`
		GraceUntilUnix  int64  `json:"grace_until_unix,omitempty"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Name != "db-password" {
		t.Errorf("name: got %q, want %q", resp.Name, "db-password")
	}
	if resp.PreviousVersion != 1 {
		t.Errorf("previous_version: got %d, want 1", resp.PreviousVersion)
	}
	if resp.NewVersion != 2 {
		t.Errorf("new_version: got %d, want 2", resp.NewVersion)
	}
	if resp.GraceUntilUnix != 0 {
		t.Errorf("grace_until_unix: got %d, want 0", resp.GraceUntilUnix)
	}
}

func TestHandleCredentialRotate_SuccessWithGrace(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialRotate(svc)

	req := buildCallToolRequest(t, "qrdb_credential_rotate", map[string]any{
		"name":                 "db-password",
		"new_value":            "new-secret",
		"grace_period_seconds": float64(3600),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialRotate: %v", err)
	}

	text := resultText(t, result)
	var resp struct {
		Name            string `json:"name"`
		PreviousVersion uint64 `json:"previous_version"`
		NewVersion      uint64 `json:"new_version"`
		GraceUntilUnix  int64  `json:"grace_until_unix,omitempty"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.GraceUntilUnix == 0 {
		t.Error("expected non-zero grace_until_unix for non-zero grace period")
	}
}

func TestHandleCredentialRotate_MissingName(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialRotate(svc)

	req := buildCallToolRequest(t, "qrdb_credential_rotate", map[string]any{
		"new_value": "v",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleCredentialRotate_MissingNewValue(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialRotate(svc)

	req := buildCallToolRequest(t, "qrdb_credential_rotate", map[string]any{
		"name": "db-password",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing new_value")
	}
}

func TestHandleCredentialRotate_NegativeGrace(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialRotate(svc)

	req := buildCallToolRequest(t, "qrdb_credential_rotate", map[string]any{
		"name":                 "db-password",
		"new_value":            "new-secret",
		"grace_period_seconds": float64(-5),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for negative grace_period_seconds")
	}
}

func TestHandleCredentialRotate_ServiceError(t *testing.T) {
	svc := newMockCredentialService()
	svc.rotateFn = func(_ context.Context, _ string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
		return nil, errors.New("rotation refused")
	}
	handler := handleCredentialRotate(svc)

	req := buildCallToolRequest(t, "qrdb_credential_rotate", map[string]any{
		"name":      "db-password",
		"new_value": "new-secret",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_credential_rotate" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_credential_rotate")
	}
}

// ---------------------------------------------------------------------------
// handleCredentialRetrieveVersion
// ---------------------------------------------------------------------------

func TestHandleCredentialRetrieveVersion_Success(t *testing.T) {
	svc := newMockCredentialService()
	svc.retrieveVersionFn = func(_ context.Context, name string, version uint64) ([]byte, error) {
		if name != "db-password" {
			t.Errorf("name: got %q, want db-password", name)
		}
		if version != 2 {
			t.Errorf("version: got %d, want 2", version)
		}
		return []byte("secret-v2"), nil
	}
	handler := handleCredentialRetrieveVersion(svc)

	req := buildCallToolRequest(t, "qrdb_credential_retrieve_version", map[string]any{
		"name":    "db-password",
		"version": float64(2),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialRetrieveVersion: %v", err)
	}

	text := resultText(t, result)
	var resp struct {
		Value string `json:"value"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Value != "secret-v2" {
		t.Errorf("value: got %q, want %q", resp.Value, "secret-v2")
	}
}

func TestHandleCredentialRetrieveVersion_ZeroAliasesCurrent(t *testing.T) {
	svc := newMockCredentialService()
	svc.retrieveVersionFn = func(_ context.Context, _ string, version uint64) ([]byte, error) {
		if version != 0 {
			t.Errorf("version: got %d, want 0", version)
		}
		return []byte("current"), nil
	}
	handler := handleCredentialRetrieveVersion(svc)

	req := buildCallToolRequest(t, "qrdb_credential_retrieve_version", map[string]any{
		"name": "db-password",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialRetrieveVersion: %v", err)
	}
	text := resultText(t, result)
	if !contains(text, `"value":"current"`) {
		t.Errorf("expected current value in response, got %q", text)
	}
}

func TestHandleCredentialRetrieveVersion_MissingName(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialRetrieveVersion(svc)

	req := buildCallToolRequest(t, "qrdb_credential_retrieve_version", map[string]any{
		"version": float64(1),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleCredentialRetrieveVersion_ServiceError(t *testing.T) {
	svc := newMockCredentialService()
	svc.retrieveVersionFn = func(_ context.Context, _ string, _ uint64) ([]byte, error) {
		return nil, errors.New("version expired")
	}
	handler := handleCredentialRetrieveVersion(svc)

	req := buildCallToolRequest(t, "qrdb_credential_retrieve_version", map[string]any{
		"name":    "db-password",
		"version": float64(1),
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCredentialListVersions
// ---------------------------------------------------------------------------

func TestHandleCredentialListVersions_Success(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialListVersions(svc)

	req := buildCallToolRequest(t, "qrdb_credential_list_versions", map[string]any{
		"name": "db-password",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialListVersions: %v", err)
	}

	text := resultText(t, result)
	var resp struct {
		Name     string `json:"name"`
		Versions []struct {
			Version        uint64 `json:"version"`
			IsCurrent      bool   `json:"is_current"`
			CreatedAtUnix  int64  `json:"created_at_unix,omitempty"`
			GraceUntilUnix int64  `json:"grace_until_unix,omitempty"`
		} `json:"versions"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Name != "db-password" {
		t.Errorf("name: got %q, want %q", resp.Name, "db-password")
	}
	if len(resp.Versions) != 2 {
		t.Fatalf("versions: got %d entries, want 2", len(resp.Versions))
	}
	if !resp.Versions[0].IsCurrent {
		t.Error("expected versions[0] to be current")
	}
	if resp.Versions[1].IsCurrent {
		t.Error("expected versions[1] not to be current")
	}
	if resp.Versions[1].GraceUntilUnix == 0 {
		t.Error("expected versions[1] to carry grace_until_unix")
	}
}

func TestHandleCredentialListVersions_MissingName(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialListVersions(svc)

	req := buildCallToolRequest(t, "qrdb_credential_list_versions", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleCredentialListVersions_ServiceError(t *testing.T) {
	svc := newMockCredentialService()
	svc.listVersionsFn = func(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
		return nil, errors.New("not found")
	}
	handler := handleCredentialListVersions(svc)

	req := buildCallToolRequest(t, "qrdb_credential_list_versions", map[string]any{
		"name": "missing",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCredentialGCExpired
// ---------------------------------------------------------------------------

func TestHandleCredentialGCExpired_Success(t *testing.T) {
	svc := newMockCredentialService()
	svc.gcExpiredFn = func(_ context.Context, name string) (int, error) {
		if name != "db-password" {
			t.Errorf("name: got %q, want %q", name, "db-password")
		}
		return 3, nil
	}
	handler := handleCredentialGCExpired(svc)

	req := buildCallToolRequest(t, "qrdb_credential_gc_expired", map[string]any{
		"name": "db-password",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCredentialGCExpired: %v", err)
	}

	text := resultText(t, result)
	var resp struct {
		Reaped int `json:"reaped"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Reaped != 3 {
		t.Errorf("reaped: got %d, want 3", resp.Reaped)
	}
}

func TestHandleCredentialGCExpired_MissingName(t *testing.T) {
	svc := newMockCredentialService()
	handler := handleCredentialGCExpired(svc)

	req := buildCallToolRequest(t, "qrdb_credential_gc_expired", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing name")
	}
}

func TestHandleCredentialGCExpired_ServiceError(t *testing.T) {
	svc := newMockCredentialService()
	svc.gcExpiredFn = func(_ context.Context, _ string) (int, error) {
		return 0, errors.New("gc failed")
	}
	handler := handleCredentialGCExpired(svc)

	req := buildCallToolRequest(t, "qrdb_credential_gc_expired", map[string]any{
		"name": "db-password",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_credential_gc_expired" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_credential_gc_expired")
	}
}

// ---------------------------------------------------------------------------
// Local helpers
// ---------------------------------------------------------------------------

// contains is a small helper for substring assertions, avoiding a strings
// import collision with other test files in this package.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
