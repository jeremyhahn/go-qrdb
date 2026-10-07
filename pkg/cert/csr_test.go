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

package cert

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/fips140"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
)

func csrFor(t *testing.T, key crypto.Signer, cn string) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cn, OrganizationalUnit: []string{"someone-else"}},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}

func TestParseCSR_AcceptsModernKeys(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	_, ed, _ := ed25519.GenerateKey(rand.Reader)
	rs, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]crypto.Signer{"ecdsa-p384": ec, "ed25519": ed, "rsa-2048": rs} {
		pub, err := ParseCSR(csrFor(t, key, "alice"))
		if err != nil || pub == nil {
			t.Fatalf("%s: ParseCSR = %v, %v", name, pub, err)
		}
	}
}

func TestParseCSR_RefusesWhatItCannotIssueFor(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	good := csrFor(t, ec, "alice")
	block, _ := pem.Decode(good)
	tampered := append([]byte(nil), block.Bytes...)
	tampered[len(tampered)-5] ^= 0xFF
	weakEC, _ := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	cases := map[string][]byte{
		"not PEM":        []byte("no"),
		"wrong PEM type": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes}),
		"does not parse": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte{1, 2, 3}}),
		"bad signature":  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: tampered}),
		"P-224 curve":    csrFor(t, weakEC, "alice"),
		"empty":          nil,
		"trailing junk":  append([]byte("junk\n"), []byte("junk")...),
	}
	// #nosec G403 -- deliberately weak: the test proves issuance refuses a key below the 2048-bit floor.
	if weakRSA, err := rsa.GenerateKey(rand.Reader, 1024); err == nil {
		cases["1024-bit RSA"] = csrFor(t, weakRSA, "alice")
	} else if !fips140.Enforced() {
		t.Fatal(err)
	}
	// else: crypto/rsa itself refuses to generate a sub-2048-bit key under
	// GODEBUG=fips140=only, which makes this refusal case unreachable
	// there -- see docs/seal/fips140-mode.md "Skipped tests under
	// fips140=only".
	for name, csr := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseCSR(csr)
			var invalid *InvalidCSRError
			if !errors.As(err, &invalid) || invalid.Error() == "" {
				t.Fatalf("ParseCSR = %v, want *InvalidCSRError", err)
			}
		})
	}
	if err := CheckClientKey("not a key"); err == nil {
		t.Fatal("CheckClientKey accepted a non-key")
	}
	if !errors.Is(&InvalidCSRError{Reason: "x", Err: errTest}, errTest) {
		t.Fatal("InvalidCSRError does not unwrap")
	}
}

var errTest = errors.New("test cause")

func TestValidateIssueRequest(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err := validateIssueRequest(&IssueRequest{Username: "alice", TenantID: "acme", PublicKey: &ec.PublicKey}); err != nil {
		t.Fatalf("a valid request: %v", err)
	}
	var ve *ValidationError
	for name, req := range map[string]*IssueRequest{
		"nil":          nil,
		"no username":  {TenantID: "acme", PublicKey: &ec.PublicKey},
		"no tenant":    {Username: "alice", PublicKey: &ec.PublicKey},
		"negative ttl": {Username: "alice", TenantID: "acme", PublicKey: &ec.PublicKey, TTL: -1},
		"no key":       {Username: "alice", TenantID: "acme"},
	} {
		if err := validateIssueRequest(req); !errors.As(err, &ve) {
			t.Fatalf("%s: validateIssueRequest = %v, want *ValidationError", name, err)
		}
	}
	weak, _ := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	var invalid *InvalidCSRError
	if err := validateIssueRequest(&IssueRequest{Username: "alice", TenantID: "acme", PublicKey: &weak.PublicKey}); !errors.As(err, &invalid) {
		t.Fatalf("a P-224 key = %v, want *InvalidCSRError", err)
	}
}

// IssueClientCert certifies the requester's key and the account the issuer
// names -- never the Subject the CSR wrote.
func TestIssueClientCert_CertifiesTheRequestersKeyAsTheNamedAccount(t *testing.T) {
	p, _, _ := newTestProvider()
	ctx := context.Background()
	if err := p.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pub, err := ParseCSR(csrFor(t, ec, "mallory"))
	if err != nil {
		t.Fatal(err)
	}
	issued, err := p.IssueClientCert(ctx, &IssueRequest{Username: "alice", TenantID: "acme", PublicKey: pub})
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(issued.CertPEM)
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject.CommonName != "alice" || len(c.Subject.OrganizationalUnit) != 1 || c.Subject.OrganizationalUnit[0] != "acme" {
		t.Fatalf("issued Subject = %v, want CN=alice OU=acme", c.Subject)
	}
	got, ok := c.PublicKey.(*ecdsa.PublicKey)
	if !ok || !got.Equal(&ec.PublicKey) {
		t.Fatal("the certificate does not certify the requester's key")
	}
	if issued.Serial == "" || !issued.NotAfter.Equal(c.NotAfter) {
		t.Fatalf("issued = %+v", issued)
	}
}

// TestIssueClientCert_TenantWithoutKeyB proves that when a tenant's Key B CA
// key has never been established -- an injected provider factory
// (WithKeyProviderFactory) whose Signer reports *cakey.NotInitializedError,
// the shape an HSM/TPM/KMS backend reports before its CA key exists --
// IssueClientCert refuses rather than minting with no CA, surfacing the
// typed *CAKeyNotAvailableError mapProviderError translates it to.
func TestIssueClientCert_TenantWithoutKeyB(t *testing.T) {
	hsm := newHSMDouble(t)
	hsm.signErr = &cakey.NotInitializedError{}
	p := NewDefaultProvider(newMockKVStore(), newMockBarrier(), WithKeyProviderFactory(singleTenantHSMFactory("no-key-b", hsm)))

	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, err := p.IssueClientCert(context.Background(), &IssueRequest{
		Username: "alice", TenantID: "no-key-b", PublicKey: &ec.PublicKey,
	})
	var notAvailable *CAKeyNotAvailableError
	if !errors.As(err, &notAvailable) {
		t.Fatalf("IssueClientCert = %v, want *CAKeyNotAvailableError", err)
	}
}
