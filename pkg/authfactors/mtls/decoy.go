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
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

var _ authfactors.DecoyEnroller = (*Factor)(nil)

// KeyKind is a certificate key type the mtls factor enrols: the ECDSA curves
// P-256, P-384 and P-521, and Ed25519. RSA is not among them.
type KeyKind int

// The key kinds, in tie-break order: when enrollments use two kinds equally
// often, the earlier one is the predominant one.
const (
	KeyP256 KeyKind = iota
	KeyP384
	KeyP521
	KeyEd25519
)

// String names the kind.
func (k KeyKind) String() string { return keyKindNames[k] }

// keyKindNames labels each kind.
var keyKindNames = map[KeyKind]string{
	KeyP256:    "ecdsa-p256",
	KeyP384:    "ecdsa-p384",
	KeyP521:    "ecdsa-p521",
	KeyEd25519: "ed25519",
}

// keyKindCurves maps the ECDSA kinds to their curves.
var keyKindCurves = map[KeyKind]elliptic.Curve{
	KeyP256: elliptic.P256(),
	KeyP384: elliptic.P384(),
	KeyP521: elliptic.P521(),
}

// curveKeyKinds maps a curve name back to its kind.
var curveKeyKinds = map[string]KeyKind{
	elliptic.P256().Params().Name: KeyP256,
	elliptic.P384().Params().Name: KeyP384,
	elliptic.P521().Params().Name: KeyP521,
}

// PublicKeyKind classifies a public key. Any key the factor does not enrol --
// RSA among them -- reports false.
func PublicKeyKind(pub crypto.PublicKey) (KeyKind, bool) {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		kind, ok := curveKeyKinds[k.Curve.Params().Name]
		return kind, ok
	case ed25519.PublicKey:
		return KeyEd25519, true
	}
	return 0, false
}

// spkiKeyKind classifies a SubjectPublicKeyInfo.
func spkiKeyKind(spki []byte) (KeyKind, bool) {
	pub, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return 0, false
	}
	return PublicKeyKind(pub)
}

// predominantKeyKind returns the kind most of like use; P-256, the kind the
// built-in tooling enrols, when none is classifiable.
func predominantKeyKind(like []*authfactors.Enrollment) KeyKind {
	counts := make(map[KeyKind]int, len(keyKindNames))
	for _, e := range like {
		if e == nil {
			continue
		}
		if kind, ok := spkiKeyKind(e.PublicKey); ok {
			counts[kind]++
		}
	}
	best := KeyP256
	for kind := KeyP256; kind <= KeyEd25519; kind++ {
		if counts[kind] > counts[best] {
			best = kind
		}
	}
	return best
}

// Decoy derivation labels; each output is bound to its purpose and version.
const (
	decoyThumbprintLabel = "qrdb/mtls-decoy/thumbprint/v1"
	decoyKeyLabel        = "qrdb/mtls-decoy/key/v1"
	// decoyScalarAttempts bounds the candidate scalars tried for one user. A
	// candidate is rejected only when it is not below the curve order: for
	// P-256 and P-384 with probability under 2^-32, for P-521 (masked to 521
	// bits) under 1/2. 64 failures in a row do not happen.
	decoyScalarAttempts = 64
)

// DecoyDerivationError reports that no decoy key could be derived.
type DecoyDerivationError struct{}

// Error returns a human-readable description.
func (e *DecoyDerivationError) Error() string {
	return "mtls: decoy key derivation failed"
}

// DecoyEnrollment implements authfactors.DecoyEnroller: an mtls enrollment
// for userID whose certificate thumbprint is HMAC-SHA256(seed, userID) and
// whose key -- of the kind like predominantly uses -- has its scalar from
// HKDF-SHA256(seed, userID, kind). The same (seed, userID) always yields the
// same enrollment, so the challenge issued from it shows the same public key
// and thumbprint on every request, as a real holder's does.
func (f *Factor) DecoyEnrollment(seed []byte, userID string, like []*authfactors.Enrollment) (*authfactors.Enrollment, error) {
	mac := hmac.New(sha256.New, seed)
	mac.Write([]byte(decoyThumbprintLabel + "\x00" + userID))
	thumb := hex.EncodeToString(mac.Sum(nil))

	pub, err := decoyPublicKey(seed, userID, predominantKeyKind(like))
	if err != nil {
		return nil, err
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, &DecoyDerivationError{}
	}
	return &authfactors.Enrollment{
		EnrollmentID: Name + ":" + thumb,
		UserID:       userID,
		FactorName:   Name,
		PublicKey:    spki,
		Metadata:     map[string]string{MetaThumbprint: thumb},
		EnrolledAt:   time.Time{},
	}, nil
}

// decoyPublicKey derives userID's decoy public key of the given kind.
func decoyPublicKey(seed []byte, userID string, kind KeyKind) (crypto.PublicKey, error) {
	info := decoyKeyLabel + "\x00" + userID + "\x00" + kind.String()
	if kind == KeyEd25519 {
		edSeed, err := hkdf.Key(sha256.New, seed, nil, info, ed25519.SeedSize)
		if err != nil {
			return nil, &DecoyDerivationError{}
		}
		return ed25519.NewKeyFromSeed(edSeed).Public(), nil
	}
	curve := keyKindCurves[kind]
	size := (curve.Params().BitSize + 7) / 8
	excess := uint(size*8 - curve.Params().BitSize)
	for attempt := 0; attempt < decoyScalarAttempts; attempt++ {
		scalar, err := hkdf.Key(sha256.New, seed, nil, info+"\x00"+strconv.Itoa(attempt), size)
		if err != nil {
			return nil, &DecoyDerivationError{}
		}
		scalar[0] &= 0xff >> excess
		if key, err := ecdsa.ParseRawPrivateKey(curve, scalar); err == nil {
			return &key.PublicKey, nil
		}
	}
	return nil, &DecoyDerivationError{}
}
