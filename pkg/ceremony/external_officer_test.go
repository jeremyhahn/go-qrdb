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
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"io"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// ---------------------------------------------------------------------------
// Helpers — issue test certificates against a self-signed CA pool.
// ---------------------------------------------------------------------------

// testCAFixture holds a self-signed CA and a pool that trusts it.
type testCAFixture struct {
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	pool   *x509.CertPool
}

func newTestCAFixture(t *testing.T) *testCAFixture {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Ext Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	pool := x509.NewCertPool()
	pool.AddCert(cert)

	return &testCAFixture{caCert: cert, caKey: caKey, pool: pool}
}

// issueLeafPEM signs a leaf certificate carrying the given public key
// against the fixture CA.
func (f *testCAFixture) issueLeafPEM(t *testing.T, pub crypto.PublicKey, cn string, notBefore, notAfter time.Time) string {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.caCert, pub, f.caKey)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func mustMarshalPubPEM(t *testing.T, pub crypto.PublicKey) string {
	t.Helper()
	pemStr, err := marshalPublicKeyPEM(pub)
	require.NoError(t, err)
	return pemStr
}

func newTestService(t *testing.T) *CeremonyService {
	t.Helper()
	svc, err := NewCeremonyService(defaultConfig(), unsealedTestBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)
	return svc
}

// ---------------------------------------------------------------------------
// Validation tests
// ---------------------------------------------------------------------------

func TestEnrollExternalOfficer_RejectsEmptyUsername(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		ShareIndex: 1,
		AuthFactor: "mtls",
		PubKeyPEM:  "ignored",
	})
	assert.ErrorIs(t, err, ErrMissingUsername)
}

func TestEnrollExternalOfficer_RejectsBadShareIndex(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "alice",
		ShareIndex: 0,
		AuthFactor: "mtls",
		PubKeyPEM:  "ignored",
	})
	assert.ErrorIs(t, err, ErrInvalidShareIndex)
}

func TestEnrollExternalOfficer_RejectsInvalidAuthFactor(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "alice",
		ShareIndex: 1,
		AuthFactor: "magic",
		PubKeyPEM:  "ignored",
	})
	assert.ErrorIs(t, err, ErrInvalidAuthFactor)
}

func TestEnrollExternalOfficer_RejectsBothCertAndPubKey(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "alice",
		ShareIndex: 1,
		AuthFactor: "mtls",
		CertPEM:    "x",
		PubKeyPEM:  "x",
	})
	assert.ErrorIs(t, err, ErrCertAndPubKeyBothSupplied)
}

func TestEnrollExternalOfficer_RejectsNeitherCertNorPubKey(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "alice",
		ShareIndex: 1,
		AuthFactor: "mtls",
	})
	assert.ErrorIs(t, err, ErrCertOrPubKeyRequired)
}

func TestEnrollExternalOfficer_RejectsInvalidPubKeyPEM(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "alice",
		ShareIndex: 1,
		AuthFactor: "mtls",
		PubKeyPEM:  "not-a-pem",
	})
	assert.ErrorIs(t, err, ErrInvalidPubKeyPEM)
}

func TestEnrollExternalOfficer_RejectsInvalidCertPEM(t *testing.T) {
	fixture := newTestCAFixture(t)
	svc := newTestService(t)
	svc.SetExternalCAPools(fixture.pool)
	_, err := svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "alice",
		ShareIndex: 1,
		AuthFactor: "mtls",
		CertPEM:    "not-a-pem",
	})
	assert.ErrorIs(t, err, ErrInvalidCertPEM)
}

func TestEnrollExternalOfficer_RejectsExpiredCert(t *testing.T) {
	fixture := newTestCAFixture(t)
	svc := newTestService(t)
	svc.SetExternalCAPools(fixture.pool)

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafPEM := fixture.issueLeafPEM(t, &leafKey.PublicKey, "alice",
		time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))

	_, err = svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "alice",
		ShareIndex: 1,
		AuthFactor: "mtls",
		CertPEM:    leafPEM,
	})
	assert.ErrorIs(t, err, ErrCertExpired)
}

