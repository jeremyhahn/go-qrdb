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

package clientlimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTP_RefusesBeforeNextRuns(t *testing.T) {
	l := newLimiter(t, 1)
	var ran int
	h := l.HTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ran++
		w.WriteHeader(http.StatusNoContent)
	}))
	serve := func(remote string, kv ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/health", nil)
		r.RemoteAddr = remote
		for i := 0; i+1 < len(kv); i += 2 {
			r.Header.Add(kv[i], kv[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if rec := serve("192.0.2.1:1"); rec.Code != http.StatusNoContent {
		t.Fatalf("first request = %d", rec.Code)
	}
	rec := serve("192.0.2.1:2", HeaderXForwardedFor, "198.51.100.1")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After header")
	}
	if !strings.Contains(rec.Body.String(), "rate limit exceeded") {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if ran != 1 {
		t.Fatalf("next ran %d times, want 1", ran)
	}
	if rec := serve("192.0.2.2:1"); rec.Code != http.StatusNoContent {
		t.Fatalf("other address = %d", rec.Code)
	}
}

func TestHTTPOnce_ChargesAndMarksTheRequest(t *testing.T) {
	l, other := newLimiter(t, 1), newLimiter(t, 1)
	var ctxs []context.Context
	h := l.HTTPOnce(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctxs = append(ctxs, r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	serve := func() int {
		r := httptest.NewRequest(http.MethodPost, "/grpc-web/a.B/C", nil)
		r.RemoteAddr = "192.0.2.1:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if code := serve(); code != http.StatusNoContent {
		t.Fatalf("first request = %d", code)
	}
	if code := serve(); code != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", code)
	}
	if len(ctxs) != 1 {
		t.Fatalf("next ran %d times, want 1", len(ctxs))
	}
	if !l.Admitted(ctxs[0]) {
		t.Fatal("the admitted request is not marked charged")
	}
	if other.Admitted(ctxs[0]) {
		t.Fatal("another limiter reads the mark as its own charge")
	}
}

func TestAdmitted_UnmarkedAndNil(t *testing.T) {
	l := newLimiter(t, 1)
	if l.Admitted(context.Background()) {
		t.Fatal("an unmarked context reads as charged")
	}
	var nilLimiter *Limiter
	if nilLimiter.Admitted(context.Background()) {
		t.Fatal("a nil limiter reports a charge")
	}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if got := nilLimiter.HTTPOnce(next); got == nil {
		t.Fatal("HTTPOnce on a nil limiter returned nil")
	}
}

func TestWriteHTTPError_OtherErrorHasNoRetryAfter(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteHTTPError(rec, errors.New("refused"))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("code %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}
