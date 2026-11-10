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

//go:build codec_json && raft

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

var errUserAuthFactorSvc = errors.New("user auth factor service failure")

// userAuthFactorPayload decodes a successful tool result into a JSON map.
func userAuthFactorPayload(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(t, result)), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// requireArgRefused asserts the handler answered with an error result and
// never reached the service.
func requireArgRefused(t *testing.T, result *mcp.CallToolResult, err error, called bool) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected error result, got %s", resultText(t, result))
	}
	if called {
		t.Fatal("service was called for a refused argument")
	}
}

func TestHandleUserAuthFactorAddTool_Success(t *testing.T) {
	var gotUser uint64
	var gotFactor *auth.AuthFactor
	svc := newCovMockUserAuthFactorSvc()
	svc.addFn = func(_ context.Context, userID uint64, f *auth.AuthFactor) error {
		gotUser, gotFactor = userID, f
		f.ID = "f-001"
		return nil
	}
	req := buildCallToolRequest(t, "qrdb_user_authfactor_add", map[string]any{
		"user_id":        "42",
		"type":           "mtls",
		"credential_b64": base64.StdEncoding.EncodeToString([]byte("creddata")),
		"label":          "my key",
		"metadata_json":  `{"k":"v"}`,
	})
	result, err := handleUserAuthFactorAddTool(svc)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := userAuthFactorPayload(t, result)

	if gotUser != 42 || gotFactor == nil || gotFactor.UserID != 42 {
		t.Fatalf("service got user %d factor %+v, want user 42", gotUser, gotFactor)
	}
	if gotFactor.Type != auth.AuthFactorMTLS || gotFactor.Label != "my key" ||
		string(gotFactor.Credential) != "creddata" || gotFactor.Metadata["k"] != "v" {
		t.Errorf("service got factor %+v", gotFactor)
	}
	factor, _ := out["factor"].(map[string]any)
	if factor["id"] != "f-001" || factor["label"] != "my key" {
		t.Errorf("payload factor = %v", factor)
	}
}

func TestHandleUserAuthFactorAddTool_InvalidArgs(t *testing.T) {
	cred := base64.StdEncoding.EncodeToString([]byte("cred"))
	cases := map[string]map[string]any{
		"bad user_id":       {"user_id": "not-a-number", "type": "mtls", "credential_b64": cred},
		"missing type/cred": {"user_id": "42"},
		"bad base64":        {"user_id": "42", "type": "mtls", "credential_b64": "!!!bad!!!"},
		"bad metadata JSON": {"user_id": "42", "type": "mtls", "credential_b64": cred, "metadata_json": "{bad"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			svc := newCovMockUserAuthFactorSvc()
			svc.addFn = func(context.Context, uint64, *auth.AuthFactor) error { called = true; return nil }
			req := buildCallToolRequest(t, "qrdb_user_authfactor_add", args)
			result, err := handleUserAuthFactorAddTool(svc)(context.Background(), req)
			requireArgRefused(t, result, err, called)
		})
	}
}

func TestHandleUserAuthFactorAddTool_ServiceError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.addFn = func(context.Context, uint64, *auth.AuthFactor) error { return errUserAuthFactorSvc }
	req := buildCallToolRequest(t, "qrdb_user_authfactor_add", map[string]any{
		"user_id":        "42",
		"type":           "mtls",
		"credential_b64": base64.StdEncoding.EncodeToString([]byte("cred")),
	})
	_, err := handleUserAuthFactorAddTool(svc)(context.Background(), req)
	requireToolError(t, err, "qrdb_user_authfactor_add", errUserAuthFactorSvc)
}

func TestHandleUserAuthFactorListTool_Success(t *testing.T) {
	var gotUser uint64
	svc := newCovMockUserAuthFactorSvc()
	svc.listFn = func(_ context.Context, userID uint64) ([]*auth.AuthFactor, error) {
		gotUser = userID
		return []*auth.AuthFactor{
			{ID: "f-1", UserID: userID, Type: auth.AuthFactorMTLS, RegisteredAt: time.Now()},
			{ID: "f-2", UserID: userID, Type: auth.AuthFactorMTLS, RegisteredAt: time.Now()},
		}, nil
	}
	req := buildCallToolRequest(t, "qrdb_user_authfactor_list", map[string]any{"user_id": "42"})
	result, err := handleUserAuthFactorListTool(svc)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := userAuthFactorPayload(t, result)

	if gotUser != 42 {
		t.Errorf("service got user %d, want 42", gotUser)
	}
	factors, _ := out["factors"].([]any)
	if out["total"] != float64(2) || len(factors) != 2 {
		t.Fatalf("payload = %v, want 2 factors", out)
	}
	if first, _ := factors[0].(map[string]any); first["id"] != "f-1" {
		t.Errorf("first factor = %v, want id f-1", first)
	}
}

