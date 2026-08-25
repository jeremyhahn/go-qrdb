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

package custodian

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrNilStore is returned when a nil KVStore is provided.
	ErrNilStore = errors.New("custodian: nil kv store provided")

	// ErrNilLogger is returned when a nil logger is provided.
	ErrNilLogger = errors.New("custodian: nil logger provided")

	// ErrNilAuditLogger is returned when a nil audit logger is provided.
	ErrNilAuditLogger = errors.New("custodian: nil audit logger provided")

	// ErrCustodianNotFound is returned when a custodian record does not exist.
	ErrCustodianNotFound = errors.New("custodian: custodian not found")

	// ErrCustodianAlreadyExists is returned when registering a custodian with
	// a username that already exists for the tenant.
	ErrCustodianAlreadyExists = errors.New("custodian: custodian already exists")

	// ErrCustodianRevoked is returned when operating on a revoked custodian.
	ErrCustodianRevoked = errors.New("custodian: custodian is revoked")

	// ErrMissingUsername is returned when a username is required but absent.
	ErrMissingUsername = errors.New("custodian: username is required")

	// ErrMissingTenantID is returned when a tenant ID is required but absent.
	ErrMissingTenantID = errors.New("custodian: tenant ID is required")

	// ErrMissingEnrollmentID is returned when an enrollment ID is required but absent.
	ErrMissingEnrollmentID = errors.New("custodian: enrollment ID is required")

	// ErrMissingPublicKeyPEM is returned when a public key PEM is required but absent.
	ErrMissingPublicKeyPEM = errors.New("custodian: public key PEM is required")

	// ErrInvalidShareIndex is returned when the share index is out of range.
	ErrInvalidShareIndex = errors.New("custodian: share index must be >= 1")

	// ErrInvalidMethod is returned when an unknown factor method is specified.
	ErrInvalidMethod = errors.New("custodian: invalid factor method")

	// ErrEmptyShareBytes is returned when share bytes are required but empty.
	ErrEmptyShareBytes = errors.New("custodian: share bytes must not be empty")

	// ErrShareAlreadyDelivered is returned when a second claim is attempted
	// for a share that has already been delivered.
	ErrShareAlreadyDelivered = errors.New("custodian: share envelope already delivered")

	// ErrEnvelopeSeal is returned when ECDH envelope sealing fails.
	ErrEnvelopeSeal = errors.New("custodian: envelope sealing failed")

	// ErrInvalidPublicKey is returned when the public key PEM cannot be parsed.
	ErrInvalidPublicKey = errors.New("custodian: invalid public key PEM")

	// ErrAtomicWriteUnsupported is returned when the KV store cannot perform
	// an atomic conditional write.
	//
	// Registration claims a tenant's username with PutIfAbsent so exactly one
	// of two concurrent enrolments for the same identity can proceed. A store
	// without that primitive can only be checked with Get and then written
	// with Put, and both callers observe "absent" in the window between them
	// — so both seal a different share against the same identity and the
	// later write decides which enrolment survives, leaving the other holder
	// with an envelope for material the store no longer describes. Emulating
	// the guarantee would hide that; refusing states it.
	ErrAtomicWriteUnsupported = errors.New("custodian: kv store does not support atomic conditional writes")

	// ErrGuardedBatchUnsupported is returned when the KV store cannot apply an
	// enrolment's three keys as one write fenced on the username claim.
	//
	// Enrolment needs the whole record to land or none of it, conditional on
	// the registration still owning the claim. Writing the keys one at a time
	// and re-checking ownership between them narrows the window in which an
	// expired-claim takeover can be overwritten; it does not close it, and
	// what slips through is a lost update with no signal (go-qrdb#21).
	ErrGuardedBatchUnsupported = errors.New("custodian: kv store does not support guarded multi-key writes")
)

// StoreError wraps a KV store operation failure.
type StoreError struct {
	Op  string
	Err error
}

