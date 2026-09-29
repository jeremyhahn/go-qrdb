// Copyright 2026 Jeremy Hahn
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

package keydelivery

import (
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hpke"
	"crypto/x509"
	"sort"
)

// KEM names. A name selects a whole RFC 9180 ciphersuite — KEM, KDF and AEAD
// — so a recipient key recorded with its KEM name fully determines how a
// payload sealed to it is opened. The names are wire and storage identifiers:
// they are recorded next to every enrolled share-holder encryption key and
// carried in every share envelope, and must never be renamed.
const (
	// KEMP256 is DHKEM(P-256, HKDF-SHA256) with HKDF-SHA256 and AES-256-GCM.
	// It is what node key delivery has always used.
	KEMP256 = "dhkem-p256"

	// KEMP384 is DHKEM(P-384, HKDF-SHA384) with HKDF-SHA384 and AES-256-GCM.
	KEMP384 = "dhkem-p384"

	// KEMX25519 is DHKEM(X25519, HKDF-SHA256) with HKDF-SHA256 and
	// AES-256-GCM. It serves hardware keys that support X25519 but not the
	// NIST curves.
	KEMX25519 = "dhkem-x25519"

	// KEMMLKEM768X25519 is the post-quantum hybrid MLKEM768-X25519 ("X-Wing",
	// draft-ietf-hpke-pq) with HKDF-SHA256 and AES-256-GCM. It is provided by
	// the Go standard library's crypto/hpke; a payload sealed to it stays
	// confidential unless BOTH ML-KEM-768 and X25519 are broken.
	KEMMLKEM768X25519 = "mlkem768-x25519"
)

// Suite is one algorithm-agile HPKE ciphersuite. It is the single HPKE
// implementation in this module: node key delivery and share-holder envelopes
// both seal and open through it.
type Suite struct {
	name string
	kem  hpke.KEM
	kdf  hpke.KDF
	aead hpke.AEAD
}

// suites is the KEM registry. It is fixed at build time: a suite is a
// security decision, not a runtime extension point.
var suites = map[string]*Suite{
	KEMP256:           {name: KEMP256, kem: hpke.DHKEM(ecdh.P256()), kdf: hpke.HKDFSHA256(), aead: hpke.AES256GCM()},
	KEMP384:           {name: KEMP384, kem: hpke.DHKEM(ecdh.P384()), kdf: hpke.HKDFSHA384(), aead: hpke.AES256GCM()},
	KEMX25519:         {name: KEMX25519, kem: hpke.DHKEM(ecdh.X25519()), kdf: hpke.HKDFSHA256(), aead: hpke.AES256GCM()},
	KEMMLKEM768X25519: {name: KEMMLKEM768X25519, kem: hpke.MLKEM768X25519(), kdf: hpke.HKDFSHA256(), aead: hpke.AES256GCM()},
}

// UnsupportedKEMError is returned when a KEM name is not in the registry.
type UnsupportedKEMError struct {
	// Name is the KEM name that was asked for.
	Name string
}

// Error returns a human-readable description.
func (e *UnsupportedKEMError) Error() string {
	return "keydelivery: unsupported KEM " + `"` + e.Name + `"`
}

// UnsupportedRecipientKeyError is returned when a public or private key cannot
// be used with any registered KEM (for example an RSA or Ed25519 key, or an
// ECDSA key on an unregistered curve).
type UnsupportedRecipientKeyError struct {
	// Reason says what was wrong with the key.
	Reason string
}

// Error returns a human-readable description.
func (e *UnsupportedRecipientKeyError) Error() string {
	return "keydelivery: key is not usable with any registered KEM: " + e.Reason
}

// KEMKeyMismatchError is returned when a key belongs to a different KEM than
// the suite asked to use it.
type KEMKeyMismatchError struct {
	// Suite is the suite the operation ran under.
	Suite string
}

// Error returns a human-readable description.
func (e *KEMKeyMismatchError) Error() string {
	return "keydelivery: key does not belong to KEM " + e.Suite
}

// LookupSuite returns the suite registered under name.
func LookupSuite(name string) (*Suite, error) {
	s, ok := suites[name]
	if !ok {
		return nil, &UnsupportedKEMError{Name: name}
	}
	return s, nil
}

