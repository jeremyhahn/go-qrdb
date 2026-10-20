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

package errclass

import (
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// wrappedError wraps an error the way a transport or service layer might.
type wrappedError struct{ err error }

func (w *wrappedError) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrappedError) Unwrap() error { return w.err }

// TestRetryAfter_ReadsEveryDelayingRefusal: each refusal that names a delay
// yields it, wrapped or not.
func TestRetryAfter_ReadsEveryDelayingRefusal(t *testing.T) {
	cases := map[string]struct {
		err  error
		want time.Duration
	}{
		"sign-csr":              {&service.SignCSRThrottledError{RetryAfter: time.Second}, time.Second},
		"quorum throttle":       {&ceremony.QuorumRateLimitedError{RetryAfter: 2 * time.Second}, 2 * time.Second},
		"SO PIN lockout":        {&pin.SOPINLockedError{RetryAfter: 3 * time.Second}, 3 * time.Second},
		"wrapped":               {&wrappedError{&pin.SOPINLockedError{RetryAfter: 4 * time.Second}}, 4 * time.Second},
		"tenant SO PIN lockout": {&auth.SOPINLockedError{RetryAfter: 5 * time.Second}, 5 * time.Second},
		"auth rate limit":       {&middleware.RateLimitedError{RetryAfter: 6 * time.Second}, 6 * time.Second},
		"undone tenant id":      {&tenant.TenantUndoPendingError{TenantID: "t", RetryAfter: 7 * time.Second}, 7 * time.Second},
	}
	for name, tc := range cases {
		got, ok := RetryAfter(tc.err)
		if !ok || got != tc.want {
			t.Errorf("%s: RetryAfter = %v, %v; want %v, true", name, got, ok, tc.want)
		}
	}
}

// TestRetryAfter_OtherErrorsNameNoDelay: nil and every other error name none.
func TestRetryAfter_OtherErrorsNameNoDelay(t *testing.T) {
	for _, err := range []error{nil, errors.New("boom"), pin.ErrPINLocked, pin.ErrPINInvalid} {
		if d, ok := RetryAfter(err); ok {
			t.Errorf("RetryAfter(%v) = %v, true; want none", err, d)
		}
	}
}
