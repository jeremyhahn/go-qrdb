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
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixedListenerProbe is a sealedwindow.ListenerProbe with a fixed state.
type fixedListenerProbe struct{ sealed, enrollment bool }

func (p fixedListenerProbe) IsSealed() bool                      { return p.sealed }
func (p fixedListenerProbe) EnrollmentOpen(context.Context) bool { return p.enrollment }
func (fixedListenerProbe) Admit(bool) func()                     { return func() {} }

// While sealed, the handler routes the allowlist to qrdb's handlers -- with no
// barrier service the officer route answers 501, not a router 404 -- and
// answers everything else, GET /health included, with the sealed envelope.
func TestNewSealedWindowHandler_RoutesTheAllowlistWhileSealed(t *testing.T) {
	h := NewSealedWindowHandler(nil, nil, nil, fixedListenerProbe{sealed: true, enrollment: true})
	for _, c := range []struct {
		method, path string
		code         int
		body         string
	}{
		{http.MethodPost, "/v1/barrier/officers", http.StatusNotImplemented, "barrier"},
		{http.MethodGet, "/health", http.StatusServiceUnavailable, `"code":"SEALED"`},
		{http.MethodGet, "/kv/key", http.StatusServiceUnavailable, `"code":"SEALED"`},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, strings.NewReader("{}")))
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.body) {
			t.Errorf("%s %s: %d %s, want %d containing %s", c.method, c.path, rec.Code, rec.Body.String(), c.code, c.body)
		}
	}
}

// A nil probe fails closed: every request is refused.
func TestNewSealedWindowHandler_NilProbeRefusesEverything(t *testing.T) {
	h := NewSealedWindowHandler(nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "SEALED_WINDOW_CLOSED") {
		t.Fatalf("nil probe: %d %s, want the closed refusal", rec.Code, rec.Body.String())
	}
}
