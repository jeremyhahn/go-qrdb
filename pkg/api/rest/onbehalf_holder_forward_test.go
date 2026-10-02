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
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
)

func holderCaller() *auth.Identity {
	return &auth.Identity{Subject: "admin", TenantID: "system",
		Metadata: map[string]string{"holder_profile": "custodian"}}
}

// TestAttachOnBehalfAssertion_CarriesTheHolderProfile: the assertion a
// follower signs for a holder caller says so, so the leader re-marks the
// identity instead of resolving the subject to a user account.
func TestAttachOnBehalfAssertion_CarriesTheHolderProfile(t *testing.T) {
	srv, _ := newForwardServer(t)
	orig := requestWithCaller(http.MethodPut, "/kv/foo", []byte("x"), holderCaller())
	fwd, _ := http.NewRequest(http.MethodPut, "https://leader/kv/foo", nil)
	if err := srv.attachOnBehalfAssertion(fwd, orig, []byte("x")); err != nil {
		t.Fatalf("attachOnBehalfAssertion: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(fwd.Header.Get(forward.HeaderAssertion))
	if err != nil {
		t.Fatalf("decode assertion: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal assertion: %v", err)
	}
	if fields["holder_profile"] != "custodian" {
		t.Fatalf("assertion %s does not carry the holder profile", raw)
	}
}

// TestForwardToLeader_UnsignedHolderIsNeverForwarded: without an assertion
// the leader authorizes a forwarded request as the forwarding NODE. A holder
// request that cannot be forwarded signed is refused here and never reaches
// the leader.
func TestForwardToLeader_UnsignedHolderIsNeverForwarded(t *testing.T) {
	var reached atomic.Bool
	leader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(leader.Close)

	reg := NewPeerAdminRegistry("63002")
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(reg))
	if srv.forwardSigner != nil {
		t.Fatal("fixture: the server must have no forward signer")
	}
	orig := requestWithCaller(http.MethodPut, "/kv/foo", []byte("x"), holderCaller())
	rec := httptest.NewRecorder()
	srv.forwardToLeader(rec, orig, leader.URL, []byte("x"))

	if reached.Load() {
		t.Fatal("a holder request was forwarded unsigned; the leader would authorize it as the node")
	}
	assertHolderForwardRefused(t, rec)
}

// assertHolderForwardRefused requires the 502 a holder forward that cannot be
// signed gets, with the fixed holder-refusal message.
func assertHolderForwardRefused(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %q)", rec.Code, rec.Body.String())
	}
	if want := (&HolderForwardUnsignedError{}).Error(); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("body %q does not name the holder refusal %q", rec.Body.String(), want)
	}
}

// errSignerOffline is the cause failingSigner reports.
var errSignerOffline = errors.New("hsm offline")

// failingSigner is the node's real signing identity whose Sign fails, as an
// HSM-backed key does when the device is unreachable.
type failingSigner struct{ crypto.Signer }

func (failingSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errSignerOffline
}

var _ crypto.Signer = failingSigner{}

// TestAttachOnBehalfAssertion_HolderSigningFailureIsRefused: a holder caller
// whose assertion cannot be signed is refused with the typed error carrying
// the cause, and nothing is attached.
func TestAttachOnBehalfAssertion_HolderSigningFailureIsRefused(t *testing.T) {
	srv, _ := newForwardServer(t)
	srv.forwardSigner = failingSigner{srv.forwardSigner}
	orig := requestWithCaller(http.MethodPut, "/kv/foo", []byte("x"), holderCaller())
	fwd, _ := http.NewRequest(http.MethodPut, "https://leader/kv/foo", nil)

	err := srv.attachOnBehalfAssertion(fwd, orig, []byte("x"))
	var refused *HolderForwardUnsignedError
	if !errors.As(err, &refused) {
		t.Fatalf("err = %v; want *HolderForwardUnsignedError", err)
	}
	if refused.Subject != "admin" {
		t.Errorf("Subject = %q, want admin", refused.Subject)
	}
	if !errors.Is(err, errSignerOffline) {
		t.Errorf("err %v does not carry the signing failure", err)
	}
	if strings.Contains(err.Error(), errSignerOffline.Error()) {
		t.Errorf("Error() %q exposes the signer's failure", err.Error())
	}
	if fwd.Header.Get(forward.HeaderAssertion) != "" || fwd.Header.Get(forward.HeaderSignature) != "" {
		t.Fatal("headers attached for an assertion that was never signed")
	}
}

// TestAttachOnBehalfAssertion_UserSigningFailureStillForwards is the control:
// a user caller keeps the earlier behaviour -- no assertion, no error.
func TestAttachOnBehalfAssertion_UserSigningFailureStillForwards(t *testing.T) {
	srv, _ := newForwardServer(t)
	srv.forwardSigner = failingSigner{srv.forwardSigner}
	orig := requestWithCaller(http.MethodPut, "/kv/foo", []byte("x"), &auth.Identity{Subject: "alice", TenantID: "acme"})
	fwd, _ := http.NewRequest(http.MethodPut, "https://leader/kv/foo", nil)

	if err := srv.attachOnBehalfAssertion(fwd, orig, []byte("x")); err != nil {
		t.Fatalf("user caller: err = %v, want nil", err)
	}
	if fwd.Header.Get(forward.HeaderAssertion) != "" {
		t.Fatal("an assertion was attached although signing failed")
	}
}

// TestForwardToLeader_HolderSigningFailureIsNeverForwarded: end to end, a
// holder request whose assertion cannot be signed never reaches the leader,
// and the 502 body does not carry the signer's failure.
func TestForwardToLeader_HolderSigningFailureIsNeverForwarded(t *testing.T) {
	var reached atomic.Bool
	leader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(leader.Close)

	srv, _ := newForwardServer(t)
	srv.forwardSigner = failingSigner{srv.forwardSigner}
	orig := requestWithCaller(http.MethodPut, "/kv/foo", []byte("x"), holderCaller())
	rec := httptest.NewRecorder()
	srv.forwardToLeader(rec, orig, leader.URL, []byte("x"))

	if reached.Load() {
		t.Fatal("a holder request was forwarded although its assertion could not be signed")
	}
	assertHolderForwardRefused(t, rec)
	if strings.Contains(rec.Body.String(), errSignerOffline.Error()) {
		t.Fatalf("502 body %q exposes the signer's failure", rec.Body.String())
	}
}

// TestForwardToLeader_UnsignedUserStillForwards is the control: a user caller
// with no signer is forwarded as before.
func TestForwardToLeader_UnsignedUserStillForwards(t *testing.T) {
	var reached atomic.Bool
	leader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(leader.Close)

	reg := NewPeerAdminRegistry("63002")
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(reg))
	orig := requestWithCaller(http.MethodPut, "/kv/foo", []byte("x"), &auth.Identity{Subject: "alice", TenantID: "acme"})
	rec := httptest.NewRecorder()
	srv.forwardToLeader(rec, orig, leader.URL, []byte("x"))
	if !reached.Load() || rec.Code != http.StatusNoContent {
		t.Fatalf("user forward: reached=%v status=%d", reached.Load(), rec.Code)
	}
}
