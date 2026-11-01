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

//go:build !integration

package rest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestHandleCeremonyInitialize_FollowerForwardsToLeader is the fix for the
// first-boot known limitation: a follower that refuses CeremonyInitialize
// with *service.AdminCredentialNotLeaderError (pkg/service/ceremony_leader_gate.go)
// now forwards the whole request to the leader over REST -- the same
// forwardToLeader/tryForwardWrite path every other system-shard write
// (membership add, KV put, ...) already uses -- instead of surfacing the
// refusal to the caller.
func TestHandleCeremonyInitialize_FollowerForwardsToLeader(t *testing.T) {
	var leaderCalls atomic.Int32
	var leaderSawBody string
	leader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaderCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		leaderSawBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"state":"initialized","spki_pin":"pin123"}`))
	}))
	defer leader.Close()

	mockSvc := newCoverageCeremonyService()
	mockSvc.initErr = &service.AdminCredentialNotLeaderError{LeaderAddress: "10.0.0.2:63001"}

	reg := NewPeerAdminRegistry("63002")
	reg.Register("10.0.0.2:63001", leader.URL)
	s := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCeremonyService(mockSvc), WithLeaderForwarding(reg))

	reqBody := `{"so_pin":"x","admin_generate":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if leaderCalls.Load() != 1 {
		t.Fatalf("leader calls = %d, want 1", leaderCalls.Load())
	}
	if leaderSawBody != reqBody {
		t.Fatalf("leader saw body %q, want %q", leaderSawBody, reqBody)
	}
	if !strings.Contains(rec.Body.String(), "initialized") {
		t.Fatalf("follower response %q does not relay the leader's answer", rec.Body.String())
	}
}

// TestHandleCeremonyInitialize_NoResolver_AnswersTheRefusal is the control:
// with no leader-forwarding resolver configured (e.g. a single-node
// deployment, or a composer that never wired one), the follower's refusal is
// answered directly, exactly as before this fix.
func TestHandleCeremonyInitialize_NoResolver_AnswersTheRefusal(t *testing.T) {
	mockSvc := newCoverageCeremonyService()
	mockSvc.initErr = &service.AdminCredentialNotLeaderError{LeaderAddress: "10.0.0.2:63001"}

	s := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0", WithCeremonyService(mockSvc))

	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize",
		strings.NewReader(`{"so_pin":"x","admin_generate":true}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)

	assertStatus(t, rec, http.StatusServiceUnavailable)
}

// TestHandleCeremonyInitialize_NoLeaderKnownYet_AnswersTheRefusal: an empty
// LeaderAddress (no election has completed yet) cannot be forwarded to
// anything, so the typed refusal is answered rather than attempted.
func TestHandleCeremonyInitialize_NoLeaderKnownYet_AnswersTheRefusal(t *testing.T) {
	mockSvc := newCoverageCeremonyService()
	mockSvc.initErr = &service.AdminCredentialNotLeaderError{LeaderAddress: ""}

	reg := NewPeerAdminRegistry("63002")
	s := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCeremonyService(mockSvc), WithLeaderForwarding(reg))

	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize",
		strings.NewReader(`{"so_pin":"x","admin_generate":true}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)

	assertStatus(t, rec, http.StatusServiceUnavailable)
}

