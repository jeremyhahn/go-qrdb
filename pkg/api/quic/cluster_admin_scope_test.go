// Copyright 2026 Jeremy Hahn
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

// No build constraint: generateCredentialE2ETLS and credentialE2ERoundTrip
// (handlers_credentials_e2e_test.go) and mockAdminService (mocks_test.go) are
// unconstrained.

package quic

import (
	"crypto/tls"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestClusterAdminScope: a tenant's Admin or SO may not add or remove a
// replica, request a snapshot or drain the node over QUIC. The cluster is a
// platform asset addressed by shard id, and the built-in Admin and SO roles
// grant these in every tenant.
func TestClusterAdminScope(t *testing.T) {
	marshal := func(m proto.Message) []byte {
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return b
	}
	ops := []struct {
		method  string
		payload []byte
	}{
		{"admin.AddMember", marshal(&pb.AddMemberRequest{ShardId: 1, ReplicaId: 9, RaftAddress: "127.0.0.1:1"})},
		{"admin.RemoveMember", marshal(&pb.RemoveMemberRequest{ShardId: 1, ReplicaId: 2})},
		{"admin.PromoteMember", marshal(&pb.PromoteMemberRequest{ShardId: 1, ReplicaId: 2})},
		{"admin.RequestSnapshot", marshal(&pb.RequestSnapshotMessage{ShardId: 1})},
		{"admin.Drain", nil},
		// The node keyspace's compaction policy: the platform's, and its
		// compaction discards every tenant's raw history on the node.
		{"admin.GetNodeCompaction", nil},
		{"admin.SetNodeCompaction", marshal(&pb.SetNodeCompactionRequest{
			Compaction: &pb.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 10},
		})},
	}
	roles := authtest.BuiltInRoles(t, tenantid.System, authtest.ClusterAdminTenant)
	for _, op := range ops {
		for _, c := range authtest.ClusterAdminCallers(t) {
			t.Run(op.method+"/"+c.Name, func(t *testing.T) {
				caller := authtest.NewFixedAuthenticator(c.Identity)
				admin := &authtest.Admin{AdminServicer: &mockAdminService{}}
				serverCfg, clientCfg := generateCredentialE2ETLS(t)
				serverCfg.ClientAuth = tls.RequireAnyClientCert
				clientCfg.Certificates = serverCfg.Certificates
				srv := NewServer(nil, admin, "127.0.0.1:0", WithTLS(serverCfg),
					WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))))
				if err := srv.Start(); err != nil {
					t.Fatalf("start server: %v", err)
				}
				t.Cleanup(srv.Stop)

				status, _ := credentialE2ERoundTrip(t, srv.Addr(), clientCfg, op.method, op.payload)
				want, calls := uint16(wire.StatusPermissionDenied), int64(0)
				if c.Admitted {
					want, calls = wire.StatusOK, 1
				}
				if status != want {
					t.Fatalf("status = %s, want %s", wire.StatusName(status), wire.StatusName(want))
				}
				if got := admin.ClusterChanges(); got != calls {
					t.Fatalf("admin calls = %d, want %d", got, calls)
				}
			})
		}
	}
}
