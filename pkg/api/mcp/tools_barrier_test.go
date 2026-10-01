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
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: BarrierServicer
// ---------------------------------------------------------------------------

type mockBarrierService struct {
	initializeFn           func(ctx context.Context, secret []byte) error
	unsealFn               func(ctx context.Context, secret []byte) error
	sealFn                 func(ctx context.Context) error
	statusFn               func(ctx context.Context) (*service.BarrierStatusInfo, error)
	initializeShamirFn     func(ctx context.Context, secret []byte, threshold, totalShares int) (*service.ShamirInitInfo, error)
	unsealWithShareFn      func(ctx context.Context, share string) (*service.UnsealShareProgress, error)
	unsealWithSharesFn     func(ctx context.Context, shares []string) error
	rekeyFn                func(ctx context.Context, threshold, total int) (*service.ShamirInitInfo, error)
	generateRecoveryKeysFn func(ctx context.Context, threshold, total int) (*service.RecoveryKeysInfo, error)
	recoverWithKeysFn      func(ctx context.Context, keys []string) error
	deleteRecoveryKeysFn   func(ctx context.Context) error
	hasRecoveryKeysFn      func(ctx context.Context) (bool, error)
	generateRootTokenFn    func(ctx context.Context, shares []string) (*service.RootTokenInfo, error)
}

func newMockBarrierService() *mockBarrierService {
	return &mockBarrierService{}
}

