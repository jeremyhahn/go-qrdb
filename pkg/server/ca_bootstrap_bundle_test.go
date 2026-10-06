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

package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
)

// testCABundlePEM returns the test CA's certificate as PEM.
func testCABundlePEM(t *testing.T) []byte {
	t.Helper()
	caCert, err := testServerCA.CACertificate(t.Context())
	if err != nil {
		t.Fatalf("CA certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})
}

// TestWithBootstrapCABundle_ServesOnlyTheHostsCertificates: a host-supplied
// first-contact bundle is read per request and served on /v1/ca/bootstrap
// with every non-CERTIFICATE block removed, so a host that hands over a PEM
// file which also carries a key never publishes that key to an
// unauthenticated caller.
func TestWithBootstrapCABundle_ServesOnlyTheHostsCertificates(t *testing.T) {
	certPEM := testCABundlePEM(t)
	keyBlock := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("must never be served")})
	bundle := func(context.Context) ([]byte, error) { return append(bytes.Clone(certPEM), keyBlock...), nil }

	srv := New(testConfig(t, true, false), newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA), WithBootstrapCABundle(bundle))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    testServerCAPool(t),
			ServerName: "localhost",
			MinVersion: tls.VersionTLS13,
		}},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + srv.RESTAddr() + "/v1/ca/bootstrap")
	if err != nil {
		t.Fatalf("GET /v1/ca/bootstrap: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %q; want 200", resp.StatusCode, body)
	}
	if bytes.Contains(body, []byte("PRIVATE KEY")) {
		t.Fatal("the bootstrap route served a non-certificate block from the host bundle")
	}
	if !bytes.Equal(bytes.TrimSpace(body), bytes.TrimSpace(certPEM)) {
		t.Fatalf("served bundle = %q, want exactly the host's certificate", body)
	}
}

// TestWithBootstrapCABundle_ConflictsWithTheClusterCABootstrap: the route has
// one owner and serves one bundle, so a server configured with both the
// cluster CA and a host bundle refuses to start rather than choosing.
func TestWithBootstrapCABundle_ConflictsWithTheClusterCABootstrap(t *testing.T) {
	certPEM := testCABundlePEM(t)
	srv := New(testConfig(t, true, false), newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA),
		WithCABootstrap(certPEM),
		WithBootstrapCABundle(func(context.Context) ([]byte, error) { return certPEM, nil }),
	)
	err := srv.Start()
	if err == nil {
		srv.Stop()
		t.Fatal("Start succeeded with two owners of /v1/ca/bootstrap")
	}
	if !errors.As(err, new(*rest.CABootstrapConflictError)) {
		t.Fatalf("Start error = %T (%v), want *rest.CABootstrapConflictError", err, err)
	}
}
