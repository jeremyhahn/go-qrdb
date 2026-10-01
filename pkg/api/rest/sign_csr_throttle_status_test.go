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

package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// A throttled ceremony-sign-csr is a PIN lockout the caller can wait out:
// 429, not the 500 an unclassified error gets.
func TestMapHTTPError_SignCSRThrottled(t *testing.T) {
	if got := mapHTTPError(&service.SignCSRThrottledError{RetryAfter: time.Second}); got != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", got)
	}
}

// The 429 tells the caller how long to wait in the standard Retry-After
// header (RFC 9110, delay-seconds, rounded up so it is never too early),
// so a client does not have to parse the body to back off correctly.
func TestWriteErrorJSON_SignCSRThrottledSetsRetryAfter(t *testing.T) {
	for _, c := range []struct {
		wait time.Duration
		want string
	}{
		{2300 * time.Millisecond, "3"},
		{5 * time.Second, "5"},
		{10 * time.Millisecond, "1"},
	} {
		rec := httptest.NewRecorder()
		err := &service.SignCSRThrottledError{RetryAfter: c.wait}
		writeErrorJSON(rec, mapHTTPError(err), err)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429", rec.Code)
		}
		if got := rec.Header().Get("Retry-After"); got != c.want {
			t.Errorf("wait %v: Retry-After = %q, want %q", c.wait, got, c.want)
		}
	}
}

// A throttled quorum-challenge request is a 429 with the same Retry-After.
func TestWriteErrorJSON_QuorumBeginThrottledSetsRetryAfter(t *testing.T) {
	rec := httptest.NewRecorder()
	err := &ceremony.QuorumRateLimitedError{RetryAfter: 1500 * time.Millisecond}
	writeErrorJSON(rec, mapHTTPError(err), err)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want 2", got)
	}
}

// Nothing else gets a Retry-After from this path.
func TestWriteErrorJSON_NoRetryAfterForOtherErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	err := &service.KeyNotFoundError{Key: "k"}
	writeErrorJSON(rec, mapHTTPError(err), err)
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q, want none", got)
	}
}
