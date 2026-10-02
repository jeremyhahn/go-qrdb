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
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// DatabaseStartFunc starts the transport under test with mw as its auth
// middleware, tenants as its tenant service and db as its database service,
// and returns a client connection to it. It registers its own cleanup.
type DatabaseStartFunc func(t *testing.T, mw *authmw.AuthMiddleware, tenants tenant.TenantServicer, db service.DatabaseServicer) *grpc.ClientConn

// DatabaseConfinement pins that there is no cross-tenant database access: a
// system-tenant Admin, Security Officer or Auditor holding every database
// permission is answered NotFound on another tenant's database, on every
// database RPC -- the data verbs, seal, unseal and the share-holder ones --
// exactly as for a missing database, and the database is left as it was.
func DatabaseConfinement(t *testing.T, start DatabaseStartFunc) {
	calls := map[string]func(ctx context.Context, c pb.QRDBServiceClient) error{
		"get": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseGet(ctx, &pb.DatabaseGetRequest{Name: "vault"})
			return err
		},
		"drop": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseDrop(ctx, &pb.DatabaseDropRequest{Name: "vault"})
			return err
		},
		"update sql": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseUpdateSQL(ctx, &pb.DatabaseUpdateSQLRequest{Name: "vault"})
			return err
		},
		"update protocol": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseUpdateProtocol(ctx, &pb.DatabaseUpdateProtocolRequest{Name: "vault"})
			return err
		},
		"update etcd": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseUpdateEtcd(ctx, &pb.DatabaseUpdateEtcdRequest{Name: "vault"})
			return err
		},
		"update raft": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseUpdateRaft(ctx, &pb.DatabaseUpdateRaftRequest{Name: "vault"})
			return err
		},
		"update barrier": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseUpdateBarrier(ctx, &pb.DatabaseUpdateBarrierRequest{Name: "vault"})
			return err
		},
		"update connection": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseUpdateConnection(ctx, &pb.DatabaseUpdateConnectionRequest{Name: "vault"})
			return err
		},
		"seal": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseSeal(ctx, &pb.DatabaseSealRequest{Name: "vault"})
			return err
		},
		"unseal": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseUnseal(ctx, &pb.DatabaseUnsealRequest{Name: "vault", Secret: "c2VjcmV0"}) //nolint:gosec // G101: a placeholder the ownership check refuses before reading, not a credential
			return err
		},
		"barrier status": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseBarrierStatus(ctx, &pb.DatabaseBarrierStatusRequest{Name: "vault"})
			return err
		},
		"holder list": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseBarrierOfficerList(ctx, &pb.DatabaseBarrierOfficerListRequest{Database: "vault"})
			return err
		},
		"quorum begin": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseBarrierQuorumBegin(ctx, &pb.DatabaseBarrierQuorumBeginRequest{
				Database: "vault", Begin: &pb.BarrierQuorumBeginRequest{Op: "unseal", Username: "alice"},
			})
			return err
		},
		"rekey start": func(ctx context.Context, c pb.QRDBServiceClient) error {
			_, err := c.DatabaseBarrierRekeyStart(ctx, &pb.DatabaseBarrierRekeyStartRequest{Database: "vault", Threshold: 2})
			return err
		},
	}
	for name, id := range authtest.SystemDatabaseCallers() {
		t.Run(name, func(t *testing.T) {
			fx := servicetest.NewShamirDatabaseOwnedBy(t, "acme", "vault", 2, 2)
			tenants := authtest.NewTenants(t, tenantid.System, "acme")
			roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
			mw := authmw.New(rbac.New(roles), authmw.WithAuthenticator(authtest.NewFixedAuthenticator(id)))
			client := pb.NewQRDBServiceClient(start(t, mw, tenants, fx.Service))
			for verb, call := range calls {
				if err := call(callCtx(t), client); status.Code(err) != codes.NotFound {
					t.Errorf("%s on acme's database = %v, want NotFound, as for a missing database", verb, err)
				}
			}
			if _, err := fx.Service.Create(database.DatabaseConfig{Name: "sysdb", Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
				t.Fatalf("create the system tenant's own database: %v", err)
			}
			// The list names the caller's own databases and only those.
			list, err := client.DatabaseList(callCtx(t), &emptypb.Empty{})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			listed := map[string]bool{}
			for _, info := range list.GetDatabases() {
				listed[info.GetName()] = true
			}
			if listed["vault"] || !listed["sysdb"] {
				t.Errorf("list = %v, want the caller's sysdb and not acme's vault", listed)
			}
			if _, err := fx.Service.Get(fx.Name); err != nil {
				t.Fatalf("acme's database was changed: %v", err)
			}
		})
	}
}
