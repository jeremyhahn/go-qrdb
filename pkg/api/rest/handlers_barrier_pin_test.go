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

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// stubPINBackend is a minimal pin.PINBackend implementation used by the
// REST barrier PIN tests. It records every Set/Verify call and returns
// configurable canned errors so each test can drive the desired path.
type stubPINBackend struct {
	strategy        pin.StrategyID
	soPINSet        bool
	userPINSet      bool
	storedSOPIN     string
	storedUserPIN   string
	verifyErr       error
	setSOPINErr     error
	setUserPINErr   error
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
	if b.setUserPINErr != nil {
		return b.setUserPINErr
	}
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

// Compile-time interface check.
var _ pin.PINBackend = (*stubPINBackend)(nil)

// newBarrierPINTestServer builds a REST Server with both a mock
// barrier service and an optional pin.PINBackend wired in.
func newBarrierPINTestServer(t *testing.T, barrierSvc service.BarrierServicer, backend pin.PINBackend) *Server {
	t.Helper()
	opts := []ServerOption{}
	if barrierSvc != nil {
		opts = append(opts, WithBarrierService(barrierSvc))
	}
	if backend != nil {
		opts = append(opts, WithPINBackend(backend))
	}
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", opts...)
}

// TestBarrierHandlers_Initialize_PersistsPINs verifies that the
// /v1/barrier/initialize handler forwards so_pin and user_pin to the
// configured pin.PINBackend before delegating to the barrier service.
func TestBarrierHandlers_Initialize_PersistsPINs(t *testing.T) {
	backend := newStubPINBackend()
	srv := newBarrierPINTestServer(t, &mockBarrierService{}, backend)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/initialize",
		strings.NewReader(`{"secret":"s","so_pin":"so-1234","user_pin":"u-5678"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if !backend.SOPINSet() || backend.storedSOPIN != "so-1234" {
		t.Fatalf("SO PIN not persisted: set=%v, stored=%q", backend.SOPINSet(), backend.storedSOPIN)
	}
	if !backend.UserPINSet() || backend.storedUserPIN != "u-5678" {
		t.Fatalf("User PIN not persisted: set=%v, stored=%q", backend.UserPINSet(), backend.storedUserPIN)
	}
}

// TestBarrierHandlers_Initialize_NoPINs verifies that the initialize
// handler is a no-op on the PIN backend when no PIN fields are supplied.
func TestBarrierHandlers_Initialize_NoPINs(t *testing.T) {
	backend := newStubPINBackend()
	srv := newBarrierPINTestServer(t, &mockBarrierService{}, backend)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/initialize",
		strings.NewReader(`{"secret":"s"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if backend.setSOPINCalls != 0 || backend.setUserPINCalls != 0 {
		t.Fatalf("PIN backend touched without PIN fields: setSOPIN=%d, setUserPIN=%d",
			backend.setSOPINCalls, backend.setUserPINCalls)
	}
}

// TestBarrierHandlers_Rekey_VerifiesSOPIN_BadPIN verifies that the rekey
// handler returns 401 when the supplied SO PIN does not match.
func TestBarrierHandlers_Rekey_VerifiesSOPIN_BadPIN(t *testing.T) {
	backend := newStubPINBackend()
	if err := backend.SetSOPIN("", "correct-pin"); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	rekeyCalled := false
	svc := &mockBarrierService{
		rekeyFn: func(_ context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
			rekeyCalled = true
			return &service.ShamirInitInfo{Threshold: threshold, TotalShares: total}, nil
		},
	}
	srv := newBarrierPINTestServer(t, svc, backend)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/rekey",
		strings.NewReader(`{"threshold":2,"total":3,"so_pin":"wrong-pin"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad SO PIN, got %d", rec.Code)
	}
	if rekeyCalled {
		t.Fatal("BarrierRekey was called despite SO PIN verification failure")
	}
	if backend.verifyCalls == 0 {
		t.Fatal("VerifySOPIN was not invoked")
	}
}

// TestBarrierHandlers_Rekey_VerifiesSOPIN_GoodPIN verifies that the rekey
// handler proceeds to the barrier service when the SO PIN matches.
func TestBarrierHandlers_Rekey_VerifiesSOPIN_GoodPIN(t *testing.T) {
	backend := newStubPINBackend()
	if err := backend.SetSOPIN("", "correct-pin"); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	rekeyCalled := false
	svc := &mockBarrierService{
		rekeyFn: func(_ context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
			rekeyCalled = true
			return &service.ShamirInitInfo{Threshold: threshold, TotalShares: total}, nil
		},
	}
	srv := newBarrierPINTestServer(t, svc, backend)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/rekey",
		strings.NewReader(`{"threshold":2,"total":3,"so_pin":"correct-pin"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !rekeyCalled {
		t.Fatal("BarrierRekey was not called after successful SO PIN verification")
	}
}

// TestBarrierHandlers_Rekey_NoPINBackend verifies the dev-mode flow where
// no PINBackend is configured: rekey proceeds without any PIN check.
func TestBarrierHandlers_Rekey_NoPINBackend(t *testing.T) {
	rekeyCalled := false
	svc := &mockBarrierService{
		rekeyFn: func(_ context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
			rekeyCalled = true
			return &service.ShamirInitInfo{Threshold: threshold, TotalShares: total}, nil
		},
	}
	srv := newBarrierPINTestServer(t, svc, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/rekey",
		strings.NewReader(`{"threshold":2,"total":3}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if !rekeyCalled {
		t.Fatal("BarrierRekey was not called in no-PIN-backend mode")
	}
}
