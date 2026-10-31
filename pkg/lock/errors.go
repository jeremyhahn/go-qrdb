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

package lock

import (
	"errors"
	"strconv"
)

// Sentinel errors for configuration and constructor validation.
var (
	// ErrInvalidTTL is returned when a LockOptions TTL is zero or negative.
	ErrInvalidTTL = errors.New("lock: TTL must be positive")

	// ErrEmptyKey is returned when a LockOptions key is empty.
	ErrEmptyKey = errors.New("lock: key must not be empty")

	// ErrUnlockContended is returned by Unlock when every one of its
	// compare-and-swap attempts lost to a concurrent write to the lock key.
	// The lock is still held; the call may be retried.
	ErrUnlockContended = errors.New("lock: unlock lost every compare-and-swap to concurrent writes")

	// ErrNilTxService is returned by NewLocker when txSvc is nil.
	ErrNilTxService = errors.New("lock: nil transaction service")

	// ErrNilLeaseService is returned by NewLocker when leaseSvc is nil.
	ErrNilLeaseService = errors.New("lock: nil lease service")

	// ErrNilWatchService is returned by NewLocker when watchSvc is nil.
	ErrNilWatchService = errors.New("lock: nil watch service")

	// ErrNilKVService is returned by NewLocker when kvSvc is nil.
	ErrNilKVService = errors.New("lock: nil KV service")
)

// LockHeldError is returned when a lock cannot be acquired because it is
// held by a different owner.
type LockHeldError struct {
	// Key is the user-provided resource key.
	Key string

	// HolderID identifies the current lock holder.
	HolderID string
}

// Error returns a human-readable description of the held lock.
func (e *LockHeldError) Error() string {
	return "lock: held by " + e.HolderID + " on key " + e.Key
}

// NotLockHolderError is returned when an unlock or extend operation
// supplies a fencing token that does not match the current lock state.
type NotLockHolderError struct {
	// Key is the user-provided resource key.
	Key string

	// ExpectedToken is the fencing token supplied by the caller.
	ExpectedToken int64

	// ActualToken is the lock's current fencing token, its key's create
	// revision.
	ActualToken int64
}

// Error returns a human-readable description of the token mismatch.
func (e *NotLockHolderError) Error() string {
	return "lock: caller is not the lock holder for key " + e.Key +
		" (expected token " + strconv.FormatInt(e.ExpectedToken, 10) +
		", actual " + strconv.FormatInt(e.ActualToken, 10) + ")"
}

// LockNotFoundError is returned when an operation targets a key that is
// not currently locked.
type LockNotFoundError struct {
	// Key is the user-provided resource key.
	Key string
}

// Error returns a human-readable description of the missing lock.
func (e *LockNotFoundError) Error() string {
	return "lock: not found for key " + e.Key
}

// The structured-refusal reasons a remote transport carries beside the
// message of a typed lock outcome (google.rpc.ErrorInfo on gRPC, its
// counterpart on every other transport), so a client rebuilds the typed
// error -- key, holder and tokens included -- without parsing text.
const (
	// ReasonLockHeld is the reason of a *LockHeldError.
	ReasonLockHeld = "LOCK_HELD"
	// ReasonNotLockHolder is the reason of a *NotLockHolderError.
	ReasonNotLockHolder = "NOT_LOCK_HOLDER"
	// ReasonLockNotFound is the reason of a *LockNotFoundError.
	ReasonLockNotFound = "LOCK_NOT_FOUND"
)

// The refusal metadata keys of the lock errors' fields.
const (
	metadataKey           = "key"
	metadataHolderID      = "holder_id"
	metadataExpectedToken = "expected_token"
	metadataActualToken   = "actual_token"
)

// Metadata encodes e as structured-refusal metadata.
func (e *LockHeldError) Metadata() map[string]string {
	return map[string]string{metadataKey: e.Key, metadataHolderID: e.HolderID}
}

// LockHeldFromMetadata rebuilds the error Metadata encoded. The reason, not
// the metadata, says the lock is held, so a missing field is left empty
// rather than losing the classification.
func LockHeldFromMetadata(md map[string]string) *LockHeldError {
	return &LockHeldError{Key: md[metadataKey], HolderID: md[metadataHolderID]}
}

// Metadata encodes e as structured-refusal metadata.
func (e *NotLockHolderError) Metadata() map[string]string {
	return map[string]string{
		metadataKey:           e.Key,
		metadataExpectedToken: strconv.FormatInt(e.ExpectedToken, 10),
		metadataActualToken:   strconv.FormatInt(e.ActualToken, 10),
	}
}

// NotLockHolderFromMetadata rebuilds the error Metadata encoded. A missing or
// malformed token is left zero rather than losing the classification.
func NotLockHolderFromMetadata(md map[string]string) *NotLockHolderError {
	return &NotLockHolderError{
		Key:           md[metadataKey],
		ExpectedToken: tokenFromMetadata(md, metadataExpectedToken),
		ActualToken:   tokenFromMetadata(md, metadataActualToken),
	}
}

// Metadata encodes e as structured-refusal metadata.
func (e *LockNotFoundError) Metadata() map[string]string {
	return map[string]string{metadataKey: e.Key}
}

// LockNotFoundFromMetadata rebuilds the error Metadata encoded.
func LockNotFoundFromMetadata(md map[string]string) *LockNotFoundError {
	return &LockNotFoundError{Key: md[metadataKey]}
}

// tokenFromMetadata decodes the fencing token under name, or zero when it is
// missing or malformed.
func tokenFromMetadata(md map[string]string, name string) int64 {
	token, err := strconv.ParseInt(md[name], 10, 64)
	if err != nil {
		return 0
	}
	return token
}
