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

package trustanchor

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"math/big"
	"testing"
	"time"
)

// selfSignedCA builds a self-signed CA certificate with the given public key
// and signing algorithm, bypassing newRoot's fixed ECDSA/P-256 choice so a
// test can probe a key strength or signature algorithm newRoot never
// produces.
func selfSignedCA(t *testing.T, cn string, pub crypto.PublicKey, priv crypto.Signer, sigAlg x509.SignatureAlgorithm, extra []pkix.Extension) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber:          nextSerial(),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
		SignatureAlgorithm:    sigAlg,
		ExtraExtensions:       extra,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatalf("create %s: %v", cn, err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return EncodePEM(c)
}

// TestParseAnchors_RefusesWeakRSA pins the security review's weak-RSA-1024
// probe: a trust anchor signed by a sub-2048-bit RSA key is refused.
func TestParseAnchors_RefusesWeakRSA(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 1024) // #nosec G403 -- deliberately weak key to prove ParseAnchors refuses it; the test IS the rejection path
	if err != nil {
		t.Skipf("cannot generate RSA-1024 in this toolchain: %v", err)
	}
	pemb := selfSignedCA(t, "weak-rsa1024", &k.PublicKey, k, x509.SHA256WithRSA, nil)

	var invalid *InvalidAnchorError
	if _, err := ParseAnchors(pemb, time.Now()); !errors.As(err, &invalid) {
		t.Fatalf("ParseAnchors(RSA-1024) = %v, want *InvalidAnchorError", err)
	}
}

// TestParseAnchors_RefusesP224 pins the security review's P-224 probe: P-224
// is below the project's EC floor (P-256) even though crypto/x509 parses it
// without complaint.
func TestParseAnchors_RefusesP224(t *testing.T) {
	k, err := ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pemb := selfSignedCA(t, "p224", &k.PublicKey, k, x509.ECDSAWithSHA256, nil)

	var invalid *InvalidAnchorError
	if _, err := ParseAnchors(pemb, time.Now()); !errors.As(err, &invalid) {
		t.Fatalf("ParseAnchors(P-224) = %v, want *InvalidAnchorError", err)
	}
}

// TestParseAnchors_RefusesSHA1Signature pins the security review's SHA-1
// probe: a certificate self-signed with SHA-1 is refused as a trust anchor
// even though it is otherwise well formed.
func TestParseAnchors_RefusesSHA1Signature(t *testing.T) {
	//nolint:staticcheck // deliberately generating a SHA-1 signed certificate to test its refusal
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "sha1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
		SignatureAlgorithm: x509.SHA1WithRSA,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Skipf("toolchain refuses to CREATE a SHA-1 signed certificate: %v", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	var invalid *InvalidAnchorError
	if _, err := ParseAnchors(EncodePEM(c), time.Now()); !errors.As(err, &invalid) {
		t.Fatalf("ParseAnchors(SHA-1 signed) = %v, want *InvalidAnchorError", err)
	}
}

// TestParseAnchors_RefusesAnOversizedAnchor pins the security review's
// oversized-anchor probe: a certificate padded with a 1 MiB opaque extension
// is refused before it ever reaches the store.
func TestParseAnchors_RefusesAnOversizedAnchor(t *testing.T) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := asn1.Marshal(make([]byte, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	ext := []pkix.Extension{{Id: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1}, Value: blob}}
	pemb := selfSignedCA(t, "huge", &k.PublicKey, k, x509.ECDSAWithSHA256, ext)

	// A certificate padded to over a megabyte trips one of two size bounds
	// depending on how its PEM armor compares to MaxBundlePEMSize -- either
	// is a correct refusal; what matters is that one of them fires.
	var invalid *InvalidAnchorError
	var tooLarge *BundleTooLargeError
	_, err = ParseAnchors(pemb, time.Now())
	if !errors.As(err, &invalid) && !errors.As(err, &tooLarge) {
		t.Fatalf("ParseAnchors(1 MiB extension) = %v, want *InvalidAnchorError or *BundleTooLargeError", err)
	}
}

// TestParseAnchors_RefusesAnOversizedBundle pins MaxBundlePEMSize directly,
// independent of any one certificate's size.
func TestParseAnchors_RefusesAnOversizedBundle(t *testing.T) {
	oversized := make([]byte, MaxBundlePEMSize+1)
	var tooLarge *BundleTooLargeError
	if _, err := ParseCertificates(oversized); !errors.As(err, &tooLarge) {
		t.Fatalf("ParseCertificates over the bundle size limit = %v, want *BundleTooLargeError", err)
	}
	if _, err := ParseAnchors(oversized, time.Now()); !errors.As(err, &tooLarge) {
		t.Fatalf("ParseAnchors over the bundle size limit = %v, want *BundleTooLargeError", err)
	}
}
