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

// Package keydelivery implements secure transport of a dedicated tenant's raft
// ROOT KEY (or its portable sealed blob) from the placement leader to the
// cluster nodes that host the tenant's replicas. It is the security core of
// Phase 4c-1c: dedicated-isolation tenant raft groups replicate across nodes,
// and each node needs the tenant barrier key to start its replica.
//
// The payload is HPKE-sealed (RFC 9180, DHKEM(P-256)-HKDF-SHA256-AES-256-GCM)
// directly to the recipient node's existing ECDSA P-256 mTLS certificate key —
// no separate key distribution. HPKE recipient-binding means only the node
// holding the matching private key can open the payload, which provides a
// cryptographic node-identity guarantee INDEPENDENT of the transport: the key
// stays confidential even when inter-node mTLS is relaxed (dev/debug). The HPKE
// `info` is a canonical encoding of the delivery context {tenant, database,
// shard, replica, target node, mode}; because Open re-derives `info` from the
// envelope fields, any tampering with that metadata makes decryption fail —
// authenticating the otherwise-plaintext envelope without a separate signature.
//
// This package owns ONLY the cryptographic envelope and replay defense. The
// authenticated channel, node-cert authorization, placement-table tenant gate,
// and barrier import are wired by the transport and seal layers.
package keydelivery

import (
	"errors"
	"time"
)

// DefaultTTL is the freshness window for a delivery envelope. An envelope whose
// IssuedAt is older than this (or in the future beyond clock skew) is rejected.
const DefaultTTL = 5 * time.Minute

// maxClockSkew is the tolerance for an envelope issued slightly in the future
// relative to the receiver's clock.
const maxClockSkew = 30 * time.Second

// nonceLen is the byte length of the anti-replay nonce.
const nonceLen = 32

// Mode identifies how the transported payload is protected and how the receiver
// must install it. It is part of the HPKE context binding, so a payload sealed
// for one mode cannot be opened as any other.
type Mode string

const (
	// ModeSelfManaged carries the tenant's already-sealed PORTABLE blob
	// (HPKE-sealed on top). No plaintext root key ever transits; the receiver
	// persists the blob and the tenant must unseal that replica itself.
	ModeSelfManaged Mode = "self-managed"
)

// valid reports whether m is a recognized mode.
func (m Mode) valid() bool {
	return m == ModeSelfManaged
}

// Meta is the delivery context. Every field binds into the HPKE `info`, so the
// sealed payload is cryptographically tied to exactly one tenant/database/
// shard/replica/target-node/mode. Meta is also what the placement-table
// authorization gate is checked against by the transport layer.
type Meta struct {
	TenantID string `json:"tenant_id"`
	// DatabaseID is the database's opaque placement id; the envelope never
	// carries the database name.
	DatabaseID   string `json:"database_id"`
	ShardID      uint64 `json:"shard_id"`
	ReplicaID    uint64 `json:"replica_id"`
	TargetNodeID string `json:"target_node_id"`
	Mode         Mode   `json:"mode"`
}

// validate reports whether the context is structurally complete.
func (m Meta) validate() error {
	switch {
	case m.TenantID == "":
		return ErrInvalidMeta
	case m.DatabaseID == "":
		return ErrInvalidMeta
	case m.ShardID == 0:
		return ErrInvalidMeta
	case m.ReplicaID == 0:
		return ErrInvalidMeta
	case m.TargetNodeID == "":
		return ErrInvalidMeta
	case !m.Mode.valid():
		return ErrInvalidMode
	}
	return nil
}

// Envelope is the wire message carrying one HPKE-sealed key delivery. It is
// JSON-serializable for transport over the cluster-peer HTTPS endpoint. The
// Payload self-contains the HPKE encapsulated key (Seal returns enc||ciphertext).
type Envelope struct {
	Meta
	// Payload is the HPKE output: the encapsulated key concatenated with the
	// AEAD ciphertext of the root key (system-managed) or portable sealed blob
	// (self-managed).
	Payload []byte `json:"payload"`
	// Nonce is a random anti-replay token unique per delivery.
	Nonce []byte `json:"nonce"`
	// IssuedAt is the leader's seal time (UTC); used for the freshness window.
	IssuedAt time.Time `json:"issued_at"`
}

// Delivery errors. Callers match these with errors.Is.
var (
	// ErrInvalidMeta indicates a structurally incomplete delivery context.
	ErrInvalidMeta = errors.New("keydelivery: incomplete delivery context")

	// ErrInvalidMode indicates an unrecognized protection mode.
	ErrInvalidMode = errors.New("keydelivery: invalid mode")

	// ErrEmptyPlaintext indicates Seal was given no key material.
	ErrEmptyPlaintext = errors.New("keydelivery: empty plaintext")

	// ErrNilRecipientKey indicates a nil recipient/own key.
	ErrNilRecipientKey = errors.New("keydelivery: nil recipient key")

	// ErrMalformedEnvelope indicates a nil or structurally invalid envelope.
	ErrMalformedEnvelope = errors.New("keydelivery: malformed envelope")

	// ErrExpired indicates the envelope is outside the freshness window.
	ErrExpired = errors.New("keydelivery: envelope expired or not yet valid")

	// ErrReplay indicates the envelope's nonce was already seen.
	ErrReplay = errors.New("keydelivery: replayed envelope nonce")

	// ErrWrongTarget indicates the envelope targets a different node.
	ErrWrongTarget = errors.New("keydelivery: envelope targets a different node")

	// ErrDecrypt indicates HPKE Open failed (wrong recipient, tampered
	// context, or corrupt payload). Deliberately opaque — no oracle.
	ErrDecrypt = errors.New("keydelivery: decryption failed")
)
