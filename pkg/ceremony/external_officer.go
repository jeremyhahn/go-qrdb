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
	"crypto/fips140"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"io"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
)

// validAuthFactors is the set of auth factors accepted at external
// officer enrollment time.
var validAuthFactors = map[string]struct{}{
	"mtls":     {},
	"webauthn": {},
	"piv":      {},
}

// externalOfficerHKDFInfo binds derived AES keys to this protocol so a
// derivation collision with another HKDF user cannot produce a usable
// content key.
const externalOfficerHKDFInfo = "go-qrdb/ceremony/external-officer/v1"

// envelopeNonceLen is the AES-GCM nonce length carried as its own field in
// the envelope wire format described on sealWithECIES.
const envelopeNonceLen = 12

// EnrollExternalOfficerRequest carries the inputs for
// CeremonyService.EnrollExternalOfficer. Exactly one of CertPEM and
// PubKeyPEM must be supplied.
type EnrollExternalOfficerRequest struct {
	// Username is the unique identifier for the officer.
	Username string

	// CertPEM is the officer's X.509 certificate, PEM-encoded.
	// When non-empty, it is validated against the configured CA pools.
	CertPEM string

	// PubKeyPEM is the officer's SPKI-encoded public key, PEM-encoded.
	// Used when the administrator asserts trust in the public key
	// directly without a chained certificate.
	PubKeyPEM string

	// ShareIndex is the 1-based Shamir share index assigned to this officer.
	ShareIndex int

	// AuthFactor identifies the authentication factor the officer uses.
	// Allowed values: "mtls", "webauthn", "piv".
	AuthFactor string

	// ShareBytes is the plaintext Shamir share to seal to the officer's
	// public key. The slice is wiped before this call returns. May be
	// nil; callers that don't yet have a share can pass nil and call
	// EnrollExternalOfficer purely to record the public-key trust.
	ShareBytes []byte
}

// EnrollExternalOfficerResponse carries the result of a successful
// EnrollExternalOfficer call.
type EnrollExternalOfficerResponse struct {
	// SealedShare is the base64-encoded sealed share envelope. The
	// envelope format depends on the officer's key algorithm:
	//
	//   RSA      — RSA-OAEP(SHA-256) ciphertext directly.
	//   ECDSA    — ECIES: ephemeral_pub_DER || iv || ciphertext (lengths
	//              are recoverable from the format header).
	//   Ed25519  — X25519 envelope (Ed25519 public converted to X25519,
	//              ephemeral X25519 keypair, HKDF-SHA256, AES-256-GCM).
	//
	// All envelopes use AAD bound to externalOfficerHKDFInfo.
	SealedShare string

	// Officer is the persisted public metadata record.
	Officer *PersistentOfficer
}

