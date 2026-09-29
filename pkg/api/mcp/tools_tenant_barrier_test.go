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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// ---------------------------------------------------------------------------
// handleTenantBarrierInit
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierInit_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantBarrierInit(svc, allowAllTenantInit)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_init", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing tenant_id")
	}
}

func TestHandleTenantBarrierInit_Success(t *testing.T) {
	var gotID, gotPassphrase string
	var gotThreshold, gotShares int
	svc := newMockTenantService()
	svc.barrierInitFn = func(_ context.Context, id string, passphrase []byte, threshold, shares int) error {
		gotID = id
		gotPassphrase = string(passphrase)
		gotThreshold = threshold
		gotShares = shares
		return nil
	}
	handler := handleTenantBarrierInit(svc, allowAllTenantInit)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_init", map[string]any{
		"tenant_id":  "acme",
		"passphrase": "supplied-secret",
		"threshold":  float64(3),
		"shares":     float64(5),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
	if gotID != "acme" || gotPassphrase != "supplied-secret" || gotThreshold != 3 || gotShares != 5 {
		t.Errorf("unexpected args: id=%q passphrase=%q threshold=%d shares=%d", gotID, gotPassphrase, gotThreshold, gotShares)
	}

	text := resultText(t, result)
	var payload struct {
		TenantID string `json:"tenant_id"`
		Strategy string `json:"strategy"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &payload); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if payload.TenantID != "acme" {
		t.Errorf("expected tenant_id=acme in init result, got %q", payload.TenantID)
	}
}

func TestHandleTenantBarrierInit_ServiceError(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierInitFn = func(_ context.Context, _ string, _ []byte, _, _ int) error {
		return tenant.ErrTenantNotFound
	}
	handler := handleTenantBarrierInit(svc, allowAllTenantInit)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_init", map[string]any{
		"tenant_id":  "missing",
		"passphrase": "supplied-secret",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
	if te.Tool != "qrdb_tenant_barrier_init" {
		t.Errorf("expected tool 'qrdb_tenant_barrier_init', got %q", te.Tool)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierUnseal
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierUnseal_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantBarrierUnseal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing tenant_id")
	}
}

func TestHandleTenantBarrierUnseal_SuccessWithKey(t *testing.T) {
	want := []byte("super-secret")
	var gotID string
	var gotKey []byte
	svc := newMockTenantService()
	svc.barrierUnsealFn = func(_ context.Context, id string, key []byte) error {
		gotID = id
		gotKey = key
		return nil
	}
	handler := handleTenantBarrierUnseal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{
		"tenant_id": "acme",
		"key":       base64.StdEncoding.EncodeToString(want),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
	if gotID != "acme" {
		t.Errorf("expected tenant_id 'acme', got %q", gotID)
	}
	if string(gotKey) != string(want) {
		t.Errorf("expected key %q, got %q", want, gotKey)
	}

	text := resultText(t, result)
	var payload map[string]bool
	if jsonErr := json.Unmarshal([]byte(text), &payload); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if !payload["unsealed"] {
		t.Error("expected unsealed=true")
	}
}

func TestHandleTenantBarrierUnseal_NoKeyDefaults(t *testing.T) {
	var gotKey []byte
	svc := newMockTenantService()
	svc.barrierUnsealFn = func(_ context.Context, _ string, key []byte) error {
		gotKey = key
		return nil
	}
	handler := handleTenantBarrierUnseal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{
		"tenant_id": "acme",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	if gotKey != nil {
		t.Errorf("expected nil key (use default), got %v", gotKey)
	}
}

func TestHandleTenantBarrierUnseal_BadBase64(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantBarrierUnseal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{
		"tenant_id": "acme",
		"key":       "!!!not-base64!!!",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid base64")
	}
}

func TestHandleTenantBarrierUnseal_ServiceError(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierUnsealFn = func(_ context.Context, _ string, _ []byte) error {
		return tenant.ErrTenantNotFound
	}
	handler := handleTenantBarrierUnseal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{
		"tenant_id": "missing",
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

// ---------------------------------------------------------------------------
// handleTenantBarrierSeal
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierSeal_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantBarrierSeal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_seal", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing tenant_id")
	}
}

func TestHandleTenantBarrierSeal_Success(t *testing.T) {
	called := false
	svc := newMockTenantService()
	svc.barrierSealFn = func(_ context.Context, id string) error {
		called = true
		if id != "acme" {
			t.Errorf("expected tenant_id 'acme', got %q", id)
		}
		return nil
	}
	handler := handleTenantBarrierSeal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_seal", map[string]any{
		"tenant_id": "acme",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
	if !called {
		t.Error("expected TenantBarrierSeal to be called")
	}

	text := resultText(t, result)
	var payload map[string]bool
	if jsonErr := json.Unmarshal([]byte(text), &payload); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if !payload["sealed"] {
		t.Error("expected sealed=true")
	}
}

func TestHandleTenantBarrierSeal_ServiceError(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierSealFn = func(_ context.Context, _ string) error {
		return errors.New("registry: not initialized")
	}
	handler := handleTenantBarrierSeal(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_seal", map[string]any{
		"tenant_id": "acme",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected ToolError")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
	if te.Tool != "qrdb_tenant_barrier_seal" {
		t.Errorf("expected tool 'qrdb_tenant_barrier_seal', got %q", te.Tool)
	}
}

// ---------------------------------------------------------------------------
// handleTenantBarrierStatus
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierStatus_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantBarrierStatus(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_status", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing tenant_id")
	}
}

func TestHandleTenantBarrierStatus_Success(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierStatusFn = func(_ context.Context, id string) (*tenant.TenantBarrierStatus, error) {
		return &tenant.TenantBarrierStatus{
			TenantID:       id,
			Sealed:         false,
			Initialized:    true,
			Strategy:       "passphrase",
			HardwareBacked: false,
		}, nil
	}
	handler := handleTenantBarrierStatus(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_status", map[string]any{
		"tenant_id": "acme",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}

	text := resultText(t, result)
	var status tenant.TenantBarrierStatus
	if jsonErr := json.Unmarshal([]byte(text), &status); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if status.TenantID != "acme" {
		t.Errorf("expected tenant_id 'acme', got %q", status.TenantID)
	}
	if status.Sealed {
		t.Error("expected sealed=false")
	}
	if !status.Initialized {
		t.Error("expected initialized=true")
	}
	if status.Strategy != "passphrase" {
		t.Errorf("expected strategy 'software', got %q", status.Strategy)
	}
}

func TestHandleTenantBarrierStatus_ServiceError(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierStatusFn = func(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
		return nil, tenant.ErrTenantNotFound
	}
	handler := handleTenantBarrierStatus(svc, allowAllTenantScope)

	req := buildCallToolRequest(t, "qrdb_tenant_barrier_status", map[string]any{
		"tenant_id": "missing",
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

// ---------------------------------------------------------------------------
// extMockMCPTenantService — wraps mockTenantService with configurable SO fns
// ---------------------------------------------------------------------------

type extMockMCPTenantService struct {
	tenant.TenantServicer
	*mockTenantService
	officerRegisterFn    func(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error)
	officerListFn        func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
	officerClaimStatusFn func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
	claimBeginFn         func(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error)
	claimCompleteFn      func(ctx context.Context, tenantID, username, nonceHex string, sig []byte, thumbprint string) (*ceremony.ShareClaimResult, error)
}

func newExtMCPTenantSvc() *extMockMCPTenantService {
	return &extMockMCPTenantService{mockTenantService: newMockTenantService()}
}

func (e *extMockMCPTenantService) TenantBarrierOfficerRegister(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	if e.officerRegisterFn != nil {
		return e.officerRegisterFn(ctx, tenantID, req)
	}
	return e.mockTenantService.TenantBarrierOfficerRegister(ctx, tenantID, req)
}
func (e *extMockMCPTenantService) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
	if e.officerListFn != nil {
		return e.officerListFn(ctx, tenantID)
	}
	return e.mockTenantService.TenantBarrierOfficerList(ctx, tenantID)
}
func (e *extMockMCPTenantService) TenantBarrierShareClaimBegin(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error) {
	if e.claimBeginFn != nil {
		return e.claimBeginFn(ctx, tenantID, username, thumbprint)
	}
	return e.mockTenantService.TenantBarrierShareClaimBegin(ctx, tenantID, username, thumbprint)
}
func (e *extMockMCPTenantService) TenantBarrierShareClaimComplete(ctx context.Context, tenantID, username, nonceHex string, sig []byte, thumbprint string) (*ceremony.ShareClaimResult, error) {
	if e.claimCompleteFn != nil {
		return e.claimCompleteFn(ctx, tenantID, username, nonceHex, sig, thumbprint)
	}
	return e.mockTenantService.TenantBarrierShareClaimComplete(ctx, tenantID, username, nonceHex, sig, thumbprint)
}

// Delegation pass-through for all other TenantServicer methods.

func (e *extMockMCPTenantService) CreateTenant(ctx context.Context, id, name string, iso tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	return e.mockTenantService.CreateTenant(ctx, id, name, iso)
}
func (e *extMockMCPTenantService) GetTenant(ctx context.Context, id string) (*tenant.TenantInfo, error) {
	return e.mockTenantService.GetTenant(ctx, id)
}
func (e *extMockMCPTenantService) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	return e.mockTenantService.ListTenants(ctx)
}
func (e *extMockMCPTenantService) UpdateTenant(ctx context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error) {
	return e.mockTenantService.UpdateTenant(ctx, id, p)
}
func (e *extMockMCPTenantService) DeleteTenant(ctx context.Context, id string) error {
	return e.mockTenantService.DeleteTenant(ctx, id)
}
func (e *extMockMCPTenantService) UpdateQuotas(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
	return e.mockTenantService.UpdateQuotas(ctx, id, q)
}

func (e *extMockMCPTenantService) UpdateEntitlements(ctx context.Context, id string, ent *quota.Entitlements) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Entitlements: ent}, nil
}
func (e *extMockMCPTenantService) TenantBarrierInit(ctx context.Context, id string, passphrase []byte, threshold, total int) (*tenant.TenantBarrierInitResult, error) {
	return e.mockTenantService.TenantBarrierInit(ctx, id, passphrase, threshold, total)
}
func (e *extMockMCPTenantService) TenantBarrierInitWithStrategy(ctx context.Context, id string, passphrase []byte, threshold, total int, sid seal.StrategyID) (*tenant.TenantBarrierInitResult, error) {
	return e.mockTenantService.TenantBarrierInitWithStrategy(ctx, id, passphrase, threshold, total, sid)
}
func (e *extMockMCPTenantService) TenantBarrierUnseal(ctx context.Context, id string, key []byte) error {
	return e.mockTenantService.TenantBarrierUnseal(ctx, id, key)
}
func (e *extMockMCPTenantService) TenantBarrierSeal(ctx context.Context, id string) error {
	return e.mockTenantService.TenantBarrierSeal(ctx, id)
}
func (e *extMockMCPTenantService) TenantBarrierStatus(ctx context.Context, id string) (*tenant.TenantBarrierStatus, error) {
	return e.mockTenantService.TenantBarrierStatus(ctx, id)
}
func (e *extMockMCPTenantService) IncrementQuotaUsage(ctx context.Context, id string, r quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	return e.mockTenantService.IncrementQuotaUsage(ctx, id, r, delta)
}
func (e *extMockMCPTenantService) DecrementQuotaUsage(ctx context.Context, id string, r quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	return e.mockTenantService.DecrementQuotaUsage(ctx, id, r, delta)
}
func (e *extMockMCPTenantService) GetQuotaUsage(ctx context.Context, id string, r quota.Resource) (int64, error) {
	return e.mockTenantService.GetQuotaUsage(ctx, id, r)
}

// ---------------------------------------------------------------------------
// Tenant barrier SO MCP handler tests
// ---------------------------------------------------------------------------

// handleTenantBarrierShareClaimBegin — mcpCertThumbprint error path.

// handleTenantBarrierShareClaimComplete tests.

// handleTenantBarrierShamirSealVote tests.

// TestTenantBarrierHandlers_ScopeDenied verifies every tenant-barrier tool
// handler refuses to invoke the tenant service when the fine-grained
// target-tenant authorizer denies the call, returning an MCP error result
// rather than a transport error.
func TestTenantBarrierHandlers_ScopeDenied(t *testing.T) {
	denied := errors.New("rbac: scope denied")
	deny := func(_ context.Context, gotTenant string, _ permission.Permission) error {
		if gotTenant != "victim" {
			t.Errorf("authorizer received tenant %q, want %q", gotTenant, "victim")
		}
		return denied
	}

	svc := newMockTenantService()
	svc.barrierInitFn = func(_ context.Context, _ string, _ []byte, _, _ int) error {
		t.Error("service must not be reached on scope denial")
		return nil
	}
	svc.barrierUnsealFn = func(_ context.Context, _ string, _ []byte) error {
		t.Error("service must not be reached on scope denial")
		return nil
	}
	svc.barrierSealFn = func(_ context.Context, _ string) error {
		t.Error("service must not be reached on scope denial")
		return nil
	}
	svc.barrierStatusFn = func(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
		t.Error("service must not be reached on scope denial")
		return nil, nil
	}

	cases := []struct {
		name    string
		handler mcpserver.ToolHandlerFunc
		args    map[string]any
	}{
		{"init", handleTenantBarrierInit(svc, func(ctx context.Context, tenantID string) error {
			return deny(ctx, tenantID, permission.TenantBarrierInit)
		}), map[string]any{"tenant_id": "victim", "passphrase": "x"}},
		{"unseal", handleTenantBarrierUnseal(svc, deny), map[string]any{"tenant_id": "victim"}},
		{"seal", handleTenantBarrierSeal(svc, deny), map[string]any{"tenant_id": "victim"}},
		{"status", handleTenantBarrierStatus(svc, deny), map[string]any{"tenant_id": "victim"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := buildCallToolRequest(t, "qrdb_tenant_barrier_"+tc.name, tc.args)
			result, err := tc.handler(context.Background(), req)
			if err != nil {
				t.Fatalf("expected MCP error result, got transport error: %v", err)
			}
			if !result.IsError {
				t.Fatal("expected IsError=true on scope denial")
			}
			if text := resultText(t, result); !strings.Contains(text, "authorization denied") {
				t.Errorf("expected 'authorization denied' in result, got %q", text)
			}
		})
	}
}
