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

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// handleTenantBarrierInit
// ---------------------------------------------------------------------------

func TestHandleTenantBarrierInit_EmptyID(t *testing.T) {
	svc := newMockTenantService()
	handler := handleTenantBarrierInit(svc)

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
	svc.barrierInitFn = func(_ context.Context, id, passphrase string, threshold, shares int) error {
		gotID = id
		gotPassphrase = passphrase
		gotThreshold = threshold
		gotShares = shares
		return nil
	}
	handler := handleTenantBarrierInit(svc)

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
	var payload map[string]bool
	if jsonErr := json.Unmarshal([]byte(text), &payload); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if !payload["initialized"] {
		t.Error("expected initialized=true")
	}
}

func TestHandleTenantBarrierInit_ServiceError(t *testing.T) {
	svc := newMockTenantService()
	svc.barrierInitFn = func(_ context.Context, _, _ string, _, _ int) error {
		return tenant.ErrTenantNotFound
	}
	handler := handleTenantBarrierInit(svc)

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
	handler := handleTenantBarrierUnseal(svc)

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
	handler := handleTenantBarrierUnseal(svc)

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
	handler := handleTenantBarrierUnseal(svc)

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
	handler := handleTenantBarrierUnseal(svc)

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
	handler := handleTenantBarrierUnseal(svc)

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
	handler := handleTenantBarrierSeal(svc)

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
	handler := handleTenantBarrierSeal(svc)

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
	handler := handleTenantBarrierSeal(svc)

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
	handler := handleTenantBarrierStatus(svc)

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
	handler := handleTenantBarrierStatus(svc)

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
	handler := handleTenantBarrierStatus(svc)

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
