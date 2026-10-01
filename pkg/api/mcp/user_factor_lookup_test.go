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

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// failingFactorLister fails every List, standing in for an unreachable
// factor store.
type failingFactorLister struct{}

func (failingFactorLister) List(context.Context, uint64) ([]*auth.AuthFactor, error) {
	return nil, errors.New("factor store down")
}

// TestUserProjection_FactorLookupFailureIsUnavailable asserts a factor-store
// failure while projecting a user fails the tool with CodeUnavailable instead
// of reporting the account as having no second factor.
func TestUserProjection_FactorLookupFailureIsUnavailable(t *testing.T) {
	u := &user.User{ID: 1, Username: "alice"}
	svc := &mockUserService{
		getUserFn: func(context.Context, uint64) (*user.User, error) { return u, nil },
		listUsersFn: func(context.Context, *user.ListUsersRequest) (*user.ListUsersResponse, error) {
			return &user.ListUsersResponse{Users: []*user.User{u}}, nil
		},
	}

	tests := []struct {
		name string
		call func() error
	}{
		{name: "get", call: func() error {
			_, err := handleUserGet(svc, failingFactorLister{})(context.Background(), makeReq(map[string]any{"id": "1"}))
			return err
		}},
		{name: "list", call: func() error {
			_, err := handleUserList(svc, failingFactorLister{})(context.Background(), makeReq(map[string]any{}))
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			var fl *user.FactorLookupError
			if !errors.As(err, &fl) {
				t.Fatalf("err = %v, want *user.FactorLookupError", err)
			}
			if got := ServiceErrorCode(err); got != wire.CodeUnavailable {
				t.Errorf("ServiceErrorCode() = %v, want CodeUnavailable", got)
			}
		})
	}
}
