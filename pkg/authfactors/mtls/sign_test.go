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

package mtls

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/fips140"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// TestSignNonce_RoundTripsThroughVerify pins that SignNonce produces exactly
// what VerifyNonceSignature accepts, for every supported key type.
func TestSignNonce_RoundTripsThroughVerify(t *testing.T) {
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	nonce := []byte("0123456789abcdef0123456789abcdef")
	for _, s := range []crypto.Signer{ecKey, rsaKey, edKey} {
		sig, err := SignNonce(s, nonce)
		if err != nil {
			t.Fatalf("%T: %v", s, err)
		}
		if !VerifyNonceSignature(s.Public(), nonce, sig) {
			t.Errorf("%T: signature does not verify", s)
		}
		if VerifyNonceSignature(s.Public(), []byte("another nonce"), sig) {
			t.Errorf("%T: signature verifies over another nonce", s)
		}
	}
}

// TestSignNonce_Refusals covers the unsupported inputs.
func TestSignNonce_Refusals(t *testing.T) {
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	var unsupported *UnsupportedSignerError
	if _, err := SignNonce(nil, []byte("n")); !errors.As(err, &unsupported) {
		t.Errorf("nil signer: %v", err)
	}
	if _, err := SignNonce(ecKey, nil); !errors.As(err, &unsupported) {
		t.Errorf("empty nonce: %v", err)
	}
	if unsupported == nil || unsupported.Error() == "" {
		t.Error("the refusal must describe itself")
	}
}

// TestParseSignerPEM covers every accepted encoding and the refusals.
// TestParseSignerPEM covers every accepted encoding and the refusals.
func TestParseSignerPEM(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	sec1, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(edKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}

	for name, block := range map[string]*pem.Block{
		"sec1":  {Type: "EC PRIVATE KEY", Bytes: sec1},
		"pkcs1": {Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)},
		"pkcs8": {Type: "PRIVATE KEY", Bytes: pkcs8},
	} {
		if _, err := ParseSignerPEM(pem.EncodeToMemory(block)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParseSignerPEM([]byte("not pem")); err == nil {
		t.Error("non-PEM input must be refused")
	}
	if _, err := ParseSignerPEM(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1}})); err == nil {
		t.Error("garbage DER must be refused")
	}

	// An X25519 key cannot sign, so ParseSignerPEM must refuse one with
	// *UnsupportedSignerError. Under GODEBUG=fips140=only the module refuses
	// x509.ParsePKCS8PrivateKey's construction of the *ecdh.PrivateKey
	// (ecdh.X25519().NewPrivateKey checks crypto/fips140.Enforced() first), so
	// the refusal is raised one step earlier, as *EnrollmentInvalidError. The
	// key is built from fixed bytes because generating one is refused there too.
	t.Run("x25519_cannot_sign", func(t *testing.T) {
		_, err := ParseSignerPEM(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x25519PKCS8()}))
		if fips140.Enforced() {
			var invalid *authfactors.EnrollmentInvalidError
			if !errors.As(err, &invalid) {
				t.Errorf("an X25519 key must be refused as unparseable under fips140=only: %v", err)
			}
			return
		}
		var unsupported *UnsupportedSignerError
		if !errors.As(err, &unsupported) {
			t.Errorf("an X25519 key cannot sign: %v", err)
		}
	})
}

// x25519PKCS8 returns an RFC 8410 PKCS#8 X25519 private key built from fixed
// bytes, because generating one is refused under fips140=only.
func x25519PKCS8() []byte {
	der := []byte{0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x6e, 0x04, 0x22, 0x04, 0x20}
	return append(der, bytes.Repeat([]byte{0x42}, 32)...)
}