func (m *mockBarrierService) BarrierInitialize(ctx context.Context, secret []byte) error {
	if m.initializeFn != nil {
		return m.initializeFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierUnseal(ctx context.Context, secret []byte) error {
	if m.unsealFn != nil {
		return m.unsealFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierSeal(ctx context.Context) error {
	if m.sealFn != nil {
		return m.sealFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierStatus(ctx context.Context) (*service.BarrierStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.BarrierStatusInfo{
		Sealed:         false,
		Strategy:       "shamir",
		HardwareBacked: false,
		InitializedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

func (m *mockBarrierService) BarrierInitializeShamir(ctx context.Context, secret []byte, threshold, totalShares int) (*service.ShamirInitInfo, error) {
	if m.initializeShamirFn != nil {
		return m.initializeShamirFn(ctx, secret, threshold, totalShares)
	}
	return &service.ShamirInitInfo{
		Threshold:   threshold,
		TotalShares: totalShares,
		ShareCount:  totalShares,
	}, nil
}

func (m *mockBarrierService) BarrierUnsealWithShare(ctx context.Context, share string) (*service.UnsealShareProgress, error) {
	if m.unsealWithShareFn != nil {
		return m.unsealWithShareFn(ctx, share)
	}
	return &service.UnsealShareProgress{
		Required:  3,
		Submitted: 1,
		Complete:  false,
	}, nil
}

func (m *mockBarrierService) BarrierUnsealWithShares(ctx context.Context, shares []string) error {
	if m.unsealWithSharesFn != nil {
		return m.unsealWithSharesFn(ctx, shares)
	}
	return nil
}

func (m *mockBarrierService) BarrierRekey(ctx context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
	if m.rekeyFn != nil {
		return m.rekeyFn(ctx, threshold, total)
	}
	return &service.ShamirInitInfo{
		Threshold:   threshold,
		TotalShares: total,
		ShareCount:  total,
	}, nil
}

func (m *mockBarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold, total int) (*service.RecoveryKeysInfo, error) {
	if m.generateRecoveryKeysFn != nil {
		return m.generateRecoveryKeysFn(ctx, threshold, total)
	}
	return &service.RecoveryKeysInfo{
		Threshold:  threshold,
		Total:      total,
		ShareCount: total,
	}, nil
}

func (m *mockBarrierService) BarrierRecoverWithKeys(ctx context.Context, keys []string) error {
	if m.recoverWithKeysFn != nil {
		return m.recoverWithKeysFn(ctx, keys)
	}
	return nil
}

func (m *mockBarrierService) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if m.deleteRecoveryKeysFn != nil {
		return m.deleteRecoveryKeysFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	if m.hasRecoveryKeysFn != nil {
		return m.hasRecoveryKeysFn(ctx)
	}
	return true, nil
}

func (m *mockBarrierService) BarrierGenerateRootToken(ctx context.Context, shares []string) (*service.RootTokenInfo, error) {
	if m.generateRootTokenFn != nil {
		return m.generateRootTokenFn(ctx, shares)
	}
	return &service.RootTokenInfo{
		Token:     "root-token-abc123",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}
func (m *mockBarrierService) BarrierOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return &service.BarrierOfficerRegisterResult{}, nil
}
func (m *mockBarrierService) BarrierOfficerList(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}
func (m *mockBarrierService) BarrierOfficerClaimStatus(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}
func (m *mockBarrierService) BarrierShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return &service.BarrierShareClaimChallenge{}, nil
}
func (m *mockBarrierService) BarrierShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return &service.BarrierShareClaimResult{}, nil
}
func (m *mockBarrierService) BarrierShamirSealVote(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
	return &service.UnsealShareProgress{}, nil
}
func (m *mockBarrierService) BarrierRecoveryOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return &service.BarrierOfficerRegisterResult{}, nil
}
func (m *mockBarrierService) BarrierRecoveryShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return &service.BarrierShareClaimChallenge{}, nil
}
func (m *mockBarrierService) BarrierRecoveryShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return &service.BarrierShareClaimResult{}, nil
}

// ---------------------------------------------------------------------------
// barrierTools registration
// ---------------------------------------------------------------------------

func TestBarrierTools_Registration(t *testing.T) {
	svc := newMockBarrierService()
	tools := barrierTools(svc, nil, nil)

	expectedNames := []string{
		"qrdb_barrier_initialize",
		"qrdb_barrier_unseal",
		"qrdb_barrier_seal",
		"qrdb_barrier_status",
		"qrdb_barrier_shamir_initialize",
		"qrdb_barrier_shamir_unseal_share",
		"qrdb_barrier_shamir_unseal_shares",
		"qrdb_barrier_rekey",
		"qrdb_barrier_recovery_generate",
		"qrdb_barrier_recovery_recover",
		"qrdb_barrier_recovery_delete",
		"qrdb_barrier_recovery_status",
		"qrdb_barrier_root_token",
		"qrdb_barrier_officer_register",
		"qrdb_barrier_officer_list",
		"qrdb_barrier_share_claim_begin",
		"qrdb_barrier_share_claim_complete",
		"qrdb_barrier_officer_claim_status",
		"qrdb_barrier_shamir_seal_vote",
		"qrdb_barrier_recovery_officer_register",
		"qrdb_barrier_recovery_share_claim_begin",
		"qrdb_barrier_recovery_share_claim_complete",
		// Registered even with a nil encMigrator (go-qrdb#558): the handler
		// itself refuses with a typed error rather than the tool being
		// omitted.
		"qrdb_barrier_enable_encryption",
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

func TestServer_WithBarrierService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	barrierSvc := newMockBarrierService()

	s := NewServer(kvSvc, adminSvc, ":0", WithBarrierService(barrierSvc))

	if s.barrierSvc != barrierSvc {
		t.Error("barrier service not set on server")
	}

	registeredTools := s.MCPServer().ListTools()
	if _, ok := registeredTools["qrdb_barrier_initialize"]; !ok {
		t.Error("qrdb_barrier_initialize not registered")
	}
	if _, ok := registeredTools["qrdb_barrier_status"]; !ok {
		t.Error("qrdb_barrier_status not registered")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierInitialize
// ---------------------------------------------------------------------------

func TestHandleBarrierInitialize_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_initialize", map[string]any{
		"secret": "mysecret",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierInitialize: %v", err)
	}

	text := resultText(t, result)
	if text != "barrier initialized" {
		t.Errorf("expected 'barrier initialized', got %q", text)
	}
}

// TestHandleBarrierInitialize_MissingSecret proves an empty secret is
// PASSED THROUGH to the service rather than rejected at the MCP handler,
// matching REST's handleBarrierInitialize (pkg/api/rest/handlers_barrier.go,
// no empty-secret check) and gRPC's qrdbServiceServer.BarrierInitialize
// (pkg/api/grpc/service_barrier.go, same). The handler-level rejection this
// test used to assert made `barrier init --barrier-strategy none` (which
// sends an empty secret -- the "none" strategy has no secret material)
// unreachable over MCP alone, breaking parity with every other transport.
// The seal layer itself still rejects an empty secret for strategies that
// require one (pkg/seal/errors.go's ErrInvalidPassphrase), so this is safe:
// it only removes a redundant, transport-specific, strategy-blind
// pre-check.
func TestHandleBarrierInitialize_MissingSecret(t *testing.T) {
	svc := newMockBarrierService()
	var gotSecret string
	gotCalled := false
	svc.initializeFn = func(_ context.Context, secret []byte) error {
		gotCalled = true
		gotSecret = string(secret)
		return nil
	}
	handler := handleBarrierInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_initialize", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success (empty secret passed through), got error result: %v", result)
	}
	if !gotCalled {
		t.Fatal("expected the service's BarrierInitialize to be called with the empty secret")
	}
	if gotSecret != "" {
		t.Errorf("expected the empty secret to reach the service unchanged, got %q", gotSecret)
	}
}

func TestHandleBarrierInitialize_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.initializeFn = func(_ context.Context, _ []byte) error {
		return errors.New("barrier already initialized")
	}
	handler := handleBarrierInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_initialize", map[string]any{
		"secret": "mysecret",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_barrier_initialize" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_barrier_initialize")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierUnseal
// ---------------------------------------------------------------------------

func TestHandleBarrierUnseal_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierUnseal(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_unseal", map[string]any{
		"secret": "mysecret",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierUnseal: %v", err)
	}

	text := resultText(t, result)
	if text != "barrier unsealed" {
		t.Errorf("expected 'barrier unsealed', got %q", text)
	}
}

