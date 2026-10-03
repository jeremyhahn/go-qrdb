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
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: BarrierServicer
// ---------------------------------------------------------------------------

type mockBarrierService struct {
	service.BarrierServicer
	initializeFn           func(ctx context.Context, secret []byte) error
	unsealFn               func(ctx context.Context, secret []byte) error
	sealFn                 func(ctx context.Context) error
	statusFn               func(ctx context.Context) (*service.BarrierStatusInfo, error)
	initializeShamirFn     func(ctx context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error)
	generateRecoveryKeysFn func(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error)
	deleteRecoveryKeysFn   func(ctx context.Context) error
	hasRecoveryKeysFn      func(ctx context.Context) (bool, error)
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

func (m *mockBarrierService) BarrierInitializeShamir(ctx context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	if m.initializeShamirFn != nil {
		return m.initializeShamirFn(ctx, threshold, totalShares, policy)
	}
	return &service.ShamirInitInfo{Threshold: threshold, TotalShares: totalShares, ShareCount: totalShares}, nil
}

func (m *mockBarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
	if m.generateRecoveryKeysFn != nil {
		return m.generateRecoveryKeysFn(ctx, threshold, policy)
	}
	return &service.RecoveryKeysInfo{Threshold: threshold, Total: threshold + 2, ShareCount: threshold + 2}, nil
}

func (m *mockBarrierService) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if m.deleteRecoveryKeysFn != nil {
		return m.deleteRecoveryKeysFn(ctx)
	}
	return nil
}

// BarrierInitialized reports the double as never initialized: its
// initialize paths answer for themselves through their canned results.
func (m *mockBarrierService) BarrierInitialized(_ context.Context) (bool, error) { return false, nil }

// BarrierClusterUnseal answers as a node without cluster sealing does.
func (m *mockBarrierService) BarrierClusterUnseal(context.Context) (*clusterseal.Report, error) {
	return nil, clusterseal.ErrInactive
}

func (m *mockBarrierService) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	if m.hasRecoveryKeysFn != nil {
		return m.hasRecoveryKeysFn(ctx)
	}
	return true, nil
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
		"qrdb_barrier_cluster_unseal",
		"qrdb_barrier_shamir_initialize",
		"qrdb_barrier_recovery_generate",
		"qrdb_barrier_recovery_delete",
		"qrdb_barrier_recovery_status",
		// Registered even with a nil encMigrator (go-qrdb#558): the handler
		// itself refuses with a typed error rather than the tool being
		// omitted.
		"qrdb_barrier_enable_encryption",
		"qrdb_barrier_officer_register",
		"qrdb_barrier_recovery_officer_register",
		"qrdb_barrier_officer_list",
		"qrdb_barrier_share_claim_begin",
		"qrdb_barrier_share_claim_complete",
		"qrdb_barrier_recovery_share_claim_begin",
		"qrdb_barrier_recovery_share_claim_complete",
		"qrdb_barrier_quorum_begin",
		"qrdb_barrier_quorum_submit",
		"qrdb_barrier_rekey_start",
		"qrdb_barrier_officer_revoke",
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
	handler := handleBarrierSeal(svc, newStubPINBackend())

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
	handler := handleBarrierSeal(svc, newStubPINBackend())

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

func TestHandleBarrierShamirInitialize_MissingThreshold(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", map[string]any{
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
	svc.initializeShamirFn = func(_ context.Context, _, _ int, _ seal.FactorPolicy) (*service.ShamirInitInfo, error) {
		return nil, errors.New("already initialized")
	}
	handler := handleBarrierShamirInitialize(svc, nil)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", map[string]any{
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
// handleBarrierRecoveryGenerate
// ---------------------------------------------------------------------------

func TestHandleBarrierRecoveryGenerate_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRecoveryGenerate(svc, newStubPINBackend())

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
	handler := handleBarrierRecoveryGenerate(svc, newStubPINBackend())

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

func TestHandleBarrierRecoveryGenerate_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.generateRecoveryKeysFn = func(_ context.Context, _ int, _ seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
		return nil, errors.New("generate failed")
	}
	handler := handleBarrierRecoveryGenerate(svc, newStubPINBackend())

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
// CeremonyCANotConfiguredError
// ---------------------------------------------------------------------------

func TestCeremonyCANotConfiguredError(t *testing.T) {
	err := ErrCeremonyCANotConfigured
	if err.Error() != "mcp: ceremony CA not configured" {
		t.Errorf("error message: got %q", err.Error())
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