func TestHandleUserAuthFactorListTool_BadUserID(t *testing.T) {
	called := false
	svc := newCovMockUserAuthFactorSvc()
	svc.listFn = func(context.Context, uint64) ([]*auth.AuthFactor, error) { called = true; return nil, nil }
	req := buildCallToolRequest(t, "qrdb_user_authfactor_list", map[string]any{"user_id": "abc"})
	result, err := handleUserAuthFactorListTool(svc)(context.Background(), req)
	requireArgRefused(t, result, err, called)
}

func TestHandleUserAuthFactorListTool_ServiceError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.listFn = func(context.Context, uint64) ([]*auth.AuthFactor, error) { return nil, errUserAuthFactorSvc }
	req := buildCallToolRequest(t, "qrdb_user_authfactor_list", map[string]any{"user_id": "42"})
	_, err := handleUserAuthFactorListTool(svc)(context.Background(), req)
	requireToolError(t, err, "qrdb_user_authfactor_list", errUserAuthFactorSvc)
}

func TestHandleUserAuthFactorGetTool_Success(t *testing.T) {
	req := buildCallToolRequest(t, "qrdb_user_authfactor_get", map[string]any{"factor_id": "f-001"})
	result, err := handleUserAuthFactorGetTool(newCovMockUserAuthFactorSvc())(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	factor, _ := userAuthFactorPayload(t, result)["factor"].(map[string]any)
	if factor["id"] != "f-001" || factor["type"] != string(auth.AuthFactorMTLS) {
		t.Errorf("payload factor = %v, want id f-001 type mtls", factor)
	}
}

func TestHandleUserAuthFactorGetTool_MissingID(t *testing.T) {
	called := false
	svc := newCovMockUserAuthFactorSvc()
	svc.getFn = func(context.Context, string) (*auth.AuthFactor, error) { called = true; return nil, nil }
	req := buildCallToolRequest(t, "qrdb_user_authfactor_get", map[string]any{})
	result, err := handleUserAuthFactorGetTool(svc)(context.Background(), req)
	requireArgRefused(t, result, err, called)
}

func TestHandleUserAuthFactorGetTool_ServiceError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.getFn = func(context.Context, string) (*auth.AuthFactor, error) { return nil, errUserAuthFactorSvc }
	req := buildCallToolRequest(t, "qrdb_user_authfactor_get", map[string]any{"factor_id": "bad"})
	_, err := handleUserAuthFactorGetTool(svc)(context.Background(), req)
	requireToolError(t, err, "qrdb_user_authfactor_get", errUserAuthFactorSvc)
}

func TestHandleUserAuthFactorRemoveTool_Success(t *testing.T) {
	var gotID string
	var gotForce bool
	svc := newCovMockUserAuthFactorSvc()
	svc.removeFn = func(_ context.Context, id string, force bool) error {
		gotID, gotForce = id, force
		return nil
	}
	req := buildCallToolRequest(t, "qrdb_user_authfactor_remove", map[string]any{"factor_id": "f-001", "force": true})
	result, err := handleUserAuthFactorRemoveTool(svc)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := userAuthFactorPayload(t, result)

	if gotID != "f-001" || !gotForce {
		t.Errorf("service got (%q, force=%v), want (f-001, force=true)", gotID, gotForce)
	}
	if out["status"] != "revoked" || out["factor_id"] != "f-001" {
		t.Errorf("payload = %v, want status revoked for f-001", out)
	}
}

func TestHandleUserAuthFactorRemoveTool_MissingID(t *testing.T) {
	called := false
	svc := newCovMockUserAuthFactorSvc()
	svc.removeFn = func(context.Context, string, bool) error { called = true; return nil }
	req := buildCallToolRequest(t, "qrdb_user_authfactor_remove", map[string]any{})
	result, err := handleUserAuthFactorRemoveTool(svc)(context.Background(), req)
	requireArgRefused(t, result, err, called)
}

func TestHandleUserAuthFactorRemoveTool_ServiceError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.removeFn = func(context.Context, string, bool) error { return errUserAuthFactorSvc }
	req := buildCallToolRequest(t, "qrdb_user_authfactor_remove", map[string]any{"factor_id": "f"})
	_, err := handleUserAuthFactorRemoveTool(svc)(context.Background(), req)
	requireToolError(t, err, "qrdb_user_authfactor_remove", errUserAuthFactorSvc)
}