func TestHandleBarrierUnseal_MissingSecret(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierUnseal(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_unseal", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing secret")
	}
}

func TestHandleBarrierUnseal_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.unsealFn = func(_ context.Context, _ []byte) error {
		return errors.New("wrong secret")
	}
	handler := handleBarrierUnseal(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_unseal", map[string]any{
		"secret": "wrongsecret",
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
// handleBarrierSeal
// ---------------------------------------------------------------------------

func TestHandleBarrierSeal_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierSeal(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_seal", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierSeal: %v", err)
	}

	text := resultText(t, result)
	if text != "barrier sealed" {
		t.Errorf("expected 'barrier sealed', got %q", text)
	}
}

func TestHandleBarrierSeal_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.sealFn = func(_ context.Context) error {
		return errors.New("seal failed")
	}
	handler := handleBarrierSeal(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_seal", map[string]any{})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_barrier_seal" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_barrier_seal")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierStatus
// ---------------------------------------------------------------------------

func TestHandleBarrierStatus_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierStatus(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_status", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierStatus: %v", err)
	}

	// Decode against the canonical snake_case wire shape (mirrors the
	// REST barrierStatusToResponse contract consumed by SDK clients).
	text := resultText(t, result)
	var wire struct {
		Sealed          bool   `json:"sealed"`
		Strategy        string `json:"strategy"`
		HardwareBacked  bool   `json:"hardware_backed"`
		InitializedAt   string `json:"initialized_at"`
		ShamirThreshold int    `json:"shamir_threshold"`
		ShamirTotal     int    `json:"shamir_total"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if wire.Sealed {
		t.Error("expected sealed=false")
	}
	if wire.Strategy != "shamir" {
		t.Errorf("strategy: got %q, want %q", wire.Strategy, "shamir")
	}
	if wire.InitializedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("initialized_at: got %q, want %q", wire.InitializedAt, "2026-01-01T00:00:00Z")
	}
}

func TestHandleBarrierStatus_ShamirFields(t *testing.T) {
	svc := newMockBarrierService()
	svc.statusFn = func(_ context.Context) (*service.BarrierStatusInfo, error) {
		return &service.BarrierStatusInfo{
			Sealed:          true,
			Strategy:        "shamir",
			ShamirThreshold: 2,
			ShamirTotal:     3,
		}, nil
	}
	handler := handleBarrierStatus(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_status", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierStatus: %v", err)
	}

	text := resultText(t, result)
	var wire struct {
		Sealed          bool   `json:"sealed"`
		Strategy        string `json:"strategy"`
		ShamirThreshold int    `json:"shamir_threshold"`
		ShamirTotal     int    `json:"shamir_total"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if wire.ShamirThreshold != 2 || wire.ShamirTotal != 3 {
		t.Errorf("shamir M/N: got %d/%d, want 2/3", wire.ShamirThreshold, wire.ShamirTotal)
	}
	// initialized_at is omitted for an uninitialized barrier.
	var raw map[string]any
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		t.Fatalf("unmarshal raw: %v", jsonErr)
	}
	if _, ok := raw["initialized_at"]; ok {
		t.Error("expected initialized_at omitted for zero InitializedAt")
	}
}

