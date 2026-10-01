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

// DELETE /users/{id}/cert-bindings/{thumbprint} — the withdrawal verb
// (go-qrdb#518). What these assert is that the ROUTE reaches the service with
// the arguments the path named, and that the store's refusals reach the client
// as the right status. The effect of a withdrawal on authentication is
// asserted against the real authenticator and the real store in
// pkg/auth/middleware.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestHandleRemoveCertBinding_RoutesUserIDAndThumbprint is the reachability
// assertion: both path parameters arrive at the service intact. A thumbprint
// that lost a character in transit would withdraw nothing and report a
// not-found, which is the failure mode this pins.
func TestHandleRemoveCertBinding_RoutesUserIDAndThumbprint(t *testing.T) {
	const thumbprint = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var gotID uint64
	var gotThumbprint string
	svc := &mockUserService{
		removeCertBindingFn: func(_ context.Context, userID uint64, tp string) (*user.User, error) {
			gotID, gotThumbprint = userID, tp
			return testUser(), nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/users/1001/cert-bindings/"+thumbprint, nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if gotID != 1001 {
		t.Fatalf("userID = %d, want 1001", gotID)
	}
	if gotThumbprint != thumbprint {
		t.Fatalf("thumbprint = %q, want %q", gotThumbprint, thumbprint)
	}

	var resp userResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != 1001 {
		t.Fatalf("response id = %d, want 1001", resp.ID)
	}
}

// TestHandleRemoveCertBinding_UnheldBindingIs404 pins the refusal. A
// withdrawal that removed nothing must not read as a success: an operator who
// mistyped a thumbprint would otherwise believe a live key was withdrawn.
func TestHandleRemoveCertBinding_UnheldBindingIs404(t *testing.T) {
	svc := &mockUserService{
		removeCertBindingFn: func(_ context.Context, userID uint64, tp string) (*user.User, error) {
			return nil, &user.CertBindingNotFoundError{UserID: userID, Thumbprint: tp}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/users/1001/cert-bindings/AAAA", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

// TestHandleRemoveCertBinding_EmptyThumbprintDoesNotDeleteTheAccount is the
// misreading worth pinning: a withdrawal whose thumbprint went missing must
// never fall through to DELETE /users/{id} and remove the ACCOUNT. The router
// declines the empty path segment outright, which is the answer; what matters
// is that the delete verb is not reached.
func TestHandleRemoveCertBinding_EmptyThumbprintDoesNotDeleteTheAccount(t *testing.T) {
	var deleted bool
	svc := &mockUserService{
		deleteUserFn: func(_ context.Context, _ uint64) error {
			deleted = true
			return nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/users/1001/cert-bindings/", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
	if deleted {
		t.Fatal("a withdrawal with no thumbprint deleted the account")
	}
}

// TestHandleRemoveCertBinding_EmptyThumbprintIs400AtTheDispatcher covers the
// dispatcher's own guard, which the router above never reaches. It is kept
// because handleUser re-parses the path rather than reading chi's parameters:
// the guard is what stands between a future route registration that admits an
// empty segment and a withdrawal that names no binding.
func TestHandleRemoveCertBinding_EmptyThumbprintIs400AtTheDispatcher(t *testing.T) {
	var called bool
	svc := &mockUserService{
		removeCertBindingFn: func(_ context.Context, _ uint64, _ string) (*user.User, error) {
			called = true
			return testUser(), nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/users/1001/cert-bindings/", nil)
	rec := httptest.NewRecorder()
	srv.handleUser(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
	if called {
		t.Fatal("the service was asked to withdraw an empty thumbprint")
	}
}

// TestHandleRemoveCertBinding_ConcurrentUpdateIs409 covers the exhausted-retry
// answer. The store retries a lost commit a bounded number of times and then
// reports the conflict rather than pretending; the client must be told to
// re-issue, not that the server broke.
func TestHandleRemoveCertBinding_ConcurrentUpdateIs409(t *testing.T) {
	svc := &mockUserService{
		removeCertBindingFn: func(_ context.Context, userID uint64, _ string) (*user.User, error) {
			return nil, &user.ConcurrentUpdateError{ID: userID}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodDelete, "/users/1001/cert-bindings/AAAA", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

// TestHandleAddCertBinding_HijackAttemptIs409 pins the status of the store's
// most security-relevant refusal: a thumbprint already bound to ANOTHER user
// in the tenant is a 409, not a 500. Before go-qrdb#536 it fell through to the
// StoreError arm, so an operator investigating a failed enrolment read "the
// server broke" instead of the one fact that matters — which user holds the
// thumbprint — and the body must carry that fact.
func TestHandleAddCertBinding_HijackAttemptIs409(t *testing.T) {
	svc := &mockUserService{
		addCertBindingFn: func(_ context.Context, userID uint64, tp string, _ int64) (*user.User, error) {
			return nil, &user.CertBindingConflictError{Thumbprint: tp, BoundUserID: 7, TargetUserID: userID}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/users/1001/cert-bindings",
		strings.NewReader(`{"thumbprint":"AAAA"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
	if body := rec.Body.String(); !strings.Contains(body, "already bound to user 7") {
		t.Fatalf("body must name the user that holds the thumbprint, got %q", body)
	}
}

// TestUserVerbs_BootstrapAdminLockoutIs409 (go-qrdb#517): the store's refusal
// of a change that would lock the bootstrap admin out is a 409 carrying its
// message on every user verb that can make one, never a success or a 500.
func TestUserVerbs_BootstrapAdminLockoutIs409(t *testing.T) {
	lockout := func(reason user.AdminLockoutReason) error {
		return &user.BootstrapAdminLockoutError{UserID: 1001, Reason: reason}
	}
	cases := []struct {
		name   string
		svc    *mockUserService
		method string
		path   string
		body   string
		want   string
	}{
		{"withdraw the last binding", &mockUserService{
			removeCertBindingFn: func(context.Context, uint64, string) (*user.User, error) {
				return nil, lockout(user.AdminLockoutNoLiveBinding)
			},
		}, http.MethodDelete, "/users/1001/cert-bindings/AAAA", "", "enrol a replacement key first"},
		{"delete the admin", &mockUserService{
			deleteUserFn: func(context.Context, uint64) error { return lockout(user.AdminLockoutDelete) },
		}, http.MethodDelete, "/users/1001", "", "cannot be deleted"},
		{"disable the admin", &mockUserService{
			disableUserFn: func(context.Context, uint64) error { return lockout(user.AdminLockoutLifecycle) },
		}, http.MethodPost, "/users/1001/disable", "", "state that can log in"},
		{"rename the admin", &mockUserService{
			updateUserFn: func(context.Context, *user.UpdateUserRequest) (*user.User, error) {
				return nil, lockout(user.AdminLockoutRename)
			},
		}, http.MethodPut, "/users/1001", `{"username":"root"}`, "cannot be renamed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newAuthServer(tc.svc, nil, nil)
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)
			assertStatus(t, rec, http.StatusConflict)
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body %q does not carry the refusal %q", rec.Body.String(), tc.want)
			}
		})
	}
}

// TestHandleAddCertBinding_PastExpiryIs400 (go-qrdb#517): an enrolment whose
// expiry has already passed is the caller's bad field value, not a server
// fault.
func TestHandleAddCertBinding_PastExpiryIs400(t *testing.T) {
	svc := &mockUserService{
		addCertBindingFn: func(_ context.Context, _ uint64, tp string, notAfter int64) (*user.User, error) {
			return nil, &user.CertBindingExpiredError{Thumbprint: tp, NotAfter: time.Unix(notAfter, 0).UTC()}
		},
	}
	srv := newAuthServer(svc, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/users/1001/cert-bindings",
		strings.NewReader(`{"thumbprint":"AAAA","not_after_unix":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "not in the future") {
		t.Fatalf("body %q does not carry the refusal", rec.Body.String())
	}
}
