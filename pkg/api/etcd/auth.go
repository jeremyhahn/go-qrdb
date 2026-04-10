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

package etcd

import (
	"context"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// authServer implements etcdserverpb.AuthServer as a stub. All methods
// return "auth not enabled" since qrdb uses mTLS for authentication.
type authServer struct {
	pb.UnimplementedAuthServer
}

// AuthEnable is not supported; qrdb uses mTLS for authentication.
func (s *authServer) AuthEnable(_ context.Context, _ *pb.AuthEnableRequest) (*pb.AuthEnableResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// AuthDisable is not supported; qrdb uses mTLS for authentication.
func (s *authServer) AuthDisable(_ context.Context, _ *pb.AuthDisableRequest) (*pb.AuthDisableResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// AuthStatus reports that etcd auth is disabled.
func (s *authServer) AuthStatus(_ context.Context, _ *pb.AuthStatusRequest) (*pb.AuthStatusResponse, error) {
	return &pb.AuthStatusResponse{
		Enabled: false,
	}, nil
}

// Authenticate is not supported; qrdb uses mTLS for authentication.
func (s *authServer) Authenticate(_ context.Context, _ *pb.AuthenticateRequest) (*pb.AuthenticateResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// UserAdd is not supported; qrdb uses mTLS for authentication.
func (s *authServer) UserAdd(_ context.Context, _ *pb.AuthUserAddRequest) (*pb.AuthUserAddResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// UserGet is not supported; qrdb uses mTLS for authentication.
func (s *authServer) UserGet(_ context.Context, _ *pb.AuthUserGetRequest) (*pb.AuthUserGetResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// UserList is not supported; qrdb uses mTLS for authentication.
func (s *authServer) UserList(_ context.Context, _ *pb.AuthUserListRequest) (*pb.AuthUserListResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// UserDelete is not supported; qrdb uses mTLS for authentication.
func (s *authServer) UserDelete(_ context.Context, _ *pb.AuthUserDeleteRequest) (*pb.AuthUserDeleteResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// UserChangePassword is not supported; qrdb uses mTLS for authentication.
func (s *authServer) UserChangePassword(_ context.Context, _ *pb.AuthUserChangePasswordRequest) (*pb.AuthUserChangePasswordResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// UserGrantRole is not supported; qrdb uses mTLS for authentication.
func (s *authServer) UserGrantRole(_ context.Context, _ *pb.AuthUserGrantRoleRequest) (*pb.AuthUserGrantRoleResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// UserRevokeRole is not supported; qrdb uses mTLS for authentication.
func (s *authServer) UserRevokeRole(_ context.Context, _ *pb.AuthUserRevokeRoleRequest) (*pb.AuthUserRevokeRoleResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// RoleAdd is not supported; qrdb uses mTLS for authentication.
func (s *authServer) RoleAdd(_ context.Context, _ *pb.AuthRoleAddRequest) (*pb.AuthRoleAddResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// RoleGet is not supported; qrdb uses mTLS for authentication.
func (s *authServer) RoleGet(_ context.Context, _ *pb.AuthRoleGetRequest) (*pb.AuthRoleGetResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// RoleList is not supported; qrdb uses mTLS for authentication.
func (s *authServer) RoleList(_ context.Context, _ *pb.AuthRoleListRequest) (*pb.AuthRoleListResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// RoleDelete is not supported; qrdb uses mTLS for authentication.
func (s *authServer) RoleDelete(_ context.Context, _ *pb.AuthRoleDeleteRequest) (*pb.AuthRoleDeleteResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// RoleGrantPermission is not supported; qrdb uses mTLS for authentication.
func (s *authServer) RoleGrantPermission(_ context.Context, _ *pb.AuthRoleGrantPermissionRequest) (*pb.AuthRoleGrantPermissionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}

// RoleRevokePermission is not supported; qrdb uses mTLS for authentication.
func (s *authServer) RoleRevokePermission(_ context.Context, _ *pb.AuthRoleRevokePermissionRequest) (*pb.AuthRoleRevokePermissionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "etcd auth is not enabled; qrdb uses mTLS")
}
