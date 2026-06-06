// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// mockAuthFactorService implements service.AuthFactorServicer for MCP tests.
// ---------------------------------------------------------------------------

type mockAuthFactorService struct {
	enrollFn    func(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error)
	challengeFn func(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error)
	verifyFn    func(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error
	listFn      func(ctx context.Context, subject string) ([]*authfactors.Enrollment, error)
	revokeFn    func(ctx context.Context, enrollmentID string) error
}

var _ service.AuthFactorServicer = (*mockAuthFactorService)(nil)

func newMockAuthFactorService() *mockAuthFactorService { return &mockAuthFactorService{} }

func (m *mockAuthFactorService) EnrollFactor(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	if m.enrollFn != nil {
		return m.enrollFn(ctx, factorName, subject, params)
	}
	return &authfactors.Enrollment{
		EnrollmentID: "e-1",
		FactorName:   factorName,
		UserID:       subject,
		TenantID:     params.TenantID,
		EnrolledAt:   time.Now(),
	}, nil
}

func (m *mockAuthFactorService) ChallengeFactor(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error) {
	if m.challengeFn != nil {
		return m.challengeFn(ctx, enrollmentID)
	}
	return &authfactors.Challenge{
		ChallengeID:  "ch-1",
		EnrollmentID: enrollmentID,
		Nonce:        []byte("nonce"),
		ExpiresAt:    time.Now().Add(5 * time.Minute),
	}, nil
}

func (m *mockAuthFactorService) VerifyFactor(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, factorName, challengeID, response)
	}
	return nil
}

func (m *mockAuthFactorService) ListFactors(ctx context.Context, subject string) ([]*authfactors.Enrollment, error) {
	if m.listFn != nil {
		return m.listFn(ctx, subject)
	}
	return []*authfactors.Enrollment{
		{EnrollmentID: "e-1", FactorName: "pkcs11", UserID: subject, EnrolledAt: time.Now()},
	}, nil
}

func (m *mockAuthFactorService) RevokeFactor(ctx context.Context, enrollmentID string) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, enrollmentID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// handleAuthFactorEnroll
// ---------------------------------------------------------------------------

func TestHandleAuthFactorEnroll_MissingFactorName(t *testing.T) {
	svc := newMockAuthFactorService()
	handler := handleAuthFactorEnroll(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_enroll", map[string]any{
		"subject": "alice",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing factor_name")
	}
}

func TestHandleAuthFactorEnroll_MissingSubject(t *testing.T) {
	svc := newMockAuthFactorService()
	handler := handleAuthFactorEnroll(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_enroll", map[string]any{
		"factor_name": "pkcs11",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing subject")
	}
}

func TestHandleAuthFactorEnroll_InvalidMethodParams(t *testing.T) {
	svc := newMockAuthFactorService()
	handler := handleAuthFactorEnroll(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_enroll", map[string]any{
		"factor_name":   "pkcs11",
		"subject":       "alice",
		"method_params": "{bad json",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid method_params JSON")
	}
}

func TestHandleAuthFactorEnroll_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.enrollFn = func(_ context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
		return &authfactors.Enrollment{
			EnrollmentID: "e-99",
			FactorName:   factorName,
			UserID:       subject,
			TenantID:     params.TenantID,
			EnrolledAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}, nil
	}
	handler := handleAuthFactorEnroll(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_enroll", map[string]any{
		"factor_name": "piv",
		"subject":     "bob",
		"tenant_id":   "t1",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
	text := resultText(t, result)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["enrollment_id"] != "e-99" {
		t.Errorf("expected enrollment_id 'e-99', got %v", out["enrollment_id"])
	}
}

func TestHandleAuthFactorEnroll_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.enrollFn = func(_ context.Context, _, _ string, _ authfactors.EnrollParams) (*authfactors.Enrollment, error) {
		return nil, errors.New("hardware not found")
	}
	handler := handleAuthFactorEnroll(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_enroll", map[string]any{
		"factor_name": "pkcs11",
		"subject":     "alice",
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
// handleAuthFactorChallenge
// ---------------------------------------------------------------------------

func TestHandleAuthFactorChallenge_MissingEnrollmentID(t *testing.T) {
	svc := newMockAuthFactorService()
	handler := handleAuthFactorChallenge(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_challenge", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing enrollment_id")
	}
}

func TestHandleAuthFactorChallenge_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	handler := handleAuthFactorChallenge(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_challenge", map[string]any{
		"enrollment_id": "e-1",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
	text := resultText(t, result)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["challenge_id"] != "ch-1" {
		t.Errorf("expected challenge_id 'ch-1', got %v", out["challenge_id"])
	}
}

