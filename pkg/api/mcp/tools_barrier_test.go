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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: BarrierServicer
// ---------------------------------------------------------------------------

type mockBarrierService struct {
	initializeFn            func(ctx context.Context, secret string) error
	unsealFn                func(ctx context.Context, secret string) error
	sealFn                  func(ctx context.Context) error
	statusFn                func(ctx context.Context) (*service.BarrierStatusInfo, error)
	initializeShamirFn      func(ctx context.Context, secret string, threshold, totalShares int) (*service.ShamirInitInfo, error)
	unsealWithShareFn       func(ctx context.Context, share string) (*service.UnsealShareProgress, error)
	unsealWithSharesFn      func(ctx context.Context, shares []string) error
	shamirListSharesFn      func(ctx context.Context) (*service.ShamirSharesInfo, error)
	shamirDeleteShareFn     func(ctx context.Context, index int) error
	shamirDeleteAllSharesFn func(ctx context.Context) error
	shamirVerifyFn          func(ctx context.Context) error
	rekeyFn                 func(ctx context.Context, threshold, total int) (*service.ShamirInitInfo, error)
	generateRecoveryKeysFn  func(ctx context.Context, threshold, total int) (*service.RecoveryKeysInfo, error)
	recoverWithKeysFn       func(ctx context.Context, keys []string) error
	deleteRecoveryKeysFn    func(ctx context.Context) error
	hasRecoveryKeysFn       func(ctx context.Context) (bool, error)
	generateRootTokenFn     func(ctx context.Context, shares []string) (*service.RootTokenInfo, error)
}

func newMockBarrierService() *mockBarrierService {
	return &mockBarrierService{}
}

func (m *mockBarrierService) BarrierInitialize(ctx context.Context, secret string) error {
	if m.initializeFn != nil {
		return m.initializeFn(ctx, secret)
	}
	return nil
}