// EnrollExternalOfficer enrolls a previously-existing officer whose key
// material lives outside go-qrdb. The administrator supplies either an
// X.509 certificate (validated against the configured CA pools) or a
// raw SPKI public key, an assigned Shamir share index, and an auth
// factor identifier. If a Shamir share is provided, it is sealed to the
// officer's public key using a key-algorithm-specific envelope.
//
// This operation does not sign CSRs and does not invoke any local CA;
// it strictly trusts the supplied trust material.
func (s *CeremonyService) EnrollExternalOfficer(
	ctx context.Context,
	req EnrollExternalOfficerRequest,
) (*EnrollExternalOfficerResponse, error) {
	defer wipeIfNonNil(req.ShareBytes)

	if req.Username == "" {
		return nil, ErrMissingUsername
	}
	if req.ShareIndex < 1 {
		return nil, ErrInvalidShareIndex
	}
	if _, ok := validAuthFactors[req.AuthFactor]; !ok {
		return nil, ErrInvalidAuthFactor
	}

	hasCert := strings.TrimSpace(req.CertPEM) != ""
	hasPub := strings.TrimSpace(req.PubKeyPEM) != ""
	if hasCert && hasPub {
		return nil, ErrCertAndPubKeyBothSupplied
	}
	if !hasCert && !hasPub {
		return nil, ErrCertOrPubKeyRequired
	}

	var (
		pubKey          crypto.PublicKey
		certThumb       string
		canonicalPubPEM string
	)

	if hasCert {
		cert, err := parseAndValidateCert(req.CertPEM, s.externalCAPool())
		if err != nil {
			return nil, err
		}
		pubKey = cert.PublicKey
		certThumb = certThumbprint(cert.Raw)
		canonicalPubPEM, err = marshalPublicKeyPEM(cert.PublicKey)
		if err != nil {
			return nil, &CertEncodeError{Err: err}
		}
	} else {
		pk, err := parsePublicKeyPEM(strings.TrimSpace(req.PubKeyPEM))
		if err != nil {
			return nil, ErrInvalidPubKeyPEM
		}
		pubKey = pk
		// Canonicalize so we always store PKIX-formatted PEM.
		canonical, err := marshalPublicKeyPEM(pk)
		if err != nil {
			return nil, &CertEncodeError{Err: err}
		}
		canonicalPubPEM = canonical
	}

	// Seal the share if one was supplied.
	var sealedB64 string
	if len(req.ShareBytes) > 0 {
		sealed, err := sealShareToPublicKey(pubKey, req.ShareBytes)
		if err != nil {
			return nil, err
		}
		sealedB64 = base64.StdEncoding.EncodeToString(sealed)
	}

	// Persist the registry entry. The caller MUST have wired a registry
	// (typically via cmd/qrdbd boot path); we attach a registry on the fly
	// only if the barrier is currently usable.
	s.mu.Lock()
	if s.officerRegistry == nil {
		reg, regErr := NewOfficerRegistry(s.barrier)
		if regErr != nil {
			s.mu.Unlock()
			return nil, regErr
		}
		s.officerRegistry = reg
	}
	registry := s.officerRegistry
	s.mu.Unlock()

	record := &PersistentOfficer{
		Username:       req.Username,
		CertThumbprint: certThumb,
		PubKeyPEM:      canonicalPubPEM,
		ShareIndex:     req.ShareIndex,
		AuthFactor:     req.AuthFactor,
		EnrolledAt:     time.Now().UTC(),
	}
	if err := registry.Save(ctx, record); err != nil {
		return nil, err
	}

	s.logger.Info("external officer enrolled",
		slog.String("username", req.Username),
		slog.String("auth_factor", req.AuthFactor),
		slog.Int("share_index", req.ShareIndex),
		slog.Bool("with_cert", hasCert))

	return &EnrollExternalOfficerResponse{
		SealedShare: sealedB64,
		Officer:     record,
	}, nil
}

// externalCAPool returns the configured external CA pool with the
// service lock held only briefly. Returns nil when no pool is set.
func (s *CeremonyService) externalCAPool() *x509.CertPool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.extCAPool
}

// wipeIfNonNil zeroes a byte slice when it is non-nil and non-empty.
func wipeIfNonNil(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// parseAndValidateCert decodes a PEM certificate, verifies its validity
// window, and chains it against the supplied CA pool. The CA pool may
// be nil to skip chaining (tests only — production uses always pass a
// configured pool).
func parseAndValidateCert(certPEM string, pool *x509.CertPool) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, ErrInvalidCertPEM
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, ErrInvalidCertPEM
	}

	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return nil, ErrCertExpired
	}

	if pool == nil {
		// No pool configured: refuse rather than silently trust. Callers
		// that intentionally accept raw public keys should pass them via
		// PubKeyPEM, not CertPEM.
		return nil, ErrCertUntrusted
	}

	opts := x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	if _, verifyErr := cert.Verify(opts); verifyErr != nil {
		return nil, ErrCertUntrusted
	}
	return cert, nil
}

// sealShareToPublicKey encrypts shareBytes to the holder of pubKey using
// an algorithm-appropriate envelope. The plaintext slice is NOT wiped
// here — callers must do that after the seal is complete.
func sealShareToPublicKey(pubKey crypto.PublicKey, shareBytes []byte) ([]byte, error) {
	switch pk := pubKey.(type) {
	case *rsa.PublicKey:
		return sealWithRSAOAEP(pk, shareBytes)
	case *ecdsa.PublicKey:
		return sealWithECIES(pk, shareBytes)
	case ed25519.PublicKey:
		// Ed25519 is a signature algorithm, so the only way to seal to such a
		// key is the birational map to X25519 -- and X25519 is not on the FIPS
		// 140-3 approved list, so crypto/ecdh rejects it under
		// GODEBUG=fips140=only. There is no approved substitute for an Ed25519
		// recipient: the officer has to enrol an RSA or NIST-curve key
		// instead. Refuse here with an actionable error rather than letting a
		// low-level ECDH failure surface as "share sealing failed".
		if fips140.Enabled() {
			return nil, ErrAlgorithmNotFIPSApproved
		}
		return sealWithX25519FromEd25519(pk, shareBytes)
	default:
		return nil, ErrUnsupportedKeyAlgorithm
	}
}

