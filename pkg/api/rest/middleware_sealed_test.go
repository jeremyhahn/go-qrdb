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
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

type fakeSealProbe struct {
	sealed, enrollment atomic.Bool
	sealedwindow.Admission
}

func (f *fakeSealProbe) IsSealed() bool                      { return f.sealed.Load() }
func (f *fakeSealProbe) EnrollmentOpen(context.Context) bool { return f.enrollment.Load() }
func (f *fakeSealProbe) seal()                               { f.sealed.Store(true) }
func (f *fakeSealProbe) unseal()                             { f.sealed.Store(false) }

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
	for _, req := range []struct{ method, path string }{
		{http.MethodGet, "/v1/barrier/status"},
		{http.MethodPost, "/v1/barrier/initialize"},
		{http.MethodPost, "/v1/barrier/unseal"},
		{http.MethodPost, "/v1/barrier/shamir/initialize"},
		{http.MethodPost, "/v1/barrier/officers"},
		{http.MethodPost, "/v1/barrier/officers/alice/claim/begin"},
		{http.MethodPost, "/v1/barrier/quorum/begin"},
		{http.MethodPost, "/v1/barrier/quorum/submit"},
		{http.MethodPost, "/v1/ceremony/sign-csr"},
	} {
		t.Run(req.method+" "+req.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			r := httptest.NewRequest(req.method, req.path, nil)
			mw(okHandler).ServeHTTP(rr, r)
			if rr.Code != http.StatusOK {
				t.Errorf("%s %s status = %d, want 200", req.method, req.path, rr.Code)
			}
		})
	}
}

// TestSealedWindow_SealedRefusesTrimmedVerbs pins the verbs removed from
// the allowlist. On the boot-time listener every allowlisted verb is
// anonymous; these are either refused by the service while sealed anyway
// (rekey, the recovery-key verbs), served post-unseal to authenticated
// callers (officer list), SO-only (external officer enrollment), or have no
// route at all.
func TestSealedWindow_SealedRefusesTrimmedVerbs(t *testing.T) {
	mw := SealedWindowMiddleware(newFakeSealed(true), nil)
	for _, req := range []struct{ method, path string }{
		{http.MethodGet, "/v1/barrier/officers"},
		{http.MethodDelete, "/v1/barrier/recovery"},
		{http.MethodPost, "/v1/barrier/recovery/generate"},
		{http.MethodGet, "/v1/barrier/recovery/status"},
		{http.MethodPost, "/v1/barrier/rekey"},
		{http.MethodPost, "/v1/barrier/shamir/verify"},
		{http.MethodPost, "/v1/init/enroll-external-officer"},
		{http.MethodGet, "/v1/bootstrap/auto"},
	} {
		t.Run(req.method+" "+req.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			r := httptest.NewRequest(req.method, req.path, nil)
			mw(okHandler).ServeHTTP(rr, r)
			if rr.Code != http.StatusServiceUnavailable {
				t.Errorf("%s %s status = %d, want 503", req.method, req.path, rr.Code)
			}
		})
	}
}

// TestSealedWindowListener_RefusesEverythingOnceUnsealed pins the boot-time
// listener's gate: it has no authentication behind it, so once the barrier
// is unsealed it refuses every request -- allowlisted ones included -- on
// the request itself.
func TestSealedWindowListener_RefusesEverythingOnceUnsealed(t *testing.T) {
	probe := newFakeSealed(true)
	mw := SealedWindowListenerMiddleware(probe, nil)
	send := func(method, path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		mw(okHandler).ServeHTTP(rr, httptest.NewRequest(method, path, nil))
		return rr
	}
	if rr := send(http.MethodPost, "/v1/barrier/unseal"); rr.Code != http.StatusOK {
		t.Fatalf("allowlisted verb while sealed: status = %d, want 200", rr.Code)
	}
	if rr := send(http.MethodGet, "/v1/credentials"); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("non-allowlisted route while sealed: status = %d, want 503", rr.Code)
	}
	probe.unseal()
	for _, req := range []struct{ method, path string }{
		{http.MethodPost, "/v1/barrier/unseal"},
		{http.MethodGet, "/v1/barrier/status"},
		{http.MethodDelete, "/v1/barrier/recovery"},
		{http.MethodGet, "/v1/credentials"},
	} {
		rr := send(req.method, req.path)
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s after unseal: status = %d, want 503", req.method, req.path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), `"code":"SEALED_WINDOW_CLOSED"`) {
			t.Errorf("%s %s after unseal: body = %q, want SEALED_WINDOW_CLOSED", req.method, req.path, rr.Body.String())
		}
		if rr.Header().Get("Retry-After") == "" {
			t.Errorf("%s %s after unseal: no Retry-After", req.method, req.path)
		}
	}
}

