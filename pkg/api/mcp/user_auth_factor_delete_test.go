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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

func TestHandleUserAuthFactorDeleteTool_Success(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	var gotID string
	var gotForce bool
	svc.deleteFn = func(_ context.Context, factorID string, force bool) error {
		gotID, gotForce = factorID, force
		return nil
	}
	req := buildCallToolRequest(t, "qrdb_user_authfactor_delete", map[string]any{
		"factor_id": "f-001", "force": true,
	})
	result, err := handleUserAuthFactorDeleteTool(svc)(context.Background(), req)
	if err != nil || result.IsError {
		t.Fatalf("unexpected failure: err=%v result=%+v", err, result)
	}
	var out map[string]string
	text := result.Content[0].(mcp.TextContent).Text
	if err := json.Unmarshal([]byte(text), &out); err != nil || out["status"] != "deleted" || out["factor_id"] != "f-001" {
		t.Fatalf("result = %s (%v), want status deleted for f-001", text, err)
	}
	if gotID != "f-001" || !gotForce {
		t.Fatalf("service saw (%q, %v), want (f-001, true)", gotID, gotForce)
	}
}

func TestHandleUserAuthFactorDeleteTool_MissingID(t *testing.T) {
	called := false
	svc := newCovMockUserAuthFactorSvc()
	svc.deleteFn = func(context.Context, string, bool) error { called = true; return nil }
	req := buildCallToolRequest(t, "qrdb_user_authfactor_delete", map[string]any{})
	result, err := handleUserAuthFactorDeleteTool(svc)(context.Background(), req)
	if err != nil || !result.IsError {
		t.Fatalf("expected an error result, got err=%v result=%+v", err, result)
	}
	if called {
		t.Fatal("service called without a factor id")
	}
}

func TestHandleUserAuthFactorDeleteTool_ServiceError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.deleteFn = func(context.Context, string, bool) error {
		return &auth.FactorRevokedError{FactorID: "f"}
	}
	req := buildCallToolRequest(t, "qrdb_user_authfactor_delete", map[string]any{"factor_id": "f"})
	_, err := handleUserAuthFactorDeleteTool(svc)(context.Background(), req)
	var te *ToolError
	if !errors.As(err, &te) || te.Tool != "qrdb_user_authfactor_delete" {
		t.Fatalf("want ToolError for qrdb_user_authfactor_delete, got %T %v", err, err)
	}
	if !errors.Is(err, auth.ErrFactorRevoked) {
		t.Fatalf("want ErrFactorRevoked in chain, got %v", err)
	}
}
