// Copyright (c) 2026 Jeremy Hahn
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

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsPublicPath_BuiltInAllowlist(t *testing.T) {
	for _, path := range []string{"/v1/ca/bootstrap", "/v1/config/initialized", "/v1/cluster/keydelivery", "/v1/cluster/nodekey"} {
		if !IsPublicPath(path) {
			t.Errorf("IsPublicPath(%q) = false, want true", path)
		}
	}
}

func TestIsPublicPath_ExactMatchOnly(t *testing.T) {
	for _, path := range []string{"", "/", "/health", "/health/", "/health/wt", "/healthz", "/v1/cluster/", "/v1/cluster/other", "/v1/kv"} {
		if IsPublicPath(path) {
			t.Errorf("IsPublicPath(%q) = true, want false", path)
		}
	}
}

// TestRESTMiddleware_PublicPathSkipsAuthenticationForEveryMethod pins that
// the exemption IsPublicPath reports is the one RESTMiddleware applies, and
// that it does not depend on the method: a CONNECT is exempt too.
func TestRESTMiddleware_PublicPathSkipsAuthenticationForEveryMethod(t *testing.T) {
	authn := &credentialAuthenticator{}
	m := newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(authn))
	for _, method := range []string{http.MethodGet, http.MethodConnect} {
		served := false
		next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			served = true
			w.WriteHeader(http.StatusOK)
		})
		req := httptest.NewRequest(method, "/v1/ca/bootstrap", nil)
		req.Header.Set("Authorization", "Bearer good")
		w := httptest.NewRecorder()
		m.RESTMiddleware(NewRESTRouteRegistry(), next).ServeHTTP(w, req)
		if !served || w.Code != http.StatusOK {
			t.Fatalf("%s /v1/ca/bootstrap: served=%v code=%d, want served", method, served, w.Code)
		}
	}
	if n := authn.calls.Load(); n != 0 {
		t.Fatalf("authenticator consulted %d times on a public path", n)
	}
}
