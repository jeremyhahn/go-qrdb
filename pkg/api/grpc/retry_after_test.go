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

package grpc

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// retryInfo returns the RetryInfo detail err's status carries, if any.
func retryInfo(t *testing.T, err error) *errdetails.RetryInfo {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a status error: %v", err)
	}
	for _, d := range st.Details() {
		if ri, ok := d.(*errdetails.RetryInfo); ok {
			return ri
		}
	}
	return nil
}

// TestMapServiceError_CarriesRetryInfo: a refusal that says how long to wait
// -- a throttled quorum request or sign-csr, an SO PIN lockout -- reaches a
// gRPC (and Unix socket) caller with a google.rpc.RetryInfo detail carrying
// the exact delay, as REST's Retry-After carries it, beside its
// RESOURCE_EXHAUSTED code. Any other failure carries no RetryInfo.
func TestMapServiceError_CarriesRetryInfo(t *testing.T) {
	cases := map[string]struct {
		err  error
		want time.Duration
	}{
		"quorum throttle": {&ceremony.QuorumRateLimitedError{RetryAfter: 1500 * time.Millisecond}, 1500 * time.Millisecond},
		"SO PIN lockout":  {&pin.SOPINLockedError{RetryAfter: 4 * time.Second}, 4 * time.Second},
		"sign-csr":        {&service.SignCSRThrottledError{RetryAfter: 2 * time.Second}, 2 * time.Second},
	}
	for name, tc := range cases {
		err := mapServiceError(tc.err)
		if status.Code(err) != codes.ResourceExhausted {
			t.Errorf("%s: code = %v, want ResourceExhausted", name, status.Code(err))
		}
		ri := retryInfo(t, err)
		if ri == nil {
			t.Fatalf("%s: no RetryInfo detail", name)
		}
		if got := ri.GetRetryDelay().AsDuration(); got != tc.want {
			t.Errorf("%s: RetryDelay = %v, want %v", name, got, tc.want)
		}
	}
	if ri := retryInfo(t, mapServiceError(errors.New("boom"))); ri != nil {
		t.Errorf("an error naming no delay carries RetryInfo %v", ri)
	}
}