// SuiteNames returns every registered KEM name in lexicographic order.
func SuiteNames() []string {
	names := make([]string, 0, len(suites))
	for n := range suites {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Name returns the suite's KEM name.
func (s *Suite) Name() string { return s.name }

// PublicKey deserializes a recipient public key in this suite's KEM encoding
// (RFC 9180 SerializePublicKey), rejecting malformed input.
func (s *Suite) PublicKey(raw []byte) (hpke.PublicKey, error) {
	if len(raw) == 0 {
		return nil, ErrNilRecipientKey
	}
	pk, err := s.kem.NewPublicKey(raw)
	if err != nil {
		return nil, &UnsupportedRecipientKeyError{Reason: err.Error()}
	}
	return pk, nil
}

// PrivateKey deserializes a private key in this suite's KEM encoding (RFC 9180
// SerializePrivateKey).
func (s *Suite) PrivateKey(raw []byte) (hpke.PrivateKey, error) {
	if len(raw) == 0 {
		return nil, ErrNilRecipientKey
	}
	sk, err := s.kem.NewPrivateKey(raw)
	if err != nil {
		return nil, &UnsupportedRecipientKeyError{Reason: err.Error()}
	}
	return sk, nil
}

// GenerateKey generates a fresh key pair for this suite.
func (s *Suite) GenerateKey() (hpke.PrivateKey, error) {
	return s.kem.GenerateKey()
}

// Seal HPKE-seals plaintext to the recipient public key raw (this suite's KEM
// encoding) under info, returning enc ‖ ciphertext. The caller owns plaintext.
func (s *Suite) Seal(raw, info, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, ErrEmptyPlaintext
	}
	pk, err := s.PublicKey(raw)
	if err != nil {
		return nil, err
	}
	return hpke.Seal(pk, s.kdf, s.aead, info, plaintext)
}

// Open decrypts a payload produced by Seal. Every failure after the key check
// is ErrDecrypt, deliberately opaque. The caller owns and must wipe the
// returned plaintext.
func (s *Suite) Open(sk hpke.PrivateKey, info, payload []byte) ([]byte, error) {
	if sk == nil {
		return nil, ErrNilRecipientKey
	}
	if sk.KEM().ID() != s.kem.ID() {
		return nil, &KEMKeyMismatchError{Suite: s.name}
	}
	pt, err := hpke.Open(sk, s.kdf, s.aead, info, payload)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// RecipientFromPublicKey maps a crypto.PublicKey onto the registered suite
// that can seal to it and returns the key in that suite's encoding. ECDSA and
// ECDH keys on P-256 or P-384, and ECDH X25519 keys, are accepted; everything
// else (RSA, Ed25519, other curves) is refused with
// UnsupportedRecipientKeyError. An ML-KEM hybrid key has no crypto.PublicKey
// form and is enrolled by its raw encoding instead.
func RecipientFromPublicKey(pub crypto.PublicKey) (string, []byte, error) {
	var ek *ecdh.PublicKey
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		converted, err := k.ECDH()
		if err != nil {
			return "", nil, &UnsupportedRecipientKeyError{Reason: err.Error()}
		}
		ek = converted
	case *ecdh.PublicKey:
		ek = k
	default:
		return "", nil, &UnsupportedRecipientKeyError{Reason: "key type has no registered KEM"}
	}
	name, err := suiteForCurve(ek.Curve())
	if err != nil {
		return "", nil, err
	}
	return name, ek.Bytes(), nil
}

// RecipientFromPKIX parses a DER SubjectPublicKeyInfo and maps it as
// RecipientFromPublicKey does.
func RecipientFromPKIX(der []byte) (string, []byte, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return "", nil, &UnsupportedRecipientKeyError{Reason: err.Error()}
	}
	return RecipientFromPublicKey(pub)
}

// PrivateKeyFor converts an ECDSA or ECDH private key to an HPKE private key
// of the named suite, refusing a key that belongs to a different KEM.
func PrivateKeyFor(name string, priv crypto.PrivateKey) (hpke.PrivateKey, error) {
	s, err := LookupSuite(name)
	if err != nil {
		return nil, err
	}
	var ek *ecdh.PrivateKey
	switch k := priv.(type) {
	case *ecdsa.PrivateKey:
		converted, cerr := k.ECDH()
		if cerr != nil {
			return nil, &UnsupportedRecipientKeyError{Reason: cerr.Error()}
		}
		ek = converted
	case *ecdh.PrivateKey:
		ek = k
	default:
		return nil, &UnsupportedRecipientKeyError{Reason: "key type has no registered KEM"}
	}
	curveSuite, err := suiteForCurve(ek.Curve())
	if err != nil {
		return nil, err
	}
	if curveSuite != s.name {
		return nil, &KEMKeyMismatchError{Suite: s.name}
	}
	return hpke.NewDHKEMPrivateKey(ek)
}

// suiteForCurve maps an ECDH curve onto its registered DHKEM suite name.
func suiteForCurve(c ecdh.Curve) (string, error) {
	switch c {
	case ecdh.P256():
		return KEMP256, nil
	case ecdh.P384():
		return KEMP384, nil
	case ecdh.X25519():
		return KEMX25519, nil
	default:
		return "", &UnsupportedRecipientKeyError{Reason: "curve has no registered DHKEM"}
	}
}