func TestHandleUserAuthFactorRotateTool_Success(t *testing.T) {
	var gotOld string
	var gotNew *auth.AuthFactor
	svc := newCovMockUserAuthFactorSvc()
	svc.rotateFn = func(_ context.Context, oldID string, f *auth.AuthFactor) error {
		gotOld, gotNew = oldID, f
		f.ID = "f-002"
		return nil
	}
	req := buildCallToolRequest(t, "qrdb_user_authfactor_rotate", map[string]any{
		"old_factor_id":  "f-old",
		"type":           "mtls",
		"credential_b64": base64.StdEncoding.EncodeToString([]byte("newcred")),
	})
	result, err := handleUserAuthFactorRotateTool(svc)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	factor, _ := userAuthFactorPayload(t, result)["factor"].(map[string]any)

	if gotOld != "f-old" || gotNew == nil || string(gotNew.Credential) != "newcred" || gotNew.Type != auth.AuthFactorMTLS {
		t.Fatalf("service got (%q, %+v), want f-old with the decoded credential", gotOld, gotNew)
	}
	if factor["id"] != "f-002" {
		t.Errorf("payload factor = %v, want id f-002", factor)
	}
}

func TestHandleUserAuthFactorRotateTool_InvalidArgs(t *testing.T) {
	cases := map[string]map[string]any{
		"missing fields": {"old_factor_id": "f"},
		"bad base64":     {"old_factor_id": "f", "type": "mtls", "credential_b64": "!!!bad!!!"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			svc := newCovMockUserAuthFactorSvc()
			svc.rotateFn = func(context.Context, string, *auth.AuthFactor) error { called = true; return nil }
			req := buildCallToolRequest(t, "qrdb_user_authfactor_rotate", args)
			result, err := handleUserAuthFactorRotateTool(svc)(context.Background(), req)
			requireArgRefused(t, result, err, called)
		})
	}
}

func TestHandleUserAuthFactorRotateTool_ServiceError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.rotateFn = func(context.Context, string, *auth.AuthFactor) error { return errUserAuthFactorSvc }
	req := buildCallToolRequest(t, "qrdb_user_authfactor_rotate", map[string]any{
		"old_factor_id":  "f",
		"type":           "mtls",
		"credential_b64": base64.StdEncoding.EncodeToString([]byte("cred")),
	})
	_, err := handleUserAuthFactorRotateTool(svc)(context.Background(), req)
	requireToolError(t, err, "qrdb_user_authfactor_rotate", errUserAuthFactorSvc)
}

func TestHandleUserAuthFactorRelabelTool_Success(t *testing.T) {
	var gotID, gotLabel string
	svc := newCovMockUserAuthFactorSvc()
	svc.relabelFn = func(_ context.Context, id, label string) error {
		gotID, gotLabel = id, label
		return nil
	}
	svc.getFn = func(_ context.Context, id string) (*auth.AuthFactor, error) {
		return &auth.AuthFactor{ID: id, Type: auth.AuthFactorMTLS, Label: gotLabel, RegisteredAt: time.Now()}, nil
	}
	req := buildCallToolRequest(t, "qrdb_user_authfactor_relabel", map[string]any{"factor_id": "f-001", "label": "new label"})
	result, err := handleUserAuthFactorRelabelTool(svc)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	factor, _ := userAuthFactorPayload(t, result)["factor"].(map[string]any)

	if gotID != "f-001" || gotLabel != "new label" {
		t.Errorf("service got (%q, %q), want (f-001, new label)", gotID, gotLabel)
	}
	if factor["id"] != "f-001" || factor["label"] != "new label" {
		t.Errorf("payload factor = %v, want the re-read factor", factor)
	}
}

func TestHandleUserAuthFactorRelabelTool_MissingFields(t *testing.T) {
	called := false
	svc := newCovMockUserAuthFactorSvc()
	svc.relabelFn = func(context.Context, string, string) error { called = true; return nil }
	req := buildCallToolRequest(t, "qrdb_user_authfactor_relabel", map[string]any{"factor_id": "f"})
	result, err := handleUserAuthFactorRelabelTool(svc)(context.Background(), req)
	requireArgRefused(t, result, err, called)
}

func TestHandleUserAuthFactorRelabelTool_RelabelError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.relabelFn = func(context.Context, string, string) error { return errUserAuthFactorSvc }
	req := buildCallToolRequest(t, "qrdb_user_authfactor_relabel", map[string]any{"factor_id": "f", "label": "lbl"})
	_, err := handleUserAuthFactorRelabelTool(svc)(context.Background(), req)
	requireToolError(t, err, "qrdb_user_authfactor_relabel", errUserAuthFactorSvc)
}

