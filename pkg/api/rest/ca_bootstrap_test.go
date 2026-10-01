// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package rest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
)

// bundleWithKey returns a CA certificate PEM followed by its private key, the
// shape a careless provider might hand back.
func bundleWithKey(t *testing.T) (certPEM, both []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "host-ca"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return certPEM, append(append([]byte{}, certPEM...), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})...)
}

func getBootstrap(srv *Server) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.dispatch(rec, httptest.NewRequest(http.MethodGet, "/v1/ca/bootstrap", nil))
	return rec
}

// The host's bundle is what the route serves, read per request, reduced to its
// certificates whatever the provider returned.
func TestCABootstrapProvider_ServesTheHostBundleCertificatesOnly(t *testing.T) {
	certPEM, both := bundleWithKey(t)
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCABootstrapProvider(func(context.Context) ([]byte, error) { return both, nil }))
	rec := getBootstrap(srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(certPEM) {
		t.Fatalf("served %q, want exactly the certificate", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "PRIVATE KEY") {
		t.Fatal("the bootstrap route published a private key")
	}
}

// A provider that cannot produce the bundle answers 503, not an empty 200.
func TestCABootstrapProvider_ErrorIsUnavailable(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCABootstrapProvider(func(context.Context) ([]byte, error) { return nil, errors.New("bundle store down") }))
	if rec := getBootstrap(srv); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

// With no source configured the path is not registered: it answers 404, and a
// module that serves its own bundle there is not a collision (the boot panic
// go-xkms's xkmsd hit on v0.25.0, where the path was claimed unconditionally).
func TestCABootstrap_UnconfiguredLeavesThePathFree(t *testing.T) {
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0")
	if rec := getBootstrap(srv); rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured: status %d, want 404", rec.Code)
	}
	mod := &httpModule{name: "host-trust", register: func(r *apimodule.HTTPRegistrar) {
		r.HandlePublic(http.MethodGet, "/v1/ca/bootstrap", noopHandler)
	}}
	if got := constructPanic(t, mod); got != nil {
		t.Fatalf("a module serving /v1/ca/bootstrap on an unconfigured server panicked: %v", got)
	}
}

// Two sources for the one route are refused at Start with the typed error,
// and neither is served.
func TestCABootstrap_TwoSourcesAreRefused(t *testing.T) {
	certPEM, _ := bundleWithKey(t)
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCABootstrap(certPEM),
		WithCABootstrapProvider(func(context.Context) ([]byte, error) { return certPEM, nil }))
	var conflict *CABootstrapConflictError
	if err := srv.Start(); !errors.As(err, &conflict) {
		srv.Stop()
		t.Fatalf("Start err = %T (%v), want *CABootstrapConflictError", err, err)
	}
	if rec := getBootstrap(srv); rec.Code != http.StatusNotFound {
		t.Fatalf("a conflicted route was served: %d", rec.Code)
	}
}
