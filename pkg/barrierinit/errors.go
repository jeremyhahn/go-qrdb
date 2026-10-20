// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package barrierinit

import (
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// Reason names why a statement was refused. The values are stable: they reach
// the caller in the error text and the audit trail.
type Reason string

const (
	// ReasonMalformed: a field is missing, out of range or oversized.
	ReasonMalformed Reason = "statement malformed"
	// ReasonMalformedKey: the signer's key is not a parsable SPKI.
	ReasonMalformedKey Reason = "signer key malformed"
	// ReasonNoAnchor: the node holds no anchor for this epoch -- no pending
	// first admin for epoch 1, no tenant CA for a later one.
	ReasonNoAnchor Reason = "no trust anchor for this epoch"
	// ReasonWrongSigner: the signer is not the barrier owner -- not the first
	// admin at epoch 1, not the certificate's key at a later one.
	ReasonWrongSigner Reason = "signer is not the barrier owner"
	// ReasonUntrustedCertificate: a later epoch's certificate is absent, does
	// not chain to the tenant CA, or does not chain to it at the statement's
	// issue time.
	ReasonUntrustedCertificate Reason = "signer certificate not trusted"
	// ReasonIssuedInFuture: the statement's signed issue time is after the
	// time it is verified at.
	ReasonIssuedInFuture Reason = "statement issued in the future"
	// ReasonNotValidAtIssue: the statement's signed issue time lies outside
	// its signer certificate's validity window.
	ReasonNotValidAtIssue Reason = "signer certificate not valid at issue time"
	// ReasonBadSignature: the signature does not verify over the statement.
	ReasonBadSignature Reason = "signature does not verify"
	// ReasonMismatch: the statement names another barrier, epoch, strategy or
	// key than the request it accompanies.
	ReasonMismatch Reason = "statement does not match the request"
)

// InitStatementError is returned when a statement is refused: by Sign, Verify,
// Book.Claim, Book.Advance and the Watcher.
type InitStatementError struct {
	Barrier clusterseal.Barrier
	Epoch   uint64
	Reason  Reason
	// Err is the underlying parse or chain failure, when there is one.
	Err error
}

func (e *InitStatementError) Error() string {
	msg := "barrierinit: statement for " + string(e.Barrier) + " epoch " + strconv.FormatUint(e.Epoch, 10) + ": " + string(e.Reason)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *InitStatementError) Unwrap() error { return e.Err }

// BarrierAlreadyInitializedError is returned by Book.Claim when another
// statement already claimed the barrier's first epoch.
type BarrierAlreadyInitializedError struct {
	Barrier clusterseal.Barrier
}

func (e *BarrierAlreadyInitializedError) Error() string {
	return "barrierinit: barrier " + string(e.Barrier) + " is already initialised"
}

// NoInitStatementError is returned by Claimer.TenantInit when the barrier has
// no recorded init statement, so nothing signed says which strategy, key
// reference and epoch it unseals with.
type NoInitStatementError struct {
	Barrier clusterseal.Barrier
}

func (e *NoInitStatementError) Error() string {
	return "barrierinit: barrier " + string(e.Barrier) + " has no recorded init statement"
}

// AnchorConflictError is returned by Pins.PinFirstAdmin when the barrier's
// first admin is already pinned to another key.
type AnchorConflictError struct {
	Barrier clusterseal.Barrier
}

func (e *AnchorConflictError) Error() string {
	return "barrierinit: barrier " + string(e.Barrier) + " is already anchored to another first admin"
}

// EpochConflictError is returned when a statement's epoch is not the one after
// the barrier's current epoch: Claim of an epoch other than 1, an Advance that
// skips, repeats or rewinds an epoch, or one that lost a race.
type EpochConflictError struct {
	Barrier clusterseal.Barrier
	// Epoch is the statement's epoch.
	Epoch uint64
	// Current is the barrier's recorded epoch, 0 when unclaimed.
	Current uint64
}

func (e *EpochConflictError) Error() string {
	return "barrierinit: barrier " + string(e.Barrier) + " is at epoch " + strconv.FormatUint(e.Current, 10) +
		"; statement for epoch " + strconv.FormatUint(e.Epoch, 10) + " does not follow it"
}

// CorruptRecordError is returned when a stored init record does not decode, or
// decodes to versions that are not this barrier's dense epochs from 1.
type CorruptRecordError struct {
	Key string
	// Reason says what is wrong with a record that decoded.
	Reason string
	// Err is the decode failure, when the record did not decode.
	Err error
}

func (e *CorruptRecordError) Error() string {
	msg := "barrierinit: init record " + e.Key + " is corrupt"
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *CorruptRecordError) Unwrap() error { return e.Err }

// GuardedWritesUnavailableError is returned by a KV whose host cannot enforce
// a compare-and-set yet: a claim written without its guard could overwrite a
// rival's, so it is refused instead.
type GuardedWritesUnavailableError struct{}

func (e *GuardedWritesUnavailableError) Error() string {
	return "barrierinit: guarded writes are not active on this cluster"
}

// ConfigError is returned by NewWatcher for a missing seam.
type ConfigError struct {
	Field string
}

func (e *ConfigError) Error() string {
	return "barrierinit: watcher config: " + e.Field + " is required"
}

// StoreError wraps a failure of the underlying KV.
type StoreError struct {
	Op  string
	Err error
}

func (e *StoreError) Error() string {
	return "barrierinit: " + e.Op + ": " + e.Err.Error()
}

func (e *StoreError) Unwrap() error { return e.Err }
