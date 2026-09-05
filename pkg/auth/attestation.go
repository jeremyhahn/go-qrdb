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

import "errors"

// AttestationLevel is what an enrollment ceremony PROVED about where a
// factor's private key lives. It is the value stored under MetadataAttestLevel
// and the input a login policy reads to decide whether a credential may
// authenticate an administrator, which is why it is a closed vocabulary rather
// than a free string: a misspelled level that was stored anyway would fail
// that policy silently, in the direction of admitting the credential or not
// depending on how the policy happens to compare.
type AttestationLevel string

const (
	// AttestationLevelNone records that no attestation was verified. It is
	// what the server writes for every factor recorded without a ceremony,
	// so a factor that proved nothing is POSITIVELY marked rather than left
	// indistinguishable from a record that predates the field.
	AttestationLevelNone AttestationLevel = "none"

	// AttestationLevelSoftware records that an attestation statement was
	// verified but was self-signed by the very key it describes. That proves
	// possession of the key and nothing about its storage; any software
	// authenticator can produce one.
	AttestationLevelSoftware AttestationLevel = "software"

	// AttestationLevelHardware records that the attestation statement chained
	// to a manufacturer trust anchor the operator configured. Only this level
	// is evidence that the key was generated inside, and cannot leave, a
	// hardware authenticator.
	AttestationLevelHardware AttestationLevel = "hardware"
)

// Valid reports whether the level is one of the defined values. The empty
// string is not valid: an unset level is an unanswered question, and the
// trusted recording path refuses it rather than defaulting it.
func (l AttestationLevel) Valid() bool {
	switch l {
	case AttestationLevelNone, AttestationLevelSoftware, AttestationLevelHardware:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer.
func (l AttestationLevel) String() string { return string(l) }

// Keys inside ReservedMetadataPrefix that the server writes. They are defined
// here, beside the prefix that protects them, so the store and every reader
// spell them identically.
const (
	// MetadataAttestLevel holds the AttestationLevel a factor proved at
	// enrollment. Present on every factor written since the server began
	// stamping it; absent only on records that predate the rule.
	MetadataAttestLevel = ReservedMetadataPrefix + "level"

	// MetadataAttestAAGUID holds the attested authenticator model (the
	// WebAuthn AAGUID, hex-encoded). Present only when a ceremony verified
	// one; a factor with no attested model carries no key rather than an
	// empty value.
	MetadataAttestAAGUID = ReservedMetadataPrefix + "aaguid"
)

// ErrInvalidAttestation is returned when a VerifiedAttestation handed to the
// trusted recording path does not describe a defined level. Callers match it
// with errors.Is; the payload is on *InvalidAttestationError.
var ErrInvalidAttestation = errors.New("auth: attestation invalid")

// InvalidAttestationError names the level that was refused.
type InvalidAttestationError struct {
	// Level is the value the caller supplied.
	Level AttestationLevel
}

// Error implements error.
func (e *InvalidAttestationError) Error() string {
	return "auth: attestation level " + string(e.Level) + " is not one of " +
		string(AttestationLevelNone) + ", " + string(AttestationLevelSoftware) + ", " +
		string(AttestationLevelHardware)
}

// Unwrap returns the sentinel so errors.Is succeeds.
func (e *InvalidAttestationError) Unwrap() error { return ErrInvalidAttestation }

// VerifiedAttestation is what an enrollment ceremony proved about an
// authenticator, carried by the ONE path that may write inside the
// server-owned metadata namespace: service.AttestationRecorder.
//
// It is a typed value on an in-process API rather than a header, a flag or a
// metadata key, deliberately. Every network transport reaches the auth-factor
// service through a handler that only ever calls Add and Rotate, both of
// which refuse caller-supplied attest_* keys and stamp Unattested; nothing on
// any wire can produce a VerifiedAttestation. Only the process hosting the
// service -- the composer that ran the WebAuthn or PIV ceremony and verified
// the attestation chain -- can construct one, and that is exactly the party
// entitled to say what the ceremony proved (go-qrdb#342).
type VerifiedAttestation struct {
	// Level is what the ceremony proved. Must be Valid.
	Level AttestationLevel

	// AAGUID is the hex-encoded attested authenticator model, when the
	// ceremony verified one. Empty means no model is recorded.
	AAGUID string
}

// Unattested is the attestation the server records for a factor written
// without a ceremony: level none, no model. It is what Add and Rotate stamp on
// every ordinary write.
func Unattested() VerifiedAttestation {
	return VerifiedAttestation{Level: AttestationLevelNone}
}

// Validate returns an *InvalidAttestationError when Level is not a defined
// level.
func (a VerifiedAttestation) Validate() error {
	if !a.Level.Valid() {
		return &InvalidAttestationError{Level: a.Level}
	}
	return nil
}

// Stamp writes the attestation into md under the reserved keys and returns
// the map, allocating one when md is nil. It is the ONLY writer of those keys
// in this codebase; ValidateMetadata has already established that md carries
// none of them, so Stamp never overwrites a caller's value -- the two together
// are the rule that the server-owned namespace is written by the server only.
//
// MetadataAttestAAGUID is set only when a model was attested; an absent key,
// not an empty value, is how "no model" is recorded.
func (a VerifiedAttestation) Stamp(md map[string]string) map[string]string {
	if md == nil {
		md = make(map[string]string, 2)
	}
	md[MetadataAttestLevel] = string(a.Level)
	if a.AAGUID != "" {
		md[MetadataAttestAAGUID] = a.AAGUID
	}
	return md
}