// sealWithRSAOAEP encrypts shareBytes with RSA-OAEP using SHA-256.
func sealWithRSAOAEP(pub *rsa.PublicKey, shareBytes []byte) ([]byte, error) {
	if pub == nil {
		return nil, ErrUnsupportedKeyAlgorithm
	}
	enc, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, shareBytes, []byte(externalOfficerHKDFInfo))
	if err != nil {
		return nil, &EnvelopeSealError{Err: err}
	}
	return enc, nil
}

// sealWithECIES implements a minimal ECIES-style envelope on the named
// curve of pub. The wire format is:
//
//	uint16 ephemeralPubDERLen | ephemeralPubDER | 12 nonce | ciphertext+tag
//
// The content key is HKDF-SHA256(sharedSecret, info=externalOfficerHKDFInfo).
// AES-256-GCM is used for confidentiality + integrity, with externalOfficerHKDFInfo
// as the AAD.
//
// Supported curves: P-256, P-384.
func sealWithECIES(pub *ecdsa.PublicKey, shareBytes []byte) ([]byte, error) {
	if pub == nil || pub.Curve == nil {
		return nil, ErrUnsupportedKeyAlgorithm
	}

	var ecdhCurve ecdh.Curve
	switch pub.Curve {
	case elliptic.P256():
		ecdhCurve = ecdh.P256()
	case elliptic.P384():
		ecdhCurve = ecdh.P384()
	default:
		return nil, ErrUnsupportedKeyAlgorithm
	}

	recipient, err := pub.ECDH()
	if err != nil {
		return nil, &EnvelopeSealError{Err: err}
	}

	ephemeral, err := ecdhCurve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, &EnvelopeSealError{Err: err}
	}

	shared, err := ephemeral.ECDH(recipient)
	if err != nil {
		return nil, &EnvelopeSealError{Err: err}
	}
	defer wipeIfNonNil(shared)

	contentKey, err := deriveContentKey(shared)
	if err != nil {
		return nil, err
	}
	defer wipeIfNonNil(contentKey)

	aead, err := newAESGCM(contentKey)
	if err != nil {
		return nil, err
	}

	nonce, ciphertext, err := sealEnvelope(aead, shareBytes, []byte(externalOfficerHKDFInfo))
	if err != nil {
		return nil, err
	}

	ephemeralDER, err := x509.MarshalPKIXPublicKey(ephemeral.PublicKey())
	if err != nil {
		return nil, &EnvelopeSealError{Err: err}
	}

	return packEnvelope(ephemeralDER, nonce, ciphertext), nil
}

// sealWithX25519FromEd25519 converts an Ed25519 public key to its
// corresponding X25519 form via the standard birational map and then
// performs an X25519+HKDF+AES-256-GCM seal.
func sealWithX25519FromEd25519(pub ed25519.PublicKey, shareBytes []byte) ([]byte, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, ErrUnsupportedKeyAlgorithm
	}

	x25519Pub, err := ed25519PublicToX25519(pub)
	if err != nil {
		return nil, err
	}

	// Generate ephemeral X25519 keypair.
	ephPriv := make([]byte, curve25519.ScalarSize)
	if _, err := rand.Read(ephPriv); err != nil {
		return nil, &EnvelopeSealError{Err: err}
	}
	defer wipeIfNonNil(ephPriv)

	ephPub, err := curve25519.X25519(ephPriv, curve25519.Basepoint)
	if err != nil {
		return nil, &EnvelopeSealError{Err: err}
	}

	shared, err := curve25519.X25519(ephPriv, x25519Pub)
	if err != nil {
		return nil, &EnvelopeSealError{Err: err}
	}
	defer wipeIfNonNil(shared)

	contentKey, err := deriveContentKey(shared)
	if err != nil {
		return nil, err
	}
	defer wipeIfNonNil(contentKey)

	aead, err := newAESGCM(contentKey)
	if err != nil {
		return nil, err
	}

	nonce, ciphertext, err := sealEnvelope(aead, shareBytes, []byte(externalOfficerHKDFInfo))
	if err != nil {
		return nil, err
	}

	// For X25519 we ship the raw 32-byte public point; wrap it as a
	// PKIX-style "raw key" by recording the public bytes verbatim.
	return packEnvelope(ephPub, nonce, ciphertext), nil
}

