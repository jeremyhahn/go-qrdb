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

package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// userLookupStub answers both user lookups with one fixed user or error.
type userLookupStub struct {
	*mockUserService
	u   *user.User
	err error
}

func (m *userLookupStub) GetUserByUsername(_ context.Context, _, _ string) (*user.User, error) {
	return m.u, m.err
}

func (m *userLookupStub) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return m.u, m.err
}

// TestGetUserByUsername_NilServiceIsUnimplemented asserts the lookup on a
// server with no user service answers Unimplemented.
func TestGetUserByUsername_NilServiceIsUnimplemented(t *testing.T) {
	s := &qrdbServiceServer{}

	_, err := s.GetUserByUsername(context.Background(), &pb.GetUserByUsernameRequest{TenantId: "t1", Username: "alice"})
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("code = %s, want Unimplemented", st.Code())
	}
}

// TestGetUserByUsername_NotFound asserts a missing user maps to NotFound.
func TestGetUserByUsername_NotFound(t *testing.T) {
	s := &qrdbServiceServer{userSvc: &userLookupStub{
		mockUserService: &mockUserService{},
		err:             &user.UserNotFoundError{Lookup: "tenant/username"},
	}}

	_, err := s.GetUserByUsername(context.Background(), &pb.GetUserByUsernameRequest{TenantId: "t1", Username: "nobody"})
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Fatalf("code = %s, want NotFound", st.Code())
	}
}

// TestGetUserByUsername_ReturnsUser asserts the found user is mapped into the
// response.
func TestGetUserByUsername_ReturnsUser(t *testing.T) {
	s := &qrdbServiceServer{userSvc: &userLookupStub{
		mockUserService: &mockUserService{},
		u:               &user.User{ID: 7, TenantID: "t1", Username: "alice"},
	}}

	resp, err := s.GetUserByUsername(context.Background(), &pb.GetUserByUsernameRequest{TenantId: "t1", Username: "alice"})
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if resp.GetId() != 7 || resp.GetTenantId() != "t1" || resp.GetUsername() != "alice" {
		t.Fatalf("resp = %+v, want id 7, tenant t1, username alice", resp)
	}
}

// TestGetUserByCertThumbprint_ReturnsUser asserts the bound user is mapped
// into the response.
func TestGetUserByCertThumbprint_ReturnsUser(t *testing.T) {
	s := &qrdbServiceServer{userSvc: &userLookupStub{
		mockUserService: &mockUserService{},
		u:               &user.User{ID: 9, TenantID: "t2", Username: "bob"},
	}}

	resp, err := s.GetUserByCertThumbprint(context.Background(), &pb.GetUserByCertThumbprintRequest{Thumbprint: "AABB"})
	if err != nil {
		t.Fatalf("GetUserByCertThumbprint: %v", err)
	}
	if resp.GetId() != 9 || resp.GetTenantId() != "t2" || resp.GetUsername() != "bob" {
		t.Fatalf("resp = %+v, want id 9, tenant t2, username bob", resp)
	}
}

// TestGetUserByCertThumbprint_NilServiceIsUnimplemented asserts the lookup on
// a server with no user service answers Unimplemented.
func TestGetUserByCertThumbprint_NilServiceIsUnimplemented(t *testing.T) {
	s := &qrdbServiceServer{}

	_, err := s.GetUserByCertThumbprint(context.Background(), &pb.GetUserByCertThumbprintRequest{Thumbprint: "AABBCC"})
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("code = %s, want Unimplemented", st.Code())
	}
}

// TestGetUserByCertThumbprint_NotFound asserts an unbound thumbprint maps to
// NotFound.
func TestGetUserByCertThumbprint_NotFound(t *testing.T) {
	s := &qrdbServiceServer{userSvc: &userLookupStub{
		mockUserService: &mockUserService{},
		err:             &user.UserNotFoundError{Lookup: "cert_spki/X"},
	}}

	_, err := s.GetUserByCertThumbprint(context.Background(), &pb.GetUserByCertThumbprintRequest{Thumbprint: "AABB"})
	if st, _ := status.FromError(err); st.Code() != codes.NotFound {
		t.Fatalf("code = %s, want NotFound", st.Code())
	}
}