func (m *mockBarrierService) BarrierUnseal(ctx context.Context, secret string) error {
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

func (m *mockBarrierService) BarrierInitializeShamir(ctx context.Context, secret string, threshold, totalShares int) (*service.ShamirInitInfo, error) {
	if m.initializeShamirFn != nil {
		return m.initializeShamirFn(ctx, secret, threshold, totalShares)
	}
	shares := make([]string, totalShares)
	for i := range shares {
		shares[i] = "share-" + string(rune('A'+i))
	}
	return &service.ShamirInitInfo{
		Shares:      shares,
		Threshold:   threshold,
		TotalShares: totalShares,
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

func (m *mockBarrierService) BarrierShamirListShares(ctx context.Context) (*service.ShamirSharesInfo, error) {
	if m.shamirListSharesFn != nil {
		return m.shamirListSharesFn(ctx)
	}
	return &service.ShamirSharesInfo{
		Count:     3,
		Threshold: 2,
		Total:     3,
	}, nil
}

func (m *mockBarrierService) BarrierShamirDeleteShare(ctx context.Context, index int) error {
	if m.shamirDeleteShareFn != nil {
		return m.shamirDeleteShareFn(ctx, index)
	}
	return nil
}

func (m *mockBarrierService) BarrierShamirDeleteAllShares(ctx context.Context) error {
	if m.shamirDeleteAllSharesFn != nil {
		return m.shamirDeleteAllSharesFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierShamirVerify(ctx context.Context) error {
	if m.shamirVerifyFn != nil {
		return m.shamirVerifyFn(ctx)
	}
	return nil
}

func (m *mockBarrierService) BarrierRekey(ctx context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
	if m.rekeyFn != nil {
		return m.rekeyFn(ctx, threshold, total)
	}
	shares := make([]string, total)
	for i := range shares {
		shares[i] = "new-share-" + string(rune('A'+i))
	}
	return &service.ShamirInitInfo{
		Shares:      shares,
		Threshold:   threshold,
		TotalShares: total,
	}, nil
}

func (m *mockBarrierService) BarrierGenerateRecoveryKeys(ctx context.Context, threshold, total int) (*service.RecoveryKeysInfo, error) {
	if m.generateRecoveryKeysFn != nil {
		return m.generateRecoveryKeysFn(ctx, threshold, total)
	}
	keys := make([]string, total)
	for i := range keys {
		keys[i] = "recovery-key-" + string(rune('A'+i))
	}
	return &service.RecoveryKeysInfo{
		Keys:      keys,
		Threshold: threshold,
		Total:     total,
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

// ---------------------------------------------------------------------------
// barrierTools registration
// ---------------------------------------------------------------------------

func TestBarrierTools_Registration(t *testing.T) {
	svc := newMockBarrierService()
	tools := barrierTools(svc)

	expectedNames := []string{
		"qrdb_barrier_initialize",
		"qrdb_barrier_unseal",
		"qrdb_barrier_seal",
		"qrdb_barrier_status",
		"qrdb_barrier_shamir_initialize",
		"qrdb_barrier_shamir_unseal_share",
		"qrdb_barrier_shamir_unseal_shares",
		"qrdb_barrier_shamir_list_shares",
		"qrdb_barrier_shamir_delete_share",
		"qrdb_barrier_shamir_delete_all_shares",
		"qrdb_barrier_shamir_verify",
		"qrdb_barrier_rekey",
		"qrdb_barrier_recovery_generate",
		"qrdb_barrier_recovery_recover",
		"qrdb_barrier_recovery_delete",
		"qrdb_barrier_recovery_status",
		"qrdb_barrier_root_token",
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
	handler := handleBarrierInitialize(svc)

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

func TestHandleBarrierInitialize_MissingSecret(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierInitialize(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_initialize", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing secret")
	}
}

func TestHandleBarrierInitialize_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.initializeFn = func(_ context.Context, _ string) error {
		return errors.New("barrier already initialized")
	}
	handler := handleBarrierInitialize(svc)

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
	svc.unsealFn = func(_ context.Context, _ string) error {
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
	handler := handleBarrierSeal(svc)

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
	handler := handleBarrierSeal(svc)

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

	text := resultText(t, result)
	var info service.BarrierStatusInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.Sealed {
		t.Error("expected sealed=false")
	}
	if info.Strategy != "shamir" {
		t.Errorf("strategy: got %q, want %q", info.Strategy, "shamir")
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
	handler := handleBarrierShamirInitialize(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", map[string]any{
		"secret":       "mysecret",
		"threshold":    float64(2),
		"total_shares": float64(3),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierShamirInitialize: %v", err)
	}

	text := resultText(t, result)
	var info service.ShamirInitInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.Threshold != 2 {
		t.Errorf("threshold: got %d, want %d", info.Threshold, 2)
	}
	if info.TotalShares != 3 {
		t.Errorf("total_shares: got %d, want %d", info.TotalShares, 3)
	}
	if len(info.Shares) != 3 {
		t.Errorf("shares: got %d, want %d", len(info.Shares), 3)
	}
}

func TestHandleBarrierShamirInitialize_MissingSecret(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirInitialize(svc)

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
	handler := handleBarrierShamirInitialize(svc)

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
	handler := handleBarrierShamirInitialize(svc)

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
	svc.initializeShamirFn = func(_ context.Context, _ string, _, _ int) (*service.ShamirInitInfo, error) {
		return nil, errors.New("already initialized")
	}
	handler := handleBarrierShamirInitialize(svc)

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
// handleBarrierShamirListShares
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirListShares_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirListShares(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_list_shares", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierShamirListShares: %v", err)
	}

	text := resultText(t, result)
	var info service.ShamirSharesInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.Count != 3 {
		t.Errorf("count: got %d, want %d", info.Count, 3)
	}
	if info.Threshold != 2 {
		t.Errorf("threshold: got %d, want %d", info.Threshold, 2)
	}
}

func TestHandleBarrierShamirListShares_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.shamirListSharesFn = func(_ context.Context) (*service.ShamirSharesInfo, error) {
		return nil, errors.New("list failed")
	}
	handler := handleBarrierShamirListShares(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_list_shares", map[string]any{})

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
// handleBarrierShamirDeleteShare
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirDeleteShare_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirDeleteShare(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_delete_share", map[string]any{
		"index": float64(2),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierShamirDeleteShare: %v", err)
	}

	text := resultText(t, result)
	if text != "share deleted" {
		t.Errorf("expected 'share deleted', got %q", text)
	}
}

func TestHandleBarrierShamirDeleteShare_MissingIndex(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirDeleteShare(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_delete_share", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing index")
	}
}

func TestHandleBarrierShamirDeleteShare_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.shamirDeleteShareFn = func(_ context.Context, _ int) error {
		return errors.New("share not found")
	}
	handler := handleBarrierShamirDeleteShare(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_delete_share", map[string]any{
		"index": float64(99),
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
// handleBarrierShamirDeleteAllShares
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirDeleteAllShares_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirDeleteAllShares(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_delete_all_shares", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierShamirDeleteAllShares: %v", err)
	}

	text := resultText(t, result)
	if text != "all shares deleted" {
		t.Errorf("expected 'all shares deleted', got %q", text)
	}
}

func TestHandleBarrierShamirDeleteAllShares_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.shamirDeleteAllSharesFn = func(_ context.Context) error {
		return errors.New("delete all failed")
	}
	handler := handleBarrierShamirDeleteAllShares(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_delete_all_shares", map[string]any{})

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
// handleBarrierShamirVerify
// ---------------------------------------------------------------------------

func TestHandleBarrierShamirVerify_Success(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierShamirVerify(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_verify", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierShamirVerify: %v", err)
	}

	text := resultText(t, result)
	if text != "shares verified" {
		t.Errorf("expected 'shares verified', got %q", text)
	}
}

func TestHandleBarrierShamirVerify_ServiceError(t *testing.T) {
	svc := newMockBarrierService()
	svc.shamirVerifyFn = func(_ context.Context) error {
		return errors.New("verification failed")
	}
	handler := handleBarrierShamirVerify(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_shamir_verify", map[string]any{})

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
	handler := handleBarrierRekey(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_rekey", map[string]any{
		"threshold": float64(3),
		"total":     float64(5),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierRekey: %v", err)
	}

	text := resultText(t, result)
	var info service.ShamirInitInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.Threshold != 3 {
		t.Errorf("threshold: got %d, want %d", info.Threshold, 3)
	}
	if info.TotalShares != 5 {
		t.Errorf("total_shares: got %d, want %d", info.TotalShares, 5)
	}
}

func TestHandleBarrierRekey_MissingThreshold(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRekey(svc)

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
	handler := handleBarrierRekey(svc)

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
	handler := handleBarrierRekey(svc)

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
	handler := handleBarrierRecoveryGenerate(svc)

	req := buildCallToolRequest(t, "qrdb_barrier_recovery_generate", map[string]any{
		"threshold": float64(2),
		"total":     float64(4),
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleBarrierRecoveryGenerate: %v", err)
	}

	text := resultText(t, result)
	var info service.RecoveryKeysInfo
	if jsonErr := json.Unmarshal([]byte(text), &info); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if info.Threshold != 2 {
		t.Errorf("threshold: got %d, want %d", info.Threshold, 2)
	}
	if info.Total != 4 {
		t.Errorf("total: got %d, want %d", info.Total, 4)
	}
	if len(info.Keys) != 4 {
		t.Errorf("keys: got %d, want %d", len(info.Keys), 4)
	}
}

func TestHandleBarrierRecoveryGenerate_MissingThreshold(t *testing.T) {
	svc := newMockBarrierService()
	handler := handleBarrierRecoveryGenerate(svc)

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
	handler := handleBarrierRecoveryGenerate(svc)

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
	handler := handleBarrierRecoveryGenerate(svc)

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
