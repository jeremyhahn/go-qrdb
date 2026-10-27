// Copyright 2026 Jeremy Hahn
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

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// signedInitRecorder records the requests both halves of a signed init reach
// the tenant service with.
type signedInitRecorder struct {
	*mockTenantService
	begin       *tenant.TenantBarrierInitBeginRequest
	complete    *tenant.TenantBarrierInitCompleteRequest
	passphrase  *secure.Buffer
	seen        []byte
	beginErr    error
	completeErr error
}

func newSignedInitRecorder() *signedInitRecorder {
	return &signedInitRecorder{mockTenantService: &mockTenantService{}}
}

func (r *signedInitRecorder) TenantBarrierInitBegin(_ context.Context, req *tenant.TenantBarrierInitBeginRequest) (*tenant.TenantBarrierInitBeginResult, error) {
	r.begin = req
	if r.beginErr != nil {
		return nil, r.beginErr
	}
	return &tenant.TenantBarrierInitBeginResult{
		TenantID: req.TenantID, Strategy: string(req.Strategy), Reference: []byte("ref"), Statement: []byte("stmt"),
		ExpiresAt: time.Unix(1_800_000_000, 0).UTC(), Threshold: req.Threshold, TotalShares: req.Shares,
	}, nil
}

func (r *signedInitRecorder) TenantBarrierInitComplete(_ context.Context, req *tenant.TenantBarrierInitCompleteRequest) (*tenant.TenantBarrierInitResult, error) {
	r.complete = req
	r.passphrase = req.Passphrase
	_ = req.Passphrase.WithBytes(func(p []byte) error { r.seen = bytes.Clone(p); return nil })
	if r.completeErr != nil {
		return nil, r.completeErr
	}
	return &tenant.TenantBarrierInitResult{TenantID: req.TenantID, Strategy: "passphrase"}, nil
}

func postSignedInit(t *testing.T, svc tenant.TenantServicer, half, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/signed-init/"+half, strings.NewReader(body))
	rec := httptest.NewRecorder()
	newTenantServer(svc).dispatch(rec, req)
	return rec
}

func TestHandleTenantBarrierSignedInitBegin_MapsRequestAndResponse(t *testing.T) {
	svc := newSignedInitRecorder()
	rec := postSignedInit(t, svc, "begin", `{"strategy":"shamir","key_ref":"kr","threshold":2,"shares":3,"public_key":"c3BraQ=="}`)

	assertStatus(t, rec, http.StatusOK)
	got := svc.begin
	if got.TenantID != "acme" || got.Strategy != seal.StrategyID("shamir") || got.KeyRef != "kr" ||
		got.Threshold != 2 || got.Shares != 3 || string(got.SignerPublicKey) != "spki" || got.Roster != nil {
		t.Fatalf("service request = %+v", got)
	}
	var resp transport.TenantBarrierSignedInitBeginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", rec.Body.String(), err)
	}
	if resp.TenantID != "acme" || string(resp.Reference) != "ref" || string(resp.Statement) != "stmt" ||
		!resp.ExpiresAt.Equal(time.Unix(1_800_000_000, 0)) || resp.Threshold != 2 || resp.TotalShares != 3 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestHandleTenantBarrierSignedInitBegin_OfficersBecomeTheRoster(t *testing.T) {
	svc := newSignedInitRecorder()
	rec := postSignedInit(t, svc, "begin", `{"strategy":"shamir","officers":[{"username":"alice","role":"so"}],"policy":"any-enrolled","public_key":"c3BraQ=="}`)

	assertStatus(t, rec, http.StatusOK)
	if svc.begin.Roster == nil || len(svc.begin.Roster.Officers) != 1 || svc.begin.Roster.Policy != seal.FactorPolicy("any-enrolled") {
		t.Fatalf("roster = %+v", svc.begin.Roster)
	}
}

func TestHandleTenantBarrierSignedInitBegin_InvalidBody(t *testing.T) {
	svc := newSignedInitRecorder()
	rec := postSignedInit(t, svc, "begin", `{not json`)

	assertStatus(t, rec, http.StatusBadRequest)
	if svc.begin != nil {
		t.Fatal("service reached with an undecodable body")
	}
}

func TestHandleTenantBarrierSignedInitBegin_ServiceErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"field required", &tenant.SignedInitFieldRequiredError{Field: "signer public key"}, http.StatusBadRequest},
		{"too many", &tenant.TooManySignedInitsError{Limit: 1}, http.StatusTooManyRequests},
		{"not found", tenant.ErrTenantNotFound, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newSignedInitRecorder()
			svc.beginErr = tc.err
			assertStatus(t, postSignedInit(t, svc, "begin", `{"public_key":"c3BraQ=="}`), tc.want)
		})
	}
}

func TestHandleTenantBarrierSignedInitComplete_MapsRequestAndAlwaysAnswersWithABody(t *testing.T) {
	svc := newSignedInitRecorder()
	rec := postSignedInit(t, svc, "complete", `{"reference":"cmVm","signature":"c2ln"}`)

	assertStatus(t, rec, http.StatusOK)
	if svc.complete.TenantID != "acme" || string(svc.complete.Reference) != "ref" || string(svc.complete.Signature) != "sig" {
		t.Fatalf("service request = %+v", svc.complete)
	}
	var resp struct {
		TenantID string `json:"tenant_id"`
		Strategy string `json:"strategy"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	if resp.TenantID != "acme" || resp.Strategy != "passphrase" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestHandleTenantBarrierSignedInitComplete_WipesDecodedPassphrase(t *testing.T) {
	svc := newSignedInitRecorder()
	assertStatus(t, postSignedInit(t, svc, "complete", `{"reference":"cmVm","signature":"c2ln","passphrase":"c2VjcmV0LXBhc3NwaHJhc2U="}`), http.StatusOK)

	if string(svc.seen) != "secret-passphrase" || svc.passphrase == nil || svc.passphrase.IsAlive() {
		t.Fatalf("passphrase %q not passed in protected memory and destroyed after the handler returned", svc.seen)
	}
}

func TestHandleTenantBarrierSignedInitComplete_InvalidBody(t *testing.T) {
	svc := newSignedInitRecorder()
	assertStatus(t, postSignedInit(t, svc, "complete", `[`), http.StatusBadRequest)
	if svc.complete != nil {
		t.Fatal("service reached with an undecodable body")
	}
}

// TestHandleTenantBarrierSignedInitComplete_RefusedReferenceIsForbidden pins
// that a bad signature, a replayed reference and an expired one are 403.
func TestHandleTenantBarrierSignedInitComplete_RefusedReferenceIsForbidden(t *testing.T) {
	for _, reason := range []tenant.SignedInitReason{tenant.SignedInitUnknown, tenant.SignedInitExpired} {
		svc := newSignedInitRecorder()
		svc.completeErr = &tenant.SignedInitError{TenantID: "acme", Reason: reason}
		assertStatus(t, postSignedInit(t, svc, "complete", `{"reference":"cmVm","signature":"c2ln"}`), http.StatusForbidden)
	}
}
