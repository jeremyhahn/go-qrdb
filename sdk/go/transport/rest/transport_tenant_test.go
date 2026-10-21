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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/barrierinit"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Per-tenant barrier server stub
// ---------------------------------------------------------------------------

// tenantBarrierServer is an in-memory implementation of the four per-tenant
// barrier REST routes. It tracks the captured request payloads so the round
// trip tests can assert that the SDK transport encoded the request correctly.
type tenantBarrierServer struct {
	gotInitTenant    atomic.Value // string
	gotInitThreshold atomic.Int64
	gotInitShares    atomic.Int64
	gotInitStatement atomic.Value // []byte
	gotUnsealTenant  atomic.Value // string
	gotUnsealKey     atomic.Value // []byte
	gotSealTenant    atomic.Value // string
	gotStatusTenant  atomic.Value // string
	failNext         atomic.Bool
	notFoundTenant   atomic.Value // string
}

func (s *tenantBarrierServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/barrier/init/begin") && r.Method == http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			if r.TLS == nil {
				_, _ = w.Write([]byte(`{}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"nonce": proofNonce, "expires": 1})
		case strings.HasSuffix(r.URL.Path, "/barrier/init/complete") && r.Method == http.MethodPost:
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tenants/"), "/barrier/init/complete")
			if s.matchNotFound(id) {
				http.Error(w, "tenant not found", http.StatusNotFound)
				return
			}
			s.gotInitTenant.Store(id)
			var req struct {
				Threshold int    `json:"threshold,omitempty"`
				Shares    int    `json:"shares,omitempty"`
				Nonce     []byte `json:"nonce,omitempty"`
				PublicKey []byte `json:"public_key,omitempty"`
				Signature []byte `json:"signature,omitempty"`
				Statement []byte `json:"init_statement,omitempty"`
			}
			if r.ContentLength > 0 {
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			}
			if r.TLS != nil && !verifyProofOn(r.TLS, id, req.Nonce, req.PublicKey, req.Signature) {
				http.Error(w, "proof of possession refused", http.StatusUnauthorized)
				return
			}
			s.gotInitThreshold.Store(int64(req.Threshold))
			s.gotInitShares.Store(int64(req.Shares))
			s.gotInitStatement.Store(req.Statement)
			if s.failNext.Swap(false) {
				http.Error(w, "boom", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/barrier/unseal") && r.Method == http.MethodPost:
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tenants/"), "/barrier/unseal")
			if s.matchNotFound(id) {
				http.Error(w, "tenant not found", http.StatusNotFound)
				return
			}
			s.gotUnsealTenant.Store(id)
			var req struct {
				Key []byte `json:"key,omitempty"`
			}
			if r.ContentLength > 0 {
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			}
			s.gotUnsealKey.Store(append([]byte(nil), req.Key...))
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/barrier/seal") && r.Method == http.MethodPost:
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tenants/"), "/barrier/seal")
			if s.matchNotFound(id) {
				http.Error(w, "tenant not found", http.StatusNotFound)
				return
			}
			s.gotSealTenant.Store(id)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/barrier/status") && r.Method == http.MethodGet:
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tenants/"), "/barrier/status")
			if s.matchNotFound(id) {
				http.Error(w, "tenant not found", http.StatusNotFound)
				return
			}
			s.gotStatusTenant.Store(id)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tenant_id":       id,
				"sealed":          false,
				"initialized":     true,
				"strategy":        "passphrase",
				"hardware_backed": false,
			})
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

func (s *tenantBarrierServer) matchNotFound(id string) bool {
	if v, ok := s.notFoundTenant.Load().(string); ok && v != "" && v == id {
		return true
	}
	return false
}

func (s *tenantBarrierServer) seenInitTenant() string {
	if v, ok := s.gotInitTenant.Load().(string); ok {
		return v
	}
	return ""
}

func (s *tenantBarrierServer) seenUnsealTenant() string {
	if v, ok := s.gotUnsealTenant.Load().(string); ok {
		return v
	}
	return ""
}

func (s *tenantBarrierServer) seenUnsealKey() []byte {
	if v, ok := s.gotUnsealKey.Load().([]byte); ok {
		return v
	}
	return nil
}

func (s *tenantBarrierServer) seenSealTenant() string {
	if v, ok := s.gotSealTenant.Load().(string); ok {
		return v
	}
	return ""
}

func (s *tenantBarrierServer) seenStatusTenant() string {
	if v, ok := s.gotStatusTenant.Load().(string); ok {
		return v
	}
	return ""
}

func newTenantBarrierTestTransport(t *testing.T) (*Transport, *tenantBarrierServer, *httptest.Server) {
	t.Helper()
	srv := &tenantBarrierServer{}
	httpSrv := httptest.NewServer(srv.handler())
	t.Cleanup(httpSrv.Close)
	rt := newWiredTransport(httpSrv)
	return rt, srv, httpSrv
}

// proofNonce is the challenge the stub's init/begin issues over TLS.
var proofNonce = bytes.Repeat([]byte{0x5a}, initproof.NonceSize)

// verifyProofOn reports whether signature proves possession of publicKey's
// private key for tenantID and proofNonce over the connection whose
// server-side TLS state is cs -- the check the node makes.
func verifyProofOn(cs *tls.ConnectionState, tenantID string, nonce, publicKey, signature []byte) bool {
	if !bytes.Equal(nonce, proofNonce) {
		return false
	}
	pub, err := x509.ParsePKIXPublicKey(publicKey)
	if err != nil {
		return false
	}
	exporter, err := initproof.Exporter(cs)
	if err != nil {
		return false
	}
	return initproof.Verify(pub, tenantID, nonce, exporter, signature)
}

// newTenantBarrierTLSTransport serves the stub over TLS 1.3 and gives the
// transport the stub client's connection pool, the shape ensureClient builds,
// so init runs on a pinned clone of it.
func newTenantBarrierTLSTransport(t *testing.T) (*Transport, *tenantBarrierServer) {
	t.Helper()
	srv := &tenantBarrierServer{}
	httpSrv := httptest.NewTLSServer(srv.handler())
	t.Cleanup(httpSrv.Close)
	rt := newWiredTransport(httpSrv)
	pool, ok := httpSrv.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("httptest client transport is %T; want *http.Transport", httpSrv.Client().Transport)
	}
	rt.httpTransport = pool
	return rt, srv
}

// ---------------------------------------------------------------------------
// TenantBarrierInit
// ---------------------------------------------------------------------------

func TestTenantBarrierInit_Success(t *testing.T) {
	rt, srv, _ := newTenantBarrierTestTransport(t)

	_, err := rt.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{
		TenantID:  "acme",
		Threshold: 3,
		Shares:    5,
	})
	if err != nil {
		t.Fatalf("TenantBarrierInit: unexpected error: %v", err)
	}

	if got := srv.seenInitTenant(); got != "acme" {
		t.Fatalf("server saw tenantID = %q, want %q", got, "acme")
	}
	if got := srv.gotInitThreshold.Load(); got != 3 {
		t.Fatalf("server saw threshold = %d, want 3", got)
	}
	if got := srv.gotInitShares.Load(); got != 5 {
		t.Fatalf("server saw shares = %d, want 5", got)
	}
}

// TestTenantBarrierInit_ProofBoundToTheConnection runs begin and complete
// over TLS 1.3: the stub verifies the signature against the exporter of the
// connection complete arrived on, so it passes only when the pinned client
// kept both calls on one connection and signed that connection's exporter.
func TestTenantBarrierInit_ProofBoundToTheConnection(t *testing.T) {
	rt, srv := newTenantBarrierTLSTransport(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	if _, err := rt.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{
		TenantID: "acme", Signer: key, Strategy: "none",
	}); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}
	if got := srv.seenInitTenant(); got != "acme" {
		t.Fatalf("server saw tenantID = %q, want acme", got)
	}

	// The same key signed acme's epoch-1 statement, which every replica
	// verifies against the pinned first admin before initializing its key.
	raw, _ := srv.gotInitStatement.Load().([]byte)
	statement, err := barrierinit.Parse(raw)
	if err != nil {
		t.Fatalf("init_statement: %v", err)
	}
	thumbprint, err := user.PublicKeySPKIThumbprint(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	if statement.Barrier != clusterseal.TenantBarrier("acme") || statement.StrategyID != "none" {
		t.Fatalf("init_statement = %+v, want acme's none statement", statement)
	}
	if err := barrierinit.Verify(statement, barrierinit.Anchors{FirstAdmin: thumbprint}); err != nil {
		t.Fatalf("init_statement does not verify against the first admin: %v", err)
	}
}

// TestTenantBarrierInit_SignerRequired: a node that asks a proof is answered
// with *transport.InitSignerRequiredError, and complete is never sent.
func TestTenantBarrierInit_SignerRequired(t *testing.T) {
	rt, srv := newTenantBarrierTLSTransport(t)

	_, err := rt.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "acme"})
	var need *transport.InitSignerRequiredError
	if !errors.As(err, &need) {
		t.Fatalf("err = %T (%v); want *transport.InitSignerRequiredError", err, err)
	}
	if got := srv.seenInitTenant(); got != "" {
		t.Fatalf("complete reached the server for %q without a proof", got)
	}
}

func TestInitProofClient_PinsOneConnection(t *testing.T) {
	rt, _ := newTenantBarrierTLSTransport(t)

	client, release := rt.initProofClient()
	defer release()
	if client == rt.client {
		t.Fatal("init must not share the pooled client")
	}
	if rt.httpTransport.MaxConnsPerHost == 1 {
		t.Fatal("pinning must clone the pool, not limit the shared one")
	}
}

func TestInitProofClient_CallerSuppliedClientUsedAsIs(t *testing.T) {
	rt, _, _ := newTenantBarrierTestTransport(t)

	client, release := rt.initProofClient()
	defer release()
	if client != rt.client {
		t.Fatal("without a pool of its own the transport's client is used as is")
	}
}

func TestTenantBarrierInit_NilRequest(t *testing.T) {
	rt, _, _ := newTenantBarrierTestTransport(t)

	_, err := rt.TenantBarrierInit(context.Background(), nil)
	if err == nil {
		t.Fatal("TenantBarrierInit(nil) returned nil error, want non-nil")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T", err)
	}
}

func TestTenantBarrierInit_EmptyTenantID(t *testing.T) {
	rt, _, _ := newTenantBarrierTestTransport(t)

	_, err := rt.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{})
	if err == nil {
		t.Fatal("TenantBarrierInit with empty TenantID returned nil error")
	}
	if !strings.Contains(err.Error(), "tenant id must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTenantBarrierInit_NotFound(t *testing.T) {
	rt, srv, _ := newTenantBarrierTestTransport(t)
	srv.notFoundTenant.Store("ghost")

	_, err := rt.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "ghost"})
	if err == nil {
		t.Fatal("expected error from 404 response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T", err)
	}
	if reqErr.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", reqErr.StatusCode, http.StatusNotFound)
	}
}

func TestTenantBarrierInit_AfterClose(t *testing.T) {
	rt, _, _ := newTenantBarrierTestTransport(t)
	_ = rt.Close()

	_, err := rt.TenantBarrierInit(context.Background(), &transport.TenantBarrierInitRequest{TenantID: "acme"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TenantBarrierUnseal
// ---------------------------------------------------------------------------

func TestTenantBarrierUnseal_Success(t *testing.T) {
	rt, srv, _ := newTenantBarrierTestTransport(t)

	want := []byte("super-secret-bytes")
	err := rt.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{
		TenantID: "acme",
		Key:      want,
	})
	if err != nil {
		t.Fatalf("TenantBarrierUnseal: unexpected error: %v", err)
	}
	if got := srv.seenUnsealTenant(); got != "acme" {
		t.Fatalf("server saw tenantID = %q, want %q", got, "acme")
	}
	if got := srv.seenUnsealKey(); !bytes.Equal(got, want) {
		t.Fatalf("server saw key = %q, want %q", got, want)
	}
}

func TestTenantBarrierUnseal_EmptyKeyOK(t *testing.T) {
	rt, srv, _ := newTenantBarrierTestTransport(t)

	err := rt.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{
		TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("TenantBarrierUnseal with empty key: %v", err)
	}
	if got := srv.seenUnsealKey(); len(got) != 0 {
		t.Fatalf("server saw key %q, want empty", got)
	}
}

func TestTenantBarrierUnseal_NilRequest(t *testing.T) {
	rt, _, _ := newTenantBarrierTestTransport(t)

	if err := rt.TenantBarrierUnseal(context.Background(), nil); err == nil {
		t.Fatal("expected non-nil error")
	}
}

func TestTenantBarrierUnseal_EmptyTenantID(t *testing.T) {
	rt, _, _ := newTenantBarrierTestTransport(t)

	if err := rt.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{}); err == nil {
		t.Fatal("expected non-nil error")
	}
}

func TestTenantBarrierUnseal_NotFound(t *testing.T) {
	rt, srv, _ := newTenantBarrierTestTransport(t)
	srv.notFoundTenant.Store("ghost")

	err := rt.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{TenantID: "ghost"})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 RequestError, got %v", err)
	}
}

func TestTenantBarrierUnseal_AfterClose(t *testing.T) {
	rt, _, _ := newTenantBarrierTestTransport(t)
	_ = rt.Close()

	err := rt.TenantBarrierUnseal(context.Background(), &transport.TenantBarrierUnsealRequest{TenantID: "acme"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TenantBarrierSeal
// ---------------------------------------------------------------------------

func TestTenantBarrierSeal_Success(t *testing.T) {
	rt, srv, _ := newTenantBarrierTestTransport(t)

	err := rt.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("TenantBarrierSeal: unexpected error: %v", err)
	}
	if got := srv.seenSealTenant(); got != "acme" {
		t.Fatalf("server saw tenantID = %q, want %q", got, "acme")
	}
}

func TestTenantBarrierSeal_NilRequest(t *testing.T) {
	rt, _, _ := newTenantBarrierTestTransport(t)

	if err := rt.TenantBarrierSeal(context.Background(), nil); err == nil {
		t.Fatal("expected non-nil error")
	}
}

func TestTenantBarrierSeal_EmptyTenantID(t *testing.T) {
	rt, _, _ := newTenantBarrierTestTransport(t)

	if err := rt.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{}); err == nil {
		t.Fatal("expected non-nil error")
	}
}

func TestTenantBarrierSeal_NotFound(t *testing.T) {
	rt, srv, _ := newTenantBarrierTestTransport(t)
	srv.notFoundTenant.Store("ghost")

	err := rt.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{TenantID: "ghost"})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 RequestError, got %v", err)
	}
}

func TestTenantBarrierSeal_AfterClose(t *testing.T) {
	rt, _, _ := newTenantBarrierTestTransport(t)
	_ = rt.Close()

	err := rt.TenantBarrierSeal(context.Background(), &transport.TenantBarrierSealRequest{TenantID: "acme"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TenantBarrierStatus
// ---------------------------------------------------------------------------

func TestTenantBarrierStatus_Success(t *testing.T) {
	rt, srv, _ := newTenantBarrierTestTransport(t)

	resp, err := rt.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("TenantBarrierStatus: unexpected error: %v", err)
	}
	if got := srv.seenStatusTenant(); got != "acme" {
		t.Fatalf("server saw tenantID = %q, want %q", got, "acme")
	}
	if resp.TenantID != "acme" {
		t.Fatalf("resp.TenantID = %q, want %q", resp.TenantID, "acme")
	}
	if resp.Sealed {
		t.Fatal("resp.Sealed = true, want false")
	}
	if !resp.Initialized {
		t.Fatal("resp.Initialized = false, want true")
	}
	if resp.Strategy != "passphrase" {
		t.Fatalf("resp.Strategy = %q, want %q", resp.Strategy, "passphrase")
	}
}

func TestTenantBarrierStatus_FillsTenantIDFromPath(t *testing.T) {
	// Server intentionally returns an empty tenant_id; transport must
	// fall back to the request value.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tenant_id":       "",
			"sealed":          true,
			"initialized":     false,
			"strategy":        "",
			"hardware_backed": false,
		})
	}))
	t.Cleanup(srv.Close)

	rt := newWiredTransport(srv)
	resp, err := rt.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if resp.TenantID != "acme" {
		t.Fatalf("TenantID = %q, want %q (fallback to request value)", resp.TenantID, "acme")
	}
}

func TestTenantBarrierStatus_DecodesKeyFingerprint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tenant_id":       "acme",
			"initialized":     true,
			"strategy":        "passphrase",
			"key_fingerprint": "0a1b2c",
		})
	}))
	t.Cleanup(srv.Close)

	rt := newWiredTransport(srv)
	resp, err := rt.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if resp.KeyFingerprint != "0a1b2c" {
		t.Fatalf("KeyFingerprint = %q, want 0a1b2c", resp.KeyFingerprint)
	}
}

// TestTenantBarrierStatus_DecodesServingNodes pins that the nodes a barrier
// init fans out to come through with their endpoints, and that a standalone
// node's status, which lists none, decodes to none.
func TestTenantBarrierStatus_DecodesServingNodes(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	rt := newWiredTransport(srv)

	body = map[string]any{
		"tenant_id": "acme",
		"nodes": []map[string]any{
			{"node": "n1:7000", "endpoints": map[string]string{"rest": "n1:8443", "grpc": "n1:9443"}},
			{"node": "n2:7000"},
		},
	}
	resp, err := rt.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	want := []transport.TenantBarrierNode{
		{Node: "n1:7000", Endpoints: map[string]string{"rest": "n1:8443", "grpc": "n1:9443"}},
		{Node: "n2:7000"},
	}
	if !reflect.DeepEqual(resp.Nodes, want) {
		t.Fatalf("Nodes = %+v, want %+v", resp.Nodes, want)
	}

	body = map[string]any{"tenant_id": "acme"}
	resp, err = rt.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "acme"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if resp.Nodes != nil {
		t.Fatalf("standalone Nodes = %+v, want none", resp.Nodes)
	}
}

func TestTenantBarrierStatus_NilRequest(t *testing.T) {
	rt, _, _ := newTenantBarrierTestTransport(t)

	if _, err := rt.TenantBarrierStatus(context.Background(), nil); err == nil {
		t.Fatal("expected non-nil error")
	}
}

func TestTenantBarrierStatus_EmptyTenantID(t *testing.T) {
	rt, _, _ := newTenantBarrierTestTransport(t)

	if _, err := rt.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{}); err == nil {
		t.Fatal("expected non-nil error")
	}
}

func TestTenantBarrierStatus_NotFound(t *testing.T) {
	rt, srv, _ := newTenantBarrierTestTransport(t)
	srv.notFoundTenant.Store("ghost")

	_, err := rt.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "ghost"})
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 RequestError, got %v", err)
	}
}

func TestTenantBarrierStatus_AfterClose(t *testing.T) {
	rt, _, _ := newTenantBarrierTestTransport(t)
	_ = rt.Close()

	_, err := rt.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "acme"})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %v", err)
	}
}

func TestTenantBarrierStatus_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "not-json")
	}))
	t.Cleanup(srv.Close)

	rt := newWiredTransport(srv)
	_, err := rt.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "acme"})
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

// TestRestTenant_ToTenantInfo_CarriesRefusal proves the REST client's wire
// decode does not drop a tenant's refusal: before this fix, restTenant had
// no Refusal field at all, so a server response carrying
// {"refusal":{"code":"pool_unavailable",...}} silently decoded to a
// TenantInfo with Refusal == nil.
func TestRestTenant_ToTenantInfo_CarriesRefusal(t *testing.T) {
	rt := restTenant{
		ID:   "bad",
		Name: "bad",
		Refusal: &transport.TenantRefusal{
			Code:    "pool_unavailable",
			Message: "volume: pool \"broken\" is unavailable on node \"1\"",
		},
	}
	info := rt.toTenantInfo()
	if info.Refusal == nil {
		t.Fatal("toTenantInfo() dropped the refusal")
	}
	if info.Refusal.Code != "pool_unavailable" {
		t.Fatalf("Refusal.Code = %q, want %q", info.Refusal.Code, "pool_unavailable")
	}
}

func TestRestTenant_ToTenantInfo_CarriesStatus(t *testing.T) {
	if got := (&restTenant{ID: "acme", Status: transport.TenantStatusSuspended}).toTenantInfo().Status; got != transport.TenantStatusSuspended {
		t.Fatalf("Status = %q; want suspended", got)
	}
	if got := (&restTenant{ID: "acme"}).toTenantInfo().Status; got != "" {
		t.Fatalf("Status = %q; want empty", got)
	}
}

// TestRestTenant_ToTenantInfo_NoRefusalStaysNil proves a served tenant
// (no refusal on the wire) still decodes with a nil Refusal.
func TestRestTenant_ToTenantInfo_NoRefusalStaysNil(t *testing.T) {
	rt := restTenant{ID: "good", Name: "good"}
	if info := rt.toTenantInfo(); info.Refusal != nil {
		t.Fatalf("Refusal = %+v, want nil", info.Refusal)
	}
}
