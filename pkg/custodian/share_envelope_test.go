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

package custodian_test

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"io"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"golang.org/x/crypto/hkdf"
)

// TestSealShareTo_RoundTrip verifies that SealShareTo produces a ciphertext
// that can be decrypted using the recipient's private key via the same
// ECDH+HKDF-SHA256+AES-256-GCM protocol.
func TestSealShareTo_RoundTrip(t *testing.T) {
	// Generate recipient P-256 key pair (simulates custodian hardware key).
	recipientPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}

	recipientPubPEM := marshalECDHPublicKeyPEM(t, recipientPriv.PublicKey())

	plaintext := []byte("test-shamir-share-bytes-32-bytes!")

	// Take a copy because SealShareTo zeroes the input.
	plaintextCopy := make([]byte, len(plaintext))
	copy(plaintextCopy, plaintext)

	envelope, err := custodian.SealShareTo(plaintext, recipientPubPEM)
	if err != nil {
		t.Fatalf("SealShareTo: %v", err)
	}

	// Verify plaintext is zeroed.
	for _, b := range plaintext {
		if b != 0 {
			t.Fatal("plaintext not zeroed after SealShareTo")
		}
	}

	// Client-side decryption using recipient private key.
	decrypted := unsealEnvelopeWithPrivKey(t, envelope, recipientPriv)

	if string(decrypted) != string(plaintextCopy) {
		t.Errorf("round-trip mismatch: got %q, want %q", decrypted, plaintextCopy)
	}
}

// TestSealShareTo_DistinctNonces verifies that two successive calls produce
// different envelopes (different ephemeral keys and nonces).
func TestSealShareTo_DistinctNonces(t *testing.T) {
	recipientPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubPEM := marshalECDHPublicKeyPEM(t, recipientPriv.PublicKey())

	share1 := []byte("share-one-32-bytes-of-secret-key")
	share2 := []byte("share-two-32-bytes-of-secret-key")
	share1Copy := append([]byte(nil), share1...)
	share2Copy := append([]byte(nil), share2...)

	env1, err := custodian.SealShareTo(share1, pubPEM)
	if err != nil {
		t.Fatalf("SealShareTo 1: %v", err)
	}
	env2, err := custodian.SealShareTo(share2, pubPEM)
	if err != nil {
		t.Fatalf("SealShareTo 2: %v", err)
	}

	if env1.EphemeralPubKeyPEM == env2.EphemeralPubKeyPEM {
		t.Error("identical ephemeral public keys across two calls")
	}
	if string(env1.Nonce) == string(env2.Nonce) {
		t.Error("identical nonces across two calls")
	}

	// Both should still decrypt correctly.
	d1 := unsealEnvelopeWithPrivKey(t, env1, recipientPriv)
	d2 := unsealEnvelopeWithPrivKey(t, env2, recipientPriv)

	if string(d1) != string(share1Copy) {
		t.Errorf("env1 decrypt mismatch: got %q", d1)
	}
	if string(d2) != string(share2Copy) {
		t.Errorf("env2 decrypt mismatch: got %q", d2)
	}
}

// TestSealShareTo_InvalidPubKey verifies that an invalid PEM returns an error
// without panicking.
func TestSealShareTo_InvalidPubKey(t *testing.T) {
	share := []byte("not-going-anywhere")
	_, err := custodian.SealShareTo(share, "not-a-valid-pem")
	if err == nil {
		t.Fatal("expected error for invalid public key PEM")
	}
}

// TestSealShareTo_WrongKeyDecrypts verifies that decrypting with a different
// private key fails.
func TestSealShareTo_WrongKeyDecrypts(t *testing.T) {
	recipientPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	wrongPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	pubPEM := marshalECDHPublicKeyPEM(t, recipientPriv.PublicKey())

	share := []byte("secret-share-bytes-123456789012")
	env, err := custodian.SealShareTo(share, pubPEM)
	if err != nil {
		t.Fatalf("SealShareTo: %v", err)
	}

	// Attempting to decrypt with the wrong key should fail or produce garbage.
	didPanic := func() (panicked bool) {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		_, decErr := tryUnsealEnvelope(env, wrongPriv)
		return decErr == nil
	}()

	if didPanic {
		t.Error("unexpected panic when decrypting with wrong key")
	}
}

// TestSealShareTo_RSAPublicKeyRejected verifies that an RSA public key
// (unsupported type after PKIX parsing) is rejected with an error and does
// not panic.
func TestSealShareTo_RSAPublicKeyRejected(t *testing.T) {
	// Generate an RSA key and PEM-encode the public key via PKIX.
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&rsaKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal RSA public key: %v", err)
	}
	rsaPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	share := []byte("test-share-bytes-should-not-seal")
	_, err = custodian.SealShareTo(share, rsaPEM)
	if err == nil {
		t.Fatal("expected error for RSA public key (unsupported type)")
	}
}

// --- helpers ---

// marshalECDHPublicKeyPEM PEM-encodes an *ecdh.PublicKey as PKIX.
func marshalECDHPublicKeyPEM(t *testing.T, pub *ecdh.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// unsealEnvelopeWithPrivKey performs the custodian-side decryption:
// ECDH(recipientPriv, ephemeralPub) → HKDF-SHA256 → AES-256-GCM decrypt.
func unsealEnvelopeWithPrivKey(t *testing.T, env *custodian.SealedEnvelope, recipientPriv *ecdh.PrivateKey) []byte {
	t.Helper()
	plaintext, err := tryUnsealEnvelope(env, recipientPriv)
	if err != nil {
		t.Fatalf("unseal envelope: %v", err)
	}
	return plaintext
}

// tryUnsealEnvelope performs the decryption without fatal. Returns error on failure.
func tryUnsealEnvelope(env *custodian.SealedEnvelope, recipientPriv *ecdh.PrivateKey) ([]byte, error) {
	const hkdfInfo = "go-qrdb/shamir/envelope/v1"

	block, _ := pem.Decode([]byte(env.EphemeralPubKeyPEM))
	if block == nil {
		return nil, custodian.ErrInvalidPublicKey
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	var ephemeralPub *ecdh.PublicKey
	switch k := pub.(type) {
	case *ecdh.PublicKey:
		ephemeralPub = k
	case *ecdsa.PublicKey:
		ephemeralPub, err = k.ECDH()
		if err != nil {
			return nil, err
		}
	default:
		return nil, custodian.ErrInvalidPublicKey
	}

	sharedSecret, err := recipientPriv.ECDH(ephemeralPub)
	if err != nil {
		return nil, err
	}

	contentKey := make([]byte, 32)
	hr := hkdf.New(sha256.New, sharedSecret, nil, []byte(hkdfInfo))
	if _, err := io.ReadFull(hr, contentKey); err != nil {
		return nil, err
	}

	blk, err := aes.NewCipher(contentKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}

	plaintext, err := gcm.Open(nil, env.Nonce, env.Ciphertext, []byte(hkdfInfo))
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}
