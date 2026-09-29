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
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
)

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

// thumbprintsEqual compares two certificate thumbprints in constant time. An
// empty presented thumbprint never matches: a caller with no verified client
// certificate is not the enrolled holder.
func thumbprintsEqual(presented, enrolled string) bool {
	if presented == "" || enrolled == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(enrolled)) == 1
}

// parsePublicKeyPEM parses a PKIX PUBLIC KEY block.
func parsePublicKeyPEM(pemStr string) (crypto.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, ErrInvalidPubKeyPEM
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	return pub, nil
}
