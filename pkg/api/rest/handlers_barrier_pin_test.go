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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
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
		strings.NewReader(`{"secret":"cw==","so_pin":"so-1234","user_pin":"u-5678"}`))
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
		strings.NewReader(`{"secret":"cw=="}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if backend.setSOPINCalls != 0 || backend.setUserPINCalls != 0 {
		t.Fatalf("PIN backend touched without PIN fields: setSOPIN=%d, setUserPIN=%d",
			backend.setSOPINCalls, backend.setUserPINCalls)
	}
}

// soPINGatedRoutes are the holder-custody operations gated behind the SO
// PIN, each with the mock hook that records whether the service was reached.
func soPINGatedRoutes(called *bool) []struct {
	name, path, body string
	svc              *mockBarrierService
} {
	round := &service.QuorumRoundInfo{Domain: seal.DomainUnseal, Op: seal.OpRekey, Required: 3, NewThreshold: 2}
	return []struct {
		name, path, body string
		svc              *mockBarrierService
	}{
		{"rekey start", "/v1/barrier/rekey/start", `{"domain":"unseal","threshold":2,"so_pin":%q}`, &mockBarrierService{
			rekeyStartFn: func(_ context.Context, _ seal.QuorumDomain, _ int, _ seal.FactorPolicy) (*service.QuorumRoundInfo, error) {
				*called = true
				return round, nil
			},
		}},
		{"officer revoke", "/v1/barrier/officers/revoke", `{"domain":"unseal","username":"alice","threshold":2,"so_pin":%q}`, &mockBarrierService{
			officerRevokeFn: func(_ context.Context, _ seal.QuorumDomain, _ string, _ int) (*service.QuorumRoundInfo, error) {
				*called = true
				return round, nil
			},
		}},
	}
}

// TestBarrierHandlers_SOPINGated_BadPIN verifies that rekey start and holder
// revocation answer 401 on a wrong SO PIN without reaching the service.
func TestBarrierHandlers_SOPINGated_BadPIN(t *testing.T) {
	var called bool
	for _, tc := range soPINGatedRoutes(&called) {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			backend := newStubPINBackend()
			if err := backend.SetSOPIN("", "correct-pin"); err != nil {
				t.Fatalf("SetSOPIN: %v", err)
			}
			srv := newBarrierPINTestServer(t, tc.svc, backend)
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(fmt.Sprintf(tc.body, "wrong-pin")))
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)

			assertStatus(t, rec, http.StatusUnauthorized)
			if called {
				t.Fatal("the service was reached despite SO PIN verification failure")
			}
			if backend.verifyCalls == 0 {
				t.Fatal("VerifySOPIN was not invoked")
			}
		})
	}
}

// TestBarrierHandlers_SOPINGated_GoodPIN verifies that a matching SO PIN
// reaches the service and returns the opened round.
func TestBarrierHandlers_SOPINGated_GoodPIN(t *testing.T) {
	var called bool
	for _, tc := range soPINGatedRoutes(&called) {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			backend := newStubPINBackend()
			if err := backend.SetSOPIN("", "correct-pin"); err != nil {
				t.Fatalf("SetSOPIN: %v", err)
			}
			srv := newBarrierPINTestServer(t, tc.svc, backend)
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(fmt.Sprintf(tc.body, "correct-pin")))
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)

			assertStatus(t, rec, http.StatusOK)
			if !called {
				t.Fatal("the service was not reached after SO PIN verification")
			}
			var round transport.QuorumRound
			decodeJSON(t, rec, &round)
			if round.Op != transport.OpRekey || round.Required != 3 || round.NewThreshold != 2 {
				t.Fatalf("round = %+v, want a rekey round needing 3 to set threshold 2", round)
			}
		})
	}
}

// TestBarrierHandlers_SOPINGated_NoPINBackend proves the gate fails closed:
// with no PIN backend configured neither operation reaches the service. The
// earlier behaviour admitted rekey with no PIN check at all.
func TestBarrierHandlers_SOPINGated_NoPINBackend(t *testing.T) {
	var called bool
	for _, tc := range soPINGatedRoutes(&called) {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			srv := newBarrierPINTestServer(t, tc.svc, nil)
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(fmt.Sprintf(tc.body, "")))
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)

			assertStatus(t, rec, http.StatusPreconditionFailed)
			if called {
				t.Fatal("the service was reached with no PIN backend configured")
			}
		})
	}
}
