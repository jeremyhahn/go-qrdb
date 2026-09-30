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
	"strconv"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// signCSRPolicy throttles ceremony-sign-csr per ceremony. The endpoint is
// reachable without authentication on the boot-time sealed-window listener,
// and the SO PIN it carries is its only authorization, so every call is an
// online guess at that PIN. Ten calls may arrive back to back, which covers
// the officers and services of a first-boot ceremony signing in turn; after
// that one call is admitted every five seconds, and a caller over budget is
// told how long to wait.
//
// Every call is counted, not only the failed ones. Counting only failures
// would need the PIN checked first, and concurrent guesses would all be
// checked before any of them was counted. Taking the permit first is
// race-free, and a throttled call cannot be used to confirm a guess.
var signCSRPolicy = ratelimit.Policy{RatePerSecond: 0.2, Burst: 10}

// signCSRLimiter is the process-wide limiter behind signCSRPolicy. It is
// started on first use and lives as long as the process: the buckets it
// keeps are one per ceremony core, and a node has one or two.
var signCSRLimiter = sync.OnceValue(func() ratelimit.Limiter {
	return ratelimit.NewTokenBucketLimiter()
})

// signCSRThrottleKey is the bucket of core's ceremony. It is keyed by the
// core's InstanceID rather than by the service wrapper, so the budget survives
// a wrapper being rebuilt over the same core, as the boot does across the
// unseal, and a new core never inherits a released one's bucket.
func signCSRThrottleKey(core *ceremony.CeremonyService) string {
	return "ceremony-sign-csr/" + strconv.FormatUint(core.InstanceID(), 10)
}

// SignCSRThrottledError is returned by CeremonySignCSR when the ceremony's
// sign-csr budget is exhausted. The SO PIN was not checked. It unwraps to
// pin.ErrPINLocked, the PIN-lockout condition every transport already answers
// as 429 / RESOURCE_EXHAUSTED.
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
