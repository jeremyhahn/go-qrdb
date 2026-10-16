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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func deleteRequest(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/v1/users/factors/delete", strings.NewReader(body))
}

func TestHandleUserAuthFactorDelete_Success(t *testing.T) {
	var gotID string
	var gotForce bool
	svc := &mockUserAuthFactorService{
		deleteFn: func(_ context.Context, factorID string, force bool) error {
			gotID, gotForce = factorID, force
			return nil
		},
	}
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithUserAuthFactorService(svc))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorDelete(rec, deleteRequest(`{"factor_id":"f-1","force":true}`))
	assertStatus(t, rec, http.StatusOK)

	var resp transport.AuthFactorDeleteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "deleted" {
		t.Fatalf("status = %q, want deleted", resp.Status)
	}
	if gotID != "f-1" || !gotForce {
		t.Fatalf("service saw (%q, %v), want (f-1, true)", gotID, gotForce)
	}
}

func TestHandleUserAuthFactorDelete_ServiceErrors(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"not found": {err: &auth.FactorNotFoundError{FactorID: "x"}, want: http.StatusNotFound},
		"revoked":   {err: &auth.FactorRevokedError{FactorID: "x"}, want: mapHTTPError(auth.ErrFactorRevoked)},
		"last":      {err: &auth.LastFactorError{UserID: 1, FactorID: "x"}, want: mapHTTPError(auth.ErrLastFactor)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := &mockUserAuthFactorService{
				deleteFn: func(context.Context, string, bool) error { return tc.err },
			}
			s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithUserAuthFactorService(svc))
			rec := httptest.NewRecorder()
			s.handleUserAuthFactorDelete(rec, deleteRequest(`{"factor_id":"x"}`))
			assertStatus(t, rec, tc.want)
		})
	}
}

func TestHandleUserAuthFactorDelete_BadBody(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithUserAuthFactorService(&mockUserAuthFactorService{}))
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorDelete(rec, deleteRequest(`not-json`))
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleUserAuthFactorDelete_NoService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	rec := httptest.NewRecorder()
	s.handleUserAuthFactorDelete(rec, deleteRequest(`{"factor_id":"x"}`))
	assertStatus(t, rec, http.StatusNotImplemented)
}
