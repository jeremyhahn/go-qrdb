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

package keydelivery

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/hpke"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"time"

	"github.com/jeremyhahn/go-enclave"
)

// infoDomain is the HPKE info domain-separation prefix. Bumping it breaks
// cross-version compatibility intentionally.
const infoDomain = "qrdb/keydelivery/v1"

// ErrUnsupportedKey indicates the certificate key is not a usable DHKEM(P-256)
// key (wrong curve or invalid point).
var ErrUnsupportedKey = errors.New("keydelivery: recipient key is not a usable P-256 key")

// hpkeKDF/hpkeAEAD pin the RFC 9180 ciphersuite alongside DHKEM(P-256):
// HKDF-SHA256 + AES-256-GCM. AES-256-GCM matches the 256-bit barrier root keys.
func hpkeKDF() hpke.KDF   { return hpke.HKDFSHA256() }
func hpkeAEAD() hpke.AEAD { return hpke.AES256GCM() }

// recipientPublicKey converts an ECDSA certificate public key to an HPKE
// DHKEM(P-256) public key, rejecting any non-P-256 key fail-closed so the KEM
// curve is pinned and cannot be downgraded by an unexpected key type.
func recipientPublicKey(pub *ecdsa.PublicKey) (hpke.PublicKey, error) {
	ecdhPub, err := pub.ECDH()
	if err != nil {
		return nil, errors.Join(ErrUnsupportedKey, err)
	}
	if ecdhPub.Curve() != ecdh.P256() {
		return nil, ErrUnsupportedKey
	}
	return hpke.NewDHKEMPublicKey(ecdhPub)
}

// ownPrivateKey mirrors recipientPublicKey for the receiver's private key.
func ownPrivateKey(priv *ecdsa.PrivateKey) (hpke.PrivateKey, error) {
	ecdhPriv, err := priv.ECDH()
	if err != nil {
		return nil, errors.Join(ErrUnsupportedKey, err)
	}
	if ecdhPriv.Curve() != ecdh.P256() {
		return nil, ErrUnsupportedKey
	}
	return hpke.NewDHKEMPrivateKey(ecdhPriv)
}

// sealInfo builds the canonical, unambiguous HPKE `info` binding the full
// delivery context — Meta plus the per-delivery Nonce and IssuedAt. Because
// Open re-derives this from the received envelope, ANY tampering with a bound
// field (including the freshness/replay fields) makes decryption fail, so the
// otherwise-plaintext envelope metadata is authenticated without a signature.
// Every field is length-prefixed so no two distinct contexts can collide.
func sealInfo(meta Meta, nonce []byte, issuedAt time.Time) []byte {
	var b []byte
	b = appendLP(b, []byte(infoDomain))
	b = appendLP(b, []byte(meta.TenantID))
	b = appendLP(b, []byte(meta.Database))
	b = appendU64(b, meta.ShardID)
	b = appendU64(b, meta.ReplicaID)
	b = appendLP(b, []byte(meta.TargetNodeID))
	b = appendLP(b, []byte(meta.Mode))
	b = appendLP(b, nonce)
	b = appendU64(b, uint64(issuedAt.UTC().UnixNano()))
	return b
}

// appendLP appends a length-prefixed (uint32 big-endian) byte field.
func appendLP(dst, field []byte) []byte {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(field)))
	dst = append(dst, l[:]...)
	return append(dst, field...)
}

// appendU64 appends a big-endian uint64.
func appendU64(dst []byte, v uint64) []byte {
	var u [8]byte
	binary.BigEndian.PutUint64(u[:], v)
	return append(dst, u[:]...)
}

// Seal HPKE-seals plaintext to the recipient node's ECDSA P-256 certificate
// public key, returning a fresh Envelope bound to meta. The caller owns
// plaintext and should wipe it after Seal returns; Seal does not retain it.
func Seal(recipientPub *ecdsa.PublicKey, meta Meta, plaintext []byte) (*Envelope, error) {
	if recipientPub == nil {
		return nil, ErrNilRecipientKey
	}
	if len(plaintext) == 0 {
		return nil, ErrEmptyPlaintext
	}
	if err := meta.validate(); err != nil {
		return nil, err
	}
	pk, err := recipientPublicKey(recipientPub)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	issuedAt := time.Now().UTC()

	info := sealInfo(meta, nonce, issuedAt)
	payload, err := hpke.Seal(pk, hpkeKDF(), hpkeAEAD(), info, plaintext)
	if err != nil {
		return nil, err
	}
	return &Envelope{
		Meta:     meta,
		Payload:  payload,
		Nonce:    nonce,
		IssuedAt: issuedAt,
	}, nil
}

