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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// stubPINBackend is a minimal pin.PINBackend used by the embedded barrier
// PIN tests. It records every Set/Verify call so each test can assert
// on the resulting backend state.
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

// TestEmbedded_BarrierInitialize_PersistsPINs verifies that the embedded
// transport's BarrierInitialize forwards SOPin / UserPin to the injected
// pin.PINBackend before delegating to the BarrierServicer.
func TestEmbedded_BarrierInitialize_PersistsPINs(t *testing.T) {
	called := false
	svc := &mockBarrierService{
		barrierInitializeFn: func(_ context.Context, secret []byte) error {
			called = true
			if string(secret) != "s" {
				t.Errorf("secret = %q, want %q", secret, "s")
			}
			return nil
		},
	}
	backend := newStubPINBackend()
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithBarrierService(svc),
		WithPINBackend(backend))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{
		Secret:  []byte("s"),
		SOPin:   "so-1234",
		UserPin: "u-5678",
	}); err != nil {
		t.Fatalf("BarrierInitialize: %v", err)
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

// TestEmbedded_BarrierRekey_BadSOPIN verifies that the embedded transport
// refuses the rekey when the supplied SO PIN does not match the PIN
// backend's record.
func TestEmbedded_BarrierRekey_BadSOPIN(t *testing.T) {
	backend := newStubPINBackend()
	if err := backend.SetSOPIN("", "correct-pin"); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	svc := &mockBarrierService{}
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithBarrierService(svc),
		WithPINBackend(backend))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = tr.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{
		Threshold: 2, Total: 3, SOPin: "wrong-pin",
	})
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

// TestEmbedded_BarrierRekey_GoodSOPIN verifies that the embedded transport
// proceeds with the rekey when the supplied SO PIN matches.
func TestEmbedded_BarrierRekey_GoodSOPIN(t *testing.T) {
	backend := newStubPINBackend()
	if err := backend.SetSOPIN("", "correct-pin"); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	svc := &mockBarrierService{}
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithBarrierService(svc),
		WithPINBackend(backend))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, err := tr.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{
		Threshold: 2, Total: 3, SOPin: "correct-pin",
	})
	if err != nil {
		t.Fatalf("BarrierRekey: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response after successful rekey")
	}
}

// TestEmbedded_BarrierRekey_NoPINBackend verifies the dev-mode flow:
// when no pin.PINBackend is injected, rekey proceeds without any PIN
// check.
func TestEmbedded_BarrierRekey_NoPINBackend(t *testing.T) {
	svc := &mockBarrierService{}
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithBarrierService(svc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, err := tr.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{
		Threshold: 2, Total: 3,
	})
	if err != nil {
		t.Fatalf("BarrierRekey: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response in no-PIN-backend mode")
	}
}
