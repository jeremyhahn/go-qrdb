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

package cert

import (
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
	"math/big"
	"testing"
	"time"
)

// selfSignedFor returns a PEM certificate for key's public half.
func selfSignedFor(t *testing.T, key crypto.Signer) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "alice", OrganizationalUnit: []string{"acme"}},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func pkcs8PEM(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal PKCS#8: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// TestEncodePKCS12_AcceptsEveryClientKeyEncoding is the regression for the
// export of a key qrdb's own CLI generated: it writes PKCS#8, and the export
// used to accept SEC 1 ("EC PRIVATE KEY") only.
func TestEncodePKCS12_AcceptsEveryClientKeyEncoding(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		signer crypto.Signer
		keyPEM []byte
	}{
		{"PKCS#8 ECDSA", ecKey, pkcs8PEM(t, ecKey)},
		{"SEC 1 ECDSA", ecKey, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER})},
		{"PKCS#1 RSA", rsaKey, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)})},
		{"PKCS#8 Ed25519", edKey, pkcs8PEM(t, edKey)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p12, err := EncodePKCS12(selfSignedFor(t, tc.signer), tc.keyPEM, nil, "pass")
			if fips140.Enforced() {
				// PKCS#12 export is refused outright under strict FIPS
				// enforcement (the format's localKeyId attribute is a
				// SHA-1 fingerprint); see the exemption register in
				// docs/seal/fips140-mode.md.
				var fipsErr *PKCS12FIPSUnavailableError
				if !errors.As(err, &fipsErr) {
					t.Fatalf("EncodePKCS12 under fips140=only = %v, want *PKCS12FIPSUnavailableError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("EncodePKCS12: %v", err)
			}
			if len(p12) == 0 {
				t.Fatal("empty archive")
			}
		})
	}
}

func TestEncodePKCS12_RefusesAKeyThatIsNotTheCertificates(t *testing.T) {
	a, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = EncodePKCS12(selfSignedFor(t, a), pkcs8PEM(t, b), nil, "pass")
	if fips140.Enforced() {
		// The FIPS-unavailable refusal is unconditional and runs before
		// the key/certificate match check, so that is what surfaces here
		// too; see the exemption register in docs/seal/fips140-mode.md.
		var fipsErr *PKCS12FIPSUnavailableError
		if !errors.As(err, &fipsErr) {
			t.Fatalf("err = %v; want *PKCS12FIPSUnavailableError", err)
		}
		return
	}
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "keyPEM" {
		t.Fatalf("err = %v; want a keyPEM ValidationError", err)
	}
}

func TestParsePrivateKeyBlock_RefusesUnknownTypesAndWeakKeys(t *testing.T) {
	if _, err := parsePrivateKeyBlock(&pem.Block{Type: "OPENSSH PRIVATE KEY", Bytes: []byte{1}}); err == nil {
		t.Fatal("an unsupported block type was accepted")
	}
	if _, err := parsePrivateKeyBlock(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("garbage")}); err == nil {
		t.Fatal("undecodable PKCS#8 was accepted")
	}
	// #nosec G403 -- deliberately weak: the test proves the export refuses a key below the 2048-bit floor.
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		// crypto/rsa itself refuses to generate a sub-2048-bit key under
		// GODEBUG=fips140=only, which makes this refusal case
		// unreachable there; see docs/seal/fips140-mode.md "Skipped
		// tests under fips140=only".
		t.Skipf("cannot generate RSA-1024 in this toolchain: %v", err)
	}
	if _, err := parsePrivateKeyBlock(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(weak)}); err == nil {
		t.Fatal("a 1024-bit RSA key was accepted")
	}
}
