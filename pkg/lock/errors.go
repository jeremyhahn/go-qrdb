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

	// ActualToken is the fencing token currently stored for the lock.
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
