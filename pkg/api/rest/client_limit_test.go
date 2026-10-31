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
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/grpcweb"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// clientLimitedREST is a REST server whose client-address limiter holds
// burst requests per address and does not refill within a test.
func clientLimitedREST(t *testing.T, authn *dispatchAuthenticator, burst int, trusted []clientlimit.TrustedProxy, opts ...ServerOption) http.Handler {
	t.Helper()
	return restWithClientLimiter(t, authn, newRESTClientLimiter(t, burst, trusted), opts...)
}

// newRESTClientLimiter returns a limiter of burst requests that does not
// refill within a test.
func newRESTClientLimiter(t *testing.T, burst int, trusted []clientlimit.TrustedProxy) *clientlimit.Limiter {
	t.Helper()
	l, err := clientlimit.New(clientlimit.Config{Policy: ratelimit.Policy{RatePerSecond: 1e-6, Burst: burst}, TrustedProxies: trusted})
	if err != nil {
		t.Fatalf("clientlimit.New: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

// restWithClientLimiter returns the handler of a REST server admitting
// every request through l.
func restWithClientLimiter(t *testing.T, authn *dispatchAuthenticator, l *clientlimit.Limiter, opts ...ServerOption) http.Handler {
	t.Helper()
	opts = append(opts,
		WithAuthMiddleware(authmw.New(dispatchDenyAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authn))),
		WithClientLimiter(l))
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", opts...).Handler()
}

func serveFrom(h http.Handler, remote, path string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestClientLimit_REST: an address past its burst is refused with 429 and
// Retry-After before authentication runs -- on a public route too -- while
// another address is still authenticated, and a forwarded-for header from
// an untrusted peer does not change the key.
func TestClientLimit_REST(t *testing.T) {
	authn := &dispatchAuthenticator{}
	h := clientLimitedREST(t, authn, 2, nil)
	const flooder = "192.0.2.1:40000"

	for i := 0; i < 2; i++ {
		if rec := serveFrom(h, flooder, "/v1/kv/k"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("request %d: status %d, want 401", i, rec.Code)
		}
	}
	rec := serveFrom(h, flooder, "/v1/kv/k")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over the burst: status %d Retry-After %q, want 429 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	for _, hdr := range [][]string{
		{"X-Forwarded-For", "203.0.113.9"},
		{"Forwarded", "for=203.0.113.9"},
	} {
		if rec := serveFrom(h, flooder, "/v1/kv/k", hdr...); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("%s from an untrusted peer: status %d, want 429", hdr[0], rec.Code)
		}
	}
	if rec := serveFrom(h, flooder, "/health"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("public route over the burst: status %d, want 429", rec.Code)
	}
	if authn.calls != 2 {
		t.Fatalf("authentications = %d, want 2 (refusals never authenticate)", authn.calls)
	}
	if rec := serveFrom(h, "198.51.100.7:40000", "/v1/kv/k"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("other address: status %d, want 401", rec.Code)
	}
}

// TestClientLimit_RESTTrustedProxy: behind a configured trusted proxy each
// forwarded client has its own bucket.
func TestClientLimit_RESTTrustedProxy(t *testing.T) {
	authn := &dispatchAuthenticator{}
	h := clientLimitedREST(t, authn, 1, []clientlimit.TrustedProxy{{Prefix: netip.MustParsePrefix("10.0.0.0/8"), Header: clientlimit.HeaderXForwardedFor}})
	const proxy = "10.0.0.1:40000"
	if rec := serveFrom(h, proxy, "/v1/kv/k", "X-Forwarded-For", "203.0.113.9"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("first client: status %d, want 401", rec.Code)
	}
	if rec := serveFrom(h, proxy, "/v1/kv/k", "X-Forwarded-For", "203.0.113.9"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("first client again: status %d, want 429", rec.Code)
	}
	if rec := serveFrom(h, proxy, "/v1/kv/k", "X-Forwarded-For", "203.0.113.10"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("second client: status %d, want 401", rec.Code)
	}
	// The proxy appends only X-Forwarded-For, so a Forwarded field reaching
	// the server is the client's own and buys no fresh bucket.
	if rec := serveFrom(h, proxy, "/v1/kv/k", "X-Forwarded-For", "203.0.113.9", "Forwarded", "for=198.51.100.1"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("client-written Forwarded: status %d, want 429 on the proxy-reported client's bucket", rec.Code)
	}
}

// TestClientLimit_RESTChargesGRPCWebCallsAtTheBoundary is the second
// finding of PR #1253's interim review: the gRPC server refuses an unknown
// service or method before its interceptors run, so a gRPC-Web call is
// charged by the HTTP limiter before it is translated. A call to a method
// that does not exist spends the budget and is refused once the address is
// exhausted, without reaching the backend; the call the backend does see
// carries the mark that keeps the gRPC interceptors, which share the
// limiter, from charging it again.
func TestClientLimit_RESTChargesGRPCWebCallsAtTheBoundary(t *testing.T) {
	l := newRESTClientLimiter(t, 1, nil)
	backend := newGRPCBackend()
	h := restWithClientLimiter(t, &dispatchAuthenticator{}, l, WithGRPCWeb(grpcweb.Config{Enabled: true}, backend))
	call := func(method string) *httptest.ResponseRecorder {
		req := grpcWebRequest(t, "")
		req.URL.Path = grpcweb.PathPrefix + method
		req.RemoteAddr = "192.0.2.1:40000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	call("qrdb.v1.NoSuchService/NoSuchMethod")
	if len(backend.calls) != 1 {
		t.Fatal("the call within the burst did not reach the gRPC backend")
	}
	if r := <-backend.calls; !l.Admitted(r.Context()) {
		t.Fatal("the translated call is not marked charged; the gRPC interceptor would charge it again")
	}
	for _, method := range []string{"qrdb.v1.NoSuchService/NoSuchMethod", "qrdb.v1.QRDBService/Get"} {
		if rec := call(method); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("%s past the burst: status %d, want 429 with Retry-After", method, rec.Code)
		}
		if len(backend.calls) != 0 {
			t.Fatalf("%s past the burst reached the gRPC backend", method)
		}
	}
}

// TestClientLimit_RESTChargesGRPCWebItAnswers covers the requests under the
// gRPC-Web prefix that the gRPC-Web handler answers itself and so never reach
// the gRPC interceptor: each is charged exactly once by the HTTP limiter --
// a burst of three admits the three of them once and refuses them all the
// second time -- and a real call past the burst is refused with them.
func TestClientLimit_RESTChargesGRPCWebItAnswers(t *testing.T) {
	backend := newGRPCBackend()
	h := clientLimitedREST(t, &dispatchAuthenticator{}, 3, nil, WithGRPCWeb(grpcweb.Config{Enabled: true}, backend))
	answered := map[string]func(*http.Request){
		"preflight":          func(r *http.Request) { r.Method = http.MethodOptions },
		"unsupported method": func(r *http.Request) { r.Method = http.MethodGet },
		"unsupported type":   func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
	}
	serve := func(name string, shape func(*http.Request)) int {
		req := grpcWebRequest(t, "")
		req.RemoteAddr = "192.0.2.1:40000"
		shape(req)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if len(backend.calls) != 0 {
			t.Fatalf("%s reached the gRPC backend", name)
		}
		return rec.Code
	}
	for name, shape := range answered {
		if code := serve(name, shape); code == http.StatusTooManyRequests {
			t.Fatalf("%s within the burst: status 429, want it admitted", name)
		}
	}
	for name, shape := range answered {
		if code := serve(name, shape); code != http.StatusTooManyRequests {
			t.Fatalf("%s past the burst: status %d, want 429", name, code)
		}
	}
	req := grpcWebRequest(t, "")
	req.RemoteAddr = "192.0.2.1:40000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests || len(backend.calls) != 0 {
		t.Fatalf("call past the HTTP burst: status %d, want 429 before the gRPC backend", rec.Code)
	}
}
