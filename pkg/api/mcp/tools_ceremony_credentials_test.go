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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
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

func (m *mockCeremonyService) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
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

// ---------------------------------------------------------------------------
// Mock: CeremonyCA
// ---------------------------------------------------------------------------

type mockCeremonyCA struct{}

func (m *mockCeremonyCA) SignCSR(_ []byte, _ *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, nil
}

func (m *mockCeremonyCA) CACertificate() (*x509.Certificate, error) {
	return nil, nil
}

func (m *mockCeremonyCA) CABundle() ([]byte, error) {
	return []byte("-----BEGIN CERTIFICATE-----\nca-bundle\n-----END CERTIFICATE-----"), nil
}

func (m *mockCeremonyCA) IssueCertificate(_ *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return &ceremony.IssuedCert{
		CertPEM: []byte("-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----"),
		KeyPEM:  []byte("-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----"),
	}, nil
}

// ---------------------------------------------------------------------------
// Mock: CredentialServicer
// ---------------------------------------------------------------------------

type mockCredentialService struct {
	submitFn   func(ctx context.Context, name string, value []byte) error
	strategyFn func() string
	deleteFn   func(ctx context.Context, name string) error
	storeFn    func(ctx context.Context, name string, value []byte) error
	retrieveFn func(ctx context.Context, name string) ([]byte, error)
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
	if _, ok := registeredTools["qrdb_credential_submit"]; !ok {
		t.Error("qrdb_credential_submit not registered")
	}
	if _, ok := registeredTools["qrdb_credential_strategy"]; !ok {
		t.Error("qrdb_credential_strategy not registered")
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