func TestHandleBarrierStatus_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.statusFn = func(_ context.Context) (*service.BarrierStatusInfo, error) {
		return nil, errors.New("status unavailable")
	}
	handler := handleBarrierStatus(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_status", map[string]any{})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_barrier_status" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_barrier_status")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierShamirInitialize
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirInitialize_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", map[string]any{
		"secret":       "mysecret",
		"threshold":    float64(2),
		"total_shares": float64(3),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierShamirInitialize: %v", err)
	}

	// Decode against the canonical snake_case wire shape produced by
	// shamirInitInfoToWire (consumed by SDK clients).
	text := resultText(t, result)
	var wire struct {
		Threshold   int `json:"threshold"`
		TotalShares int `json:"total_shares"`
		ShareCount  int `json:"share_count"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if wire.Threshold != 2 {
		t.Errorf("threshold: got %d, want %d", wire.Threshold, 2)
	}
	if wire.TotalShares != 3 {
		t.Errorf("total_shares: got %d, want %d", wire.TotalShares, 3)
	}
	if wire.ShareCount != 3 {
		t.Errorf("share_count: got %d, want %d", wire.ShareCount, 3)
	}
}

func TestHandleBarrierShamirInitialize_MissingSecret(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", map[string]any{
		"threshold":    float64(2),
		"total_shares": float64(3),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing secret")
	}
}

func TestHandleBarrierShamirInitialize_MissingThreshold(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", map[string]any{
		"secret":       "mysecret",
		"total_shares": float64(3),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing threshold")
	}
}

func TestHandleBarrierShamirInitialize_MissingTotalShares(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", map[string]any{
		"secret":    "mysecret",
		"threshold": float64(2),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing total_shares")
	}
}

func TestHandleBarrierShamirInitialize_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.initializeShamirFn = func(_ context.Context, _ []byte, _, _ int) (*service.ShamirInitInfo, error) {
		return nil, errors.New("already initialized")
	}
	handler := handleBarrierShamirInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", map[string]any{
		"secret":       "mysecret",
		"threshold":    float64(2),
		"total_shares": float64(3),
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
// handleBarrierShamirUnsealShare
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirUnsealShare_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirUnsealShare(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_unseal_share", map[string]any{
		"share": "share-value-ABC",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierShamirUnsealShare: %v", err)
	}

	text := resultText(t, result)
	var progress service.UnsealShareProgress
	if jsonErr := json.Unmarshal([]byte(text), &progress); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if progress.Required != 3 {
		t.Errorf("required: got %d, want %d", progress.Required, 3)
	}
	if progress.Submitted != 1 {
		t.Errorf("submitted: got %d, want %d", progress.Submitted, 1)
	}
}

func TestHandleBarrierShamirUnsealShare_MissingShare(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirUnsealShare(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_unseal_share", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing share")
	}
}

func TestHandleBarrierShamirUnsealShare_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.unsealWithShareFn = func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
		return nil, errors.New("invalid share")
	}
	handler := handleBarrierShamirUnsealShare(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_unseal_share", map[string]any{
		"share": "bad-share",
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
// handleBarrierShamirUnsealShares
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirUnsealShares_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirUnsealShares(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_unseal_shares", map[string]any{
		"shares": "share1, share2, share3",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierShamirUnsealShares: %v", err)
	}

	text := resultText(t, result)
	if text != "barrier unsealed" {
		t.Errorf("expected 'barrier unsealed', got %q", text)
	}
}

func TestHandleBarrierShamirUnsealShares_MissingShares(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirUnsealShares(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_unseal_shares", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing shares")
	}
}

func TestHandleBarrierShamirUnsealShares_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.unsealWithSharesFn = func(_ context.Context, _ []string) error {
		return errors.New("insufficient shares")
	}
	handler := handleBarrierShamirUnsealShares(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_unseal_shares", map[string]any{
		"shares": "share1,share2",
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
// handleBarrierRekey
// ---------------------------------------------------------------------------

func TestHandleBarrierRekey_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRekey(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_rekey", map[string]any{
		"threshold": float64(3),
		"total":     float64(5),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierRekey: %v", err)
	}

	// Decode against the canonical snake_case wire shape produced by
	// shamirInitInfoToWire (consumed by SDK clients).
	text := resultText(t, result)
	var wire struct {
		Threshold   int `json:"threshold"`
		TotalShares int `json:"total_shares"`
		ShareCount  int `json:"share_count"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if wire.Threshold != 3 {
		t.Errorf("threshold: got %d, want %d", wire.Threshold, 3)
	}
	if wire.TotalShares != 5 {
		t.Errorf("total_shares: got %d, want %d", wire.TotalShares, 5)
	}
}

func TestHandleBarrierRekey_MissingThreshold(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRekey(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_rekey", map[string]any{
		"total": float64(5),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing threshold")
	}
}

func TestHandleBarrierRekey_MissingTotal(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRekey(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_rekey", map[string]any{
		"threshold": float64(2),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing total")
	}
}

