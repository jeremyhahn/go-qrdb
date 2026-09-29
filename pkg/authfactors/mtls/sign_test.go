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
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
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
func TestParseSignerPEM(t *testing.T) {
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	sec1, _ := x509.MarshalECPrivateKey(ecKey)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(edKey)
	xKey, _ := ecdh.X25519().GenerateKey(rand.Reader)
	xPKCS8, _ := x509.MarshalPKCS8PrivateKey(xKey)

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
	var unsupported *UnsupportedSignerError
	if _, err := ParseSignerPEM(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: xPKCS8})); !errors.As(err, &unsupported) {
		t.Errorf("an X25519 key cannot sign: %v", err)
	}
}
