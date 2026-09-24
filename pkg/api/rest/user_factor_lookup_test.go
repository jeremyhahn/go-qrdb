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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestUserProjection_FactorLookupFailureIsUnavailable asserts a factor-store
// failure while projecting a user fails the response with 503 instead of
// reporting the account as having no second factor.
func TestUserProjection_FactorLookupFailureIsUnavailable(t *testing.T) {
	u := &user.User{ID: 1, Username: "alice"}
	users := &mockUserService{
		getUserFn: func(context.Context, uint64) (*user.User, error) { return u, nil },
		listUsersFn: func(context.Context, *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			return &user.ListUsersResponse{Users: []*user.User{u}}, nil
		},
	}
	factors := &mockUserAuthFactorService{listFn: func(context.Context, uint64) ([]*auth.AuthFactor, error) {
		return nil, errors.New("factor store down")
	}}
	s := &Server{userSvc: users, userAuthFactorSvc: factors}

	tests := []struct {
		name string
		call func(w http.ResponseWriter)
	}{
		{name: "get", call: func(w http.ResponseWriter) {
			s.handleGetUser(w, httptest.NewRequest(http.MethodGet, "/users/1", nil), 1)
		}},
		{name: "list", call: func(w http.ResponseWriter) {
			s.handleListUsers(w, httptest.NewRequest(http.MethodGet, "/users/list", nil))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tt.call(rec)
			assertStatus(t, rec, http.StatusServiceUnavailable)
		})
	}
}

func TestUserToResponse_FactorLookupError(t *testing.T) {
	factors := &mockUserAuthFactorService{listFn: func(context.Context, uint64) ([]*auth.AuthFactor, error) {
		return nil, errors.New("factor store down")
	}}
	resp, err := userToResponse(context.Background(), factors, &user.User{ID: 1})
	var fl *user.FactorLookupError
	if !errors.As(err, &fl) || resp != nil {
		t.Fatalf("userToResponse() = (%v, %v), want (nil, *user.FactorLookupError)", resp, err)
	}
	if got := mapHTTPError(err); got != http.StatusServiceUnavailable {
		t.Errorf("mapHTTPError(FactorLookupError) = %d, want 503", got)
	}
}
