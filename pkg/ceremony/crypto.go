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
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"errors"
	"math/big"
)

// verifyNonceSignature verifies that the provided signature was produced
// by the holder of the given public key over the nonce. Supports RSA
// (PKCS1v15 with SHA-256), ECDSA (ASN.1 DER with SHA-256), and Ed25519.
func verifyNonceSignature(pubKey crypto.PublicKey, nonce, signature []byte) error {
	switch key := pubKey.(type) {
	case *rsa.PublicKey:
		digest := sha256.Sum256(nonce)
		if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
			return ErrChallengeVerificationFailed
		}
		return nil

	case *ecdsa.PublicKey:
		digest := sha256.Sum256(nonce)
		// Parse ASN.1 DER encoded ECDSA signature.
		var sig struct {
			R, S *big.Int
		}
		if _, err := asn1.Unmarshal(signature, &sig); err != nil {
			return ErrChallengeVerificationFailed
		}
		if !ecdsa.Verify(key, digest[:], sig.R, sig.S) {
			return ErrChallengeVerificationFailed
		}
		return nil

	case ed25519.PublicKey:
		if !ed25519.Verify(key, nonce, signature) {
			return ErrChallengeVerificationFailed
		}
		return nil

	default:
		return ErrChallengeVerificationFailed
	}
}

// ecdsaSignatureSize returns the maximum ASN.1 DER encoded signature
// size for the given curve. This is used for pre-allocation but is not
// currently exposed.
func ecdsaSignatureSize(curve elliptic.Curve) int {
	// ASN.1 DER: SEQUENCE { INTEGER r, INTEGER s }
	// Each integer is at most (curve_bytes + 1) bytes plus 2 bytes tag+length.
	// Sequence overhead is 2-4 bytes.
	byteLen := (curve.Params().BitSize + 7) / 8
	return 2 + 2 + (byteLen + 2) + 2 + (byteLen + 2)
}

// computeSPKIPin computes the SPKI pin for a certificate by taking the
// SHA-256 hash of the DER-encoded SubjectPublicKeyInfo and returning
// the result as a standard base64 string.
func computeSPKIPin(cert *x509.Certificate) (string, error) {
	if cert == nil {
		return "", &SPKIPinError{Err: errors.New("certificate is nil")}
	}
	digest := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(digest[:]), nil
}
