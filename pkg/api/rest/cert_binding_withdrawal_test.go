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
	"testing"

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
