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

package clusterenroll

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// selfSignedCAForTest returns a minimal self-signed CA certificate PEM
// signed by key, for exercising verifyJoinedLeaf's chain checks directly
// without a full cakey.Provider round trip.
func selfSignedCAForTest(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate (CA): %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// leafSignedByForTest returns a leaf certificate PEM for leafPub, signed by
// caKey/caCertPEM.
func leafSignedByForTest(t *testing.T, caCertPEM []byte, caKey *ecdsa.PrivateKey, leafPub *ecdsa.PublicKey) []byte {
	t.Helper()
	block, _ := pem.Decode(caCertPEM)
	if block == nil {
		t.Fatal("selfSignedCAForTest produced invalid PEM")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate (CA): %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, leafPub, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate (leaf): %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestVerifyJoinedLeaf_Success(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	anchorPEM := selfSignedCAForTest(t, caKey)
	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafPEM := leafSignedByForTest(t, anchorPEM, caKey, &nodeKey.PublicKey)

	if err := verifyJoinedLeaf(anchorPEM, leafPEM, &nodeKey.PublicKey); err != nil {
		t.Fatalf("verifyJoinedLeaf (matching chain and key): %v", err)
	}
}

func TestVerifyJoinedLeaf_InvalidAnchorPEM(t *testing.T) {
	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	err := verifyJoinedLeaf([]byte("not pem"), []byte("not pem"), &nodeKey.PublicKey)
	assertJoinedLeafVerifyError(t, err, "not valid PEM")
}

func TestVerifyJoinedLeaf_InvalidAnchorCertDER(t *testing.T) {
	badAnchor := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not a real certificate DER")})
	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	err := verifyJoinedLeaf(badAnchor, []byte("not pem"), &nodeKey.PublicKey)
	assertJoinedLeafVerifyError(t, err, "anchor certificate:")
}

func TestVerifyJoinedLeaf_InvalidLeafPEM(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	anchorPEM := selfSignedCAForTest(t, caKey)
	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	err := verifyJoinedLeaf(anchorPEM, []byte("not pem"), &nodeKey.PublicKey)
	assertJoinedLeafVerifyError(t, err, "issued leaf is not valid PEM")
}

func TestVerifyJoinedLeaf_InvalidLeafCertDER(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	anchorPEM := selfSignedCAForTest(t, caKey)
	nodeKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	badLeaf := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not a real certificate DER")})

	err := verifyJoinedLeaf(anchorPEM, badLeaf, &nodeKey.PublicKey)
	assertJoinedLeafVerifyError(t, err, "issued leaf:")
}

func TestVerifyJoinedLeaf_WrongPublicKeyRefused(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	anchorPEM := selfSignedCAForTest(t, caKey)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafPEM := leafSignedByForTest(t, anchorPEM, caKey, &leafKey.PublicKey)

	// A DIFFERENT key than the one the leaf was issued for.
	wrongKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	err := verifyJoinedLeaf(anchorPEM, leafPEM, &wrongKey.PublicKey)
	assertJoinedLeafVerifyError(t, err, "does not carry this joiner's own public key")
}

func assertJoinedLeafVerifyError(t *testing.T, err error, wantSubstring string) {
	t.Helper()
	if err == nil {
		t.Fatal("verifyJoinedLeaf succeeded, want an error")
	}
	var verifyErr *JoinedLeafVerifyError
	if !errors.As(err, &verifyErr) {
		t.Fatalf("verifyJoinedLeaf error = %T (%v), want *JoinedLeafVerifyError", err, err)
	}
	if !strings.Contains(verifyErr.Reason, wantSubstring) {
		t.Fatalf("Reason = %q, want it to contain %q", verifyErr.Reason, wantSubstring)
	}
}

func TestJoinedLeafVerifyError_ErrorString(t *testing.T) {
	err := &JoinedLeafVerifyError{Reason: "something went wrong"}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Fatalf("Error() = %q, want it to contain the reason", err.Error())
	}
}
