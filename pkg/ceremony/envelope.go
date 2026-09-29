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
	"crypto/hpke"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// shareEnvelopeVersion is the envelope format version.
const shareEnvelopeVersion = 1

// shareEnvelopeInfoDomain domain-separates the HPKE info of a share envelope
// from every other HPKE use (node key delivery uses its own domain).
const shareEnvelopeInfoDomain = "qrdb/share-envelope/v1"

// ShareEnvelope is one holder's share, HPKE-sealed to the holder's enrolled
// encryption key. It is what a claim delivers, exactly once, and the only form
// in which a share leaves the node that distributed it. The header is public;
// every header field is bound into the HPKE info, so a header edited in
// transit makes the envelope unopenable rather than misleading.
type ShareEnvelope struct {
	// Version is the envelope format version.
	Version int `json:"version"`

	// KEM names the HPKE suite (see pkg/keydelivery) the holder's key
	// belongs to.
	KEM string `json:"kem"`

	// Scope is the barrier scope ("system", "recovery", "tenant:<id>").
	Scope string `json:"scope"`

	// Domain is the key domain the share reconstructs.
	Domain seal.QuorumDomain `json:"domain"`

	// Holder is the recipient's username.
	Holder string `json:"holder"`

	// ShareIndex is the share's x-coordinate.
	ShareIndex int `json:"share_index"`

	// Threshold and Total describe the share set.
	Threshold int `json:"threshold"`
	Total     int `json:"total"`

	// Distribution identifies the distribution the share belongs to.
	Distribution string `json:"distribution"`

	// RosterDigest is the hex SHA-256 of the roster the share set was bound
	// to.
	RosterDigest string `json:"roster_digest"`

	// Ciphertext is the HPKE output (encapsulated key ‖ AEAD ciphertext) of
	// the base64 share value.
	Ciphertext []byte `json:"ciphertext"`
}

// info renders the envelope's HPKE info: the domain separator followed by
// every header field, each length-prefixed so no two headers collide.
func (e *ShareEnvelope) info() []byte {
	var b []byte
	lp := func(s string) {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(s)))
		b = append(append(b, l[:]...), s...)
	}
	u32 := func(v int) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(v))
		b = append(b, n[:]...)
	}
	lp(shareEnvelopeInfoDomain)
	u32(e.Version)
	lp(e.KEM)
	lp(e.Scope)
	lp(string(e.Domain))
	lp(e.Holder)
	u32(e.ShareIndex)
	u32(e.Threshold)
	u32(e.Total)
	lp(e.Distribution)
	lp(e.RosterDigest)
	return b
}

// sealShareEnvelope seals shareValue to the holder's enrolled key. shareValue
// is read, never retained; the caller owns it.
func sealShareEnvelope(scope BarrierScope, roster *seal.HolderRoster, digest []byte, h *seal.RosterHolder, rec *PersistentOfficer, shareValue []byte) ([]byte, error) {
	suite, err := keydelivery.LookupSuite(rec.EncryptionKEM)
	if err != nil {
		return nil, &ShareEnvelopeError{Holder: h.Username, Err: err}
	}
	env := &ShareEnvelope{
		Version:      shareEnvelopeVersion,
		KEM:          rec.EncryptionKEM,
		Scope:        scope.String(),
		Domain:       roster.Domain,
		Holder:       h.Username,
		ShareIndex:   h.ShareIndex,
		Threshold:    roster.Threshold,
		Total:        roster.Total,
		Distribution: roster.Distribution,
		RosterDigest: hex.EncodeToString(digest),
	}
	ct, err := suite.Seal(rec.EncryptionKey, env.info(), shareValue)
	if err != nil {
		return nil, &ShareEnvelopeError{Holder: h.Username, Err: err}
	}
	env.Ciphertext = ct
	return json.Marshal(env)
}

// ParseShareEnvelope decodes a claimed envelope.
func ParseShareEnvelope(raw []byte) (*ShareEnvelope, error) {
	var env ShareEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &ShareEnvelopeError{Err: err}
	}
	if env.Version != shareEnvelopeVersion || len(env.Ciphertext) == 0 {
		return nil, &ShareEnvelopeError{Holder: env.Holder, Err: ErrShareEnvelopeMalformed}
	}
	return &env, nil
}

// Open decrypts the envelope with the holder's private key, which must belong
// to the envelope's KEM. It returns the base64 share value the holder submits
// to a quorum; the caller owns and should wipe it. Opening needs no server:
// holders open their envelopes offline.
func (e *ShareEnvelope) Open(sk hpke.PrivateKey) ([]byte, error) {
	suite, err := keydelivery.LookupSuite(e.KEM)
	if err != nil {
		return nil, &ShareEnvelopeError{Holder: e.Holder, Err: err}
	}
	pt, err := suite.Open(sk, e.info(), e.Ciphertext)
	if err != nil {
		return nil, &ShareEnvelopeError{Holder: e.Holder, Err: err}
	}
	return pt, nil
}

// OpenWithKey opens the envelope with an ECDSA or ECDH private key — the
// holder's certificate key, or a separately enrolled DHKEM key — converted
// to the envelope's KEM. Holders whose envelope key is an X25519 or hybrid
// HPKE key load it with keydelivery.Suite.PrivateKey and call Open.
func (e *ShareEnvelope) OpenWithKey(priv crypto.PrivateKey) ([]byte, error) {
	sk, err := keydelivery.PrivateKeyFor(e.KEM, priv)
	if err != nil {
		return nil, &ShareEnvelopeError{Holder: e.Holder, Err: err}
	}
	return e.Open(sk)
}
