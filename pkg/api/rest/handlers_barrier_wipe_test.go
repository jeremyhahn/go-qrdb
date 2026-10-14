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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Server-side zeroization of decoded barrier credentials
//
// The wire field being []byte only means the credential CAN be wiped. These
// tests are about whether it IS: each handler owns the slice the JSON decoder
// produced, and zeroes it before returning, on the success path and on the
// error paths alike.
//
// The mocks below deliberately RETAIN the slice they were handed rather than
// copying it. That is the whole mechanism: the service layer borrows the
// credential and the handler wipes the backing array afterwards, so a
// retained alias reads as zeros once the handler has returned. Delete the
// `defer secure.Wipe(...)` from the handler and these fail.
// ---------------------------------------------------------------------------

// b64 renders a plaintext secret the way the wire carries a []byte field.
func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// assertWiped fails unless every byte of b is zero. A zero-length slice is a
// failure too: it would mean the handler never saw the secret at all and the
// test is proving nothing.
func assertWiped(t *testing.T, what string, b []byte) {
	t.Helper()
	if len(b) == 0 {
		t.Fatalf("%s: the handler was never handed a credential; this test proves nothing", what)
	}
	for i, c := range b {
		if c != 0 {
			t.Fatalf("%s: byte %d survived the handler at %q — the decoded credential was not zeroized", what, i, b)
		}
	}
}

func TestHandleBarrierInitialize_WipesDecodedSecret(t *testing.T) {
	var retained []byte
	svc := &mockBarrierService{
		initializeFn: func(_ context.Context, secret []byte) error {
			retained = secret
			if string(secret) != "hunter2" {
				t.Fatalf("secret = %q, want %q", secret, "hunter2")
			}
			return nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/initialize",
		strings.NewReader(`{"secret":"`+b64("hunter2")+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	assertWiped(t, "BarrierInitialize", retained)
}

// TestHandleBarrierInitialize_WipesOnServiceError is the error path: the
// credential is zeroized even when the barrier refuses it, which is the case
// a bare post-call wipe would miss.
func TestHandleBarrierInitialize_WipesOnServiceError(t *testing.T) {
	var retained []byte
	svc := &mockBarrierService{
		initializeFn: func(_ context.Context, secret []byte) error {
			retained = secret
			return errors.New("barrier already initialized")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/initialize",
		strings.NewReader(`{"secret":"`+b64("hunter2")+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusNoContent {
		t.Fatalf("expected the service error to surface, got %d", rec.Code)
	}
	assertWiped(t, "BarrierInitialize (error path)", retained)
}

func TestHandleBarrierUnseal_WipesDecodedSecret(t *testing.T) {
	var retained []byte
	svc := &mockBarrierService{
		unsealFn: func(_ context.Context, secret []byte) error {
			retained = secret
			return nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal",
		strings.NewReader(`{"secret":"`+b64("hunter2")+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	assertWiped(t, "BarrierUnseal", retained)
}

// TestHandleBarrierUnseal_WipesOnWrongSecret covers the path an attacker
// drives most often: a rejected unseal must not leave the attempted
// credential lying in the daemon's heap.
func TestHandleBarrierUnseal_WipesOnWrongSecret(t *testing.T) {
	var retained []byte
	svc := &mockBarrierService{
		unsealFn: func(_ context.Context, secret []byte) error {
			retained = secret
			return errors.New("invalid credentials")
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal",
		strings.NewReader(`{"secret":"`+b64("wrong-guess")+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code == http.StatusNoContent {
		t.Fatalf("expected the unseal failure to surface, got %d", rec.Code)
	}
	assertWiped(t, "BarrierUnseal (rejected)", retained)
}

// TestHandleBarrierQuorumSubmit_WipesShare proves a holder's share is
// zeroized once the submission returns, on the counted path and on the
// refused path alike: the share leaves the holder's envelope only to be
// counted and must not outlive the request in the daemon's heap.
func TestHandleBarrierQuorumSubmit_WipesShare(t *testing.T) {
	for name, svcErr := range map[string]error{"counted": nil, "refused": errors.New("factor verification failed")} {
		t.Run(name, func(t *testing.T) {
			var retained []byte
			svc := &mockBarrierService{
				quorumSubmitFn: func(_ context.Context, req *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
					retained = req.Share
					if string(req.Share) != "share-bytes" {
						t.Fatalf("share = %q, want %q", req.Share, "share-bytes")
					}
					if svcErr != nil {
						return nil, svcErr
					}
					return &service.QuorumOutcomeInfo{Domain: req.Domain, Op: req.Op, Required: 3, Submitted: 1}, nil
				},
			}
			srv := newBarrierTestServer(t, svc)

			body := `{"domain":"unseal","op":"unseal","username":"alice","challenge_id":"c1","factor":"mtls","response":"` + b64("sig") + `","share":"` + b64("share-bytes") + `"}`
			req := httptest.NewRequest(http.MethodPost, "/v1/barrier/quorum/submit", strings.NewReader(body))
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)

			if (svcErr == nil) != (rec.Code == http.StatusOK) {
				t.Fatalf("status = %d for the %s path", rec.Code, name)
			}
			assertWiped(t, "BarrierQuorumSubmit ("+name+")", retained)
		})
	}
}

func TestHandleTenantBarrierInit_WipesDecodedPassphrase(t *testing.T) {
	var retained []byte
	svc := &mockTenantService{
		tenantBarrierInitFn: func(_ context.Context, _ string, passphrase []byte, _, _ int) error {
			retained = passphrase
			return nil
		},
		getTenantFn: func(_ context.Context, id string) (*tenant.TenantInfo, error) {
			return &tenant.TenantInfo{ID: id}, nil
		},
	}
	srv := newTenantServer(svc)

	body := `{"passphrase":"` + b64("tenant-pw") + `","strategy":"passphrase"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/init/complete",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
	}
	assertWiped(t, "TenantBarrierInit", retained)
}

// TestBarrierWireFieldIsBase64 pins the decode side of the wire change: the
// server reads a []byte field, so the body carries base64. Sending the
// plaintext where base64 is expected must NOT be silently accepted as the
// credential — it would decode to a different byte string.
func TestBarrierWireFieldIsBase64(t *testing.T) {
	var retained []byte
	svc := &mockBarrierService{
		initializeFn: func(_ context.Context, secret []byte) error {
			retained = bytes.Clone(secret)
			return nil
		},
	}
	srv := newBarrierTestServer(t, svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/barrier/initialize",
		strings.NewReader(`{"secret":"`+b64("hunter2")+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNoContent)
	if string(retained) != "hunter2" {
		t.Fatalf("decoded secret = %q, want %q — the wire field is base64", retained, "hunter2")
	}
}
