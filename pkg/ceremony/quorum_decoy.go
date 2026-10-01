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
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// Decoy quorum challenges.
//
// A quorum challenge for a real holder carries that holder's enrolled public
// key and certificate thumbprint, and both are the same on every request. A
// decoy -- the answer for a name outside the roster -- must therefore be just
// as stable, or two requests for one name would tell a holder from a stranger.
// Its key and thumbprint are derived from a per-scope random seed and the
// name: thumbprint = HMAC-SHA256(seed, name), key scalar = HKDF-SHA256(seed,
// domain || name), on the curve the roster's holders predominantly use. The
// seed lives as long as the scope instance, which the composer keeps for the
// life of the process; a restart draws a new one, so a caller who observes a
// decoy across a restart sees it change.
//
// Every quorum-challenge request derives the decoy for the name it asks
// about, whether or not the name is a holder, so a real and a decoy answer
// cost the same work (decoyIdentityFor), and the cost does not reveal which.

// decoyKeyKind is a public-key algorithm a decoy can be derived for.
type decoyKeyKind int

// The derivable kinds, in tie-break order: when the roster uses two kinds
// equally often, the earlier one is chosen.
const (
	decoyP256 decoyKeyKind = iota
	decoyP384
	decoyP521
	decoyEd25519
)

// String names the kind in derivation labels.
func (k decoyKeyKind) String() string { return decoyKindNames[k] }

// decoyKindNames labels each kind.
var decoyKindNames = map[decoyKeyKind]string{
	decoyP256:    "ecdsa-p256",
	decoyP384:    "ecdsa-p384",
	decoyP521:    "ecdsa-p521",
	decoyEd25519: "ed25519",
}

// decoyCurves maps the ECDSA kinds to their curves.
var decoyCurves = map[decoyKeyKind]elliptic.Curve{
	decoyP256: elliptic.P256(),
	decoyP384: elliptic.P384(),
	decoyP521: elliptic.P521(),
}

// decoyCurveKinds maps a curve name back to its kind.
var decoyCurveKinds = map[string]decoyKeyKind{
	elliptic.P256().Params().Name: decoyP256,
	elliptic.P384().Params().Name: decoyP384,
	elliptic.P521().Params().Name: decoyP521,
}

// Derivation labels. Each output is bound to its purpose and version.
const (
	decoyThumbprintLabel = "qrdb/quorum-decoy/thumbprint/v1"
	decoyKeyLabel        = "qrdb/quorum-decoy/key/v1"
	// decoyScalarAttempts bounds the candidate scalars tried for one name.
	// A candidate is rejected only when it is not below the curve order,
	// which for P-256 and P-384 happens with probability under 2^-32, and
	// for P-521 (after masking to 521 bits) under 1/2; 64 failures in a row
	// do not happen.
	decoyScalarAttempts = 64
)

// decoyIdentity is the stable public face a quorum challenge shows for a
// name: the SubjectPublicKeyInfo and certificate thumbprint an mtls challenge
// carries.
type decoyIdentity struct {
	spki       []byte
	thumbprint string
}

// spkiDecoyKind classifies an mtls factor's SubjectPublicKeyInfo. RSA and any
// other algorithm report false: no decoy is derived for them.
func spkiDecoyKind(spki []byte) (decoyKeyKind, bool) {
	pub, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return 0, false
	}
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		kind, ok := decoyCurveKinds[k.Curve.Params().Name]
		return kind, ok
	case ed25519.PublicKey:
		return decoyEd25519, true
	}
	return 0, false
}

// rosterDecoyKind returns the key kind the roster's holders' mtls factors
// predominantly use, so a decoy's key looks like a real holder's. A roster
// with no derivable mtls key -- empty, or every holder's key RSA -- yields
// P-256, the kind the built-in tooling enrols.
func rosterDecoyKind(roster *seal.HolderRoster) decoyKeyKind {
	counts := make(map[decoyKeyKind]int, len(decoyKindNames))
	if roster != nil {
		for _, h := range roster.Holders {
			for _, f := range h.Factors {
				if f.Name != mtls.Name {
					continue
				}
				if kind, ok := spkiDecoyKind(f.PublicKey); ok {
					counts[kind]++
				}
			}
		}
	}
	best := decoyP256
	for kind := decoyP256; kind <= decoyEd25519; kind++ {
		if counts[kind] > counts[best] {
			best = kind
		}
	}
	return best
}

// decoyIdentityFor derives username's decoy identity of the given kind from
// the scope's seed. The same name always yields the same identity for the
// life of the scope.
func (h *holderScope) decoyIdentityFor(username string, kind decoyKeyKind) (*decoyIdentity, error) {
	mac := hmac.New(sha256.New, h.decoySeed)
	mac.Write([]byte(decoyThumbprintLabel + "\x00" + username))
	thumb := hex.EncodeToString(mac.Sum(nil))

	pub, err := h.decoyPublicKey(username, kind)
	if err != nil {
		return nil, err
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, ErrInitFailed
	}
	return &decoyIdentity{spki: spki, thumbprint: thumb}, nil
}

// decoyPublicKey derives username's decoy public key of the given kind.
func (h *holderScope) decoyPublicKey(username string, kind decoyKeyKind) (crypto.PublicKey, error) {
	info := decoyKeyLabel + "\x00" + string(h.domain) + "\x00" + username + "\x00" + kind.String()
	if kind == decoyEd25519 {
		seed, err := hkdf.Key(sha256.New, h.decoySeed, nil, info, ed25519.SeedSize)
		if err != nil {
			return nil, ErrInitFailed
		}
		return ed25519.NewKeyFromSeed(seed).Public(), nil
	}
	curve := decoyCurves[kind]
	size := (curve.Params().BitSize + 7) / 8
	excess := uint(size*8 - curve.Params().BitSize)
	for attempt := 0; attempt < decoyScalarAttempts; attempt++ {
		scalar, err := hkdf.Key(sha256.New, h.decoySeed, nil, info+"\x00"+strconv.Itoa(attempt), size)
		if err != nil {
			return nil, ErrInitFailed
		}
		scalar[0] &= 0xff >> excess
		if key, err := ecdsa.ParseRawPrivateKey(curve, scalar); err == nil {
			return &key.PublicKey, nil
		}
	}
	return nil, ErrInitFailed
}