func TestEnrollExternalOfficer_RejectsUntrustedCert(t *testing.T) {
	fixture := newTestCAFixture(t)
	other := newTestCAFixture(t) // separate trust root

	svc := newTestService(t)
	svc.SetExternalCAPools(fixture.pool) // does not include 'other'

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafPEM := other.issueLeafPEM(t, &leafKey.PublicKey, "alice",
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	_, err = svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "alice",
		ShareIndex: 1,
		AuthFactor: "mtls",
		CertPEM:    leafPEM,
	})
	assert.ErrorIs(t, err, ErrCertUntrusted)
}

func TestEnrollExternalOfficer_RejectsCertWithoutPool(t *testing.T) {
	svc := newTestService(t)
	// Note: NO SetExternalCAPools call — pool is nil.

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	fixture := newTestCAFixture(t)
	leafPEM := fixture.issueLeafPEM(t, &leafKey.PublicKey, "alice",
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	_, err = svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "alice",
		ShareIndex: 1,
		AuthFactor: "mtls",
		CertPEM:    leafPEM,
	})
	assert.ErrorIs(t, err, ErrCertUntrusted)
}

// ---------------------------------------------------------------------------
// Cryptographic round-trip tests
// ---------------------------------------------------------------------------

func TestEnrollExternalOfficer_RSA2048_RoundTrip(t *testing.T) {
	enrollAndDecrypt_RSA(t, 2048)
}

func TestEnrollExternalOfficer_RSA4096_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("RSA-4096 keygen is slow; skipped under -short")
	}
	enrollAndDecrypt_RSA(t, 4096)
}

func enrollAndDecrypt_RSA(t *testing.T, bits int) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, bits)
	require.NoError(t, err)
	pubPEM := mustMarshalPubPEM(t, &priv.PublicKey)

	svc := newTestService(t)
	share := []byte("rsa-share-payload-must-be-short-ish")
	shareCopy := append([]byte(nil), share...)

	resp, err := svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "rsa-officer",
		ShareIndex: 1,
		AuthFactor: "mtls",
		PubKeyPEM:  pubPEM,
		ShareBytes: share,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.SealedShare)

	// Decrypt with the matching RSA private key.
	sealed, err := base64.StdEncoding.DecodeString(resp.SealedShare)
	require.NoError(t, err)
	plain, err := rsa.DecryptOAEP(sha256.New(), nil, priv, sealed, []byte(externalOfficerHKDFInfo))
	require.NoError(t, err)
	assert.Equal(t, shareCopy, plain)
}

func TestEnrollExternalOfficer_P256ECDSA_RoundTrip(t *testing.T) {
	enrollAndDecrypt_ECDSA(t, elliptic.P256(), ecdh.P256())
}

func TestEnrollExternalOfficer_P384ECDSA_RoundTrip(t *testing.T) {
	enrollAndDecrypt_ECDSA(t, elliptic.P384(), ecdh.P384())
}

func enrollAndDecrypt_ECDSA(t *testing.T, curve elliptic.Curve, ecdhCurve ecdh.Curve) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(curve, rand.Reader)
	require.NoError(t, err)
	pubPEM := mustMarshalPubPEM(t, &priv.PublicKey)

	svc := newTestService(t)
	share := []byte("ecdsa-test-share-payload")
	shareCopy := append([]byte(nil), share...)

	resp, err := svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "ec-officer",
		ShareIndex: 1,
		AuthFactor: "mtls",
		PubKeyPEM:  pubPEM,
		ShareBytes: share,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.SealedShare)

	sealed, err := base64.StdEncoding.DecodeString(resp.SealedShare)
	require.NoError(t, err)

	ephPub, nonce, ciphertext := unpackEnvelope(t, sealed)

	// Convert ECDSA priv to ECDH and perform ECDH with the ephemeral.
	ecdhPriv, err := priv.ECDH()
	require.NoError(t, err)
	ephPubKey, err := ecdhCurve.NewPublicKey(unwrapPKIXECPubKey(t, ephPub))
	require.NoError(t, err)
	shared, err := ecdhPriv.ECDH(ephPubKey)
	require.NoError(t, err)

	contentKey := deriveTestContentKey(t, shared)
	block, err := aes.NewCipher(contentKey)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(externalOfficerHKDFInfo))
	require.NoError(t, err)
	assert.Equal(t, shareCopy, plain)
}

