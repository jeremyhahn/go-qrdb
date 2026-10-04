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
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock: CeremonyServicer
// ---------------------------------------------------------------------------

type mockCeremonyService struct {
	initializeFn     func(ctx context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error)
	claimShareFn     func(ctx context.Context, username string) ([]byte, error)
	signCSRFn        func(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error)
	statusFn         func(ctx context.Context) (*service.CeremonyStatusInfo, error)
	capturedInitOpts *service.CeremonyInitializeOptions
}

func newMockCeremonyService() *mockCeremonyService {
	return &mockCeremonyService{}
}

func (m *mockCeremonyService) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, opts *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	m.capturedInitOpts = opts
	if m.initializeFn != nil {
		return m.initializeFn(ctx, ca)
	}
	return &service.CeremonyInitResult{
		SPKIPin:    "abc123pin",
		State:      "initialized",
		ShareCount: 3,
	}, nil
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

func (m *mockCeremonyCA) SignCSR(_ context.Context, _ []byte, _ *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, nil
}

func (m *mockCeremonyCA) CACertificate(context.Context) (*x509.Certificate, error) {
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
// ceremonyTools registration
// ---------------------------------------------------------------------------

func TestCeremonyTools_Registration(t *testing.T) {
	svc := newMockCeremonyService()
	ca := &mockCeremonyCA{}
	tools := ceremonyTools(svc, ca)

	expectedNames := []string{
		"qrdb_ceremony_initialize",
		"qrdb_ceremony_status",
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

// TestHandleCeremonyInitialize_NilCAReachesTheService proves a nil CA
// (BYO-TLS: WithCeremonyCA never applied) reaches the ceremony service
// layer rather than being refused by this tool. ceremony.CeremonyService.
// Initialize is nil-safe by construction -- a BYO-TLS node pins its own
// configured leaf instead of self-issuing one -- so refusing here (the
// pre-fix behavior, go-qrdb#560 review item 5) only blocked `qrdb init` on
// a BYO-TLS deployment using this tool, for no safety reason.
func TestHandleCeremonyInitialize_NilCAReachesTheService(t *testing.T) {
	svc := newMockCeremonyService()
	var capturedCA ceremony.CeremonyCA
	svc.initializeFn = func(_ context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
		capturedCA = ca
		return &service.CeremonyInitResult{SPKIPin: "abc123pin", State: "initialized"}, nil
	}
	handler := handleCeremonyInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{})

	if _, err := handler(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedCA != nil {
		t.Fatalf("expected the service to be called with a nil CA (BYO-TLS), got %#v", capturedCA)
	}
}

// ceremonyInitWireResponse mirrors the canonical snake_case wire shape
// handleCeremonyInitialize now emits (tools_ceremony.go's
// ceremonyInitToWireResponse), matching REST's handlers_ceremony.go
// ceremonyInitToResponse and the SDK's transport.CeremonyInitializeResponse
// json tags -- NOT the raw service.CeremonyInitResult's PascalCase Go
// field names, which the handler used to marshal directly (a bug: the SDK
// client could never decode "SPKIPin"/"State"/... against its snake_case
// struct tags).
type ceremonyInitWireResponse struct {
	SPKIPin    string `json:"spki_pin"`
	CACertPEM  string `json:"ca_cert_pem"`
	TLSCertPEM string `json:"tls_cert_pem"`
	State      string `json:"state"`
	ShareCount int    `json:"share_count"`
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
	var info ceremonyInitWireResponse
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

// TestHandleCeremonyInitialize_WipesAdminKeyPEM proves the generate path's
// private key is wiped from the service-layer result
// (CeremonyInitResult.WipeAdminKeyPEM) once this handler has built its own,
// independent copy for the wire response -- go-qrdb#560 review item 7:
// "WipeAdminKeyPEM runs on every transport, not only REST." The client
// must still receive the correct, un-zeroed key (base64-encoded on the
// wire, since admin_key_pem is now bytes): proving the wipe happens AFTER
// holderwire.CeremonyInitTo's bytes.Clone and after holderResult has
// already serialized it, never before.
func TestHandleCeremonyInitialize_WipesAdminKeyPEM(t *testing.T) {
	result := &service.CeremonyInitResult{
		SPKIPin:               "abc123pin",
		State:                 "initialized",
		AdminCertPEM:          []byte("admin-cert"),
		AdminKeyPEM:           []byte("admin-key"),
		AdminCredentialSource: "generated",
	}
	svc := newMockCeremonyService()
	svc.initializeFn = func(_ context.Context, _ ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
		return result, nil
	}
	handler := handleCeremonyInitialize(svc, &mockCeremonyCA{})

	req := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{})
	out, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCeremonyInitialize: %v", err)
	}

	text := resultText(t, out)
	var wire struct {
		AdminKeyPEM []byte `json:"admin_key_pem"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		t.Fatalf("unmarshal admin_key_pem: %v", jsonErr)
	}
	if string(wire.AdminKeyPEM) != "admin-key" {
		t.Fatalf("client must still receive the un-zeroed key, got %q", wire.AdminKeyPEM)
	}
	if result.AdminKeyPEM != nil {
		t.Fatalf("expected the service result's AdminKeyPEM to be wiped (nil) after the call, got %q", result.AdminKeyPEM)
	}
}

// TestHandleCeremonyInitialize_ForwardsAllFields is the regression guard for
// the bug where the MCP qrdb_ceremony_initialize tool handler only forwarded
// so_pin/user_pin into service.CeremonyInitializeOptions, silently dropping
// credential_seal_strategy, threshold, officers, hostname, and sans from the
// tool arguments.
//
// officers/sans are sent as JSON-ENCODED STRINGS, not native arrays: the
// tool schema declares them via mcp.WithString (toolCeremonyInitialize),
// matching the codebase-wide MCP convention for array-valued arguments
// (sdk/go/transport/mcp/transport_ceremony.go's CeremonyInitialize
// json.Marshals req.Officers/req.SANs into string args; tools_vector.go's
// "ids" argument is the server-side precedent). A native []any argument
// value (the old form this test used to send) is never what a real client
// transmits and wire.DecodeArgs no longer accepts it.
func TestHandleCeremonyInitialize_ForwardsAllFields(t *testing.T) {
	svc := newMockCeremonyService()
	ca := &mockCeremonyCA{}
	handler := handleCeremonyInitialize(svc, ca)

	officersJSON, err := json.Marshal([]map[string]string{
		{"username": "officer-1", "csr_pem": "csr1"},
		{"username": "officer-2", "csr_pem": "csr2"},
	})
	if err != nil {
		t.Fatalf("marshal officers: %v", err)
	}
	sansJSON, err := json.Marshal([]string{"alt1.example", "alt2.example"})
	if err != nil {
		t.Fatalf("marshal sans: %v", err)
	}

	req := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{
		"so_pin":                   "pin",
		"user_pin":                 "upin",
		"credential_seal_strategy": "shamir",
		"threshold":                2,
		"officers":                 string(officersJSON),
		"hostname":                 "override.example",
		"sans":                     string(sansJSON),
	})

	if _, err := handler(context.Background(), req); err != nil {
		t.Fatalf("handleCeremonyInitialize: %v", err)
	}

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
	if len(got.SANs) != 2 || got.SANs[0] != "alt1.example" {
		t.Errorf("SANs = %v", got.SANs)
	}
	if len(got.Officers) != 2 || got.Officers[0].Username != "officer-1" || got.Officers[0].CSRPEM != "csr1" {
		t.Errorf("Officers = %+v", got.Officers)
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

// ceremonyStatusWireResponse mirrors the canonical snake_case wire shape
// handleCeremonyStatus now emits (a {"state":...,"spki_pin":...} map),
// matching REST's ceremonyStatusToResponse and
// transport.CeremonyStatusResponse's json tags -- NOT the raw
// service.CeremonyStatusInfo's PascalCase Go field names the handler used
// to marshal directly.
type ceremonyStatusWireResponse struct {
	State   string `json:"state"`
	SPKIPin string `json:"spki_pin"`
}

func TestHandleCeremonyStatus_Success(t *testing.T) {
	svc := newMockCeremonyService()
	handler := handleCeremonyStatus(svc)

	req := buildCallToolRequest(t, "qrdb_ceremony_status", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCeremonyStatus: %v", err)
	}

	text := resultText(t, result)
	var info ceremonyStatusWireResponse
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
	var signResult transport.SignCSRInitResponse
	if jsonErr := json.Unmarshal([]byte(text), &signResult); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if !strings.Contains(signResult.CertPEM, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("expected CertPEM to contain a PEM certificate, got: %q", signResult.CertPEM)
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
		"value": base64.StdEncoding.EncodeToString([]byte("secret123")),
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
		"value": base64.StdEncoding.EncodeToString([]byte("secret123")),
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
		"value": base64.StdEncoding.EncodeToString([]byte("secret123")),
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
		"new_value": base64.StdEncoding.EncodeToString([]byte("new-secret")),
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
		"new_value":            base64.StdEncoding.EncodeToString([]byte("new-secret")),
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
		"new_value": base64.StdEncoding.EncodeToString([]byte("v")),
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
		"new_value":            base64.StdEncoding.EncodeToString([]byte("new-secret")),
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
		"new_value": base64.StdEncoding.EncodeToString([]byte("new-secret")),
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
		Value []byte `json:"value"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if string(resp.Value) != "secret-v2" {
		t.Errorf("value: got %q, want %q", string(resp.Value), "secret-v2")
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
	if !contains(text, `"value":"Y3VycmVudA=="`) {
		t.Errorf("expected base64 current value in response, got %q", text)
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
