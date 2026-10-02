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

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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
		"sign-csr":        {&service.SignCSRThrottledError{RetryAfter: time.Second}, time.Second},
		"quorum throttle": {&ceremony.QuorumRateLimitedError{RetryAfter: 2 * time.Second}, 2 * time.Second},
		"SO PIN lockout":  {&pin.SOPINLockedError{RetryAfter: 3 * time.Second}, 3 * time.Second},
		"wrapped":         {&wrappedError{&pin.SOPINLockedError{RetryAfter: 4 * time.Second}}, 4 * time.Second},
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

// TestRetryAfterSeconds_RoundsUpAndFloorsAtOne: whole seconds, never early,
// never zero.
func TestRetryAfterSeconds_RoundsUpAndFloorsAtOne(t *testing.T) {
	cases := map[time.Duration]int64{
		0:                       1,
		10 * time.Millisecond:   1,
		time.Second:             1,
		1500 * time.Millisecond: 2,
		15 * time.Minute:        900,
	}
	for d, want := range cases {
		if got := RetryAfterSeconds(d); got != want {
			t.Errorf("RetryAfterSeconds(%v) = %d, want %d", d, got, want)
		}
	}
}
