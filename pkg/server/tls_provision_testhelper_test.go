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
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// testServerCA is a self-signed CeremonyCA shared across the orchestrator
// tests. Because qrdb transports are TLS-only, the start paths auto-provision
// a server certificate from the node's CeremonyCA when no explicit TLS config
// is supplied; tests that start a protocol without WithTLS pass this CA via
// WithCeremonyCA so the listener comes up with an auto-provisioned TLS 1.3
// certificate instead of failing closed. It is built once at package init; a
// failure here is a programming error, so mustTestCeremonyCA panics.
var testServerCA = mustTestCeremonyCA()

// mustTestCeremonyCA generates a fresh CA and wraps it in a ceremony.LocalCA.
func mustTestCeremonyCA() ceremony.CeremonyCA {
	caCertPEM, caKeyPEM, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		panic("server test CA: generate CA: " + err.Error())
	}
	ca, err := ceremony.NewLocalCA(caCertPEM, caKeyPEM)
	if err != nil {
		panic("server test CA: new local CA: " + err.Error())
	}
	return ca
}

// testServerCAPool returns an x509 pool trusting testServerCA, so a client can
// verify a certificate auto-provisioned from it.
func testServerCAPool(t *testing.T) *x509.CertPool {
	t.Helper()
	caPEM, err := testServerCA.CABundle()
	if err != nil {
		t.Fatalf("server test CA bundle: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("server test CA bundle: no certificate appended to pool")
	}
	return pool
}

// testAutoProvisionClientTLSOption returns the SDK transport option a client
// uses to trust a server certificate auto-provisioned from testServerCA,
// verifying it under the "localhost" SAN.
func testAutoProvisionClientTLSOption(t *testing.T) transport.Option {
	t.Helper()
	return transport.WithTLSConfig(&tls.Config{
		RootCAs:    testServerCAPool(t),
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	})
}
