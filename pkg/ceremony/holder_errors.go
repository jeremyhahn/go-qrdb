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

// StagedDistributionMissingError is returned by Committed when no envelopes
// are staged for the distribution the barrier reports durable: another
// distribution replaced them before this one committed. The envelopes in
// force do not open the durable blob, so nothing is swapped in or recorded.
type StagedDistributionMissingError struct {
	// Distribution is the committed roster's distribution identifier.
	Distribution string
}

// Error returns a human-readable description.
func (e *StagedDistributionMissingError) Error() string {
	return "ceremony: no envelopes are staged for distribution " + e.Distribution + "; another distribution replaced them before it committed"
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

// HolderLimitReachedError is returned when a holder is registered on a scope
// that fixes N before initialization -- a database barrier, at its create --
// whose barrier is not yet initialized and already has every holder its
// unseal key will be split to. Initialization needs exactly Total holders, so
// one more could only be refused there -- after the holders had enrolled --
// and no holder can be revoked before initialization. Remove an enrolled
// holder first, or initialize.
type HolderLimitReachedError struct {
	// Total is the number of holders the unseal key is split to (N).
	Total int
}

// Error returns a human-readable description.
func (e *HolderLimitReachedError) Error() string {
	return "ceremony: all " + strconv.Itoa(e.Total) + " share holders the barrier is split to are enrolled; " +
		"no more can be enrolled before it is initialized"
}

// InvalidHolderNameError is returned for a username that cannot be a
// certificate CN.
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

// UnsupportedHolderKeyError is returned when a holder's certificate key is
// not one the share-holder model enrols: ECDSA P-256, P-384 or P-521, or
// Ed25519. RSA is refused, because no decoy can be derived per name for an
// RSA key at a cost that does not itself reveal the decoy, so an RSA holder
// would be told apart from a stranger by the key type alone.
type UnsupportedHolderKeyError struct {
	// Algorithm names the refused key's algorithm.
	Algorithm string
}

// Error returns a human-readable description.
func (e *UnsupportedHolderKeyError) Error() string {
	return "ceremony: a share holder's certificate key must be ECDSA P-256, P-384, P-521 or Ed25519, not " + e.Algorithm
}