// Error returns a human-readable description of the store failure.
func (e *StoreError) Error() string {
	return fmt.Sprintf("custodian: store %s failed: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error.
func (e *StoreError) Unwrap() error { return e.Err }

// CorruptRecordError is returned when a custodian record in the tenant index
// cannot be decoded during duplicate detection.
//
// Registration refuses instead of skipping the record. Decoding is how a
// record's username is learned, so an undecodable record cannot be ruled out
// as the requested username's existing enrolment; skipping it would allow a
// second custodian — with a second sealed share — to be enrolled for a
// username that may already hold one. An operator repairs or quarantines the
// record, which is a deliberate act rather than a silent one.
type CorruptRecordError struct {
	// ID is the custodian identifier whose record could not be decoded.
	ID string
	// Err is the underlying decode failure.
	Err error
}

// Error returns a human-readable description of the corrupt record.
func (e *CorruptRecordError) Error() string {
	return fmt.Sprintf("custodian: record %s is corrupt and cannot be decoded: %v", e.ID, e.Err)
}

// Unwrap returns the underlying decode failure.
func (e *CorruptRecordError) Unwrap() error { return e.Err }

// OrphanedEnrolmentError is returned when a registration failed partway
// through persisting and the compensating deletes could not remove everything
// it had written.
//
// The keys it names are still in the store, and the sealed envelope for them
// is not: envelopes are returned to the caller, never stored. The duplicate
// check will honour the surviving record — correctly, since it cannot tell an
// orphan from a completed enrolment — so this custodian cannot re-enrol until
// an operator removes the listed keys.
type OrphanedEnrolmentError struct {
	// ID is the custodian identifier the failed registration used.
	ID string
	// Keys are the store keys that survived the rollback.
	Keys []string
	// Cause is the write failure that triggered the rollback.
	Cause error
}

// Error returns a human-readable description naming the surviving keys.
func (e *OrphanedEnrolmentError) Error() string {
	return fmt.Sprintf("custodian: registration of %s failed (%v) and left %s behind; remove them before retrying",
		e.ID, e.Cause, strings.Join(e.Keys, ", "))
}

// Unwrap returns the write failure that triggered the rollback.
func (e *OrphanedEnrolmentError) Unwrap() error { return e.Cause }

// ClaimLostError is returned when a registration finds that the username claim
// it acquired is no longer its own.
//
// Claims EXPIRE (go-qrdb#21), so holding one is not a fact that stays true. A
// registration that stalls past the TTL — a slow seal, a slow store, a context
// with no deadline, clock skew between nodes — has its claim taken over by
// another caller, which may already have committed the enrolment.
//
// The registration abandons rather than compensating. Its own earlier writes
// share keys with the winner's, so deleting them could remove the winner's
// enrolment; the winner's commit supersedes them instead. Nothing is leaked by
// abandoning: the sealed envelope this call produced is returned to nobody,
// and the share plaintext was already zeroed by SealShareTo.
//
// The remedy is to retry. The winner holds the identity, so a retry reports
// ErrCustodianAlreadyExists if that enrolment is the intended one, or succeeds
// once it is revoked.
type ClaimLostError struct {
	// ID is the identifier this registration would have taken.
	ID string
	// Key is the username index key whose claim was lost.
	Key string
}

// Error returns a human-readable description of the lost claim.
func (e *ClaimLostError) Error() string {
	return "custodian: the username claim at " + e.Key + " was taken over by another " +
		"registration before this enrolment of " + e.ID + " could commit; retry"
}

// IncompleteEnrolmentError is returned when a tenant's username index still
// holds a registration's claim token rather than a custodian identifier.
//
// Registration claims the username before it seals a share and replaces the
// claim with the identifier only once the record is written, so this is the
// state of an enrolment that is either still in flight on another caller or
// was interrupted before it committed. Both refuse: admitting a second
// registration here is precisely the concurrent duplicate the claim exists to
// prevent.
//
// It is reported distinctly from ErrCustodianAlreadyExists because the remedy
// differs. No custodian holds this username yet — a retry succeeds once the
// in-flight registration finishes or, if the claim was left by an interrupted
// one, once an operator removes Key. Nothing was lost with it: the claim is
// taken before any share is sealed, so an enrolment interrupted in this window
// never produced envelope material.
type IncompleteEnrolmentError struct {
	// ID is the identifier the registration holding the claim would take.
	ID string
	// Key is the claimed username index key.
	Key string
	// ExpiresAt is when the claim stops being authoritative and another
	// registration may take it over. Meaningful only when Readable is true.
	ExpiresAt time.Time
	// Readable reports whether the claim carried an expiry this build could
	// parse. A claim written before expiries existed, or a corrupt one, is
	// treated as never expiring and needs an operator to clear it — see
	// docs/seal/custodians.md.
	Readable bool
}

// Error returns a human-readable description of the incomplete enrolment.
func (e *IncompleteEnrolmentError) Error() string {
	if !e.Readable {
		return fmt.Sprintf("custodian: %s is claimed by an uncommitted registration of %s, and the claim carries no readable expiry; remove %s to release it",
			e.Key, e.ID, e.Key)
	}
	return fmt.Sprintf("custodian: %s is claimed by an uncommitted registration of %s; retry after %s, when the claim expires and is taken over automatically",
		e.Key, e.ID, e.ExpiresAt.UTC().Format(time.RFC3339))
}
