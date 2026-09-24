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

package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestUserProjection_FactorLookupFailureIsUnavailable asserts a factor-store
// failure while projecting a user fails the RPC with Unavailable instead of
// reporting the account as having no second factor.
func TestUserProjection_FactorLookupFailureIsUnavailable(t *testing.T) {
	u := &user.User{ID: 1, Username: "alice"}
	svc := &mockUserService{getResult: u, listResult: &user.ListUsersResponse{Users: []*user.User{u}}}
	s := &qrdbServiceServer{userSvc: svc, userAuthFactorSvc: &mockUserAuthFactorSvc{listErr: errors.New("factor store down")}}

	tests := []struct {
		name string
		call func() error
	}{
		{name: "get", call: func() error {
			_, err := s.GetUser(context.Background(), &pb.GetUserRequest{Id: 1})
			return err
		}},
		{name: "list", call: func() error {
			_, err := s.ListUsers(context.Background(), &pb.ListUsersRequest{})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := status.Code(tt.call()); got != codes.Unavailable {
				t.Fatalf("code = %v, want Unavailable", got)
			}
		})
	}
}
