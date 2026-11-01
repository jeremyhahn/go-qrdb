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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// mockUserServiceWithCert wraps mockUserService and overrides
// UserByCertThumbprint so the test can control the response.
type mockUserServiceWithCert struct {
	*mockUserService
	certFn func(ctx context.Context, thumbprint string) (*user.User, error)
}

func (m *mockUserServiceWithCert) UserByCertThumbprint(ctx context.Context, thumbprint string) (*user.User, error) {
	if m.certFn != nil {
		return m.certFn(ctx, thumbprint)
	}
	return nil, nil
}

func TestHandleGetUserByUsername_Success(t *testing.T) {
	svc := &mockUserService{
		getUserByUsernameFn: func(_ context.Context, tenantID, username string) (*user.User, error) {
			if tenantID != "t1" {
				t.Errorf("tenantID = %q, want t1", tenantID)
			}
			if username != "alice" {
				t.Errorf("username = %q, want alice", username)
			}
			return testUser(), nil
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/by-username/alice?tenantID=t1", nil)
	rec := httptest.NewRecorder()
	srv.handleUser(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp userResponse
	decodeJSON(t, rec, &resp)
	if resp.Username != "alice" {
		t.Fatalf("username = %q, want alice", resp.Username)
	}
}

func TestHandleGetUserByUsername_ServiceError(t *testing.T) {
	svc := &mockUserService{
		getUserByUsernameFn: func(_ context.Context, _, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "alice"}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/by-username/alice", nil)
	rec := httptest.NewRecorder()
	srv.handleUser(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleGetUserByUsername_MethodNotAllowed(t *testing.T) {
	srv := newAuthServer(&mockUserService{}, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/users/by-username/alice", nil)
	rec := httptest.NewRecorder()
	srv.handleUser(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleGetUserByUsername_EmptyUsername(t *testing.T) {
	srv := newAuthServer(&mockUserService{}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/by-username/", nil)
	rec := httptest.NewRecorder()
	srv.handleUser(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleGetUserByCertThumbprint_Success(t *testing.T) {
	certSvc := &mockUserServiceWithCert{
		mockUserService: &mockUserService{},
		certFn: func(_ context.Context, thumbprint string) (*user.User, error) {
			if thumbprint != "aabbcc" {
				t.Errorf("thumbprint = %q, want aabbcc", thumbprint)
			}
			return testUser(), nil
		},
	}
	// Through the router, so a route registered with the wrong handler fails
	// here; route_mapping_guard_test.go checks only the permission.
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), ":0",
		WithUserService(certSvc))

	req := httptest.NewRequest(http.MethodGet, "/users/by-cert-thumbprint/aabbcc", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp userResponse
	decodeJSON(t, rec, &resp)
	if resp.ID != 1001 {
		t.Fatalf("id = %d, want 1001", resp.ID)
	}
}

func TestHandleGetUserByCertThumbprint_NotFound(t *testing.T) {
	certSvc := &mockUserServiceWithCert{
		mockUserService: &mockUserService{},
		certFn: func(_ context.Context, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "cert_spki/deadbeef"}
		},
	}
	srv := newAuthServer(certSvc, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/by-cert-thumbprint/deadbeef", nil)
	rec := httptest.NewRecorder()
	srv.handleUser(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleGetUserByCertThumbprint_MethodNotAllowed(t *testing.T) {
	srv := newAuthServer(&mockUserService{}, nil, nil)

	req := httptest.NewRequest(http.MethodPut, "/users/by-cert-thumbprint/aabbcc", nil)
	rec := httptest.NewRecorder()
	srv.handleUser(rec, req)

	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

func TestHandleGetUserByCertThumbprint_EmptyThumbprint(t *testing.T) {
	srv := newAuthServer(&mockUserService{}, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/users/by-cert-thumbprint/", nil)
	rec := httptest.NewRecorder()
	srv.handleUser(rec, req)

	assertStatus(t, rec, http.StatusBadRequest)
}
