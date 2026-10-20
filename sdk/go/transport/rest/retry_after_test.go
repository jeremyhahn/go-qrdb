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

package rest

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// errorResponse is a REST error response with the given Retry-After value.
func errorResponse(retryAfter string) *http.Response {
	h := http.Header{}
	if retryAfter != "" {
		h.Set("Retry-After", retryAfter)
	}
	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Status:     "429 Too Many Requests",
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(`{"error":"SO PIN locked"}`)),
	}
}

// TestDecodeErrorResponse_CarriesTheRetryDelay: the Retry-After header --
// delay-seconds as the server sends it, or an HTTP-date -- reaches the caller
// as RequestError.RetryAfter; an absent, malformed or past value names none.
func TestDecodeErrorResponse_CarriesTheRetryDelay(t *testing.T) {
	tr := &Transport{}
	retryAt := func(v string) time.Duration {
		t.Helper()
		var reqErr *transport.RequestError
		resp := errorResponse(v)
		defer func() { _ = resp.Body.Close() }()
		if err := tr.decodeErrorResponse("BarrierRekeyStart", resp); !errors.As(err, &reqErr) {
			t.Fatalf("decoded %T, want *transport.RequestError", err)
		}
		return reqErr.RetryAfter
	}
	if got := retryAt("3"); got != 3*time.Second {
		t.Fatalf("Retry-After 3 = %v, want 3s", got)
	}
	if got := retryAt(time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)); got < 59*time.Minute || got > time.Hour {
		t.Fatalf("Retry-After an hour from now = %v", got)
	}
	for _, v := range []string{"", "soon", "0", "-4", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)} {
		if got := retryAt(v); got != 0 {
			t.Errorf("Retry-After %q = %v, want 0", v, got)
		}
	}
}

// The 503 a create of a not-yet-released undone id answers keeps its status
// and its delay, so a caller can tell it to retry rather than give up.
func TestDecodeErrorResponse_UndoPendingIsRetryable503(t *testing.T) {
	resp := errorResponse("5")
	resp.StatusCode, resp.Status = http.StatusServiceUnavailable, "503 Service Unavailable"
	resp.Body = io.NopCloser(strings.NewReader(`{"error":"tenant: id acme is pending the undo cleanup"}`))
	defer func() { _ = resp.Body.Close() }()
	var reqErr *transport.RequestError
	if err := (&Transport{}).decodeErrorResponse("CreateTenant", resp); !errors.As(err, &reqErr) {
		t.Fatalf("decoded %T, want *transport.RequestError", err)
	}
	if reqErr.StatusCode != http.StatusServiceUnavailable || reqErr.RetryAfter != 5*time.Second {
		t.Fatalf("RequestError = %+v, want 503 with RetryAfter 5s", reqErr)
	}
}
