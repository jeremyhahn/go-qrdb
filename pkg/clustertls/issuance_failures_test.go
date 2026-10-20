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

package clustertls_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// invalidNodeURI embeds a null byte in the host, an ASCII control character
// url.Parse rejects.
const invalidNodeURI = "spiffe://\x00host/node/127.0.0.1:63001"

// testCA returns a freshly generated CA for issuance tests.
func testCA(t *testing.T) (certPEM, keyPEM []byte, cert *x509.Certificate, key *ecdsa.PrivateKey) {
	t.Helper()
	certPEM, keyPEM, cert, key, err := clustertls.GenerateCA(time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	return certPEM, keyPEM, cert, key
}

// TestIssuance_InvalidURIReturnsGenerateError verifies every issuance entry
// point that takes a node URI reports an unparseable one as *GenerateError
// rather than issuing a certificate without it.
func TestIssuance_InvalidURIReturnsGenerateError(t *testing.T) {
	t.Parallel()

	caCertPEM, caKeyPEM, caCert, caKey := testCA(t)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	tests := []struct {
		name string
		call func() error
	}{
		{"IdentityLeafTemplate", func() error {
			_, err := clustertls.IdentityLeafTemplate(time.Hour, invalidNodeURI)
			return err
		}},
		{"NodeLeafTemplate", func() error {
			_, err := clustertls.NodeLeafTemplate(time.Hour, invalidNodeURI)
			return err
		}},
		{"GenerateNodeCert", func() error {
			_, _, err := clustertls.GenerateNodeCert(caCert, caKey, time.Hour, invalidNodeURI)
			return err
		}},
		{"IssueLeafForKey", func() error {
			_, err := clustertls.IssueLeafForKey(caCertPEM, caKeyPEM, &leafKey.PublicKey, invalidNodeURI)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.call()
			var genErr *clustertls.GenerateError
			if !errors.As(err, &genErr) {
				t.Fatalf("err = %T %v, want *clustertls.GenerateError", err, err)
			}
		})
	}
}

// TestIdentityLeafTemplate_NonNodeURINoAddressSAN verifies a URI that is not a
// SPIFFE node ID still lands in the URI SAN but contributes no DNS or IP SAN:
// only a node identity names an address.
func TestIdentityLeafTemplate_NonNodeURINoAddressSAN(t *testing.T) {
	t.Parallel()

	for _, uri := range []string{
		"https://db.example.com/node/10.0.0.2:9000",
		"spiffe://qrdb.cluster/workload/api",
	} {
		t.Run(uri, func(t *testing.T) {
			t.Parallel()
			tmpl, err := clustertls.IdentityLeafTemplate(time.Hour, uri)
			if err != nil {
				t.Fatalf("IdentityLeafTemplate: %v", err)
			}
			if len(tmpl.DNSNames) != 0 || len(tmpl.IPAddresses) != 0 {
				t.Fatalf("DNSNames = %v, IPAddresses = %v, want none", tmpl.DNSNames, tmpl.IPAddresses)
			}
			if len(tmpl.URIs) != 1 || tmpl.URIs[0].String() != uri {
				t.Fatalf("URIs = %v, want exactly [%s]", tmpl.URIs, uri)
			}
		})
	}
}

// TestIssuance_MismatchedCAKeyReturnsGenerateError verifies a signing key that
// does not belong to the CA certificate is refused with *GenerateError: no
// leaf is issued that would fail verification against the CA it names.
func TestIssuance_MismatchedCAKeyReturnsGenerateError(t *testing.T) {
	t.Parallel()

	_, _, caCert, _ := testCA(t)
	_, _, _, otherKey := testCA(t)

	tests := []struct {
		name string
		call func() error
	}{
		{"GenerateNodeCert", func() error {
			_, _, err := clustertls.GenerateNodeCert(caCert, otherKey, time.Hour)
			return err
		}},
		{"GenerateClientCert", func() error {
			_, _, err := clustertls.GenerateClientCert(caCert, otherKey, "db.example.com", "tenant-a", "client", time.Hour)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.call()
			var genErr *clustertls.GenerateError
			if !errors.As(err, &genErr) {
				t.Fatalf("err = %T %v, want *clustertls.GenerateError", err, err)
			}
		})
	}
}

// TestIssueLeafForKey_UnsupportedPublicKeyReturnsGenerateError verifies a
// public key x509 cannot encode is reported as *GenerateError.
func TestIssueLeafForKey_UnsupportedPublicKeyReturnsGenerateError(t *testing.T) {
	t.Parallel()

	caCertPEM, caKeyPEM, _, _ := testCA(t)
	for name, pub := range map[string]any{
		"nil":    nil,
		"string": "not a public key",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			certPEM, err := clustertls.IssueLeafForKey(caCertPEM, caKeyPEM, pub)
			var genErr *clustertls.GenerateError
			if !errors.As(err, &genErr) {
				t.Fatalf("err = %T %v, want *clustertls.GenerateError", err, err)
			}
			if certPEM != nil {
				t.Fatalf("certPEM = %q, want nil on error", certPEM)
			}
		})
	}
}
