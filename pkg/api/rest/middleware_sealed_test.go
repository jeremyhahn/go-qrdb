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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeSealProbe struct {
	sealed atomic.Bool
}

func (f *fakeSealProbe) IsSealed() bool { return f.sealed.Load() }
func (f *fakeSealProbe) seal()          { f.sealed.Store(true) }
func (f *fakeSealProbe) unseal()        { f.sealed.Store(false) }

func newFakeSealed(sealed bool) *fakeSealProbe {
	p := &fakeSealProbe{}
	if sealed {
		p.seal()
	}
	return p
}

// okHandler is the protected handler placed downstream of the
// middleware; if it runs, the request was permitted through.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
})

func TestSealedWindow_NilProbeIsPassthrough(t *testing.T) {
	mw := SealedWindowMiddleware(nil, nil)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/kv/foo", nil)
	mw(okHandler).ServeHTTP(rr, r)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestSealedWindow_UnsealedIsPassthrough(t *testing.T) {
	mw := SealedWindowMiddleware(newFakeSealed(false), nil)
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/kv/foo", nil)
	mw(okHandler).ServeHTTP(rr, r)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
}

func TestSealedWindow_SealedNonAllowlistReturns503(t *testing.T) {
	probe := newFakeSealed(true)
	mw := SealedWindowMiddleware(probe, nil)
	for _, path := range []string{
		"/v1/kv/foo",
		"/v1/iam/users",
		"/v1/orchestrator/sessions",
		"/v1/vector/index",
	} {
		t.Run(path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, path, nil)
			mw(okHandler).ServeHTTP(rr, r)
			if rr.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", rr.Code)
			}
			if got := rr.Header().Get("Retry-After"); got != "5" {
				t.Errorf("Retry-After = %q, want 5", got)
			}
			if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if !strings.Contains(rr.Body.String(), `"code":"SEALED"`) {
				t.Errorf("body = %q, want SEALED code", rr.Body.String())
			}
		})
	}
}

func TestSealedWindow_SealedAllowlistPasses(t *testing.T) {
	probe := newFakeSealed(true)
	mw := SealedWindowMiddleware(probe, nil)
	for _, path := range []string{
		"/v1/barrier/status",
		"/v1/barrier/initialize",
		"/v1/barrier/unseal",
		"/v1/barrier/shamir/initialize",
		"/v1/barrier/shamir/unseal-share",
		"/v1/barrier/shamir/unseal-shares",
		"/v1/barrier/shamir/verify",
		"/v1/barrier/recovery/generate",
		"/v1/barrier/recovery/recover",
		"/v1/barrier/recovery/status",
		"/v1/init/status",
		"/v1/init/claim-cert",
		"/v1/init/claim-share",
		"/v1/init/enroll-external-officer",
		"/v1/bootstrap/auto",
		"/v1/bootstrap/dane",
	} {
		t.Run(path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, path, nil)
			mw(okHandler).ServeHTTP(rr, r)
			if rr.Code != http.StatusOK {
				t.Errorf("path %s status = %d, want 200", path, rr.Code)
			}
		})
	}
}

func TestSealedWindow_DynamicSealUnsealBehaviour(t *testing.T) {
	probe := newFakeSealed(false)
	mw := SealedWindowMiddleware(probe, nil)
	send := func() int {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/v1/kv/foo", nil)
		mw(okHandler).ServeHTTP(rr, r)
		return rr.Code
	}
	if got := send(); got != http.StatusOK {
		t.Errorf("initial status = %d, want 200", got)
	}
	probe.seal()
	if got := send(); got != http.StatusServiceUnavailable {
		t.Errorf("after seal status = %d, want 503", got)
	}
	probe.unseal()
	if got := send(); got != http.StatusOK {
		t.Errorf("after unseal status = %d, want 200", got)
	}
}

func TestSealedWindow_CustomAllowlist(t *testing.T) {
	custom := []string{"/v1/custom/", "/v1/exact"}
	mw := SealedWindowMiddleware(newFakeSealed(true), custom)
	cases := []struct {
		path string
		want int
	}{
		{"/v1/custom/anything", http.StatusOK},
		{"/v1/exact", http.StatusOK},
		{"/v1/exact-but-prefixed", http.StatusServiceUnavailable},
		{"/v1/health", http.StatusServiceUnavailable},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, c.path, nil)
			mw(okHandler).ServeHTTP(rr, r)
			if rr.Code != c.want {
				t.Errorf("status = %d, want %d", rr.Code, c.want)
			}
		})
	}
}

func TestPathInAllowlist_PrefixVsExact(t *testing.T) {
	allow := []string{"/v1/init/", "/v1/health"}
	cases := []struct {
		path string
		want bool
	}{
		{"/v1/init/", true},
		{"/v1/init/anything", true},
		{"/v1/init", false},
		{"/v1/health", true},
		{"/v1/health/sub", false},
	}
	for _, c := range cases {
		if got := pathInAllowlist(c.path, allow); got != c.want {
			t.Errorf("path=%s got=%v want=%v", c.path, got, c.want)
		}
	}
}
