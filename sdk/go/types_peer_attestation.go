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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// The attested-caller gate (go-qrdb#278). qrdb owns the seam: the verifier
// interface, the verdict it returns, and the gate that enforces it on every
// network transport. The concrete verifier -- quote verification over cached
// evidence -- is go-xkms's, and is supplied by the composer.
type (
	// PeerAttestationVerifier renders a verdict on an authenticated peer's
	// measured state from cached evidence. go-xkms implements it.
	PeerAttestationVerifier = auth.PeerAttestationVerifier
	// AttestedPeer is what the gate hands the verifier: the authenticated
	// identity and the verified certificate on the connection.
	AttestedPeer = auth.AttestedPeer
	// PeerAttestation is the verifier's verdict.
	PeerAttestation = auth.PeerAttestation
	// PeerAttestationFailure classifies a refused verdict.
	PeerAttestationFailure = auth.PeerAttestationFailure

	// PeerAttestationPolicy names the gated permissions and the maximum
	// evidence age.
	PeerAttestationPolicy = authmw.PeerAttestationPolicy
	// PeerAttestationGate is the validated gate a node installs.
	PeerAttestationGate = authmw.PeerAttestationGate
	// PeerAttestationRefusedError is the refusal a gated request gets.
	PeerAttestationRefusedError = authmw.PeerAttestationRefusedError
	// PeerAttestationConfigError reports a policy that does not describe a
	// gate.
	PeerAttestationConfigError = authmw.PeerAttestationConfigError
	// PeerAttestationReason is the stable refusal class.
	PeerAttestationReason = authmw.PeerAttestationReason
)

// The PeerAttestationFailure values.
const (
	PeerAttestationFailureNone          = auth.PeerAttestationFailureNone
	PeerAttestationFailureAbsence       = auth.PeerAttestationFailureAbsence
	PeerAttestationFailureCryptographic = auth.PeerAttestationFailureCryptographic
)

// The PeerAttestationReason values.
const (
	PeerAttestationNoPeerCertificate   = authmw.PeerAttestationNoPeerCertificate
	PeerAttestationForwarded           = authmw.PeerAttestationForwarded
	PeerAttestationVerifierError       = authmw.PeerAttestationVerifierError
	PeerAttestationNoVerdict           = authmw.PeerAttestationNoVerdict
	PeerAttestationPeerMismatch        = authmw.PeerAttestationPeerMismatch
	PeerAttestationNoEvidence          = authmw.PeerAttestationNoEvidence
	PeerAttestationEvidenceStale       = authmw.PeerAttestationEvidenceStale
	PeerAttestationEvidenceFromFuture  = authmw.PeerAttestationEvidenceFromFuture
	PeerAttestationStateRejected       = authmw.PeerAttestationStateRejected
	PeerAttestationInconsistentVerdict = authmw.PeerAttestationInconsistentVerdict
)

// NewPeerAttestationGate validates a policy and builds the gate; pass it to the
// node with node.WithPeerAttestationGate.
var NewPeerAttestationGate = authmw.NewPeerAttestationGate
