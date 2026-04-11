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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/lock"

// ---------------------------------------------------------------------------
// Lock types
// ---------------------------------------------------------------------------

// Lock represents an acquired distributed lock. It carries the fencing token
// used to detect stale holders.
type Lock = lock.Lock

// LockOptions configures lock acquisition behavior.
type LockOptions = lock.LockOptions

// ---------------------------------------------------------------------------
// Lock interface
// ---------------------------------------------------------------------------

// Locker is the distributed lock interface.
type Locker = lock.Locker

// ---------------------------------------------------------------------------
// Lock error types
// ---------------------------------------------------------------------------

// LockHeldError is returned when a lock cannot be acquired because it is held
// by a different owner.
type LockHeldError = lock.LockHeldError

// NotLockHolderError is returned when an unlock or extend operation supplies
// a fencing token that does not match the current lock state.
type NotLockHolderError = lock.NotLockHolderError

// LockNotFoundError is returned when an operation targets a key that is not
// currently locked.
type LockNotFoundError = lock.LockNotFoundError

// ---------------------------------------------------------------------------
// Lock sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrInvalidTTL is returned when a LockOptions TTL is zero or negative.
	ErrInvalidTTL = lock.ErrInvalidTTL

	// ErrEmptyKey is returned when a LockOptions key is empty.
	ErrEmptyKey = lock.ErrEmptyKey
)

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

// NewLocker creates a new distributed lock service. All four service
// dependencies are required.
var NewLocker = lock.NewLocker