func TestHandleUserAuthFactorRelabelTool_GetError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.getFn = func(context.Context, string) (*auth.AuthFactor, error) { return nil, errUserAuthFactorSvc }
	req := buildCallToolRequest(t, "qrdb_user_authfactor_relabel", map[string]any{"factor_id": "f", "label": "lbl"})
	_, err := handleUserAuthFactorRelabelTool(svc)(context.Background(), req)
	requireToolError(t, err, "qrdb_user_authfactor_relabel", errUserAuthFactorSvc)
}

func TestHandleUserAuthFactorLoginTool_Success(t *testing.T) {
	var gotType auth.AuthFactorType
	var gotKey []byte
	svc := newCovMockUserAuthFactorSvc()
	svc.lookupFn = func(_ context.Context, typ auth.AuthFactorType, key []byte) (*auth.AuthFactor, error) {
		gotType, gotKey = typ, key
		return &auth.AuthFactor{ID: "f-001", UserID: 42, Type: typ, RegisteredAt: time.Now()}, nil
	}
	req := buildCallToolRequest(t, "qrdb_user_authfactor_login", map[string]any{
		"type":           "mtls",
		"lookup_key_b64": base64.StdEncoding.EncodeToString([]byte("lookupkey")),
	})
	result, err := handleUserAuthFactorLoginTool(svc, nil)(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := userAuthFactorPayload(t, result)

	if gotType != auth.AuthFactorMTLS || string(gotKey) != "lookupkey" {
		t.Errorf("service got (%q, %q), want (mtls, lookupkey)", gotType, gotKey)
	}
	if out["user_id"] != float64(42) || out["active"] != true {
		t.Errorf("payload = %v, want user_id 42 active", out)
	}
}

func TestHandleUserAuthFactorLoginTool_InvalidArgs(t *testing.T) {
	cases := map[string]map[string]any{
		"missing fields": {"type": "mtls"},
		"bad base64":     {"type": "mtls", "lookup_key_b64": "!!!bad!!!"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			svc := newCovMockUserAuthFactorSvc()
			svc.lookupFn = func(context.Context, auth.AuthFactorType, []byte) (*auth.AuthFactor, error) {
				called = true
				return nil, nil
			}
			req := buildCallToolRequest(t, "qrdb_user_authfactor_login", args)
			result, err := handleUserAuthFactorLoginTool(svc, nil)(context.Background(), req)
			requireArgRefused(t, result, err, called)
		})
	}
}

func TestHandleUserAuthFactorLoginTool_ServiceError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.lookupFn = func(context.Context, auth.AuthFactorType, []byte) (*auth.AuthFactor, error) {
		return nil, errUserAuthFactorSvc
	}
	req := buildCallToolRequest(t, "qrdb_user_authfactor_login", map[string]any{
		"type":           "mtls",
		"lookup_key_b64": base64.StdEncoding.EncodeToString([]byte("key")),
	})
	_, err := handleUserAuthFactorLoginTool(svc, nil)(context.Background(), req)
	requireToolError(t, err, "qrdb_user_authfactor_login", errUserAuthFactorSvc)
}

func TestFactorToMCPMap_Nil(t *testing.T) {
	if m := factorToMCPMap(nil); m != nil {
		t.Errorf("factorToMCPMap(nil) = %v, want nil", m)
	}
}

func TestFactorToMCPMap_OptionalFields(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	full := factorToMCPMap(&auth.AuthFactor{
		ID:             "f-1",
		UserID:         42,
		Type:           auth.AuthFactorMTLS,
		Credential:     []byte("cred"),
		SPKICredential: []byte("spki"),
		RegisteredAt:   now,
		LastUsedAt:     &now,
		RevokedAt:      &now,
	})
	want := map[string]any{
		"credential":      base64.StdEncoding.EncodeToString([]byte("cred")),
		"spki_credential": base64.StdEncoding.EncodeToString([]byte("spki")),
		"registered_at":   now.Format(time.RFC3339),
		"last_used_at":    now.Format(time.RFC3339),
		"revoked_at":      now.Format(time.RFC3339),
	}
	for k, v := range want {
		if full[k] != v {
			t.Errorf("%s = %v, want %v", k, full[k], v)
		}
	}

	bare := factorToMCPMap(&auth.AuthFactor{ID: "f-2", RegisteredAt: now})
	for _, k := range []string{"spki_credential", "last_used_at", "revoked_at"} {
		if _, ok := bare[k]; ok {
			t.Errorf("unset %s present in %v", k, bare)
		}
	}
}