func TestHandleBarrierRekey_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.rekeyFn = func(_ context.Context, _, _ int) (*service.ShamirInitInfo, error) {
		return nil, errors.New("rekey failed")
	}
	handler := handleBarrierRekey(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_rekey", map[string]any{
		"threshold": float64(2),
		"total":     float64(3),
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
// handleBarrierRecoveryGenerate
// ---------------------------------------------------------------------------

func TestHandleBarrierRecoveryGenerate_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRecoveryGenerate(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_generate", map[string]any{
		"threshold": float64(2),
		"total":     float64(4),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierRecoveryGenerate: %v", err)
	}

	// Decode against the canonical snake_case wire shape.
	text := resultText(t, result)
	var wire struct {
		Threshold  int `json:"threshold"`
		Total      int `json:"total"`
		ShareCount int `json:"share_count"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if wire.Threshold != 2 {
		t.Errorf("threshold: got %d, want %d", wire.Threshold, 2)
	}
	if wire.Total != 4 {
		t.Errorf("total: got %d, want %d", wire.Total, 4)
	}
	if wire.ShareCount != 4 {
		t.Errorf("share_count: got %d, want %d", wire.ShareCount, 4)
	}
}

func TestHandleBarrierRecoveryGenerate_MissingThreshold(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRecoveryGenerate(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_generate", map[string]any{
		"total": float64(4),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing threshold")
	}
}

func TestHandleBarrierRecoveryGenerate_MissingTotal(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRecoveryGenerate(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_generate", map[string]any{
		"threshold": float64(2),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing total")
	}
}

func TestHandleBarrierRecoveryGenerate_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.generateRecoveryKeysFn = func(_ context.Context, _, _ int) (*service.RecoveryKeysInfo, error) {
		return nil, errors.New("generate failed")
	}
	handler := handleBarrierRecoveryGenerate(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_generate", map[string]any{
		"threshold": float64(2),
		"total":     float64(3),
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
// handleBarrierRecoveryRecover
// ---------------------------------------------------------------------------

func TestHandleBarrierRecoveryRecover_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRecoveryRecover(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_recover", map[string]any{
		"keys": "key1, key2, key3",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierRecoveryRecover: %v", err)
	}

	text := resultText(t, result)
	if text != "barrier recovered" {
		t.Errorf("expected 'barrier recovered', got %q", text)
	}
}

func TestHandleBarrierRecoveryRecover_MissingKeys(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRecoveryRecover(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_recover", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing keys")
	}
}

func TestHandleBarrierRecoveryRecover_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.recoverWithKeysFn = func(_ context.Context, _ []string) error {
		return errors.New("recovery failed")
	}
	handler := handleBarrierRecoveryRecover(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_recover", map[string]any{
		"keys": "key1,key2",
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
// handleBarrierRecoveryDelete
// ---------------------------------------------------------------------------

func TestHandleBarrierRecoveryDelete_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRecoveryDelete(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_delete", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierRecoveryDelete: %v", err)
	}

	text := resultText(t, result)
	if text != "recovery keys deleted" {
		t.Errorf("expected 'recovery keys deleted', got %q", text)
	}
}

func TestHandleBarrierRecoveryDelete_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.deleteRecoveryKeysFn = func(_ context.Context) error {
		return errors.New("delete failed")
	}
	handler := handleBarrierRecoveryDelete(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_delete", map[string]any{})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_barrier_recovery_delete" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_barrier_recovery_delete")
	}
}

// ---------------------------------------------------------------------------
// handleBarrierRecoveryStatus
// ---------------------------------------------------------------------------

func TestHandleBarrierRecoveryStatus_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRecoveryStatus(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_status", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierRecoveryStatus: %v", err)
	}

	text := resultText(t, result)
	var resp struct {
		HasRecoveryKeys bool `json:"has_recovery_keys"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if !resp.HasRecoveryKeys {
		t.Error("expected has_recovery_keys=true")
	}
}

func TestHandleBarrierRecoveryStatus_False(t *testing.T) {
	svc := newMockBarrierService()
	svc.hasRecoveryKeysFn = func(_ context.Context) (bool, error) {
		return false, nil
	}
	handler := handleBarrierRecoveryStatus(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_status", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierRecoveryStatus: %v", err)
	}

	text := resultText(t, result)
	var resp struct {
		HasRecoveryKeys bool `json:"has_recovery_keys"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.HasRecoveryKeys {
		t.Error("expected has_recovery_keys=false")
	}
}

func TestHandleBarrierRecoveryStatus_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.hasRecoveryKeysFn = func(_ context.Context) (bool, error) {
		return false, errors.New("status failed")
	}
	handler := handleBarrierRecoveryStatus(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_status", map[string]any{})

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
// handleBarrierRootToken
// ---------------------------------------------------------------------------

func TestHandleBarrierRootToken_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRootToken(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_root_token", map[string]any{
		"shares": "share1, share2, share3",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierRootToken: %v", err)
	}

	text := resultText(t, result)
	var info service.RootTokenInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.Token != "root-token-abc123" {
		t.Errorf("token: got %q, want %q", info.Token, "root-token-abc123")
	}
}

func TestHandleBarrierRootToken_MissingShares(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRootToken(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_root_token", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing shares")
	}
}

func TestHandleBarrierRootToken_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.generateRootTokenFn = func(_ context.Context, _ []string) (*service.RootTokenInfo, error) {
		return nil, errors.New("root token generation failed")
	}
	handler := handleBarrierRootToken(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_root_token", map[string]any{
		"shares": "share1,share2,share3",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_barrier_root_token" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_barrier_root_token")
	}
}

// ---------------------------------------------------------------------------
// CeremonyCANotConfiguredError
// ---------------------------------------------------------------------------

func TestCeremonyCANotConfiguredError(t *testing.T) {
	err := ErrCeremonyCANotConfigured
	if err.Error() != "mcp: ceremony CA not configured" {
		t.Errorf("error message: got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// extMockMCPBarrierService — wraps mockBarrierService with configurable SO fns
// ---------------------------------------------------------------------------

type extMockMCPBarrierService struct {
	*mockBarrierService
	officerRegisterFn         func(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	officerListFn             func(ctx context.Context) ([]*service.BarrierOfficerStatus, error)
	officerClaimStatusFn      func(ctx context.Context) ([]*service.BarrierOfficerStatus, error)
	shareClaimBeginFn         func(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	shareClaimCompleteFn      func(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
	shamirSealVoteFn          func(ctx context.Context, share string) (*service.UnsealShareProgress, error)
	recoveryOfficerRegisterFn func(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	recoveryClaimBeginFn      func(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error)
	recoveryClaimCompleteFn   func(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error)
}

func newExtMCPBarrierSvc() *extMockMCPBarrierService {
	return &extMockMCPBarrierService{mockBarrierService: newMockBarrierService()}
}

var _ service.BarrierServicer = (*extMockMCPBarrierService)(nil)

func (e *extMockMCPBarrierService) BarrierOfficerRegister(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if e.officerRegisterFn != nil {
		return e.officerRegisterFn(ctx, req)
	}
	return e.mockBarrierService.BarrierOfficerRegister(ctx, req)
}
func (e *extMockMCPBarrierService) BarrierOfficerList(ctx context.Context) ([]*service.BarrierOfficerStatus, error) {
	if e.officerListFn != nil {
		return e.officerListFn(ctx)
	}
	return e.mockBarrierService.BarrierOfficerList(ctx)
}
func (e *extMockMCPBarrierService) BarrierOfficerClaimStatus(ctx context.Context) ([]*service.BarrierOfficerStatus, error) {
	if e.officerClaimStatusFn != nil {
		return e.officerClaimStatusFn(ctx)
	}
	return e.mockBarrierService.BarrierOfficerClaimStatus(ctx)
}
func (e *extMockMCPBarrierService) BarrierShareClaimBegin(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if e.shareClaimBeginFn != nil {
		return e.shareClaimBeginFn(ctx, username, thumbprint)
	}
	return e.mockBarrierService.BarrierShareClaimBegin(ctx, username, thumbprint)
}
func (e *extMockMCPBarrierService) BarrierShareClaimComplete(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	if e.shareClaimCompleteFn != nil {
		return e.shareClaimCompleteFn(ctx, username, nonceHex, sig, thumbprint)
	}
	return e.mockBarrierService.BarrierShareClaimComplete(ctx, username, nonceHex, sig, thumbprint)
}
func (e *extMockMCPBarrierService) BarrierShamirSealVote(ctx context.Context, share string) (*service.UnsealShareProgress, error) {
	if e.shamirSealVoteFn != nil {
		return e.shamirSealVoteFn(ctx, share)
	}
	return e.mockBarrierService.BarrierShamirSealVote(ctx, share)
}
func (e *extMockMCPBarrierService) BarrierRecoveryOfficerRegister(ctx context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if e.recoveryOfficerRegisterFn != nil {
		return e.recoveryOfficerRegisterFn(ctx, req)
	}
	return e.mockBarrierService.BarrierRecoveryOfficerRegister(ctx, req)
}
func (e *extMockMCPBarrierService) BarrierRecoveryShareClaimBegin(ctx context.Context, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	if e.recoveryClaimBeginFn != nil {
		return e.recoveryClaimBeginFn(ctx, username, thumbprint)
	}
	return e.mockBarrierService.BarrierRecoveryShareClaimBegin(ctx, username, thumbprint)
}
func (e *extMockMCPBarrierService) BarrierRecoveryShareClaimComplete(ctx context.Context, username, nonceHex string, sig []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	if e.recoveryClaimCompleteFn != nil {
		return e.recoveryClaimCompleteFn(ctx, username, nonceHex, sig, thumbprint)
	}
	return e.mockBarrierService.BarrierRecoveryShareClaimComplete(ctx, username, nonceHex, sig, thumbprint)
}

// ---------------------------------------------------------------------------
// System barrier SO MCP handler tests
// ---------------------------------------------------------------------------

func TestHandleBarrierOfficerRegister_MCP_MissingUsername(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	handler := handleBarrierOfficerRegister(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_officer_register", map[string]any{
		"csr_pem": "csr-data",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing username")
	}
}

func TestHandleBarrierOfficerRegister_MCP_MissingCSR(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	handler := handleBarrierOfficerRegister(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_officer_register", map[string]any{
		"username": "alice",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing csr_pem")
	}
}

func TestHandleBarrierOfficerRegister_MCP_Success(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	svc.officerRegisterFn = func(_ context.Context, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		if req.Username != "alice" {
			t.Fatalf("username = %q, want alice", req.Username)
		}
		return &service.BarrierOfficerRegisterResult{CertPEM: []byte("signed-cert"), ShareIndex: 1}, nil
	}
	handler := handleBarrierOfficerRegister(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_officer_register", map[string]any{
		"username": "alice",
		"csr_pem":  "csr-data",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleBarrierOfficerRegister_MCP_ServiceError(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	svc.officerRegisterFn = func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		return nil, service.ErrCeremonyNotConfigured
	}
	handler := handleBarrierOfficerRegister(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_officer_register", map[string]any{
		"username": "alice",
		"csr_pem":  "csr-data",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

func TestHandleBarrierOfficerList_MCP_Success(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	svc := newExtMCPBarrierSvc()
	svc.officerListFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return []*service.BarrierOfficerStatus{
			{Username: "alice", ShareIndex: 1, EnrolledAt: now},
		}, nil
	}
	handler := handleBarrierOfficerList(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_officer_list", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
	text := resultText(t, result)
	var resp map[string]interface{}
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	officers, ok := resp["officers"].([]interface{})
	if !ok || len(officers) != 1 {
		t.Fatalf("expected 1 officer in officers array, got %v", resp["officers"])
	}
}

func TestHandleBarrierOfficerList_MCP_ServiceError(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	svc.officerListFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return nil, ceremony.ErrInsufficientOfficers
	}
	handler := handleBarrierOfficerList(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_officer_list", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

func TestHandleBarrierOfficerClaimStatus_MCP_Success(t *testing.T) {
	now := time.Date(2026, 2, 3, 4, 0, 0, 0, time.UTC)
	svc := newExtMCPBarrierSvc()
	svc.officerClaimStatusFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return []*service.BarrierOfficerStatus{
			{Username: "bob", ShareIndex: 2, ClaimedAt: now},
		}, nil
	}
	handler := handleBarrierOfficerClaimStatus(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_officer_claim_status", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleBarrierOfficerClaimStatus_MCP_ServiceError(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	svc.officerClaimStatusFn = func(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
		return nil, ceremony.ErrOfficerNotFound
	}
	handler := handleBarrierOfficerClaimStatus(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_officer_claim_status", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

// handleBarrierShareClaimBegin — mcpCertThumbprint error (no TLS in ctx).

func TestHandleBarrierShareClaimBegin_MCP_MissingUsername(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	handler := handleBarrierShareClaimBegin(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_share_claim_begin", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing username")
	}
}

func TestHandleBarrierShareClaimBegin_MCP_MissingCert(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	handler := handleBarrierShareClaimBegin(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_share_claim_begin", map[string]any{
		"username": "alice",
	})
	// context.Background() has no TLS state → mcpCertThumbprint returns ToolError.
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError for missing client cert")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

func TestHandleBarrierShareClaimBegin_MCP_ServiceError(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	svc.shareClaimBeginFn = func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
		return nil, ceremony.ErrOfficerNotFound
	}
	handler := handleBarrierShareClaimBegin(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_share_claim_begin", map[string]any{
		"username": "alice",
	})
	_, err := handler(ctxWithFakeTLS(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

func TestHandleBarrierShareClaimBegin_MCP_Success(t *testing.T) {
	expires := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	svc := newExtMCPBarrierSvc()
	svc.shareClaimBeginFn = func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
		return &service.BarrierShareClaimChallenge{NonceHex: "deadbeef", ExpiresAt: expires}, nil
	}
	handler := handleBarrierShareClaimBegin(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_share_claim_begin", map[string]any{
		"username": "alice",
	})
	result, err := handler(ctxWithFakeTLS(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
}

// handleBarrierShareClaimComplete tests.

func TestHandleBarrierShareClaimComplete_MCP_MissingUsername(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	handler := handleBarrierShareClaimComplete(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_share_claim_complete", map[string]any{
		"nonce_hex": "aabb",
		"signature": base64.StdEncoding.EncodeToString([]byte("sig")),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing username")
	}
}

func TestHandleBarrierShareClaimComplete_MCP_MissingNonce(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	handler := handleBarrierShareClaimComplete(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_share_claim_complete", map[string]any{
		"username":  "alice",
		"signature": base64.StdEncoding.EncodeToString([]byte("sig")),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing nonce_hex")
	}
}

func TestHandleBarrierShareClaimComplete_MCP_MissingSignature(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	handler := handleBarrierShareClaimComplete(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_share_claim_complete", map[string]any{
		"username":  "alice",
		"nonce_hex": "aabb",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing signature")
	}
}

func TestHandleBarrierShareClaimComplete_MCP_BadBase64(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	handler := handleBarrierShareClaimComplete(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_share_claim_complete", map[string]any{
		"username":  "alice",
		"nonce_hex": "aabb",
		"signature": "!!!not-base64!!!",
	})
	// mcpCertThumbprint is called before base64 decode, so we inject a valid cert.
	result, err := handler(ctxWithFakeTLS(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for bad base64 signature")
	}
}

func TestHandleBarrierShareClaimComplete_MCP_MissingCert(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	handler := handleBarrierShareClaimComplete(svc)

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	req := buildCallToolRequest(t, "qrdb_barrier_share_claim_complete", map[string]any{
		"username":  "alice",
		"nonce_hex": "aabb",
		"signature": sig,
	})
	// No TLS state in context → mcpCertThumbprint returns ToolError.
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError for missing client cert")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

func TestHandleBarrierShareClaimComplete_MCP_ServiceError(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	svc.shareClaimCompleteFn = func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
		return nil, ceremony.ErrCertThumbprintMismatch
	}
	handler := handleBarrierShareClaimComplete(svc)

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	req := buildCallToolRequest(t, "qrdb_barrier_share_claim_complete", map[string]any{
		"username":  "alice",
		"nonce_hex": "aabb",
		"signature": sig,
	})
	_, err := handler(ctxWithFakeTLS(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

func TestHandleBarrierShareClaimComplete_MCP_Success(t *testing.T) {
	shareBytes := []byte("shamir-share-bytes")
	svc := newExtMCPBarrierSvc()
	svc.shareClaimCompleteFn = func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
		return &service.BarrierShareClaimResult{ShareIndex: 1, ShareBytes: shareBytes}, nil
	}
	handler := handleBarrierShareClaimComplete(svc)

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	req := buildCallToolRequest(t, "qrdb_barrier_share_claim_complete", map[string]any{
		"username":  "alice",
		"nonce_hex": "aabb",
		"signature": sig,
	})
	result, err := handler(ctxWithFakeTLS(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
}

// handleBarrierShamirSealVote tests.

func TestHandleBarrierShamirSealVote_MCP_MissingShare(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	handler := handleBarrierShamirSealVote(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_seal_vote", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing share")
	}
}

func TestHandleBarrierShamirSealVote_MCP_Success(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	svc.shamirSealVoteFn = func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
		return &service.UnsealShareProgress{Required: 3, Submitted: 1, Complete: false}, nil
	}
	handler := handleBarrierShamirSealVote(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_seal_vote", map[string]any{
		"share": "abc123",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleBarrierShamirSealVote_MCP_ServiceError(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	svc.shamirSealVoteFn = func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
		return nil, seal.ErrSealVoteVerificationFailed
	}
	handler := handleBarrierShamirSealVote(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_seal_vote", map[string]any{
		"share": "abc",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

// Recovery officer tests.

func TestHandleBarrierRecoveryOfficerRegister_MCP_Success(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	svc.recoveryOfficerRegisterFn = func(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
		return &service.BarrierOfficerRegisterResult{CertPEM: []byte("recovery-cert"), ShareIndex: 2}, nil
	}
	handler := handleBarrierRecoveryOfficerRegister(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_officer_register", map[string]any{
		"username": "recov-officer",
		"csr_pem":  "csr-data",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleBarrierRecoveryOfficerRegister_MCP_MissingUsername(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	handler := handleBarrierRecoveryOfficerRegister(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_officer_register", map[string]any{
		"csr_pem": "csr-data",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing username")
	}
}

func TestHandleBarrierRecoveryShareClaimBegin_MCP_MissingCert(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	handler := handleBarrierRecoveryShareClaimBegin(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_share_claim_begin", map[string]any{
		"username": "alice",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError for missing client cert")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

func TestHandleBarrierRecoveryShareClaimBegin_MCP_Success(t *testing.T) {
	expires := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	svc := newExtMCPBarrierSvc()
	svc.recoveryClaimBeginFn = func(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
		return &service.BarrierShareClaimChallenge{NonceHex: "cc00ff", ExpiresAt: expires}, nil
	}
	handler := handleBarrierRecoveryShareClaimBegin(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_share_claim_begin", map[string]any{
		"username": "alice",
	})
	result, err := handler(ctxWithFakeTLS(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
}

func TestHandleBarrierRecoveryShareClaimComplete_MCP_MissingCert(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	handler := handleBarrierRecoveryShareClaimComplete(svc)

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	req := buildCallToolRequest(t, "qrdb_barrier_recovery_share_claim_complete", map[string]any{
		"username":  "alice",
		"nonce_hex": "aabb",
		"signature": sig,
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError for missing client cert")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}

func TestHandleBarrierRecoveryShareClaimComplete_MCP_Success(t *testing.T) {
	svc := newExtMCPBarrierSvc()
	svc.recoveryClaimCompleteFn = func(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
		return &service.BarrierShareClaimResult{ShareIndex: 2, ShareBytes: []byte("recovery-share")}, nil
	}
	handler := handleBarrierRecoveryShareClaimComplete(svc)

	sig := base64.StdEncoding.EncodeToString([]byte("sig"))
	req := buildCallToolRequest(t, "qrdb_barrier_recovery_share_claim_complete", map[string]any{
		"username":  "alice",
		"nonce_hex": "aabb",
		"signature": sig,
	})
	result, err := handler(ctxWithFakeTLS(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
}

// mcpCertThumbprint success path (covered via ShareClaimBegin above, but also
// test the Error() method on mcpClientCertRequiredError directly).

func TestMCPClientCertRequiredError(t *testing.T) {
	e := &mcpClientCertRequiredError{}
	if e.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestMCPCertThumbprint_Success(t *testing.T) {
	ctx := ctxWithFakeTLS()
	thumb, err := mcpCertThumbprint(ctx, "test-tool")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thumb == "" {
		t.Fatal("expected non-empty thumbprint")
	}
}

func TestMCPCertThumbprint_MissingTLS(t *testing.T) {
	_, err := mcpCertThumbprint(context.Background(), "test-tool")
	if err == nil {
		t.Fatal("expected ToolError for missing TLS state")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
}
