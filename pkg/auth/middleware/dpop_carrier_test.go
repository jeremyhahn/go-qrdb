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
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// dpopCapture records the DPoP metadata its Authenticate call saw.
type dpopCapture struct {
	got auth.DPoPRequest
	ok  bool
}

func (d *dpopCapture) Authenticate(ctx context.Context) (*auth.Identity, error) {
	d.got, d.ok = auth.DPoPRequestFromContext(ctx)
	return &auth.Identity{Subject: "dpop-alice", TenantID: "t1"}, nil
}

func (d *dpopCapture) Name() string { return "dpop-capture-stub" }

// TestRESTMiddleware_DPoPReachesAuthenticators verifies the proof, method and
// target URI reach an authenticator on REST.
func TestRESTMiddleware_DPoPReachesAuthenticators(t *testing.T) {
	cap := &dpopCapture{}
	m := New(&mockAuthorizer{}, WithAuthenticator(cap))
	handler := m.RESTMiddleware(bearerCarrierRoutes(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "https://db.example.com/status?q=1", nil)
	req.TLS = &tls.ConnectionState{}
	req.Header.Set("DPoP", "proof-1")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	want := auth.DPoPRequest{Proof: "proof-1", Method: "GET", URI: "https://db.example.com/status"}
	if !cap.ok || cap.got != want {
		t.Fatalf("authenticator saw %+v, %v; want %+v", cap.got, cap.ok, want)
	}
}

// TestRESTMiddleware_NoDPoPHeaderCarriesNone verifies absence is reported.
func TestRESTMiddleware_NoDPoPHeaderCarriesNone(t *testing.T) {
	cap := &dpopCapture{}
	m := New(&mockAuthorizer{}, WithAuthenticator(cap))
	handler := m.RESTMiddleware(bearerCarrierRoutes(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/status", nil))
	if cap.ok {
		t.Fatalf("authenticator saw DPoP %+v on a request without one", cap.got)
	}
}

// TestInjectDPoP_GRPCMetadata verifies the gRPC carrier: POST, the authority
// plus full method, https under TLS.
func TestInjectDPoP_GRPCMetadata(t *testing.T) {
	md := metadata.Pairs("dpop", "proof-g", ":authority", "db.example.com:8443")
	base := metadata.NewIncomingContext(context.Background(), md)

	plain, ok := auth.DPoPRequestFromContext(injectDPoP(base, "/qrdb.v1.KV/Get"))
	want := auth.DPoPRequest{Proof: "proof-g", Method: "POST", URI: "http://db.example.com:8443/qrdb.v1.KV/Get"}
	if !ok || plain != want {
		t.Fatalf("plaintext: got %+v, %v; want %+v", plain, ok, want)
	}

	tlsCtx := injectTLSState(peer.NewContext(base, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{}}}))
	secure, ok := auth.DPoPRequestFromContext(injectDPoP(tlsCtx, "/qrdb.v1.KV/Get"))
	if !ok || secure.URI != "https://db.example.com:8443/qrdb.v1.KV/Get" {
		t.Fatalf("tls: got %+v, %v", secure, ok)
	}
}

// TestInjectDPoP_AbsentMetadataStoresNothing verifies a call without "dpop"
// metadata carries nothing.
func TestInjectDPoP_AbsentMetadataStoresNothing(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no metadata":    context.Background(),
		"other metadata": metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer x")),
	} {
		if _, ok := auth.DPoPRequestFromContext(injectDPoP(ctx, "/qrdb.v1.KV/Get")); ok {
			t.Errorf("%s: DPoP carried without a proof", name)
		}
	}
}
