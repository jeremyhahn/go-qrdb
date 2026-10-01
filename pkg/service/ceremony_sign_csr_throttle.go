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

package service

import (
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
)

// SignCSRThrottledError is returned by CeremonySignCSR while the ceremony's
// SO PIN check is locked out after repeated failures
// (ceremony.SOPINLockedError); the presented PIN was not checked. It unwraps
// to pin.ErrPINLocked, the PIN-lockout condition every transport answers as
// 429 / RESOURCE_EXHAUSTED, and REST sets Retry-After from RetryAfter.
type SignCSRThrottledError struct {
	// RetryAfter is the minimum wait before the next call is admitted.
	RetryAfter time.Duration
}

// Error implements the error interface.
func (e *SignCSRThrottledError) Error() string {
	return "service: ceremony sign-csr throttled after repeated SO PIN attempts; retry after " + e.RetryAfter.String()
}

// Unwrap returns pin.ErrPINLocked.
func (e *SignCSRThrottledError) Unwrap() error { return pin.ErrPINLocked }
