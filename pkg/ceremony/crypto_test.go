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
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyNonceSignature_ECDSA_Success(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	nonce := make([]byte, 32)
	_, err = rand.Read(nonce)
	require.NoError(t, err)

	digest := sha256.Sum256(nonce)
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	require.NoError(t, err)

	sig, err := asn1.Marshal(struct{ R, S *big.Int }{R: r, S: s})
	require.NoError(t, err)

	err = verifyNonceSignature(&key.PublicKey, nonce, sig)
	assert.NoError(t, err)
}

func TestVerifyNonceSignature_ECDSA_BadSignature(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	nonce := make([]byte, 32)
	_, err = rand.Read(nonce)
	require.NoError(t, err)

	badSig := []byte("not-a-valid-signature")

	err = verifyNonceSignature(&key.PublicKey, nonce, badSig)
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

func TestVerifyNonceSignature_ECDSA_WrongKey(t *testing.T) {
	key1, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	key2, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	nonce := make([]byte, 32)
	_, err = rand.Read(nonce)
	require.NoError(t, err)

	digest := sha256.Sum256(nonce)
	r, s, err := ecdsa.Sign(rand.Reader, key1, digest[:])
	require.NoError(t, err)

	sig, err := asn1.Marshal(struct{ R, S *big.Int }{R: r, S: s})
	require.NoError(t, err)

	// Verify with wrong public key.
	err = verifyNonceSignature(&key2.PublicKey, nonce, sig)
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

func TestVerifyNonceSignature_RSA_Success(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	nonce := make([]byte, 32)
	_, err = rand.Read(nonce)
	require.NoError(t, err)

	digest := sha256.Sum256(nonce)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	require.NoError(t, err)

	err = verifyNonceSignature(&key.PublicKey, nonce, sig)
	assert.NoError(t, err)
}

func TestVerifyNonceSignature_RSA_BadSignature(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	nonce := make([]byte, 32)
	_, err = rand.Read(nonce)
	require.NoError(t, err)

	badSig := []byte("definitely-not-a-valid-rsa-signature")

	err = verifyNonceSignature(&key.PublicKey, nonce, badSig)
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

func TestVerifyNonceSignature_Ed25519_Success(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	nonce := make([]byte, 32)
	_, err = rand.Read(nonce)
	require.NoError(t, err)

	sig := ed25519.Sign(priv, nonce)

	err = verifyNonceSignature(pub, nonce, sig)
	assert.NoError(t, err)
}

func TestVerifyNonceSignature_Ed25519_BadSignature(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	nonce := make([]byte, 32)
	_, err = rand.Read(nonce)
	require.NoError(t, err)

	badSig := make([]byte, ed25519.SignatureSize)

	err = verifyNonceSignature(pub, nonce, badSig)
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

func TestVerifyNonceSignature_UnsupportedKeyType(t *testing.T) {
	// Use a string as an unsupported key type.
	err := verifyNonceSignature("unsupported-key-type", []byte("nonce"), []byte("sig"))
	assert.ErrorIs(t, err, ErrChallengeVerificationFailed)
}

func TestComputeSPKIPin_ValidCert(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(certDER)
	require.NoError(t, err)

	pin, err := computeSPKIPin(cert)
	require.NoError(t, err)
	assert.NotEmpty(t, pin)

	// Pin should be deterministic.
	pin2, err := computeSPKIPin(cert)
	require.NoError(t, err)
	assert.Equal(t, pin, pin2)
}

func TestComputeSPKIPin_NilCert(t *testing.T) {
	pin, err := computeSPKIPin(nil)
	assert.Empty(t, pin)
	assert.Error(t, err)

	var spkiErr *SPKIPinError
	assert.ErrorAs(t, err, &spkiErr)
}

func TestEcdsaSignatureSize(t *testing.T) {
	curves := []struct {
		name  string
		curve elliptic.Curve
	}{
		{"P256", elliptic.P256()},
		{"P384", elliptic.P384()},
		{"P521", elliptic.P521()},
	}
	for _, tc := range curves {
		t.Run(tc.name, func(t *testing.T) {
			size := ecdsaSignatureSize(tc.curve)
			assert.Greater(t, size, 0)
		})
	}
}
