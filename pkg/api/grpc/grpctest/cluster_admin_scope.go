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

package grpctest

import (
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ClusterStartFunc starts the transport under test with mw as its auth
// middleware and admin as its admin service, and returns a client connection
// to it. It may set admin's embedded AdminServicer to its package's own admin
// double for the methods the transport calls besides the four under test. It
// registers its own cleanup.
type ClusterStartFunc func(t *testing.T, mw *authmw.AuthMiddleware, admin *authtest.Admin) *grpc.ClientConn

// ClusterAdminScope is the cluster-admin scope conformance suite for the
// transports that serve the QRDBService protobuf API. Adding and removing a
// replica, requesting a snapshot and draining the node act on the cluster, a
// platform asset addressed by shard id; the built-in Admin and SO roles grant
// them in every tenant, and only the system tenant's holders may use them.
func ClusterAdminScope(t *testing.T, start ClusterStartFunc) {
	ops := []struct {
		name string
		call func(c pb.QRDBServiceClient, t *testing.T) error
	}{
		{"add member", func(c pb.QRDBServiceClient, t *testing.T) error {
			_, err := c.AddMember(callCtx(t), &pb.AddMemberRequest{ShardId: 1, ReplicaId: 9, RaftAddress: "127.0.0.1:1"})
			return err
		}},
		{"remove member", func(c pb.QRDBServiceClient, t *testing.T) error {
			_, err := c.RemoveMember(callCtx(t), &pb.RemoveMemberRequest{ShardId: 1, ReplicaId: 2})
			return err
		}},
		{"request snapshot", func(c pb.QRDBServiceClient, t *testing.T) error {
			_, err := c.RequestSnapshot(callCtx(t), &pb.RequestSnapshotMessage{ShardId: 1})
			return err
		}},
		{"drain", func(c pb.QRDBServiceClient, t *testing.T) error {
			_, err := c.Drain(callCtx(t), &emptypb.Empty{})
			return err
		}},
	}
	roles := authtest.BuiltInRoles(t, tenant.SystemTenantID, authtest.ClusterAdminTenant)
	for _, op := range ops {
		for _, c := range authtest.ClusterAdminCallers(t) {
			t.Run(op.name+"/"+c.Name, func(t *testing.T) {
				caller := authtest.NewFixedAuthenticator(c.Identity)
				admin := &authtest.Admin{}
				conn := start(t, authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller)), admin)
				err := op.call(pb.NewQRDBServiceClient(conn), t)
				if c.Admitted {
					if err != nil || admin.ClusterChanges() != 1 {
						t.Fatalf("err=%v, calls=%d; want served once", err, admin.ClusterChanges())
					}
					return
				}
				wantCode(t, op.name, err, codes.PermissionDenied)
				if admin.ClusterChanges() != 0 {
					t.Fatalf("a refused %s reached the admin service", op.name)
				}
			})
		}
	}
}
