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

package ceremony

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
)

// TestServerTLSConfig_ProvisionsTLS13LeafChainedToCA verifies the happy path:
// the returned config is pinned to TLS 1.3, carries exactly one leaf, and that
// leaf is signed by the CA and covers localhost plus any extra SAN supplied.
func TestServerTLSConfig_ProvisionsTLS13LeafChainedToCA(t *testing.T) {
	ca := newTestLocalCA(t)

	cfg, err := ServerTLSConfig(context.Background(), ca, "node.internal")
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %#x, want TLS 1.3", cfg.MinVersion)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("Certificates length = %d, want 1", len(cfg.Certificates))
	}

	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	caCert, err := ca.CACertificate()
	if err != nil {
		t.Fatalf("CA certificate: %v", err)
	}
	if err := leaf.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("leaf not signed by CA: %v", err)
	}

	if err := leaf.VerifyHostname("localhost"); err != nil {
		t.Errorf("leaf does not cover localhost: %v", err)
	}
	if err := leaf.VerifyHostname("node.internal"); err != nil {
		t.Errorf("leaf does not cover the extra SAN node.internal: %v", err)
	}
}

// TestServerTLSConfig_NilCA verifies a nil CA fails closed with ErrNilCA rather
// than returning a usable (unauthenticated) config.
func TestServerTLSConfig_NilCA(t *testing.T) {
	cfg, err := ServerTLSConfig(context.Background(), nil)
	if cfg != nil {
		t.Fatal("expected nil config when CA is nil")
	}
	if !errors.Is(err, ErrNilCA) {
		t.Fatalf("expected ErrNilCA, got %v", err)
	}
}

// TestServerTLSConfig_IssueFailureWrapped verifies an issuance failure surfaces
// as a typed *ServerTLSError.
func TestServerTLSConfig_IssueFailureWrapped(t *testing.T) {
	_, err := ServerTLSConfig(context.Background(), failingCA{})
	var provErr *ServerTLSError
	if !errors.As(err, &provErr) {
		t.Fatalf("expected *ServerTLSError, got %T: %v", err, err)
	}
}

// failingCA is a CeremonyCA whose IssueCertificate always fails, exercising the
// error-wrapping path of ServerTLSConfig.
type failingCA struct{}

func (failingCA) SignCSR(context.Context, []byte, *SignCSROptions) (*x509.Certificate, error) {
	return nil, errAlwaysFail
}
func (failingCA) CACertificate() (*x509.Certificate, error) { return nil, errAlwaysFail }
func (failingCA) CABundle() ([]byte, error)                 { return nil, errAlwaysFail }
func (failingCA) IssueCertificate(context.Context, *IssueCertRequest) (*IssuedCert, error) {
	return nil, errAlwaysFail
}

var errAlwaysFail = errors.New("ceremony: test issuance failure")
