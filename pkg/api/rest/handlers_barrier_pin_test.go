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
	"time"

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

// TestBarrierHandlers_SOPINGated_CarriesThePIN verifies that rekey start and
// holder revocation hand the request's so_pin to the barrier service, which
// verifies it behind the SO PIN lockout, and return the opened round.
func TestBarrierHandlers_SOPINGated_CarriesThePIN(t *testing.T) {
	var called bool
	for _, tc := range soPINGatedRoutes(&called) {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			srv := newBarrierPINTestServer(t, tc.svc, nil)
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(fmt.Sprintf(tc.body, "correct-pin")))
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)

			assertStatus(t, rec, http.StatusOK)
			if !called || len(tc.svc.soPINs) != 1 || tc.svc.soPINs[0] != "correct-pin" {
				t.Fatalf("the service was handed SO PINs %q (reached %v)", tc.svc.soPINs, called)
			}
			var round transport.QuorumRound
			decodeJSON(t, rec, &round)
			if round.Op != transport.OpRekey || round.Required != 3 || round.NewThreshold != 2 {
				t.Fatalf("round = %+v, want a rekey round needing 3 to set threshold 2", round)
			}
		})
	}
}

// TestBarrierHandlers_SOPINGated_ServiceRefusals verifies the barrier
// service's SO PIN refusals render by class: a wrong PIN 401, no PIN backend
// or no SO PIN provisioned 412, and a locked-out check 429 with Retry-After.
func TestBarrierHandlers_SOPINGated_ServiceRefusals(t *testing.T) {
	for refusal, want := range map[error]int{
		pin.ErrPINInvalid:           http.StatusUnauthorized,
		pin.ErrBackendNotConfigured: http.StatusPreconditionFailed,
		pin.ErrPINNotSet:            http.StatusPreconditionFailed,
		&pin.SOPINLockedError{RetryAfter: 1500 * time.Millisecond}: http.StatusTooManyRequests,
	} {
		svc := &mockBarrierService{
			rekeyStartFn: func(context.Context, seal.QuorumDomain, int, seal.FactorPolicy) (*service.QuorumRoundInfo, error) {
				return nil, refusal
			},
		}
		srv := newBarrierPINTestServer(t, svc, nil)
		req := httptest.NewRequest(http.MethodPost, "/v1/barrier/rekey/start", strings.NewReader(`{"domain":"unseal","threshold":2,"so_pin":"x"}`))
		rec := httptest.NewRecorder()
		srv.dispatch(rec, req)
		assertStatus(t, rec, want)
		if want == http.StatusTooManyRequests && rec.Header().Get("Retry-After") != "2" {
			t.Errorf("Retry-After = %q, want 2", rec.Header().Get("Retry-After"))
		}
	}
}

// TestBarrierHandlers_ChangePassphrase_RequiresSOPIN verifies the passphrase
// change is SO-gated like every other custody operation: no PIN backend is
// 412, a missing or wrong SO PIN 401, and in none of those is the barrier
// service reached; the right SO PIN reaches it.
func TestBarrierHandlers_ChangePassphrase_RequiresSOPIN(t *testing.T) {
	provisioned := func() pin.PINBackend {
		backend := newStubPINBackend()
		backend.storedSOPIN, backend.soPINSet = "so-1234", true
		return backend
	}
	cases := map[string]struct {
		backend pin.PINBackend
		soPIN   string
		want    int
	}{
		"no pin backend": {nil, "so-1234", http.StatusPreconditionFailed},
		"missing pin":    {provisioned(), "", http.StatusUnauthorized},
		"wrong pin":      {provisioned(), "wrong", http.StatusUnauthorized},
		"right pin":      {provisioned(), "so-1234", http.StatusNoContent},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			called := false
			svc := &mockBarrierService{
				changePassphraseFn: func(context.Context, []byte, []byte) error {
					called = true
					return nil
				},
			}
			srv := newBarrierPINTestServer(t, svc, tc.backend)
			body := fmt.Sprintf(`{"current":"b2xk","next":"bmV3","so_pin":%q}`, tc.soPIN)
			req := httptest.NewRequest(http.MethodPost, "/v1/barrier/change-passphrase", strings.NewReader(body))
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)

			assertStatus(t, rec, tc.want)
			if reached := tc.want == http.StatusNoContent; called != reached {
				t.Fatalf("service reached = %v, want %v", called, reached)
			}
		})
	}
}