// TestSealedWindowListener_NilProbeFailsClosed: without a probe the
// listener's gate cannot know the barrier is sealed, so it refuses.
func TestSealedWindowListener_NilProbeFailsClosed(t *testing.T) {
	rr := httptest.NewRecorder()
	SealedWindowListenerMiddleware(nil, nil)(okHandler).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

// TestServer_WithSealedWindowListener_InstallsListenerGate verifies the
// server option selects the listener's gate and that WithSealedWindowProbe
// applied after it restores the regular one.
func TestServer_WithSealedWindowListener_InstallsListenerGate(t *testing.T) {
	probe := newFakeSealed(false)
	s := &Server{}
	WithSealedWindowListener(probe)(s)
	if s.sealedWindowProbe == nil || s.sealedWindowListener == nil {
		t.Fatal("WithSealedWindowListener did not install the listener gate")
	}
	WithSealedWindowProbe(probe)(s)
	if s.sealedWindowListener != nil {
		t.Fatal("WithSealedWindowProbe applied last must install the regular gate")
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

func TestRequestInAllowlist_PrefixExactAndMethod(t *testing.T) {
	allow := []string{"/v1/init/", "/v1/health", "POST /v1/officers"}
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/v1/init/", true},
		{http.MethodGet, "/v1/init/anything", true},
		{http.MethodGet, "/v1/init", false},
		{http.MethodGet, "/v1/health", true},
		{http.MethodGet, "/v1/health/sub", false},
		{http.MethodPost, "/v1/officers", true},
		{http.MethodGet, "/v1/officers", false},
	}
	for _, c := range cases {
		if got := requestInAllowlist(httptest.NewRequest(c.method, c.path, nil), allow); got != c.want {
			t.Errorf("%s %s got=%v want=%v", c.method, c.path, got, c.want)
		}
	}
}

// TestSealedWindowListener_OfficerRegistrationFollowsEnrollment: on the
// boot-time listener, anonymous officer and recovery-officer registration is
// served while enrollment is open and refused 403 ENROLLMENT_CLOSED once it
// has closed; the claim-status view and the regular gate are unaffected.
func TestSealedWindowListener_OfficerRegistrationFollowsEnrollment(t *testing.T) {
	probe := newFakeSealed(true)
	listener := SealedWindowListenerMiddleware(probe, nil)(okHandler)
	regular := SealedWindowMiddleware(probe, nil)(okHandler)
	send := func(h http.Handler, method, path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(method, path, nil))
		return rr
	}
	for _, path := range []string{"/v1/barrier/officers", "/v1/barrier/recovery/officers"} {
		rr := send(listener, http.MethodPost, path)
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), `"code":"ENROLLMENT_CLOSED"`) {
			t.Errorf("POST %s, enrollment closed: status %d body %q, want 403 ENROLLMENT_CLOSED", path, rr.Code, rr.Body.String())
		}
		if rr.Header().Get("Retry-After") != "" {
			t.Errorf("POST %s, enrollment closed: Retry-After set; waiting will not reopen enrollment", path)
		}
		if rr := send(regular, http.MethodPost, path); rr.Code != http.StatusOK {
			t.Errorf("regular gate, POST %s: status %d, want 200 (authentication decides)", path, rr.Code)
		}
	}
	if rr := send(listener, http.MethodGet, "/v1/barrier/officers/claim-status"); rr.Code != http.StatusOK {
		t.Errorf("claim-status with enrollment closed: status %d, want 200", rr.Code)
	}
	probe.enrollment.Store(true)
	for _, path := range []string{"/v1/barrier/officers", "/v1/barrier/recovery/officers"} {
		if rr := send(listener, http.MethodPost, path); rr.Code != http.StatusOK {
			t.Errorf("POST %s, enrollment open: status %d, want 200", path, rr.Code)
		}
	}
}

// TestSealedWindowGate_MarksListenerRequestsAnonymous: requests the boot-time
// listener admits carry the sealedwindow.Anonymous mark; a regular gate's do
// not.
func TestSealedWindowGate_MarksListenerRequestsAnonymous(t *testing.T) {
	probe := newFakeSealed(true)
	var anonymous bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anonymous = sealedwindow.Anonymous(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	SealedWindowListenerMiddleware(probe, nil)(h).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil))
	if !anonymous {
		t.Error("the listener did not mark an admitted request anonymous")
	}
	SealedWindowMiddleware(probe, nil)(h).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil))
	if anonymous {
		t.Error("a regular gate marked a request anonymous")
	}
}
