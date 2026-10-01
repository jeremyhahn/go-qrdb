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
	"errors"
	"strconv"
	"time"
)

// Share-holder errors.
var (
	// ErrSelfEnrolment is returned when a principal tries to enrol itself as
	// a share holder. Split knowledge: whoever enrols a holder is not that
	// holder.
	ErrSelfEnrolment = errors.New("ceremony: a principal cannot enrol itself as a share holder")

	// ErrTrustDomainNotConfigured is returned when a holder certificate is
	// requested before the ceremony knows the SPIFFE trust domain the CA
	// assigns identities in.
	ErrTrustDomainNotConfigured = errors.New("ceremony: no SPIFFE trust domain configured for CA-assigned identities")

	// ErrDuplicateHolderKey is returned when a registration reuses a
	// certificate key or encryption key already enrolled in the scope. One
	// person holds one share per scope, whatever their role.
	ErrDuplicateHolderKey = errors.New("ceremony: the key is already enrolled for another share holder of this scope")

	// ErrInvalidHolderRole is returned for a role other than so or custodian.
	ErrInvalidHolderRole = errors.New("ceremony: share holder role must be so or custodian")

	// ErrQuorumChallengeNotFound is returned when a share submission answers
	// no challenge this node issued (or one already used or expired).
	ErrQuorumChallengeNotFound = errors.New("ceremony: no pending quorum challenge with that id")

	// ErrQuorumChallengeMismatch is returned when a challenge is answered for
	// a different holder, operation, domain, factor or roster than it was
	// issued for.
	ErrQuorumChallengeMismatch = errors.New("ceremony: the quorum challenge was issued for a different submission")

	// ErrTooManyPendingChallenges is returned when the scope already holds
	// its maximum of unanswered quorum challenges.
	ErrTooManyPendingChallenges = errors.New("ceremony: too many pending quorum challenges; retry once some expire")

	// ErrShareEnvelopeMalformed is returned for an envelope that does not
	// decode as the current format.
	ErrShareEnvelopeMalformed = errors.New("ceremony: malformed share envelope")

	// ErrRegistrationWhileSealed is returned when a holder is registered
	// while the scope's barrier is sealed and already initialized. The
	// persisted roster is behind the barrier, so a sealed scope can neither
	// see whether the name is taken nor persist the record; enrolment while
	// sealed is legitimate only before initialization. Unseal first.
	ErrRegistrationWhileSealed = errors.New("ceremony: share holders cannot be registered while the initialized barrier is sealed")
)

// QuorumRateLimitedError is returned when a quorum request -- a challenge
// request or a share submission -- is refused by the scope's throttle.
// Nothing was issued and nothing in the submission was examined.
type QuorumRateLimitedError struct {
	// RetryAfter is the delay before a retry can succeed.
	RetryAfter time.Duration
}

// Error returns a human-readable description.
func (e *QuorumRateLimitedError) Error() string {
	return "ceremony: too many quorum requests; retry after " + e.RetryAfter.String()
}

// HolderRecordConflictError is returned when a holder record enrolled while
// the barrier was sealed names a holder storage already holds with a
// different identity. The persisted record is authoritative and is never
// overwritten.
type HolderRecordConflictError struct {
	// Username is the conflicting holder.
	Username string
}

// Error returns a human-readable description.
func (e *HolderRecordConflictError) Error() string {
	return "ceremony: share holder " + strconv.Quote(e.Username) + " is already enrolled with a different identity"
}

// InvalidHolderNameError is returned for a username that cannot be a
// certificate CN and a SPIFFE path segment.
type InvalidHolderNameError struct {
	Name string
}

// Error returns a human-readable description.
func (e *InvalidHolderNameError) Error() string {
	return "ceremony: " + strconv.Quote(e.Name) + " is not a valid principal name (1-64 characters from A-Z a-z 0-9 . _ @ -)"
}

// EncryptionKeyRequiredError is returned when the holder's certificate key
// cannot receive an HPKE envelope (an RSA or Ed25519 key, say) and no
// separate encryption key was enrolled.
type EncryptionKeyRequiredError struct {
	Err error
}

// Error returns a human-readable description.
func (e *EncryptionKeyRequiredError) Error() string {
	return "ceremony: the certificate key cannot receive a share envelope; enrol a separate encryption key: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EncryptionKeyRequiredError) Unwrap() error { return e.Err }

// DuplicateFactorError is returned when a registration names a factor twice
// (or names "mtls", which every holder enrols from its certificate).
type DuplicateFactorError struct {
	Factor string
}

// Error returns a human-readable description.
func (e *DuplicateFactorError) Error() string {
	return "ceremony: factor " + strconv.Quote(e.Factor) + " is empty or enrolled more than once"
}

// FactorNotEnrolledError is returned when the designated factor is not among
// the factors being enrolled.
type FactorNotEnrolledError struct {
	Factor string
}

// Error returns a human-readable description.
func (e *FactorNotEnrolledError) Error() string {
	return "ceremony: the designated factor " + strconv.Quote(e.Factor) + " is not enrolled"
}

// HolderFactorEnrollmentError wraps a factor implementation's enrolment
// failure.
type HolderFactorEnrollmentError struct {
	Factor string
	Err    error
}

// Error returns a human-readable description.
func (e *HolderFactorEnrollmentError) Error() string {
	return "ceremony: enrolling factor " + strconv.Quote(e.Factor) + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *HolderFactorEnrollmentError) Unwrap() error { return e.Err }

// ShareEnvelopeError wraps a failure sealing or opening a share envelope.
type ShareEnvelopeError struct {
	Holder string
	Err    error
}

// Error returns a human-readable description.
func (e *ShareEnvelopeError) Error() string {
	return "ceremony: share envelope for " + strconv.Quote(e.Holder) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ShareEnvelopeError) Unwrap() error { return e.Err }
