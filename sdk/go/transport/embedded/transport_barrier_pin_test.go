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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/pinhook"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest"
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

	soPIN, userPIN := transport.Secret("so-1234"), transport.Secret("u-5678")
	defer func() {
		// The transport consumes the request PINs: both are zero once the
		// call returns, after the backend stored the original values.
		sotest.RequireZero(t, "BarrierInitialize SOPin", soPIN)
		sotest.RequireZero(t, "BarrierInitialize UserPin", userPIN)
	}()
	if err := tr.BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{
		Secret:  []byte("s"),
		SOPin:   soPIN,
		UserPin: userPIN,
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

// TestEmbedded_RequestPINs_ZeroedOnRefusal: a call refused before it reaches
// the service (no barrier service, no ceremony service) still zeroes every
// PIN its request carries.
func TestEmbedded_RequestPINs_ZeroedOnRefusal(t *testing.T) {
	tr, err := New(newMockKVService(), &mockAdminService{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	soPIN, userPIN := transport.Secret("so-1234"), transport.Secret("u-5678")
	if err := tr.BarrierInitialize(ctx, &transport.BarrierInitializeRequest{Secret: []byte("s"), SOPin: soPIN, UserPin: userPIN}); err == nil {
		t.Fatal("BarrierInitialize without a barrier service succeeded")
	}
	sotest.RequireZero(t, "BarrierInitialize SOPin", soPIN)
	sotest.RequireZero(t, "BarrierInitialize UserPin", userPIN)

	rekeyPIN := transport.Secret("so-1234")
	if _, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: transport.DomainUnseal, Threshold: 2, SOPin: rekeyPIN}); err == nil {
		t.Fatal("BarrierRekeyStart without a barrier service succeeded")
	}
	sotest.RequireZero(t, "BarrierRekeyStart SOPin", rekeyPIN)

	ceremonySO, ceremonyUser := transport.Secret("so-1234"), transport.Secret("u-5678")
	if _, err := tr.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{SOPin: ceremonySO, UserPin: ceremonyUser}); err == nil {
		t.Fatal("CeremonyInitialize without a ceremony service succeeded")
	}
	sotest.RequireZero(t, "CeremonyInitialize SOPin", ceremonySO)
	sotest.RequireZero(t, "CeremonyInitialize UserPin", ceremonyUser)
}

// TestEmbedded_ChangePassphrase_ZeroesTheBoundPINCopy: the copy of a
// WithSOPIN-bound PIN that transport.ResolveSOPIN hands the embedded
// transport is zero once the call returns, after the PIN backend accepted
// the original value.
func TestEmbedded_ChangePassphrase_ZeroesTheBoundPINCopy(t *testing.T) {
	called := false
	svc := &mockBarrierService{
		barrierChangePassphraseFn: func(context.Context, []byte, []byte) error {
			called = true
			return nil
		},
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithBarrierService(svc), WithPINBackend(pintest.NewSOPINBackend(t)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bound, release, err := transport.WithSOPIN(context.Background(), []byte(pintest.ThrottledSOPIN))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	rec := &pinhook.Recorder{}
	if err := tr.BarrierChangePassphrase(pinhook.With(bound, rec), &transport.BarrierChangePassphraseRequest{
		Current: []byte("old-passphrase"),
		Next:    []byte("new-passphrase"),
	}); err != nil {
		t.Fatalf("BarrierChangePassphrase with the SO PIN bound: %v", err)
	}
	if !called {
		t.Fatal("the PIN backend refused the bound PIN: the service was not reached")
	}
	copies := rec.Copies()
	if len(copies) != 1 {
		t.Fatalf("resolved %d copies of the bound PIN, want 1", len(copies))
	}
	sotest.RequireZero(t, "the resolved copy of the bound SO PIN", copies[0])
}
