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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestMCP_CeremonyInitialize_ForwardsAllFields is the MCP transport's
// regression guard for go-qrdb#560 review B1, mirroring
// pkg/api/rest's TestHandleCeremonyInitialize_ForwardsAllFields and
// pkg/api/grpc's TestGRPC_CeremonyInitialize_ForwardsAllFields: it proves
// so_pin reaches service.CeremonyInitializeOptions.SOPin unchanged. The
// review finding was that nothing checked the SO PIN on an admin-credential
// reissue (pkg/ceremony/admin_credential.go's ReissueAdminCredential) and
// that "the awaiting-mode passthroughs on REST, gRPC/Unix and MCP let any
// caller reach it" -- the fix lives once in the shared service layer
// (pkg/ceremony, pkg/service), so each transport's own obligation is only
// to forward the field rather than drop it, exactly as this test checks.
func TestMCP_CeremonyInitialize_ForwardsAllFields(t *testing.T) {
	svc := newMockCeremonyService()
	ca := &mockCeremonyCA{}
	handler := handleCeremonyInitialize(svc, ca)

	req := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{
		"so_pin":                   "test-so-pin",
		"user_pin":                 "test-user-pin",
		"credential_seal_strategy": "shamir",
		"threshold":                float64(2),
		"admin_generate":           true,
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleCeremonyInitialize: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %v", result)
	}

	got := svc.capturedInitOpts
	if got == nil {
		t.Fatal("expected CeremonyInitialize to be called with non-nil opts")
	}
	if got.SOPin != "test-so-pin" {
		t.Errorf("SOPin = %q, want %q", got.SOPin, "test-so-pin")
	}
	if got.UserPin != "test-user-pin" {
		t.Errorf("UserPin = %q, want %q", got.UserPin, "test-user-pin")
	}
	if got.CredentialSealStrategy != "shamir" {
		t.Errorf("CredentialSealStrategy = %q, want %q", got.CredentialSealStrategy, "shamir")
	}
	if got.Threshold != 2 {
		t.Errorf("Threshold = %d, want 2", got.Threshold)
	}
	if !got.AdminGenerate {
		t.Error("AdminGenerate = false, want true")
	}
}

// TestMCP_CeremonyInitialize_SOPINRefusalPropagates proves the MCP tool
// handler surfaces a SO-PIN refusal from the service layer as a *ToolError
// (never as a success result), so a caller retrying the admin-credential
// step over MCP without the correct SO PIN cannot reach
// ReissueAdminCredential's side effects -- go-qrdb#560 review B1's own
// wording names MCP as one of the three passthroughs a caller could
// otherwise use to bypass the gate.
func TestMCP_CeremonyInitialize_SOPINRefusalPropagates(t *testing.T) {
	svc := newMockCeremonyService()
	svc.initializeFn = func(_ context.Context, _ ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
		return nil, pin.ErrPINInvalid
	}
	ca := &mockCeremonyCA{}
	handler := handleCeremonyInitialize(svc, ca)

	req := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{
		"admin_generate": true,
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected the SO-PIN refusal to propagate as an error, got success")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected *ToolError, got %T", err)
	}
	if !errors.Is(toolErr.Err, pin.ErrPINInvalid) {
		t.Errorf("expected the refusal to be pin.ErrPINInvalid, got %v", toolErr.Err)
	}
	if toolErr.Tool != "qrdb_ceremony_initialize" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_ceremony_initialize")
	}
}
