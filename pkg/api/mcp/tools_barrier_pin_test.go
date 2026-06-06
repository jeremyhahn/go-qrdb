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

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
)

// stubPINBackend is a minimal pin.PINBackend used by the MCP barrier PIN
// tests. It records every Set/Verify call and lets each test drive the
// desired path.
type stubPINBackend struct {
	strategy        pin.StrategyID
	soPINSet        bool
	userPINSet      bool
	storedSOPIN     string
	storedUserPIN   string
	verifyErr       error
	setSOPINErr     error
	setSOPINCalls   int
	setUserPINCalls int
	verifyCalls     int
}

func newStubPINBackend() *stubPINBackend {
	return &stubPINBackend{strategy: pin.StrategySoftware}
}

func (b *stubPINBackend) Strategy() pin.StrategyID { return b.strategy }

func (b *stubPINBackend) SetSOPIN(_, newSOPIN string) error {
	b.setSOPINCalls++
	if b.setSOPINErr != nil {
		return b.setSOPINErr
	}
	b.storedSOPIN = newSOPIN
	b.soPINSet = true
	return nil
}

func (b *stubPINBackend) SetUserPIN(_, newUserPIN string) error {
	b.setUserPINCalls++
	b.storedUserPIN = newUserPIN
	b.userPINSet = true
	return nil
}

func (b *stubPINBackend) ChangeSOPIN(_, _ string) error   { return nil }
func (b *stubPINBackend) ChangeUserPIN(_, _ string) error { return nil }

func (b *stubPINBackend) VerifySOPIN(p string) error {
	b.verifyCalls++
	if b.verifyErr != nil {
		return b.verifyErr
	}
	if p != b.storedSOPIN {
		return pin.ErrPINInvalid
	}
	return nil
}

func (b *stubPINBackend) VerifyUserPIN(_ string) error         { return nil }
func (b *stubPINBackend) IsInitialized() bool                  { return b.soPINSet }
func (b *stubPINBackend) SOPINSet() bool                       { return b.soPINSet }
func (b *stubPINBackend) UserPINSet() bool                     { return b.userPINSet }
func (b *stubPINBackend) GetLockoutStatus() *pin.LockoutStatus { return nil }
func (b *stubPINBackend) ResetLockout(_ string) error          { return nil }

var _ pin.PINBackend = (*stubPINBackend)(nil)

// TestMCP_HandleBarrierInitialize_PersistsPINs verifies that the MCP
// qrdb_barrier_initialize tool forwards so_pin / user_pin to the
// configured pin.PINBackend before delegating to the barrier service.
func TestMCP_HandleBarrierInitialize_PersistsPINs(t *testing.T) {
	called := false
	svc := newMockBarrierService()
	svc.initializeFn = func(_ context.Context, secret string) error {
		called = true
		if secret != "s" {
			t.Errorf("secret = %q, want %q", secret, "s")
		}
		return nil
	}
	backend := newStubPINBackend()
	handler := handleBarrierInitialize(svc, backend)

	req := buildCallToolRequest(t, "qrdb_barrier_initialize", map[string]any{
		"secret":   "s",
		"so_pin":   "so-1234",
		"user_pin": "u-5678",
	})

	if _, err := handler(context.Background(), req); err != nil {
		t.Fatalf("handleBarrierInitialize: %v", err)
	}
	if !called {
		t.Fatal("barrier initialize was not called")
	}
	if !backend.SOPINSet() || backend.storedSOPIN != "so-1234" {
		t.Fatalf("SO PIN not persisted: set=%v stored=%q", backend.SOPINSet(), backend.storedSOPIN)
	}
	if !backend.UserPINSet() || backend.storedUserPIN != "u-5678" {
		t.Fatalf("User PIN not persisted: set=%v stored=%q", backend.UserPINSet(), backend.storedUserPIN)
	}
}

// TestMCP_HandleBarrierRekey_BadSOPIN verifies that the MCP rekey tool
// rejects the call when the supplied SO PIN does not match the PIN
// backend's record.
func TestMCP_HandleBarrierRekey_BadSOPIN(t *testing.T) {
	backend := newStubPINBackend()
	if err := backend.SetSOPIN("", "correct-pin"); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	svc := newMockBarrierService()
	handler := handleBarrierRekey(svc, backend)

	req := buildCallToolRequest(t, "qrdb_barrier_rekey", map[string]any{
		"threshold": 2,
		"total":     3,
		"so_pin":    "wrong-pin",
	})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for bad SO PIN, got nil")
	}
	if !errors.Is(err, pin.ErrPINInvalid) {
		t.Fatalf("expected ErrPINInvalid, got %v", err)
	}
	if backend.verifyCalls == 0 {
		t.Fatal("VerifySOPIN was not invoked")
	}
}

// TestMCP_HandleBarrierRekey_GoodSOPIN verifies that the MCP rekey tool
// proceeds when the supplied SO PIN matches.
func TestMCP_HandleBarrierRekey_GoodSOPIN(t *testing.T) {
	backend := newStubPINBackend()
	if err := backend.SetSOPIN("", "correct-pin"); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	svc := newMockBarrierService()
	handler := handleBarrierRekey(svc, backend)

	req := buildCallToolRequest(t, "qrdb_barrier_rekey", map[string]any{
		"threshold": 2,
		"total":     3,
		"so_pin":    "correct-pin",
	})

	if _, err := handler(context.Background(), req); err != nil {
		t.Fatalf("handleBarrierRekey: %v", err)
	}
}
