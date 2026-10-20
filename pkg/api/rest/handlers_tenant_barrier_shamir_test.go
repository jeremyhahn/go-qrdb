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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// shamirSplitRecorder answers every barrier init with result or err and
// records the options the handler passed, resolved.
type shamirSplitRecorder struct {
	*mockTenantService
	result *tenant.TenantBarrierInitResult
	err    error
	calls  int
	got    tenant.TenantBarrierInitSettings
}

func (s *shamirSplitRecorder) TenantBarrierInitWithStrategy(_ context.Context, _ string, _ []byte, _, _ int, _ seal.StrategyID, _ *tenant.TenantBarrierShamirRoster, opts ...tenant.TenantBarrierInitOption) (*tenant.TenantBarrierInitResult, error) {
	s.calls++
	s.got = tenant.ResolveTenantBarrierInitOptions(opts...)
	return s.result, s.err
}

func newShamirSplitRecorder() *shamirSplitRecorder {
	return &shamirSplitRecorder{mockTenantService: &mockTenantService{}, result: &tenant.TenantBarrierInitResult{
		TenantID: "acme", Strategy: "shamir", Threshold: 2, TotalShares: 2,
		Roster: []byte("roster"), UnsealKeyCheck: []byte("check"),
	}}
}

func postTenantBarrierInit(t *testing.T, svc tenant.TenantServicer, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/acme/barrier/init/complete", strings.NewReader(string(raw)))
	rec := httptest.NewRecorder()
	newTenantServer(svc).dispatch(rec, req)
	return rec
}

// The first request echoes nothing; it is answered with the split to sign.
func TestHandleTenantBarrierInit_FirstShamirRequestReturnsTheSplitAndEchoesNothing(t *testing.T) {
	svc := newShamirSplitRecorder()
	rec := postTenantBarrierInit(t, svc, map[string]any{"strategy": "shamir", "threshold": 2, "shares": 2})

	assertStatus(t, rec, http.StatusOK)
	if svc.got.PreparedRoster != nil || svc.got.PreparedUnsealKeyCheck != nil {
		t.Fatalf("service saw an echo %+v on the first request", svc.got)
	}
	var resp struct {
		Roster         []byte `json:"roster"`
		UnsealKeyCheck []byte `json:"unseal_key_check"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", rec.Body.String(), err)
	}
	if string(resp.Roster) != "roster" || string(resp.UnsealKeyCheck) != "check" {
		t.Fatalf("response %s, want the prepared split", rec.Body.String())
	}
}

func TestHandleTenantBarrierInit_PendingAdminIsASuccessCarryingTheStatus(t *testing.T) {
	svc := newShamirSplitRecorder()
	svc.result = &tenant.TenantBarrierInitResult{TenantID: "acme", Strategy: "shamir", AdminPending: true}
	rec := postTenantBarrierInit(t, svc, map[string]any{"strategy": "shamir", "threshold": 2, "shares": 2})

	assertStatus(t, rec, http.StatusOK)
	var resp struct {
		AdminPending bool `json:"admin_pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", rec.Body.String(), err)
	}
	if !resp.AdminPending {
		t.Fatalf("response %s, want admin_pending", rec.Body.String())
	}
}

func TestHandleTenantBarrierInit_CompletedAdminIsNotPending(t *testing.T) {
	svc := newShamirSplitRecorder()
	rec := postTenantBarrierInit(t, svc, map[string]any{"strategy": "shamir", "threshold": 2, "shares": 2})

	assertStatus(t, rec, http.StatusOK)
	var resp struct {
		AdminPending bool `json:"admin_pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", rec.Body.String(), err)
	}
	if resp.AdminPending {
		t.Fatalf("response %s, want no pending admin", rec.Body.String())
	}
}

// The second request's echo and statement reach the service unchanged.
func TestHandleTenantBarrierInit_SecondShamirRequestPassesTheEchoToTheService(t *testing.T) {
	svc := newShamirSplitRecorder()
	rec := postTenantBarrierInit(t, svc, map[string]any{
		"strategy": "shamir", "threshold": 2, "shares": 2,
		"roster": b64("roster"), "unseal_key_check": b64("check"), "init_statement": b64("statement"),
	})

	assertStatus(t, rec, http.StatusOK)
	if svc.calls != 1 {
		t.Fatalf("service called %d times, want 1", svc.calls)
	}
	if string(svc.got.PreparedRoster) != "roster" || string(svc.got.PreparedUnsealKeyCheck) != "check" || string(svc.got.InitStatement) != "statement" {
		t.Fatalf("service saw %+v, want the echoed split and its statement", svc.got)
	}
}

// A mismatched echo is the caller's fault, answered 409 rather than 500.
func TestHandleTenantBarrierInit_MismatchedEchoIsAConflict(t *testing.T) {
	svc := newShamirSplitRecorder()
	svc.err = &seal.PreparedShamirMismatchError{Field: "roster"}
	rec := postTenantBarrierInit(t, svc, map[string]any{
		"strategy": "shamir", "roster": b64("other"), "unseal_key_check": b64("check"),
	})

	assertStatus(t, rec, http.StatusConflict)
	if string(svc.got.PreparedRoster) != "other" {
		t.Fatalf("service saw %+v, want the caller's echo", svc.got)
	}
	if !strings.Contains(rec.Body.String(), "roster") {
		t.Fatalf("body %s does not name the mismatched field", rec.Body.String())
	}
}

// An echo that is not base64 is refused before the service is called.
func TestHandleTenantBarrierInit_UndecodableEchoIsRefused(t *testing.T) {
	svc := newShamirSplitRecorder()
	rec := postTenantBarrierInit(t, svc, map[string]any{"strategy": "shamir", "roster": "%%%", "unseal_key_check": b64("check")})

	assertStatus(t, rec, http.StatusBadRequest)
	if svc.calls != 0 {
		t.Fatalf("service called %d times, want 0", svc.calls)
	}
}