func TestEnrollExternalOfficer_Ed25519_RoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubPEM := mustMarshalPubPEM(t, pub)

	svc := newTestService(t)
	share := []byte("ed25519-test-share")
	shareCopy := append([]byte(nil), share...)

	resp, err := svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "ed-officer",
		ShareIndex: 1,
		AuthFactor: "mtls",
		PubKeyPEM:  pubPEM,
		ShareBytes: share,
	})
	require.NoError(t, err)

	sealed, err := base64.StdEncoding.DecodeString(resp.SealedShare)
	require.NoError(t, err)
	ephPub, nonce, ciphertext := unpackEnvelope(t, sealed)

	// Derive X25519 secret from Ed25519 private and ephemeral X25519 pub.
	x25519Priv := ed25519PrivToX25519ForTest(t, priv)
	shared, err := curve25519.X25519(x25519Priv, ephPub)
	require.NoError(t, err)

	contentKey := deriveTestContentKey(t, shared)
	block, err := aes.NewCipher(contentKey)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(externalOfficerHKDFInfo))
	require.NoError(t, err)
	assert.Equal(t, shareCopy, plain)
}

// ---------------------------------------------------------------------------
// Persistence + cert path
// ---------------------------------------------------------------------------

func TestEnrollExternalOfficer_PersistsRegistry(t *testing.T) {
	svc := newTestService(t)

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	pubPEM := mustMarshalPubPEM(t, &priv.PublicKey)

	resp, err := svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "registry-officer",
		ShareIndex: 3,
		AuthFactor: "piv",
		PubKeyPEM:  pubPEM,
	})
	require.NoError(t, err)
	assert.Empty(t, resp.SealedShare, "no share supplied -> empty sealed share")

	got, err := svc.OfficerRegistry().Load(context.Background(), "registry-officer")
	require.NoError(t, err)
	assert.Equal(t, 3, got.ShareIndex)
	assert.Equal(t, "piv", got.AuthFactor)
	assert.NotEmpty(t, got.PubKeyPEM)
	assert.Empty(t, got.CertThumbprint, "pubkey-only enrollment must not record a thumbprint")
}

func TestEnrollExternalOfficer_CertPath_Success(t *testing.T) {
	fixture := newTestCAFixture(t)
	svc := newTestService(t)
	svc.SetExternalCAPools(fixture.pool)

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafPEM := fixture.issueLeafPEM(t, &leafKey.PublicKey, "cert-officer",
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	share := []byte("cert-share")
	shareCopy := append([]byte(nil), share...)
	resp, err := svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "cert-officer",
		ShareIndex: 1,
		AuthFactor: "mtls",
		CertPEM:    leafPEM,
		ShareBytes: share,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp.SealedShare)
	assert.NotEmpty(t, resp.Officer.CertThumbprint, "cert path must record a thumbprint")

	// Decrypt with the matching ECDSA private key.
	sealed, err := base64.StdEncoding.DecodeString(resp.SealedShare)
	require.NoError(t, err)
	ephPub, nonce, ciphertext := unpackEnvelope(t, sealed)
	ecdhPriv, err := leafKey.ECDH()
	require.NoError(t, err)
	ephPubKey, err := ecdh.P256().NewPublicKey(unwrapPKIXECPubKey(t, ephPub))
	require.NoError(t, err)
	shared, err := ecdhPriv.ECDH(ephPubKey)
	require.NoError(t, err)
	contentKey := deriveTestContentKey(t, shared)
	block, err := aes.NewCipher(contentKey)
	require.NoError(t, err)
	gcm, err := cipher.NewGCM(block)
	require.NoError(t, err)
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(externalOfficerHKDFInfo))
	require.NoError(t, err)
	assert.Equal(t, shareCopy, plain)
}