// TestHandleCeremonyInitialize_OversizedBody_Is413: the body is read with
// readBoundedBody under the server's own configured max, the same bound
// handleMembershipAdd and handleKVPut already read their raw bodies under
// -- a request over that bound is refused as too large, never as a generic
// read failure.
func TestHandleCeremonyInitialize_OversizedBody_Is413(t *testing.T) {
	mockSvc := newCoverageCeremonyService()
	s := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithCeremonyService(mockSvc), WithMaxBodyBytes(8))

	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize",
		strings.NewReader(`{"so_pin":"this-request-body-is-longer-than-8-bytes"}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)

	assertStatus(t, rec, http.StatusRequestEntityTooLarge)
}

func TestHandleCeremonyInitialize_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCeremonyInitialize_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()),
		WithCeremonyCA(&coverageCeremonyCA{}))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

// TestHandleCeremonyInitialize_ForwardsAllFields proves every request field
// reaches the service's CeremonyInitializeOptions.
func TestHandleCeremonyInitialize_ForwardsAllFields(t *testing.T) {
	svc := newCoverageCeremonyService()
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(svc),
		WithCeremonyCA(&coverageCeremonyCA{}))

	body := `{
		"so_pin": "pin",
		"user_pin": "upin",
		"credential_seal_strategy": "shamir",
		"threshold": 2,
		"officers": [
			{"username": "officer-1", "csr_pem": "-----BEGIN CERTIFICATE REQUEST-----\ncsr1\n-----END CERTIFICATE REQUEST-----\n"},
			{"username": "officer-2", "csr_pem": "-----BEGIN CERTIFICATE REQUEST-----\ncsr2\n-----END CERTIFICATE REQUEST-----\n"}
		],
		"hostname": "override.example",
		"sans": ["alt1.example", "alt2.example"]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/initialize", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCeremonyInitialize(rec, req)
	assertStatus(t, rec, http.StatusOK)

	got := svc.capturedInitOpts
	if got == nil {
		t.Fatal("expected CeremonyInitialize to be called with non-nil opts")
	}
	if got.SOPin != "pin" || got.UserPin != "upin" {
		t.Errorf("SOPin/UserPin not forwarded: %+v", got)
	}
	if got.CredentialSealStrategy != "shamir" {
		t.Errorf("CredentialSealStrategy = %q, want %q", got.CredentialSealStrategy, "shamir")
	}
	if got.Threshold != 2 {
		t.Errorf("Threshold = %d, want 2", got.Threshold)
	}
	if got.Hostname != "override.example" {
		t.Errorf("Hostname = %q, want %q", got.Hostname, "override.example")
	}
	if len(got.SANs) != 2 || got.SANs[0] != "alt1.example" || got.SANs[1] != "alt2.example" {
		t.Errorf("SANs = %v, want [alt1.example alt2.example]", got.SANs)
	}
	if len(got.Officers) != 2 {
		t.Fatalf("Officers = %v, want 2 entries", got.Officers)
	}
	if got.Officers[0].Username != "officer-1" || got.Officers[0].CSRPEM == "" {
		t.Errorf("Officers[0] = %+v", got.Officers[0])
	}
	if got.Officers[1].Username != "officer-2" || got.Officers[1].CSRPEM == "" {
		t.Errorf("Officers[1] = %+v", got.Officers[1])
	}
}

func TestHandleCeremonyStatus_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/v1/ceremony/status", nil)
	rec := httptest.NewRecorder()
	s.handleCeremonyStatus(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCeremonyStatus_Success(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()))
	req := httptest.NewRequest(http.MethodGet, "/v1/ceremony/status", nil)
	rec := httptest.NewRecorder()
	s.handleCeremonyStatus(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	if resp["state"] != "initialized" || resp["spki_pin"] != "pin123" {
		t.Errorf("status response = %v, want state=initialized spki_pin=pin123", resp)
	}
}

func TestHandleCeremonyStatus_ServiceError(t *testing.T) {
	svc := newCoverageCeremonyService()
	svc.statusErr = &service.KeyNotFoundError{Key: "k"}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/ceremony/status", nil)
	rec := httptest.NewRecorder()
	s.handleCeremonyStatus(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleCeremonySignCSR_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/sign-csr", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleCeremonySignCSR(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleCeremonySignCSR_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()))
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/sign-csr", strings.NewReader(`bad`))
	rec := httptest.NewRecorder()
	s.handleCeremonySignCSR(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleCeremonySignCSR_Success(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(newCoverageCeremonyService()))
	body := `{"username":"alice","so_pin":"pin","csr_pem":"csr","role":"officer"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/sign-csr", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCeremonySignCSR(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp map[string]any
	decodeJSON(t, rec, &resp)
	if resp["cert_pem"] != "signed-cert" {
		t.Errorf("cert_pem = %v, want signed-cert", resp["cert_pem"])
	}
}

func TestHandleCeremonySignCSR_ServiceError(t *testing.T) {
	svc := newCoverageCeremonyService()
	svc.signCSRErr = &service.KeyNotFoundError{Key: "k"}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithCeremonyService(svc))
	body := `{"username":"alice","so_pin":"pin","csr_pem":"csr","role":"officer"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ceremony/sign-csr", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCeremonySignCSR(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}
