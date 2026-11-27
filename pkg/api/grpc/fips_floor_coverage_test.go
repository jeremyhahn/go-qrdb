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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// emailLookupStub answers GetUserByEmail with one fixed user or error.
type emailLookupStub struct {
	*mockUserService
	u   *user.User
	err error
}

func (m *emailLookupStub) GetUserByEmail(_ context.Context, _, _ string) (*user.User, error) {
	return m.u, m.err
}

func TestGetUserByEmail(t *testing.T) {
	tests := []struct {
		name     string
		svc      *qrdbServiceServer
		wantCode codes.Code
	}{
		{
			name:     "no user service is unimplemented",
			svc:      &qrdbServiceServer{},
			wantCode: codes.Unimplemented,
		},
		{
			name: "missing user is not found",
			svc: &qrdbServiceServer{userSvc: &emailLookupStub{
				mockUserService: &mockUserService{},
				err:             &user.UserNotFoundError{Lookup: "tenant/email"},
			}},
			wantCode: codes.NotFound,
		},
		{
			name: "found user is returned",
			svc: &qrdbServiceServer{userSvc: &emailLookupStub{
				mockUserService: &mockUserService{},
				u:               &user.User{ID: 9, TenantID: "t1", Username: "bob"},
			}},
			wantCode: codes.OK,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := tc.svc.GetUserByEmail(context.Background(), &pb.GetUserByEmailRequest{TenantId: "t1", Email: "bob@example.com"})
			if st, _ := status.FromError(err); st.Code() != tc.wantCode {
				t.Fatalf("code = %s, want %s (err %v)", st.Code(), tc.wantCode, err)
			}
			if tc.wantCode == codes.OK && (resp.GetId() != 9 || resp.GetUsername() != "bob") {
				t.Fatalf("resp = %+v, want id 9, username bob", resp)
			}
		})
	}
}

func TestDatabaseScale(t *testing.T) {
	tests := []struct {
		name     string
		svc      *qrdbServiceServer
		wantCode codes.Code
	}{
		{
			name:     "no database service is unimplemented",
			svc:      &qrdbServiceServer{},
			wantCode: codes.Unimplemented,
		},
		{
			name:     "service error is mapped",
			svc:      &qrdbServiceServer{databaseSvc: &mockDatabaseService{updateErr: errors.New("boom")}},
			wantCode: codes.Internal,
		},
		{
			name:     "scaled database is returned",
			svc:      &qrdbServiceServer{databaseSvc: &mockDatabaseService{updateResult: &database.DatabaseInfo{Name: "db1"}}},
			wantCode: codes.OK,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := tc.svc.DatabaseScale(context.Background(), &pb.DatabaseScaleRequest{Name: "db1", TargetReplicas: 3})
			if st, _ := status.FromError(err); st.Code() != tc.wantCode {
				t.Fatalf("code = %s, want %s (err %v)", st.Code(), tc.wantCode, err)
			}
			if tc.wantCode == codes.OK && resp.GetName() != "db1" {
				t.Fatalf("resp = %+v, want name db1", resp)
			}
		})
	}
}

func TestDatabaseBarrierStatusToProto(t *testing.T) {
	if got := databaseBarrierStatusToProto(nil); got.GetSealed() || got.GetStrategy() != "" {
		t.Fatalf("nil status = %+v, want empty", got)
	}
	at := time.Unix(1700000000, 0)
	got := databaseBarrierStatusToProto(&service.DatabaseBarrierStatusInfo{
		BarrierStatus: &seal.BarrierStatus{Sealed: true, Strategy: "shamir", HardwareBacked: true, InitializedAt: at},
	})
	if !got.GetSealed() || got.GetStrategy() != "shamir" || !got.GetHardwareBacked() || got.GetInitializedAtUnix() != at.Unix() {
		t.Fatalf("status = %+v, want sealed shamir hardware-backed at %d", got, at.Unix())
	}
}

func TestResolveMaxMessageBytes(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{in: 0, want: DefaultMaxMessageBytes},
		{in: -1, want: DefaultMaxMessageBytes},
		{in: 1 << 20, want: 1 << 20},
	}
	for _, tc := range tests {
		if got := ResolveMaxMessageBytes(tc.in); got != tc.want {
			t.Fatalf("ResolveMaxMessageBytes(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestEntitlementsToProto_NilIsNil(t *testing.T) {
	if got := entitlementsToProto(nil); got != nil {
		t.Fatalf("entitlementsToProto(nil) = %+v, want nil", got)
	}
}

func TestGroupHandlers_NoGroupServiceIsUnimplemented(t *testing.T) {
	s := &qrdbServiceServer{}
	ctx := context.Background()
	tests := []struct {
		name string
		call func() error
	}{
		{name: "GetGroupByName", call: func() error {
			_, err := s.GetGroupByName(ctx, &pb.GetGroupByNameRequest{})
			return err
		}},
		{name: "DeleteGroup", call: func() error {
			_, err := s.DeleteGroup(ctx, &pb.DeleteGroupRequest{})
			return err
		}},
		{name: "AddGroupMember", call: func() error {
			_, err := s.AddGroupMember(ctx, &pb.GroupMemberRequest{})
			return err
		}},
		{name: "RemoveGroupMember", call: func() error {
			_, err := s.RemoveGroupMember(ctx, &pb.GroupMemberRequest{})
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if st, _ := status.FromError(tc.call()); st.Code() != codes.Unimplemented {
				t.Fatalf("code = %s, want Unimplemented", st.Code())
			}
		})
	}
	if got := groupToProto(nil); got != nil {
		t.Fatalf("groupToProto(nil) = %+v, want nil", got)
	}
}

func TestVectorUpdateNamespace_NoVectorServiceFails(t *testing.T) {
	s := &qrdbServiceServer{}
	if _, err := s.VectorUpdateNamespace(context.Background(), &pb.VectorUpdateNamespaceRequest{Namespace: "ns"}); err == nil {
		t.Fatal("VectorUpdateNamespace with no vector service succeeded, want an error")
	}
}
