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
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterca"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// clusterJoinTestBarrier is a trivial in-memory seal.EncryptionBarrier
// (no actual encryption -- this is a unit test of the REST binding, not of
// cakey.SoftwareProvider, which already has its own encrypted-round-trip
// coverage in pkg/cakey).
type clusterJoinTestBarrier struct{}

func (clusterJoinTestBarrier) Encrypt(p []byte) ([]byte, error) { return p, nil }
func (clusterJoinTestBarrier) Decrypt(c []byte) ([]byte, error) { return c, nil }
func (clusterJoinTestBarrier) IsSealed() bool                   { return false }

// newTestClusterCAProvider returns an initialized cluster-CA provider for
// the cluster-join REST tests.
func newTestClusterCAProvider(t *testing.T) *cakey.SoftwareProvider {
	t.Helper()
	p := clusterca.NewProvider(kvstore.NewMemoryKVStore(), clusterJoinTestBarrier{})
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize cluster CA: %v", err)
	}
	return p
}

// newTestJoinCSR builds a self-signed CSR for a fresh ECDSA P-256 key pair,
// for submitting to /v1/enroll/join.
func newTestJoinCSR(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatalf("CreateCertificateRequest: %v", err)
	}
	return der
}

func postClusterJoin(srv *Server, req clusterenroll.JoinRequest) *httptest.ResponseRecorder {
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, httptest.NewRequest(http.MethodPost, clusterenroll.JoinHTTPPath, bytes.NewReader(body)))
	return rec
}

func TestClusterJoin_SignsAValidCSR(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithClusterJoin(provider, "s3cret-token"))

	csrDER := newTestJoinCSR(t)
	rec := postClusterJoin(srv, clusterenroll.JoinRequest{Token: "s3cret-token", CSRDER: csrDER})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var resp clusterenroll.JoinResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("response carries an error: %s", resp.Error)
	}
	block, _ := pem.Decode(resp.CertPEM)
	if block == nil {
		t.Fatal("response has no leaf certificate PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	caCert, err := provider.Certificate(context.Background())
	if err != nil {
		t.Fatalf("provider.Certificate: %v", err)
	}
	if err := leaf.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("issued leaf does not chain to the cluster CA: %v", err)
	}
}

func TestClusterJoin_WrongTokenRefused(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithClusterJoin(provider, "correct-token"))

	rec := postClusterJoin(srv, clusterenroll.JoinRequest{Token: "wrong-token", CSRDER: newTestJoinCSR(t)})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body.String())
	}
	var resp clusterenroll.JoinResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == "" {
		t.Fatal("response carries no error for a rejected token")
	}
}

func TestClusterJoin_InvalidCSRRefused(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithClusterJoin(provider, "tok"))

	rec := postClusterJoin(srv, clusterenroll.JoinRequest{Token: "tok", CSRDER: []byte("not a csr")})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

func TestClusterJoin_MalformedBody(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithClusterJoin(provider, "tok"))

	rec := httptest.NewRecorder()
	srv.dispatch(rec, httptest.NewRequest(http.MethodPost, clusterenroll.JoinHTTPPath, bytes.NewReader([]byte("not json"))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// Unconfigured: the route is not registered at all, matching
// /v1/ca/bootstrap's own "leave the path free" rule.
func TestClusterJoin_UnconfiguredLeavesThePathFree(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	rec := postClusterJoin(srv, clusterenroll.JoinRequest{Token: "tok", CSRDER: newTestJoinCSR(t)})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured: status %d, want 404", rec.Code)
	}
}

// A provider with no token (or vice versa) is refused at Start, not served
// with no admission control.
func TestClusterJoin_MissingTokenRefusedAtStart(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithClusterJoin(provider, ""))
	var missingToken *ClusterJoinMissingTokenError
	if err := srv.Start(); !errors.As(err, &missingToken) {
		srv.Stop()
		t.Fatalf("Start err = %T (%v), want *ClusterJoinMissingTokenError", err, err)
	}
}