// ed25519PublicToX25519 converts an Ed25519 public key (32 bytes) to the
// corresponding X25519 public key. The mapping is well-defined: the
// Edwards point's y-coordinate maps to Montgomery u via u = (1+y)/(1-y).
//
// We use crypto/ed25519's exported helper indirectly: we lift the public
// key to a curve point via decoding and then convert. Since Go's stdlib
// does not expose this conversion, we implement it directly.
func ed25519PublicToX25519(ed25519Pub ed25519.PublicKey) ([]byte, error) {
	// Use the well-known property that the Ed25519 public key bytes
	// encode an Edwards point; the X25519 form is derived from the
	// Edwards y-coordinate. Implement via FieldElement arithmetic.
	// Since Go does not export filippo.io/edwards25519 in stdlib, use
	// the curve25519 package's explicit helper if available; otherwise
	// fall back to a manual computation.
	//
	// crypto/ecdh and golang.org/x/crypto/curve25519 do not expose the
	// conversion directly, but golang.org/x/crypto/ed25519 likewise
	// does not. We implement the conversion using filippo.io/edwards25519
	// through the indirect Go module path that is already a transitive
	// dependency of x/crypto.
	return convertEd25519ToX25519(ed25519Pub)
}

// deriveContentKey runs HKDF-SHA256 on sharedSecret to produce a 32-byte
// AES-256 key bound to externalOfficerHKDFInfo.
func deriveContentKey(sharedSecret []byte) ([]byte, error) {
	out := make([]byte, 32)
	r := hkdf.New(sha256.New, sharedSecret, nil, []byte(externalOfficerHKDFInfo))
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, &EnvelopeSealError{Err: err}
	}
	return out, nil
}

// newAESGCM constructs an AEAD over the provided 32-byte AES-256 key.
//
// cipher.NewGCMWithRandomNonce is the only GCM construction approved under
// GODEBUG=fips140=only: SP 800-38D requires the module to own nonce
// generation, so cipher.NewGCM (arbitrary IVs) is rejected. The returned AEAD
// reports NonceSize() == 0 and prepends the nonce it samples to the sealed
// output -- use sealEnvelope rather than calling Seal directly.
func newAESGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, &EnvelopeSealError{Err: err}
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, &EnvelopeSealError{Err: err}
	}
	return aead, nil
}

// sealEnvelope seals plaintext under aead and splits the module-generated
// nonce back off the front of the result, because the envelope wire format
// carries the nonce as its own field. The bytes handed to packEnvelope are
// identical to those produced when this code sampled the nonce itself, so
// envelopes already delivered to external officers remain decryptable.
func sealEnvelope(aead cipher.AEAD, plaintext, aad []byte) (nonce, ciphertext []byte, err error) {
	// #nosec G407 -- the nonce argument MUST be nil here: this AEAD is cipher.NewGCMWithRandomNonce, which generates a fresh 96-bit nonce inside the FIPS module on every Seal and prepends it to the ciphertext. gosec reads the nil as a hardcoded IV; passing anything else makes Seal panic.
	sealed := aead.Seal(nil, nil, plaintext, aad)
	if len(sealed) < envelopeNonceLen {
		return nil, nil, &EnvelopeSealError{Err: ErrShareSealFailed}
	}
	return sealed[:envelopeNonceLen:envelopeNonceLen], sealed[envelopeNonceLen:], nil
}

// packEnvelope concatenates the wire fields with a length-prefixed
// ephemeral public key.
func packEnvelope(ephemeralPub, nonce, ciphertext []byte) []byte {
	out := make([]byte, 0, 2+len(ephemeralPub)+len(nonce)+len(ciphertext))
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(ephemeralPub)))
	out = append(out, hdr[:]...)
	out = append(out, ephemeralPub...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)
	return out
}

// EnvelopeSealError wraps cryptographic seal failures. The underlying
// error is preserved for diagnostics but is never the public sentinel
// ErrShareSealFailed; callers can use errors.Is(err, ErrShareSealFailed)
// to detect any seal failure.
type EnvelopeSealError struct {
	Err error
}

// Error returns the description.
func (e *EnvelopeSealError) Error() string {
	return "ceremony: envelope seal failed: " + e.Err.Error()
}

// Unwrap returns the underlying cause.
func (e *EnvelopeSealError) Unwrap() error {
	return e.Err
}

// Is matches the public sentinel ErrShareSealFailed so callers using
// errors.Is can detect any sealing failure regardless of cause.
func (e *EnvelopeSealError) Is(target error) bool {
	return target == ErrShareSealFailed
}