func TestHandleAuthFactorChallenge_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.challengeFn = func(_ context.Context, _ string) (*authfactors.Challenge, error) {
		return nil, errors.New("enrollment revoked")
	}
	handler := handleAuthFactorChallenge(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_challenge", map[string]any{
		"enrollment_id": "e-1",
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
// handleAuthFactorVerify
// ---------------------------------------------------------------------------

func TestHandleAuthFactorVerify_MissingChallengeID(t *testing.T) {
	svc := newMockAuthFactorService()
	handler := handleAuthFactorVerify(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_verify", map[string]any{
		"response_json": `{"value":"c2ln"}`,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing challenge_id")
	}
}

func TestHandleAuthFactorVerify_MissingResponseJSON(t *testing.T) {
	svc := newMockAuthFactorService()
	handler := handleAuthFactorVerify(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_verify", map[string]any{
		"challenge_id": "ch-1",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing response_json")
	}
}

func TestHandleAuthFactorVerify_InvalidResponseJSON(t *testing.T) {
	svc := newMockAuthFactorService()
	handler := handleAuthFactorVerify(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_verify", map[string]any{
		"challenge_id":  "ch-1",
		"response_json": "{bad json",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for invalid response_json")
	}
}

func TestHandleAuthFactorVerify_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	handler := handleAuthFactorVerify(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_verify", map[string]any{
		"challenge_id":  "ch-1",
		"response_json": `{"value":"c2ln","metadata":{}}`,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
	text := resultText(t, result)
	if text != `{"status":"verified"}` {
		t.Errorf("unexpected response: %q", text)
	}
}

func TestHandleAuthFactorVerify_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.verifyFn = func(_ context.Context, _, _ string, _ *authfactors.Response) error {
		return errors.New("sig mismatch")
	}
	handler := handleAuthFactorVerify(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_verify", map[string]any{
		"challenge_id":  "ch-1",
		"response_json": `{"value":"c2ln"}`,
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
// handleAuthFactorList
// ---------------------------------------------------------------------------

func TestHandleAuthFactorList_Success(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.listFn = func(_ context.Context, subject string) ([]*authfactors.Enrollment, error) {
		revokedAt := time.Now().Add(-time.Hour)
		return []*authfactors.Enrollment{
			{EnrollmentID: "e-1", FactorName: "pkcs11", UserID: subject, EnrolledAt: time.Now()},
			{EnrollmentID: "e-2", FactorName: "piv", UserID: subject, EnrolledAt: time.Now(), RevokedAt: revokedAt},
		}, nil
	}
	handler := handleAuthFactorList(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_list", map[string]any{"subject": "alice"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
	text := resultText(t, result)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	total, _ := out["total"].(float64)
	if int(total) != 2 {
		t.Errorf("expected total=2, got %v", out["total"])
	}
}

func TestHandleAuthFactorList_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.listFn = func(_ context.Context, _ string) ([]*authfactors.Enrollment, error) {
		return nil, errors.New("db error")
	}
	handler := handleAuthFactorList(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_list", map[string]any{})
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
// handleAuthFactorRevoke
// ---------------------------------------------------------------------------

func TestHandleAuthFactorRevoke_MissingEnrollmentID(t *testing.T) {
	svc := newMockAuthFactorService()
	handler := handleAuthFactorRevoke(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_revoke", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for missing enrollment_id")
	}
}

func TestHandleAuthFactorRevoke_Success(t *testing.T) {
	called := false
	svc := newMockAuthFactorService()
	svc.revokeFn = func(_ context.Context, id string) error {
		called = true
		if id != "e-1" {
			return errors.New("wrong id: " + id)
		}
		return nil
	}
	handler := handleAuthFactorRevoke(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_revoke", map[string]any{"enrollment_id": "e-1"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected IsError: %v", result.Content)
	}
	if !called {
		t.Error("expected RevokeFactor to be called")
	}
	text := resultText(t, result)
	if text != `{"status":"revoked"}` {
		t.Errorf("unexpected response: %q", text)
	}
}

func TestHandleAuthFactorRevoke_ServiceError(t *testing.T) {
	svc := newMockAuthFactorService()
	svc.revokeFn = func(_ context.Context, _ string) error {
		return errors.New("revoke failed")
	}
	handler := handleAuthFactorRevoke(svc)

	req := buildCallToolRequest(t, "qrdb_authfactor_revoke", map[string]any{"enrollment_id": "e-1"})
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
// authFactorTools registration
// ---------------------------------------------------------------------------

func TestWithAuthFactorService_ToolsRegistered(t *testing.T) {
	svc := newMockAuthFactorService()
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithAuthFactorService(svc))

	if s.authFactorSvc == nil {
		t.Fatal("expected authFactorSvc to be set on server")
	}

	tools := s.MCPServer().ListTools()
	expected := []string{
		"qrdb_authfactor_enroll",
		"qrdb_authfactor_challenge",
		"qrdb_authfactor_verify",
		"qrdb_authfactor_list",
		"qrdb_authfactor_revoke",
	}
	for _, name := range expected {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %s not registered", name)
		}
	}
}
