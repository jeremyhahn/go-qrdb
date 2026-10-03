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

package quic

import (
	"crypto/tls"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// mustJSON encodes v or fails the test.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// mustProto encodes m or fails the test.
func mustProto(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestDatabaseConfinement: over a real QUIC connection, with the real RBAC and
// built-in roles, a system-tenant Admin, Security Officer or Auditor holding
// every database permission is answered StatusNotFound on another tenant's
// database, on every database method -- the data verbs, seal, unseal and the
// share-holder ones -- exactly as for a missing database, and the database is
// left as it was. There is no cross-tenant database access.
func TestDatabaseConfinement(t *testing.T) {
	for name, id := range authtest.SystemDatabaseCallers() {
		t.Run(name, func(t *testing.T) {
			fx := servicetest.NewShamirDatabaseOwnedBy(t, "acme", "vault", 2, 2)
			roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
			mw := authmw.New(rbac.New(roles), authmw.WithAuthenticator(authtest.NewFixedAuthenticator(id)))
			serverCfg, clientCfg := generateCredentialE2ETLS(t)
			serverCfg.ClientAuth = tls.RequireAnyClientCert
			clientCfg.Certificates = serverCfg.Certificates
			srv := NewServer(nil, nil, "127.0.0.1:0",
				WithTLS(serverCfg), WithTenantService(authtest.NewTenants(t, tenantid.System, "acme")),
				WithDatabaseService(fx.Service), WithAuthMiddleware(mw))
			if err := srv.Start(); err != nil {
				t.Fatalf("start server: %v", err)
			}
			t.Cleanup(srv.Stop)

			calls := map[string][]byte{
				"db.Get":                mustJSON(t, databaseNameRequest{Name: "vault"}),
				"db.Drop":               mustJSON(t, databaseNameRequest{Name: "vault"}),
				"db.UpdateSQL":          mustJSON(t, map[string]any{"name": "vault"}),
				"db.UpdateProtocol":     mustJSON(t, map[string]any{"name": "vault"}),
				"db.UpdateEtcd":         mustJSON(t, map[string]any{"name": "vault"}),
				"db.UpdateRaft":         mustJSON(t, databaseUpdateRaftRequest{Name: "vault", Raft: &databaseRaftRequest{}}),
				"db.UpdateBarrier":      mustJSON(t, databaseUpdateBarrierRequest{Name: "vault", Barrier: &database.DatabaseBarrierConfig{}}),
				"db.UpdateConnection":   mustJSON(t, databaseUpdateConnectionRequest{Name: "vault", Connection: &database.DatabaseConnectionConfig{}}),
				"db.Seal":               mustProto(t, &pb.DatabaseSealRequest{Name: "vault"}),
				"db.Unseal":             mustProto(t, &pb.DatabaseUnsealRequest{Name: "vault", Secret: "c2VjcmV0"}),
				"db.BarrierStatus":      mustProto(t, &pb.DatabaseBarrierStatusRequest{Name: "vault"}),
				"db.BarrierOfficerList": mustProto(t, &pb.DatabaseBarrierOfficerListRequest{Database: "vault"}),
				"db.BarrierQuorumBegin": mustProto(t, &pb.DatabaseBarrierQuorumBeginRequest{
					Database: "vault", Begin: &pb.BarrierQuorumBeginRequest{Op: "unseal", Username: "alice"},
				}),
				"db.BarrierRekeyStart": mustProto(t, &pb.DatabaseBarrierRekeyStartRequest{Database: "vault", Threshold: 2}),
			}
			for method, payload := range calls {
				if status, body := credentialE2ERoundTrip(t, srv.Addr(), clientCfg, method, payload); status != wire.StatusNotFound {
					t.Errorf("%s on acme's database = status %d (%s), want StatusNotFound, as for a missing database", method, status, body)
				}
			}
			if _, err := fx.Service.Create(database.DatabaseConfig{Name: "sysdb", Engine: database.EngineKV, Backend: database.BackendMemory}); err != nil {
				t.Fatalf("create the system tenant's own database: %v", err)
			}
			// The list names the caller's own databases and only those.
			status, body := credentialE2ERoundTrip(t, srv.Addr(), clientCfg, "db.List", []byte("{}"))
			if status != wire.StatusOK {
				t.Fatalf("db.List = status %d (%s)", status, body)
			}
			if strings.Contains(string(body), `"vault"`) || !strings.Contains(string(body), `"sysdb"`) {
				t.Errorf("db.List = %s, want the caller's sysdb and not acme's vault", body)
			}
			// A tenant named in a create request is discarded: the database is
			// the caller's, never acme's.
			createReq := mustJSON(t, map[string]any{"name": "smuggled", "tenantId": "acme", "engine": "kv", "backend": "memory"})
			if status, body := credentialE2ERoundTrip(t, srv.Addr(), clientCfg, "db.Create", createReq); status == wire.StatusOK {
				if info, err := fx.Service.Get("smuggled"); err != nil || !database.OwnedBy(info.TenantID, tenantid.System) {
					t.Errorf("create naming tenant acme made %+v (%v), want a database the system tenant owns", info, err)
				}
			} else if name == "admin" || status != wire.StatusPermissionDenied {
				t.Errorf("db.Create naming tenant acme = status %d (%s), want it created for the caller (admin) or refused", status, body)
			}
			if _, err := fx.Service.Get(fx.Name); err != nil {
				t.Fatalf("acme's database was changed: %v", err)
			}
		})
	}
}
