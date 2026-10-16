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
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
)

// delegatedCaller authenticates a real acme delegation and returns it with
// the delegated identity; only the mTLS authenticator can make one.
func delegatedCaller(t *testing.T) (*authtest.Delegation, *auth.Identity) {
	t.Helper()
	d := authtest.NewDelegation(t, delegation.Grant{Tenant: "acme", Principal: "rest-fwd-test", Scope: []string{"kv:put"}})
	return d, d.Identity(t)
}

// TestAttachOnBehalfAssertion_CarriesTheDelegation: the assertion a follower
// signs for a delegated caller carries the delegation certificate, which the
// leader verifies again.
func TestAttachOnBehalfAssertion_CarriesTheDelegation(t *testing.T) {
	d, caller := delegatedCaller(t)
	srv, _ := newForwardServer(t)
	orig := requestWithCaller(http.MethodPut, "/kv/foo", []byte("x"), caller)
	fwd, _ := http.NewRequest(http.MethodPut, "https://leader/kv/foo", nil)
	if err := srv.attachOnBehalfAssertion(fwd, orig, []byte("x")); err != nil {
		t.Fatalf("attachOnBehalfAssertion: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(fwd.Header.Get(forward.HeaderAssertion))
	if err != nil {
		t.Fatalf("decode assertion: %v", err)
	}
	var a forward.CallerAssertion
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("unmarshal assertion: %v", err)
	}
	if a.Subject != caller.Subject || a.TenantID != "acme" || a.Delegation != base64.StdEncoding.EncodeToString(d.Certificate.Raw) {
		t.Fatalf("assertion %s does not carry the delegated caller and its certificate", raw)
	}
}

// TestForwardToLeader_UnsignedDelegationIsNeverForwarded: unsigned, the
// leader would authorize the request as the forwarding node, so a delegated
// request this node cannot sign for is refused before it leaves.
func TestForwardToLeader_UnsignedDelegationIsNeverForwarded(t *testing.T) {
	var reached atomic.Bool
	leader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(leader.Close)

	_, caller := delegatedCaller(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithLeaderForwarding(NewPeerAdminRegistry("63002")))
	orig := requestWithCaller(http.MethodPut, "/kv/foo", []byte("x"), caller)
	rec := httptest.NewRecorder()
	srv.forwardToLeader(rec, orig, leader.URL, []byte("x"))

	if reached.Load() {
		t.Fatal("a delegated request was forwarded unsigned; the leader would authorize it as the node")
	}
	assertHolderForwardRefused(t, rec)
}

// TestAttachOnBehalfAssertion_DelegationSigningFailureIsRefused: a delegated
// caller whose assertion cannot be signed is refused, nothing attached.
func TestAttachOnBehalfAssertion_DelegationSigningFailureIsRefused(t *testing.T) {
	_, caller := delegatedCaller(t)
	srv, _ := newForwardServer(t)
	srv.forwardSigner = failingSigner{srv.forwardSigner}
	orig := requestWithCaller(http.MethodPut, "/kv/foo", []byte("x"), caller)
	fwd, _ := http.NewRequest(http.MethodPut, "https://leader/kv/foo", nil)

	err := srv.attachOnBehalfAssertion(fwd, orig, []byte("x"))
	var refused *UnsignedForwardRefusedError
	if !errors.As(err, &refused) || refused.Subject != caller.Subject {
		t.Fatalf("err = %v; want *UnsignedForwardRefusedError for %s", err, caller.Subject)
	}
	if fwd.Header.Get(forward.HeaderAssertion) != "" || fwd.Header.Get(forward.HeaderSignature) != "" {
		t.Fatal("headers attached for an assertion that was never signed")
	}
}
