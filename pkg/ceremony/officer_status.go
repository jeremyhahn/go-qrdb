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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// EncryptionKeyEnrolment names a share-envelope recipient key enrolled
// separately from the holder's certificate key — for example a PIV
// key-management (9d) key, or an X25519 or post-quantum hybrid key.
type EncryptionKeyEnrolment struct {
	// KEM names the HPKE suite (see pkg/keydelivery.SuiteNames).
	KEM string

	// PublicKey is the recipient public key in that suite's encoding.
	PublicKey []byte
}

// FactorEnrolment asks for one additional authentication factor to be
// enrolled through its registered authfactors.Factor.
type FactorEnrolment struct {
	// Name is the factor name ("fido2", "piv", ...).
	Name string

	// Params are the factor's enrolment inputs (the authenticator's
	// attestation, a PIV slot certificate, ...).
	Params map[string]any
}

// RegisterOfficerRequest registers one share holder — a Security Officer or a
// key custodian; the two are enrolled the same way.
type RegisterOfficerRequest struct {
	// Username is the unique identifier for the holder in its scope.
	Username string

	// Role is so or custodian; empty means so.
	Role seal.HolderRole

	// CSRPEM is the holder's PEM certificate signing request. Only its
	// public key is used: the CA assigns the certificate's identity.
	CSRPEM []byte

	// EncryptionKey is an optional separate share-envelope key. Without one
	// the certificate key receives the envelopes, which needs an ECDSA
	// P-256/P-384 key.
	EncryptionKey *EncryptionKeyEnrolment

	// Factors are additional authentication factors to enrol. "mtls" is
	// always enrolled, from the issued certificate.
	Factors []FactorEnrolment

	// Designated names the factor admitted under the designated factor
	// policy; empty means "mtls".
	Designated string

	// Enroller is the authenticated principal performing the enrolment; it
	// may not enrol itself. Empty only during first-boot bootstrap.
	Enroller string
}

// RegisterOfficerResult is the outcome of a successful registration. It
// carries no share material: shares come only from a distribution.
type RegisterOfficerResult struct {
	// CertPEM is the certificate the CA issued, to be delivered to the
	// holder out of band.
	CertPEM []byte

	// Role is the holder's role.
	Role seal.HolderRole

	// Designated is the designated factor.
	Designated string

	// EncryptionKEM is the HPKE suite the holder's envelopes are sealed to.
	EncryptionKEM string

	// Factors lists the enrolled factor names.
	Factors []string
}

// OfficerStatus is a non-secret view of an enrolled share holder.
type OfficerStatus struct {
	Username       string          `json:"username"`
	Role           seal.HolderRole `json:"role"`
	CertThumbprint string          `json:"cert_thumbprint,omitempty"`
	ShareIndex     int             `json:"share_index"`
	Designated     string          `json:"designated"`
	Factors        []string        `json:"factors"`
	EncryptionKEM  string          `json:"encryption_kem"`
	EnrolledAt     time.Time       `json:"enrolled_at"`
	ClaimedAt      time.Time       `json:"claimed_at,omitempty"` // zero = unclaimed
	SharePending   bool            `json:"share_pending"`        // true = sealed, awaiting claim
}

// ShareClaimResult is returned to the holder exactly once upon a successful
// CompleteClaimShare.
type ShareClaimResult struct {
	// ShareIndex is the holder's share index.
	ShareIndex int `json:"share_index"`

	// Envelope is the holder's share envelope (a ShareEnvelope, JSON). It is
	// excluded from struct marshalling so it is emitted only deliberately,
	// to the holder's own session.
	Envelope []byte `json:"-"`
}

// QuorumChallenge is the factor challenge a holder answers to submit a share
// toward a quorum. It carries only public data.
type QuorumChallenge struct {
	ChallengeID string
	Domain      seal.QuorumDomain
	Op          seal.QuorumOp
	Holder      string
	Factor      string
	Nonce       []byte
	MethodData  map[string]string
	ExpiresAt   time.Time
}