// TestEnrollExternalOfficer_UnsupportedKeyAlgorithm verifies that DSA / unusual
// keys are rejected. We use an unsupported curve (P-521) to trigger the path.
func TestEnrollExternalOfficer_UnsupportedKeyAlgorithm(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	require.NoError(t, err)
	pubPEM := mustMarshalPubPEM(t, &priv.PublicKey)

	svc := newTestService(t)
	_, err = svc.EnrollExternalOfficer(context.Background(), EnrollExternalOfficerRequest{
		Username:   "p521",
		ShareIndex: 1,
		AuthFactor: "mtls",
		PubKeyPEM:  pubPEM,
		ShareBytes: []byte("payload"),
	})
	assert.ErrorIs(t, err, ErrUnsupportedKeyAlgorithm)
}

// ---------------------------------------------------------------------------
// Test-only helpers — keep these out of production code paths.
// ---------------------------------------------------------------------------

func unpackEnvelope(t *testing.T, sealed []byte) ([]byte, []byte, []byte) {
	t.Helper()
	require.GreaterOrEqual(t, len(sealed), 2)
	ephLen := int(binary.BigEndian.Uint16(sealed[:2]))
	require.GreaterOrEqual(t, len(sealed)-2, ephLen+12)
	eph := sealed[2 : 2+ephLen]
	nonce := sealed[2+ephLen : 2+ephLen+12]
	ct := sealed[2+ephLen+12:]
	return eph, nonce, ct
}

// unwrapPKIXECPubKey extracts the raw EC point bytes from a PKIX-DER
// encoded EC public key. The output is suitable for ecdh.NewPublicKey.
func unwrapPKIXECPubKey(t *testing.T, der []byte) []byte {
	t.Helper()
	pub, err := x509.ParsePKIXPublicKey(der)
	require.NoError(t, err)
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		ecdhPub, err := k.ECDH()
		require.NoError(t, err)
		return ecdhPub.Bytes()
	default:
		// already an ECDH public key encoded as raw bytes.
		return der
	}
}

func deriveTestContentKey(t *testing.T, shared []byte) []byte {
	t.Helper()
	out := make([]byte, 32)
	r := hkdf.New(sha256.New, shared, nil, []byte(externalOfficerHKDFInfo))
	_, err := io.ReadFull(r, out)
	require.NoError(t, err)
	return out
}

// ed25519PrivToX25519ForTest performs the SHA-512(seed) → clamp →
// X25519 scalar conversion required to verify Ed25519-keyed envelopes.
func ed25519PrivToX25519ForTest(t *testing.T, priv ed25519.PrivateKey) []byte {
	t.Helper()
	require.Len(t, priv, ed25519.PrivateKeySize)
	seed := priv.Seed()
	h := sha512.Sum512(seed)
	// Standard X25519 clamping per RFC 7748.
	h[0] &= 248
	h[31] &= 127
	h[31] |= 64
	return h[:32]
}

// ---------------------------------------------------------------------------
// EnvelopeSealError sanity tests
// ---------------------------------------------------------------------------

func TestEnvelopeSealError_IsAndUnwrap(t *testing.T) {
	inner := io.ErrUnexpectedEOF
	e := &EnvelopeSealError{Err: inner}
	assert.Contains(t, e.Error(), "envelope seal failed")
	assert.ErrorIs(t, e, inner)
	assert.ErrorIs(t, e, ErrShareSealFailed)
}