// Receiver opens delivery envelopes addressed to one node, enforcing target
// match, a freshness window, and replay defense before HPKE decryption.
type Receiver struct {
	priv    *ecdsa.PrivateKey
	nodeID  string
	guard   *ReplayGuard
	noGuard bool
	ttl     time.Duration
	now     func() time.Time
}

// ReceiverOption configures a Receiver.
type ReceiverOption func(*Receiver)

// WithTTL overrides the freshness window (default DefaultTTL).
func WithTTL(ttl time.Duration) ReceiverOption {
	return func(r *Receiver) {
		if ttl > 0 {
			r.ttl = ttl
		}
	}
}

// WithReplayGuard supplies a shared replay guard. By default each Receiver owns
// a guard with retention equal to its TTL.
func WithReplayGuard(g *ReplayGuard) ReceiverOption {
	return func(r *Receiver) {
		if g != nil {
			r.guard = g
		}
	}
}

// WithoutReplayGuard disables the per-nonce replay guard for this Receiver. Use
// it ONLY when the same envelope is legitimately opened more than once and replay
// is bounded by another mechanism. The cluster-migration receiver uses it: the
// envelope lives in the consensus-protected replicated record (not the network),
// it is HPKE-bound to its scope + target node, idempotency comes from the
// migration generation guard, and a retryable per-node recycle re-opens the same
// envelope on each pass — a replay guard would brick that legitimate retry. Do
// NOT use it on the network-facing key-delivery receiver, where replay defense is
// required.
func WithoutReplayGuard() ReceiverOption {
	return func(r *Receiver) { r.noGuard = true }
}

// NewReceiver builds a Receiver bound to this node's ECDSA P-256 mTLS private
// key and its NodeID. The private key is validated as DHKEM-usable up front.
func NewReceiver(priv *ecdsa.PrivateKey, nodeID string, opts ...ReceiverOption) (*Receiver, error) {
	if priv == nil {
		return nil, ErrNilRecipientKey
	}
	if nodeID == "" {
		return nil, ErrInvalidMeta
	}
	if _, err := ownPrivateKey(priv); err != nil {
		return nil, err
	}
	r := &Receiver{
		priv:   priv,
		nodeID: nodeID,
		ttl:    DefaultTTL,
		now:    time.Now,
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.guard == nil && !r.noGuard {
		r.guard = NewReplayGuard(r.ttl)
	}
	return r, nil
}

// Open verifies and decrypts an envelope addressed to this node. On success it
// returns the plaintext (raw root key for system-managed, sealed portable blob
// for self-managed); the caller owns and must wipe it. All failures are typed;
// a decryption failure is opaque (no padding/oracle distinction).
func (r *Receiver) Open(env *Envelope) ([]byte, error) {
	if env == nil || len(env.Payload) == 0 || len(env.Nonce) != nonceLen || env.IssuedAt.IsZero() {
		return nil, ErrMalformedEnvelope
	}
	if err := env.validate(); err != nil {
		return nil, err
	}
	if env.TargetNodeID != r.nodeID {
		return nil, ErrWrongTarget
	}

	now := r.now().UTC()
	issued := env.IssuedAt.UTC()
	if issued.After(now.Add(maxClockSkew)) || issued.Before(now.Add(-r.ttl)) {
		return nil, ErrExpired
	}
	sk, err := ownPrivateKey(r.priv)
	if err != nil {
		return nil, err
	}
	info := sealInfo(env.Meta, env.Nonce, env.IssuedAt)
	plaintext, err := hpke.Open(sk, hpkeKDF(), hpkeAEAD(), info, env.Payload)
	if err != nil {
		return nil, ErrDecrypt
	}

	// Record the nonce only AFTER a successful decryption. Recording before
	// would let an attacker pollute the guard with junk-payload envelopes
	// (each pinned for the full TTL) — a cheap memory-amplification DoS. A
	// legitimate replay still decrypts identically here and then trips the
	// guard; its re-decrypted plaintext is wiped before returning.
	if r.guard != nil && r.guard.observe(env.Nonce) {
		enclave.Wipe(plaintext)
		return nil, ErrReplay
	}
	return plaintext, nil
}
