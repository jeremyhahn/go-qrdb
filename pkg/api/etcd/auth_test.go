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

//go:build qrdb_etcd && !integration

package etcd

import (
	"context"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthServer_AuthStatus_ReturnsDisabled(t *testing.T) {
	s := &authServer{}
	resp, err := s.AuthStatus(context.Background(), &pb.AuthStatusRequest{})
	if err != nil {
		t.Fatalf("AuthStatus returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("AuthStatus returned nil response")
	}
	if resp.Enabled {
		t.Error("AuthStatus.Enabled = true, want false")
	}
}

func TestAuthServer_AuthStatus_NilRequest(t *testing.T) {
	s := &authServer{}
	resp, err := s.AuthStatus(context.Background(), nil)
	if err != nil {
		t.Fatalf("AuthStatus with nil request returned unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("AuthStatus with nil request returned nil response")
	}
	if resp.Enabled {
		t.Error("AuthStatus.Enabled = true, want false")
	}
}

func TestAuthServer_UnimplementedMethods(t *testing.T) {
	tests := []struct {
		name string
		call func(s *authServer) error
	}{
		{
			name: "AuthEnable",
			call: func(s *authServer) error {
				_, err := s.AuthEnable(context.Background(), &pb.AuthEnableRequest{})
				return err
			},
		},
		{
			name: "AuthDisable",
			call: func(s *authServer) error {
				_, err := s.AuthDisable(context.Background(), &pb.AuthDisableRequest{})
				return err
			},
		},
		{
			name: "Authenticate",
			call: func(s *authServer) error {
				_, err := s.Authenticate(context.Background(), &pb.AuthenticateRequest{})
				return err
			},
		},
		{
			name: "UserAdd",
			call: func(s *authServer) error {
				_, err := s.UserAdd(context.Background(), &pb.AuthUserAddRequest{})
				return err
			},
		},
		{
			name: "UserGet",
			call: func(s *authServer) error {
				_, err := s.UserGet(context.Background(), &pb.AuthUserGetRequest{})
				return err
			},
		},
		{
			name: "UserList",
			call: func(s *authServer) error {
				_, err := s.UserList(context.Background(), &pb.AuthUserListRequest{})
				return err
			},
		},
		{
			name: "UserDelete",
			call: func(s *authServer) error {
				_, err := s.UserDelete(context.Background(), &pb.AuthUserDeleteRequest{})
				return err
			},
		},
		{
			name: "UserChangePassword",
			call: func(s *authServer) error {
				_, err := s.UserChangePassword(context.Background(), &pb.AuthUserChangePasswordRequest{})
				return err
			},
		},
		{
			name: "UserGrantRole",
			call: func(s *authServer) error {
				_, err := s.UserGrantRole(context.Background(), &pb.AuthUserGrantRoleRequest{})
				return err
			},
		},
		{
			name: "UserRevokeRole",
			call: func(s *authServer) error {
				_, err := s.UserRevokeRole(context.Background(), &pb.AuthUserRevokeRoleRequest{})
				return err
			},
		},
		{
			name: "RoleAdd",
			call: func(s *authServer) error {
				_, err := s.RoleAdd(context.Background(), &pb.AuthRoleAddRequest{})
				return err
			},
		},
		{
			name: "RoleGet",
			call: func(s *authServer) error {
				_, err := s.RoleGet(context.Background(), &pb.AuthRoleGetRequest{})
				return err
			},
		},
		{
			name: "RoleList",
			call: func(s *authServer) error {
				_, err := s.RoleList(context.Background(), &pb.AuthRoleListRequest{})
				return err
			},
		},
		{
			name: "RoleDelete",
			call: func(s *authServer) error {
				_, err := s.RoleDelete(context.Background(), &pb.AuthRoleDeleteRequest{})
				return err
			},
		},
		{
			name: "RoleGrantPermission",
			call: func(s *authServer) error {
				_, err := s.RoleGrantPermission(context.Background(), &pb.AuthRoleGrantPermissionRequest{})
				return err
			},
		},
		{
			name: "RoleRevokePermission",
			call: func(s *authServer) error {
				_, err := s.RoleRevokePermission(context.Background(), &pb.AuthRoleRevokePermissionRequest{})
				return err
			},
		},
	}

	s := &authServer{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(s)
			if err == nil {
				t.Fatalf("%s: expected error, got nil", tt.name)
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("%s: expected gRPC status error, got %T: %v", tt.name, err, err)
			}
			if st.Code() != codes.Unimplemented {
				t.Errorf("%s: code = %v, want %v", tt.name, st.Code(), codes.Unimplemented)
			}
			if st.Message() == "" {
				t.Errorf("%s: expected non-empty error message", tt.name)
			}
		})
	}
}

func TestAuthServer_UnimplementedMethods_NilRequests(t *testing.T) {
	tests := []struct {
		name string
		call func(s *authServer) error
	}{
		{
			name: "AuthEnable_nil",
			call: func(s *authServer) error {
				_, err := s.AuthEnable(context.Background(), nil)
				return err
			},
		},
		{
			name: "AuthDisable_nil",
			call: func(s *authServer) error {
				_, err := s.AuthDisable(context.Background(), nil)
				return err
			},
		},
		{
			name: "Authenticate_nil",
			call: func(s *authServer) error {
				_, err := s.Authenticate(context.Background(), nil)
				return err
			},
		},
		{
			name: "UserAdd_nil",
			call: func(s *authServer) error {
				_, err := s.UserAdd(context.Background(), nil)
				return err
			},
		},
		{
			name: "UserGet_nil",
			call: func(s *authServer) error {
				_, err := s.UserGet(context.Background(), nil)
				return err
			},
		},
		{
			name: "UserList_nil",
			call: func(s *authServer) error {
				_, err := s.UserList(context.Background(), nil)
				return err
			},
		},
		{
			name: "UserDelete_nil",
			call: func(s *authServer) error {
				_, err := s.UserDelete(context.Background(), nil)
				return err
			},
		},
		{
			name: "UserChangePassword_nil",
			call: func(s *authServer) error {
				_, err := s.UserChangePassword(context.Background(), nil)
				return err
			},
		},
		{
			name: "UserGrantRole_nil",
			call: func(s *authServer) error {
				_, err := s.UserGrantRole(context.Background(), nil)
				return err
			},
		},
		{
			name: "UserRevokeRole_nil",
			call: func(s *authServer) error {
				_, err := s.UserRevokeRole(context.Background(), nil)
				return err
			},
		},
		{
			name: "RoleAdd_nil",
			call: func(s *authServer) error {
				_, err := s.RoleAdd(context.Background(), nil)
				return err
			},
		},
		{
			name: "RoleGet_nil",
			call: func(s *authServer) error {
				_, err := s.RoleGet(context.Background(), nil)
				return err
			},
		},
		{
			name: "RoleList_nil",
			call: func(s *authServer) error {
				_, err := s.RoleList(context.Background(), nil)
				return err
			},
		},
		{
			name: "RoleDelete_nil",
			call: func(s *authServer) error {
				_, err := s.RoleDelete(context.Background(), nil)
				return err
			},
		},
		{
			name: "RoleGrantPermission_nil",
			call: func(s *authServer) error {
				_, err := s.RoleGrantPermission(context.Background(), nil)
				return err
			},
		},
		{
			name: "RoleRevokePermission_nil",
			call: func(s *authServer) error {
				_, err := s.RoleRevokePermission(context.Background(), nil)
				return err
			},
		},
	}

	s := &authServer{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(s)
			if err == nil {
				t.Fatalf("%s: expected error, got nil", tt.name)
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("%s: expected gRPC status error, got %T: %v", tt.name, err, err)
			}
			if st.Code() != codes.Unimplemented {
				t.Errorf("%s: code = %v, want %v", tt.name, st.Code(), codes.Unimplemented)
			}
		})
	}
}
