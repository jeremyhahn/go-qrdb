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

// SAN validation on the joiner's CSR: validateJoinCSRIdentity and its
// integration into HandleJoinRequest.
package clusterenroll

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"net"
	"net/url"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// buildCSRWithSANs builds a raw PKCS#10 request carrying exactly the SANs
// given, for testing validateJoinCSRIdentity's rejection of shapes
// buildJoinCSR itself never produces.
func buildCSRWithSANs(t *testing.T, uris []string, dnsNames []string, ips []string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.CertificateRequest{Subject: pkix.Name{CommonName: "qrdb-node"}}
	for _, raw := range uris {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", raw, err)
		}
		template.URIs = append(template.URIs, u)
	}
	template.DNSNames = dnsNames
	for _, raw := range ips {
		ip := net.ParseIP(raw)
		if ip == nil {
			t.Fatalf("net.ParseIP(%q) failed", raw)
		}
		template.IPAddresses = append(template.IPAddresses, ip)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		t.Fatalf("CreateCertificateRequest: %v", err)
	}
	return der
}

func parseCSRForTest(t *testing.T, der []byte) *x509.CertificateRequest {
	t.Helper()
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		t.Fatalf("ParseCertificateRequest: %v", err)
	}
	return csr
}

func TestValidateJoinCSRIdentity_EmptyClaimedIdentityRefused(t *testing.T) {
	der := buildCSRWithSANs(t, []string{"spiffe://test.cluster/node/node-1"}, nil, nil)
	err := validateJoinCSRIdentity(parseCSRForTest(t, der), "")
	var identErr *JoinCSRIdentityError
	if !errors.As(err, &identErr) {
		t.Fatalf("validateJoinCSRIdentity with an empty claimed identity = %v, want *JoinCSRIdentityError", err)
	}
}

func TestValidateJoinCSRIdentity_DNSNameRefused(t *testing.T) {
	claimed := "spiffe://test.cluster/node/node-1"
	der := buildCSRWithSANs(t, []string{claimed}, []string{"node-1.example.com"}, nil)
	err := validateJoinCSRIdentity(parseCSRForTest(t, der), claimed)
	var identErr *JoinCSRIdentityError
	if !errors.As(err, &identErr) {
		t.Fatalf("validateJoinCSRIdentity with a DNS SAN = %v, want *JoinCSRIdentityError", err)
	}
}

func TestValidateJoinCSRIdentity_IPAddressRefused(t *testing.T) {
	claimed := "spiffe://test.cluster/node/node-1"
	der := buildCSRWithSANs(t, []string{claimed}, nil, []string{"10.0.0.4"})
	err := validateJoinCSRIdentity(parseCSRForTest(t, der), claimed)
	var identErr *JoinCSRIdentityError
	if !errors.As(err, &identErr) {
		t.Fatalf("validateJoinCSRIdentity with an IP SAN = %v, want *JoinCSRIdentityError", err)
	}
}

func TestValidateJoinCSRIdentity_NoURIRefused(t *testing.T) {
	der := buildCSRWithSANs(t, nil, nil, nil)
	err := validateJoinCSRIdentity(parseCSRForTest(t, der), "spiffe://test.cluster/node/node-1")
	var identErr *JoinCSRIdentityError
	if !errors.As(err, &identErr) {
		t.Fatalf("validateJoinCSRIdentity with no URI SAN = %v, want *JoinCSRIdentityError", err)
	}
}

func TestValidateJoinCSRIdentity_MultipleURIsRefused(t *testing.T) {
	der := buildCSRWithSANs(t, []string{
		"spiffe://test.cluster/node/node-1",
		"spiffe://test.cluster/node/node-2",
	}, nil, nil)
	err := validateJoinCSRIdentity(parseCSRForTest(t, der), "spiffe://test.cluster/node/node-1")
	var identErr *JoinCSRIdentityError
	if !errors.As(err, &identErr) {
		t.Fatalf("validateJoinCSRIdentity with two URI SANs = %v, want *JoinCSRIdentityError", err)
	}
}

func TestValidateJoinCSRIdentity_MismatchedURIRefused(t *testing.T) {
	der := buildCSRWithSANs(t, []string{"spiffe://test.cluster/node/node-1"}, nil, nil)
	err := validateJoinCSRIdentity(parseCSRForTest(t, der), "spiffe://test.cluster/node/node-2")
	var identErr *JoinCSRIdentityError
	if !errors.As(err, &identErr) {
		t.Fatalf("validateJoinCSRIdentity with a mismatched URI = %v, want *JoinCSRIdentityError", err)
	}
}

func TestValidateJoinCSRIdentity_MatchingURIAccepted(t *testing.T) {
	claimed := "spiffe://test.cluster/node/node-1"
	der := buildCSRWithSANs(t, []string{claimed}, nil, nil)
	if err := validateJoinCSRIdentity(parseCSRForTest(t, der), claimed); err != nil {
		t.Fatalf("validateJoinCSRIdentity with a matching single URI SAN = %v, want nil", err)
	}
}

func TestJoinCSRIdentityError_ErrorString(t *testing.T) {
	err := &JoinCSRIdentityError{Reason: "boom"}
	if err.Error() == "" {
		t.Error("JoinCSRIdentityError.Error() is empty")
	}
}

// ---------------------------------------------------------------------------
// Integration: HandleJoinRequest refuses a CSR/NodeURI mismatch BEFORE the
// token is touched, exactly like the proof-of-possession and key-strength
// checks beside it -- a corrected resubmission with the SAME token must
// still succeed.
// ---------------------------------------------------------------------------

func TestHandleJoinRequest_CSRIdentityMismatchDoesNotConsumeToken(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	token := mintTestJoinToken(t, store, "10.0.0.9:63001")
	boundNodeURI := "spiffe://" + testTrustDomain + "/node/10.0.0.9:63001"

	_, mismatchedCSR := buildMatchingJoinCSR(t, "spiffe://attacker.example/node/anything")
	resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
		Token:   token,
		CSRDER:  mismatchedCSR,
		NodeURI: boundNodeURI,
	}, nil)
	if resp.Error == "" {
		t.Fatal("HandleJoinRequest with a CSR/NodeURI mismatch succeeded, want refusal")
	}

	// Retry with the SAME token and a CORRECTED, matching CSR: this must
	// still succeed, proving the mismatch above never touched the token.
	_, correctedCSR := buildMatchingJoinCSR(t, boundNodeURI)
	resp2 := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
		Token:   token,
		CSRDER:  correctedCSR,
		NodeURI: boundNodeURI,
	}, nil)
	if resp2.Error != "" {
		t.Fatalf("retry with the SAME token after a CSR identity mismatch = %+v, want a successfully issued leaf", resp2)
	}
	if len(resp2.CertPEM) == 0 {
		t.Fatal("retry after a CSR identity mismatch issued no certificate")
	}
}

func TestHandleJoinRequest_MissingNodeURIRefused(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	store := kvstore.NewMemoryKVStore()
	token := mintTestJoinToken(t, store, "10.0.0.10:63001")

	_, csrDER := buildMatchingJoinCSR(t, "spiffe://"+testTrustDomain+"/node/10.0.0.10:63001")
	resp := HandleJoinRequest(context.Background(), provider, store, testTrustDomain, &JoinRequest{
		Token:  token,
		CSRDER: csrDER,
		// NodeURI deliberately omitted.
	}, nil)
	if resp.Error == "" {
		t.Fatal("HandleJoinRequest with no claimed NodeURI succeeded, want refusal")
	}
}
