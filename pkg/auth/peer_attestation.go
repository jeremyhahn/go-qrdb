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

package auth

import (
	"context"
	"crypto/x509"
	"time"
)

// PeerAttestationVerifier renders a verdict on whether an authenticated peer's
// MEASURED STATE is currently acceptable (go-qrdb#278).
//
// qrdb owns this seam and nothing behind it. The concrete verifier -- quote
// verification against an attestation key, a PCR baseline, an evidence cache
// fed by a background collector -- belongs to go-xkms and is injected through
// the auth middleware's peer-attestation gate. qrdb never parses evidence.
//
// The gate runs AFTER identity is established and after RBAC has granted the
// request: "who is this peer" and "is its state acceptable right now" are
// different claims, and keeping them apart is what lets attestation be
// withdrawn independently of authentication.
//
// Implementations MUST:
//
//   - look the peer's evidence and enrolled attestation key up by the
//     AUTHENTICATED identity in peer, never from anything the request carries;
//   - consult cached, already-collected evidence only -- never trigger
//     collection on the request path, which would put a TPM quote on every
//     request;
//   - report in PeerAttestation.PeerThumbprint which peer the evidence they
//     judged actually belongs to, so the gate can refuse evidence for a
//     different peer than the one on the connection;
//   - return an error rather than a verdict when they cannot decide. The gate
//     refuses on any error; it never admits on one.
type PeerAttestationVerifier interface {
	VerifyPeer(ctx context.Context, peer AttestedPeer) (*PeerAttestation, error)
}

// AttestedPeer is what the gate hands a PeerAttestationVerifier: the peer as
// authentication established it. Nothing in it comes from a request header or
// body.
type AttestedPeer struct {
	// Identity is the authenticated, store-enriched identity. Verifiers must
	// treat it as read-only.
	Identity *Identity

	// Certificate is the verified leaf certificate on the connection.
	Certificate *x509.Certificate

	// Thumbprint is Certificate's canonical SPKI thumbprint -- the same
	// identifier a user's certificate binding holds.
	Thumbprint string
}

// PeerAttestationFailure classifies a refused verdict. The classes are
// deliberately the ones an operator must tell apart: absent or stale evidence
// is an operational condition, evidence that is present and WRONG is a
// security event.
type PeerAttestationFailure int

const (
	// PeerAttestationFailureNone is the class of a passing verdict.
	PeerAttestationFailureNone PeerAttestationFailure = iota

	// PeerAttestationFailureAbsence: there was nothing acceptable to judge --
	// no evidence, evidence too old, no baseline, a required measurement not
	// quoted.
	PeerAttestationFailureAbsence

	// PeerAttestationFailureCryptographic: evidence was present and failed --
	// a bad signature, an untrusted attestation key, a measurement that does
	// not match the baseline. This is the security event.
	PeerAttestationFailureCryptographic
)

// String returns the audit spelling of the class.
func (f PeerAttestationFailure) String() string {
	if name, ok := peerAttestationFailureNames[f]; ok {
		return name
	}
	return "unknown"
}

var peerAttestationFailureNames = map[PeerAttestationFailure]string{
	PeerAttestationFailureNone:          "none",
	PeerAttestationFailureAbsence:       "absence",
	PeerAttestationFailureCryptographic: "cryptographic",
}

// PeerAttestation is a verifier's verdict about one peer's measured state.
type PeerAttestation struct {
	// PeerThumbprint is the SPKI thumbprint of the peer the judged evidence
	// belongs to. The gate refuses a verdict whose thumbprint is not the
	// connection's.
	PeerThumbprint string

	// CollectedAt is when the judged evidence was collected. The gate refuses
	// a zero value, a value in the future, and a value older than its
	// configured maximum evidence age -- independently of the verifier's own
	// age check, so a verifier configured with a looser bound cannot widen
	// the gate's.
	CollectedAt time.Time

	// Passed is the verifier's verdict.
	Passed bool

	// Failure classifies a refused verdict; it must be
	// PeerAttestationFailureNone exactly when Passed is true.
	Failure PeerAttestationFailure

	// Reason names every failed check, for the audit record.
	Reason string
}
