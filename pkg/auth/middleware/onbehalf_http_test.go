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

package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

func httpOBMiddleware(direct *mockAuthenticator) *AuthMiddleware {
	stored := &user.User{ID: obAliceID, Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive, RoleIDs: []string{"kv-writer"}}
	// This file is about on-behalf forwarding, not auth.mtls_binding_policy
	// (go-qrdb#517/#548): alice has no enrolled certificate binding, and
	// under the default MTLSBindingRequired that refuses every cert login
	// for her, including the ordinary (non-forwarded) chain these tests
	// also exercise. MTLSBindingEnrolled is the documented explicit
	// opt-out for an account policy was never meant to gate here.
	return New(&capturingAuthorizer{}, WithAuthenticator(direct), WithUserService(&mockUserServicer{user: stored}),
		WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
}

// TestAuthenticateHTTP_ForwardedRequestIsTheCaller: a request a cluster node
// forwarded authenticates as the asserted caller, marked on-behalf, and its
// body is still there for the handler.
func TestAuthenticateHTTP_ForwardedRequestIsTheCaller(t *testing.T) {
	cert, key := obNodeCert(t, obNodeID)
	body := []byte(`{"jsonrpc":"2.0","method":"tools/call"}`)
	r := buildOnBehalfRequest(t, cert, key, obAliceSubject, "acme", obNodeID, time.Now(), body, http.MethodPost, "/mcp")
	m := httpOBMiddleware(&mockAuthenticator{name: "mtls", err: auth.ErrNoPeerCertificate})

	ctx, id, err := m.AuthenticateHTTP(r.Context(), httptest.NewRecorder(), r)
	if err != nil || id == nil || id.Subject != obAliceSubject || !IsOnBehalf(id) || !IsOnBehalf(auth.IdentityFromContext(ctx)) {
		t.Fatalf("AuthenticateHTTP = %+v, %v; want alice, on-behalf", id, err)
	}
	if got, _ := io.ReadAll(r.Body); string(got) != string(body) {
		t.Fatalf("body after authentication = %q; want it restored", got)
	}
}

// TestAuthenticateHTTP_AnyOtherRequestRunsTheChain: no assertion, the chain
// authenticates; a tampered one is refused, never downgraded to the chain.
func TestAuthenticateHTTP_AnyOtherRequestRunsTheChain(t *testing.T) {
	direct := &mockAuthenticator{name: "mtls", identity: &auth.Identity{Subject: "alice", TenantID: "acme"}}
	m := httpOBMiddleware(direct)
	r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	if _, id, err := m.AuthenticateHTTP(r.Context(), httptest.NewRecorder(), r); err != nil || id == nil || IsOnBehalf(id) {
		t.Fatalf("AuthenticateHTTP without an assertion = %+v, %v; want the chain's caller", id, err)
	}

	cert, key := obNodeCert(t, obNodeID)
	r = buildOnBehalfRequest(t, cert, key, obAliceSubject, "acme", obNodeID, time.Now(), []byte("signed"), http.MethodPost, "/mcp")
	r.Body = io.NopCloser(strings.NewReader("swapped"))
	if _, _, err := m.AuthenticateHTTP(r.Context(), httptest.NewRecorder(), r); err == nil {
		t.Fatal("a forwarded request whose body was swapped authenticated")
	}
	if IsOnBehalf(nil) {
		t.Fatal("no identity is on-behalf")
	}
}
