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

//go:build !integration

package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock: ServiceRegistry (for resolver error paths)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Helper: build test auth factor
// ---------------------------------------------------------------------------

func testAuthFactor() *auth.AuthFactor {
	return &auth.AuthFactor{
		ID:           "factor-abc",
		UserID:       42,
		Type:         auth.AuthFactorType("totp"),
		Label:        "Phone",
		Credential:   []byte("cred"),
		RegisteredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// ---------------------------------------------------------------------------
// handleGetUserByUsername
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// handleUserAuthFactorAdd
// ---------------------------------------------------------------------------

func TestHandleUserAuthFactorAdd_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/users/factors", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorAdd(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleUserAuthFactorAdd_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(&mockUserAuthFactorService{}))
	req := httptest.NewRequest(http.MethodPost, "/v1/users/factors", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorAdd(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUserAuthFactorAdd_Success(t *testing.T) {
	added := false
	svc := &mockUserAuthFactorService{
		addFn: func(_ context.Context, userID uint64, _ *auth.AuthFactor) error {
			if userID != 7 {
				t.Errorf("userID = %d, want 7", userID)
			}
			added = true
			return nil
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))

	body := `{"user_id":7,"type":"totp","label":"Phone","credential":"Y3JlZA=="}`
	req := httptest.NewRequest(http.MethodPost, "/v1/users/factors", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorAdd(rec, req)
	assertStatus(t, rec, http.StatusOK)
	if !added {
		t.Error("Add not called")
	}
}

func TestHandleUserAuthFactorAdd_ServiceError(t *testing.T) {
	svc := &mockUserAuthFactorService{
		addFn: func(_ context.Context, _ uint64, _ *auth.AuthFactor) error {
			return auth.ErrFactorAlreadyExists
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))

	body := `{"user_id":7,"type":"totp"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/users/factors", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorAdd(rec, req)
	assertStatus(t, rec, http.StatusConflict)
}

// ---------------------------------------------------------------------------
// handleUserAuthFactorList
// ---------------------------------------------------------------------------

func TestHandleUserAuthFactorList_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/v1/users/factors/list?user_id=1", nil)
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorList(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleUserAuthFactorList_BadUserID(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(&mockUserAuthFactorService{}))
	for name, query := range map[string]string{
		"missing": "",
		"invalid": "?user_id=abc",
		"zero":    "?user_id=0",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/users/factors/list"+query, nil)
			rec := httptest.NewRecorder()
			s.handleUserAuthFactorList(rec, req)
			assertStatus(t, rec, http.StatusBadRequest)
		})
	}
}

func TestHandleUserAuthFactorList_Success(t *testing.T) {
	svc := &mockUserAuthFactorService{
		listFn: func(_ context.Context, userID uint64) ([]*auth.AuthFactor, error) {
			if userID != 42 {
				t.Errorf("userID = %d, want 42", userID)
			}
			return []*auth.AuthFactor{testAuthFactor()}, nil
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/users/factors/list?user_id=42", nil)
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorList(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp transport.AuthFactorListResponse
	decodeJSON(t, rec, &resp)
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
}

func TestHandleUserAuthFactorList_ServiceError(t *testing.T) {
	svc := &mockUserAuthFactorService{
		listFn: func(_ context.Context, _ uint64) ([]*auth.AuthFactor, error) {
			return nil, auth.ErrUserHasNoFactors
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/users/factors/list?user_id=99", nil)
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorList(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// handleUserAuthFactorRotate
// ---------------------------------------------------------------------------

func TestHandleUserAuthFactorRotate_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/users/factors/rotate", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorRotate(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleUserAuthFactorRotate_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(&mockUserAuthFactorService{}))
	req := httptest.NewRequest(http.MethodPost, "/v1/users/factors/rotate", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorRotate(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUserAuthFactorRotate_Success(t *testing.T) {
	rotated := false
	svc := &mockUserAuthFactorService{
		rotateFn: func(_ context.Context, oldID string, _ *auth.AuthFactor) error {
			if oldID != "old-factor" {
				t.Errorf("oldID = %q, want old-factor", oldID)
			}
			rotated = true
			return nil
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))

	body := `{"old_factor_id":"old-factor","new_factor":{"user_id":5,"type":"totp"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/users/factors/rotate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorRotate(rec, req)
	assertStatus(t, rec, http.StatusOK)
	if !rotated {
		t.Error("Rotate not called")
	}
}

func TestHandleUserAuthFactorRotate_ServiceError(t *testing.T) {
	svc := &mockUserAuthFactorService{
		rotateFn: func(_ context.Context, _ string, _ *auth.AuthFactor) error {
			return auth.ErrFactorNotFound
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))
	body := `{"old_factor_id":"x","new_factor":{"user_id":5,"type":"totp"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/users/factors/rotate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorRotate(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// handleUserAuthFactorLogin
// ---------------------------------------------------------------------------

func TestHandleUserAuthFactorLogin_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodPost, "/v1/users/factors/login", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorLogin(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleUserAuthFactorLogin_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(&mockUserAuthFactorService{}))
	req := httptest.NewRequest(http.MethodPost, "/v1/users/factors/login", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorLogin(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUserAuthFactorLogin_Success(t *testing.T) {
	svc := &mockUserAuthFactorService{
		lookupByCredentialFn: func(_ context.Context, t auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
			return &auth.AuthFactor{ID: "f1", UserID: 99, Type: t}, nil
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))

	body := `{"type":"totp","lookup_key":"dGVzdA=="}`
	req := httptest.NewRequest(http.MethodPost, "/v1/users/factors/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorLogin(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp transport.AuthFactorLoginResponse
	decodeJSON(t, rec, &resp)
	if resp.UserID != 99 {
		t.Fatalf("user_id = %d, want 99", resp.UserID)
	}
	if !resp.Active {
		t.Error("active = false, want true")
	}
}

func TestHandleUserAuthFactorLogin_ServiceError(t *testing.T) {
	svc := &mockUserAuthFactorService{
		lookupByCredentialFn: func(_ context.Context, _ auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
			return nil, auth.ErrFactorNotFound
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))
	body := `{"type":"totp","lookup_key":"dGVzdA=="}`
	req := httptest.NewRequest(http.MethodPost, "/v1/users/factors/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorLogin(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// handleUserAuthFactorByPath – GET, DELETE, PUT /label, method-not-allowed
// ---------------------------------------------------------------------------

func TestHandleUserAuthFactorByPath_NilService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	req := httptest.NewRequest(http.MethodGet, "/v1/users/factors/f1", nil)
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorByPath(rec, req)
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleUserAuthFactorByPath_EmptyFactorID(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(&mockUserAuthFactorService{}))
	req := httptest.NewRequest(http.MethodGet, "/v1/users/factors/", nil)
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorByPath(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUserAuthFactorByPath_Get_Success(t *testing.T) {
	svc := &mockUserAuthFactorService{
		getFn: func(_ context.Context, factorID string) (*auth.AuthFactor, error) {
			if factorID != "f1" {
				t.Errorf("factorID = %q, want f1", factorID)
			}
			return testAuthFactor(), nil
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/users/factors/f1", nil)
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorByPath(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var resp transport.AuthFactorGetResponse
	decodeJSON(t, rec, &resp)
	if resp.Factor.ID != "factor-abc" {
		t.Fatalf("factor_id = %q, want factor-abc", resp.Factor.ID)
	}
}

func TestHandleUserAuthFactorByPath_Get_ServiceError(t *testing.T) {
	svc := &mockUserAuthFactorService{
		getFn: func(_ context.Context, _ string) (*auth.AuthFactor, error) {
			return nil, auth.ErrFactorNotFound
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))
	req := httptest.NewRequest(http.MethodGet, "/v1/users/factors/missing", nil)
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorByPath(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleUserAuthFactorByPath_Delete_Success(t *testing.T) {
	removed := false
	svc := &mockUserAuthFactorService{
		removeFn: func(_ context.Context, factorID string, force bool) error {
			if factorID != "f1" {
				t.Errorf("factorID = %q, want f1", factorID)
			}
			if force {
				t.Error("force should be false by default")
			}
			removed = true
			return nil
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))
	req := httptest.NewRequest(http.MethodDelete, "/v1/users/factors/f1", nil)
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorByPath(rec, req)
	assertStatus(t, rec, http.StatusOK)
	if !removed {
		t.Error("Remove not called")
	}
}

func TestHandleUserAuthFactorByPath_Delete_Force(t *testing.T) {
	var sawForce bool
	svc := &mockUserAuthFactorService{
		removeFn: func(_ context.Context, _ string, force bool) error {
			sawForce = force
			return nil
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))
	req := httptest.NewRequest(http.MethodDelete, "/v1/users/factors/f1?force=true", nil)
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorByPath(rec, req)
	assertStatus(t, rec, http.StatusOK)
	if !sawForce {
		t.Error("force=true not passed to Remove")
	}
}

func TestHandleUserAuthFactorByPath_Delete_LastFactor(t *testing.T) {
	svc := &mockUserAuthFactorService{
		removeFn: func(_ context.Context, _ string, _ bool) error {
			return auth.ErrLastFactor
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))
	req := httptest.NewRequest(http.MethodDelete, "/v1/users/factors/f1", nil)
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorByPath(rec, req)
	assertStatus(t, rec, http.StatusConflict)
}

func TestHandleUserAuthFactorByPath_Put_Label_Success(t *testing.T) {
	relabeled := false
	svc := &mockUserAuthFactorService{
		relabelFn: func(_ context.Context, factorID, label string) error {
			if factorID != "f1" || label != "Work Phone" {
				t.Errorf("relabel(%q, %q) unexpected", factorID, label)
			}
			relabeled = true
			return nil
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))

	body := `{"label":"Work Phone"}`
	req := httptest.NewRequest(http.MethodPut, "/v1/users/factors/f1/label", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorByPath(rec, req)
	assertStatus(t, rec, http.StatusOK)
	if !relabeled {
		t.Error("Relabel not called")
	}
}

func TestHandleUserAuthFactorByPath_Put_Label_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(&mockUserAuthFactorService{}))
	req := httptest.NewRequest(http.MethodPut, "/v1/users/factors/f1/label", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorByPath(rec, req)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUserAuthFactorByPath_Put_Label_RelabelError(t *testing.T) {
	svc := &mockUserAuthFactorService{
		relabelFn: func(_ context.Context, _ string, _ string) error {
			return auth.ErrFactorNotFound
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))
	req := httptest.NewRequest(http.MethodPut, "/v1/users/factors/f1/label", strings.NewReader(`{"label":"x"}`))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorByPath(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleUserAuthFactorByPath_Put_Label_GetAfterRelabelError(t *testing.T) {
	// Relabel succeeds but the subsequent Get fails.
	svc := &mockUserAuthFactorService{
		relabelFn: func(_ context.Context, _ string, _ string) error { return nil },
		getFn: func(_ context.Context, _ string) (*auth.AuthFactor, error) {
			return nil, auth.ErrFactorNotFound
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(svc))
	req := httptest.NewRequest(http.MethodPut, "/v1/users/factors/f1/label", strings.NewReader(`{"label":"x"}`))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorByPath(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleUserAuthFactorByPath_Put_NoLabelSubpath_NotFound(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(&mockUserAuthFactorService{}))
	// PUT without the /label sub-path should 404.
	req := httptest.NewRequest(http.MethodPut, "/v1/users/factors/f1/other", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorByPath(rec, req)
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleUserAuthFactorByPath_MethodNotAllowed(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0",
		WithUserAuthFactorService(&mockUserAuthFactorService{}))
	req := httptest.NewRequest(http.MethodPost, "/v1/users/factors/f1", nil)
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorByPath(rec, req)
	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

// ---------------------------------------------------------------------------
// userAuthFactorToRecord – nil guard
// ---------------------------------------------------------------------------

func TestUserAuthFactorToRecord_Nil(t *testing.T) {
	rec := userAuthFactorToRecord(nil)
	if rec.ID != "" {
		t.Errorf("expected empty record for nil factor, got %+v", rec)
	}
}

func TestUserAuthFactorToRecord_Full(t *testing.T) {
	f := testAuthFactor()
	rec := userAuthFactorToRecord(f)
	if rec.ID != f.ID {
		t.Errorf("ID = %q, want %q", rec.ID, f.ID)
	}
	if rec.UserID != f.UserID {
		t.Errorf("UserID = %d, want %d", rec.UserID, f.UserID)
	}
	if rec.Label != f.Label {
		t.Errorf("Label = %q, want %q", rec.Label, f.Label)
	}
}

// ---------------------------------------------------------------------------
// mapHTTPError – all error codes
// ---------------------------------------------------------------------------

func TestMapUserAuthFactorError_AllCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, http.StatusOK},
		{"factor not found", auth.ErrFactorNotFound, http.StatusNotFound},
		{"factor revoked", auth.ErrFactorRevoked, http.StatusGone},
		{"last factor", auth.ErrLastFactor, http.StatusConflict},
		{"already exists", auth.ErrFactorAlreadyExists, http.StatusConflict},
		{"type unsupported", auth.ErrFactorTypeUnsupported, http.StatusBadRequest},
		{"invalid factor", auth.ErrInvalidFactor, http.StatusBadRequest},
		{"no factors", auth.ErrUserHasNoFactors, http.StatusNotFound},
		{"unknown", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapHTTPError(tc.err)
			if got != tc.want {
				t.Errorf("mapHTTPError(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}
