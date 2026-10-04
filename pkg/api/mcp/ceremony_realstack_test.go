// Copyright (c) 2026 Jeremy Hahn
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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
)

// TestMCP_CeremonyInitialize_RealSOPINGateRefusesWrongPIN is the
// go-qrdb#560 review B MCP equivalent of pkg/api/rest's
// TestHandleCeremonyInitialize_RealSOPINGateRefusesWrongPIN: unlike this
// file's own sibling TestMCP_CeremonyInitialize_SOPINRefusalPropagates (a
// mock CeremonyServicer that returns pin.ErrPINInvalid directly, proving
// only that MCP renders that error as a *ToolError, not that anything
// actually checks a PIN), this drives a REAL ceremony.CeremonyService, a
// REAL pin.SoftwareBackend and the REAL
// service.CeremonyServiceWithAdminRebind wrapper through the MCP tool
// handler. Removing pin.RequireProvisionedSOPIN from
// ReissueAdminCredential (or reverting VerifyReissueSOPIN's ordering) turns
// this red; the mock-based sibling test cannot.
func TestMCP_CeremonyInitialize_RealSOPINGateRefusesWrongPIN(t *testing.T) {
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	handler := handleCeremonyInitialize(wrapped, ca)

	// Wrong SO PIN: refused, never bound.
	req := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{
		"so_pin": "wrong-pin", "admin_generate": true,
	})
	_, err := handler(context.Background(), req)
	if !errors.Is(err, pin.ErrPINInvalid) {
		t.Fatalf("wrong SO PIN: got %v, want pin.ErrPINInvalid", err)
	}

	// Missing SO PIN entirely: also refused.
	req = buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{
		"admin_generate": true,
	})
	_, err = handler(context.Background(), req)
	if !errors.Is(err, pin.ErrPINInvalid) {
		t.Fatalf("missing SO PIN: got %v, want pin.ErrPINInvalid", err)
	}

	// The correct SO PIN still reissues and binds cleanly -- proving the
	// refusals above were about the PIN, not a stack that always refuses.
	req = buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{
		"so_pin": "correct-horse-battery", "admin_generate": true,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("correct SO PIN: unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("correct SO PIN: expected success, got error result: %v", result)
	}
}

// TestMCP_CeremonyInitialize_RealSOPINGateCheckedBeforeAdminAlreadyBound is
// the OTHER half of the go-qrdb#560 review B ordering check, over MCP:
// against an admin that IS already bound, a wrong SO PIN must still answer
// the PIN failure (pin.ErrPINInvalid), never
// service.AdminAlreadyBoundError's own distinct signal -- which would leak
// the admin's binding state to a caller who never authenticated at all.
func TestMCP_CeremonyInitialize_RealSOPINGateCheckedBeforeAdminAlreadyBound(t *testing.T) {
	wrapped, ca, _ := servicetest.NewRealReissueStack(t, "correct-horse-battery")
	handler := handleCeremonyInitialize(wrapped, ca)

	bindReq := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{
		"so_pin": "correct-horse-battery", "admin_generate": true,
	})
	result, err := handler(context.Background(), bindReq)
	if err != nil || result.IsError {
		t.Fatalf("bind: expected success, got result=%v err=%v", result, err)
	}

	wrongReq := buildCallToolRequest(t, "qrdb_ceremony_initialize", map[string]any{
		"so_pin": "wrong-pin", "admin_generate": true,
	})
	_, err = handler(context.Background(), wrongReq)
	var alreadyBound *service.AdminAlreadyBoundError
	if errors.As(err, &alreadyBound) {
		t.Fatal("wrong SO PIN against an already-bound admin leaked AdminAlreadyBoundError instead of the PIN failure")
	}
	if !errors.Is(err, pin.ErrPINInvalid) {
		t.Fatalf("got %v, want pin.ErrPINInvalid", err)
	}
}
