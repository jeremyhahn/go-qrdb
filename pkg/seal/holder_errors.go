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

package seal

import (
	"errors"
	"strconv"
)

// Share-holder quorum errors.
var (
	// ErrHolderRosterMissing is returned when a domain has no wrapped blob,
	// or its blob predates holder rosters. A Shamir barrier initialized
	// before holder rosters existed must be re-initialized: there is no
	// unauthenticated path left to unseal it through.
	ErrHolderRosterMissing = errors.New("qrdb/seal: no holder roster for this key domain; the barrier predates authenticated share holders or was never initialized")

	// ErrHolderVerifierNotConfigured is returned when a share submission
	// arrives and no HolderVerifier has been wired. Submissions fail closed:
	// a share is never counted without an authenticated holder.
	ErrHolderVerifierNotConfigured = errors.New("qrdb/seal: no holder verifier configured; share submissions are refused")

	// ErrQuorumRoundNotOpen is returned when a rekey share arrives with no
	// rekey round open. A rekey round is opened explicitly (StartRekey); the
	// other operations open on their first submission.
	ErrQuorumRoundNotOpen = errors.New("qrdb/seal: no quorum round open for this operation")

	// ErrQuorumRoundInProgress is returned by StartRekey when a rekey round
	// for the domain is already open.
	ErrQuorumRoundInProgress = errors.New("qrdb/seal: a quorum round for this operation is already in progress")
)

// InvalidRosterError is returned when a holder roster is malformed or
// inconsistent.
type InvalidRosterError struct {
	// Reason says what is wrong.
	Reason string
}

// Error returns a human-readable description.
func (e *InvalidRosterError) Error() string {
	return "qrdb/seal: invalid holder roster: " + e.Reason
}

// HolderNotInRosterError is returned when a submission names a holder the
// roster does not list.
type HolderNotInRosterError struct {
	// Holder is the username submitted.
	Holder string
}

// Error returns a human-readable description.
func (e *HolderNotInRosterError) Error() string {
	return "qrdb/seal: " + e.Holder + " is not a share holder of this key"
}

// FactorNotAdmittedError is returned when a holder tries to authenticate with
// a factor the roster's policy does not admit for them.
type FactorNotAdmittedError struct {
	// Holder is the username.
	Holder string
	// Factor is the factor name presented.
	Factor string
	// Policy is the roster's factor policy.
	Policy FactorPolicy
}

// Error returns a human-readable description.
func (e *FactorNotAdmittedError) Error() string {
	return "qrdb/seal: factor " + `"` + e.Factor + `"` + " is not admitted for " + e.Holder + " under the " + string(e.Policy) + " policy"
}

// HolderRevokedError is returned when a holder being revoked by the open
// rekey round submits to it.
type HolderRevokedError struct {
	// Holder is the username.
	Holder string
}

// Error returns a human-readable description.
func (e *HolderRevokedError) Error() string {
	return "qrdb/seal: " + e.Holder + " is being revoked and cannot contribute to this quorum"
}

// StaleRosterError is returned when a submission or a rekey was prepared
// against a roster the domain has since replaced, while a quorum round is
// open on the newer one. The newer round is left as it is. Retrying reads the
// current roster.
type StaleRosterError struct {
	// Domain is the quorum domain whose roster changed.
	Domain QuorumDomain
}

// Error returns a human-readable description.
func (e *StaleRosterError) Error() string {
	return "qrdb/seal: the " + string(e.Domain) + " roster changed while the request was prepared; retry"
}

// HolderAlreadySubmittedError is returned when a holder submits a second
// share to the same round. It matches ErrShamirDuplicateShare.
type HolderAlreadySubmittedError struct {
	// Holder is the username.
	Holder string
}

// Error returns a human-readable description.
func (e *HolderAlreadySubmittedError) Error() string {
	return "qrdb/seal: " + e.Holder + " has already contributed to this quorum; each holder counts once"
}

// Unwrap returns ErrShamirDuplicateShare.
func (e *HolderAlreadySubmittedError) Unwrap() error { return ErrShamirDuplicateShare }

// ShareIndexMismatchError is returned when a submitted share's x-coordinate
// is not the submitting holder's share index: the share belongs to somebody
// else.
type ShareIndexMismatchError struct {
	// Holder is the username.
	Holder string
	// Want is the holder's share index.
	Want int
}

// Error returns a human-readable description.
func (e *ShareIndexMismatchError) Error() string {
	return "qrdb/seal: the share submitted by " + e.Holder + " is not share " + strconv.Itoa(e.Want) + "; a holder may submit only their own share"
}

// HolderAuthenticationError is returned when the holder verifier refuses a
// submission's proof.
//
// It is OPAQUE across the transport boundary: Error is one fixed message,
// naming neither the holder nor the verifier's reason, and it does not unwrap
// to the verifier's error. The sealed window admits submissions anonymously,
// and the reason -- a challenge not found, a signature that does not verify,
// a mismatched operation -- would tell a holder from a stranger. The cause is
// kept in Err for the server's own log and audit, which record it; no
// transport renders it.
type HolderAuthenticationError struct {
	// Holder is the username the submission named. Server-side only.
	Holder string
	// Err is the verifier's error. Server-side only: log and audit it, never
	// return it to the caller.
	Err error
}

// Error returns the one fixed message every refused proof gets.
func (e *HolderAuthenticationError) Error() string {
	return "qrdb/seal: share holder authentication failed"
}

// QuorumOpInvalidError is returned for an unknown domain or an operation that
// cannot run in the named domain (for example "recover" in the unseal domain).
type QuorumOpInvalidError struct {
	// Domain is the requested domain.
	Domain QuorumDomain
	// Op is the requested operation.
	Op QuorumOp
}

// Error returns a human-readable description.
func (e *QuorumOpInvalidError) Error() string {
	return "qrdb/seal: operation " + `"` + string(e.Op) + `"` + " is not valid in key domain " + `"` + string(e.Domain) + `"`
}

// RevocationBreaksQuorumError is returned when a rekey would leave fewer
// holders than the threshold: after removing Removed holders only Remaining
// are left, fewer than Threshold.
type RevocationBreaksQuorumError struct {
	// Threshold is the threshold the rekey would need.
	Threshold int
	// Remaining is how many holders would remain.
	Remaining int
}

// Error returns a human-readable description.
func (e *RevocationBreaksQuorumError) Error() string {
	return "qrdb/seal: the rekey leaves " + strconv.Itoa(e.Remaining) + " holders, fewer than the threshold of " + strconv.Itoa(e.Threshold)
}

// Unwrap returns ErrShamirThresholdInvalid.
func (e *RevocationBreaksQuorumError) Unwrap() error { return ErrShamirThresholdInvalid }
