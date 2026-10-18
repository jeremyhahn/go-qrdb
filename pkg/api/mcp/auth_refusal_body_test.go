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

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

const refusalTestChallenge = `Bearer realm="mcp"`

// refusalBody decodes the JSON error body a refusal carries, failing the test
// when the body is not REST's error shape.
func refusalBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not a JSON error: %v", rec.Body.String(), err)
	}
	return body.Error
}

func TestWriteAuthenticationRefusal_401CarriesTheMessageAndChallenge(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAuthenticationRefusal(rec, auth.ErrUnauthenticated, refusalTestChallenge)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := refusalBody(t, rec); got != auth.ErrUnauthenticated.Error() {
		t.Errorf("error = %q, want %q", got, auth.ErrUnauthenticated.Error())
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != refusalTestChallenge {
		t.Errorf("WWW-Authenticate = %q, want %q", got, refusalTestChallenge)
	}
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Errorf("Retry-After = %q on a refusal naming no delay", got)
	}
}

func TestWriteAuthenticationRefusal_403CarriesTheMessageWithoutChallenge(t *testing.T) {
	err := &authmw.UserDisabledError{Username: "alice", TenantID: "acme", Lifecycle: "suspended"}
	rec := httptest.NewRecorder()
	writeAuthenticationRefusal(rec, err, refusalTestChallenge)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got := refusalBody(t, rec); got != err.Error() {
		t.Errorf("error = %q, want %q", got, err.Error())
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != "" {
		t.Errorf("WWW-Authenticate = %q on a 403", got)
	}
}

func TestWriteAuthenticationRefusal_429KeepsRetryAfterWithTheBody(t *testing.T) {
	err := &authmw.RateLimitedError{RetryAfter: 1500 * time.Millisecond}
	rec := httptest.NewRecorder()
	writeAuthenticationRefusal(rec, err, refusalTestChallenge)
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Errorf("Retry-After = %q, want \"2\"", got)
	}
	if got := refusalBody(t, rec); got != err.Error() {
		t.Errorf("error = %q, want %q", got, err.Error())
	}
}

// TestWriteAuthenticationRefusal_EveryReasonCarriesItsMessage holds each
// refusal of an account or credential to the status REST answers it with
// (authmw.HTTPStatus) and to a body naming the refusal itself.
func TestWriteAuthenticationRefusal_EveryReasonCarriesItsMessage(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"suspended", &authmw.UserDisabledError{Username: "alice", TenantID: "acme", Lifecycle: "suspended"}, http.StatusForbidden},
		{"disabled", &authmw.UserDisabledError{Username: "bob", TenantID: "acme", Lifecycle: "disabled"}, http.StatusForbidden},
		{"revoked certificate", auth.ErrCertificateRevoked, http.StatusUnauthorized},
		{"no credential", auth.ErrUnauthenticated, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeAuthenticationRefusal(rec, tc.err, refusalTestChallenge)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if want := authmw.HTTPStatus(tc.err, http.StatusUnauthorized); rec.Code != want {
				t.Fatalf("status = %d, REST answers %d", rec.Code, want)
			}
			if got := refusalBody(t, rec); got != tc.err.Error() {
				t.Errorf("error = %q, want %q", got, tc.err.Error())
			}
			wantChallenge := ""
			if tc.wantStatus == http.StatusUnauthorized {
				wantChallenge = refusalTestChallenge
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != wantChallenge {
				t.Errorf("WWW-Authenticate = %q, want %q", got, wantChallenge)
			}
		})
	}
}
