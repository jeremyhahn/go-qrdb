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

package middleware

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// wrapped wraps an error the way a caller up the stack might.
type wrapped struct{ err error }

func (w *wrapped) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrapped) Unwrap() error { return w.err }

// TestRetryAfter_ReadsEveryDelayingAuthRefusal: a rate-limited request and a
// tenant SO PIN lockout each name their delay, wrapped or not; nothing else
// names one.
func TestRetryAfter_ReadsEveryDelayingAuthRefusal(t *testing.T) {
	cases := map[string]struct {
		err  error
		want time.Duration
	}{
		"rate limit":         {&RateLimitedError{RetryAfter: 2 * time.Second}, 2 * time.Second},
		"SO PIN lockout":     {&auth.SOPINLockedError{RetryAfter: 3 * time.Second}, 3 * time.Second},
		"wrapped SO lockout": {&wrapped{&auth.SOPINLockedError{RetryAfter: 4 * time.Second}}, 4 * time.Second},
	}
	for name, tc := range cases {
		if got, ok := RetryAfter(tc.err); !ok || got != tc.want {
			t.Errorf("%s: RetryAfter = %v, %v; want %v, true", name, got, ok, tc.want)
		}
	}
	for _, err := range []error{nil, errors.New("boom"), auth.ErrSOPINLocked, auth.ErrUnauthenticated} {
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

// TestGRPCError_CarriesRetryInfo: an auth-layer refusal that names a delay
// reaches a gRPC (and Unix socket) caller with a google.rpc.RetryInfo detail
// carrying it exactly, as a service-layer refusal does; any other carries none.
func TestGRPCError_CarriesRetryInfo(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want time.Duration
	}{
		"SO PIN lockout": {&auth.SOPINLockedError{RetryAfter: 1500 * time.Millisecond}, 1500 * time.Millisecond},
		"rate limit":     {&RateLimitedError{RetryAfter: 2 * time.Second}, 2 * time.Second},
	} {
		st := status.Convert(GRPCError(tc.err, codes.Unknown))
		if st.Code() != codes.ResourceExhausted {
			t.Errorf("%s: code %v, want ResourceExhausted", name, st.Code())
		}
		var got *errdetails.RetryInfo
		for _, d := range st.Details() {
			if ri, ok := d.(*errdetails.RetryInfo); ok {
				got = ri
			}
		}
		if got == nil || got.GetRetryDelay().AsDuration() != tc.want {
			t.Errorf("%s: RetryInfo %v, want %v", name, got, tc.want)
		}
	}
	if d := status.Convert(GRPCError(auth.ErrUnauthenticated, codes.Unknown)).Details(); len(d) != 0 {
		t.Errorf("an error naming no delay carries details %v", d)
	}
}

// TestSetRetryAfterHeader_TenantSOPINLockout: the REST face of a tenant SO
// PIN lockout carries Retry-After, rounded up.
func TestSetRetryAfterHeader_TenantSOPINLockout(t *testing.T) {
	rec := httptest.NewRecorder()
	setRetryAfterHeader(rec, &auth.SOPINLockedError{RetryAfter: 1500 * time.Millisecond})
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After %q, want \"2\"", got)
	}
}
