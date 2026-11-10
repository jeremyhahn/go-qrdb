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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestDatabaseConfinement: in process, a system-tenant Admin, Security Officer
// or Auditor is answered *database.NotFoundError on another tenant's database,
// on every database verb -- the data verbs, seal, unseal and the share-holder
// ones -- exactly as for a missing database, and the database is left as it
// was. The embedded transport resolves every verb through the database
// service's tenant-scoped methods, as the network transports do; there is no
// cross-tenant database access.
func TestDatabaseConfinement(t *testing.T) {
	calls := map[string]func(ctx context.Context, tr *Transport) error{
		"get": func(ctx context.Context, tr *Transport) error {
			_, err := tr.GetDatabase(ctx, "vault")
			return err
		},
		"drop": func(ctx context.Context, tr *Transport) error {
			return tr.DropDatabase(ctx, "vault")
		},
		"update protocol": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseProtocol(ctx, transport.UpdateDatabaseProtocolRequest{Name: "vault"})
			return err
		},
		"update etcd": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseEtcd(ctx, transport.UpdateDatabaseEtcdRequest{Name: "vault"})
			return err
		},
		"update raft": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseRaft(ctx, transport.UpdateDatabaseRaftRequest{Name: "vault"})
			return err
		},
		"update barrier": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseBarrier(ctx, transport.UpdateDatabaseBarrierRequest{Name: "vault"})
			return err
		},
		"update connection": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseConnection(ctx, transport.UpdateDatabaseConnectionRequest{Name: "vault"})
			return err
		},
		"update sql": func(ctx context.Context, tr *Transport) error {
			_, err := tr.UpdateDatabaseSQL(ctx, transport.UpdateDatabaseSQLRequest{Name: "vault"})
			return err
		},
		"seal": func(ctx context.Context, tr *Transport) error {
			return tr.SealDatabase(ctx, "vault")
		},
		"unseal": func(ctx context.Context, tr *Transport) error {
			return tr.UnsealDatabase(ctx, "vault", "secret")
		},
		"barrier status": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DatabaseBarrierStatus(ctx, "vault")
			return err
		},
		"holder list": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DatabaseBarrierOfficerList(ctx, "vault")
			return err
		},
		"quorum begin": func(ctx context.Context, tr *Transport) error {
			_, err := tr.DatabaseBarrierQuorumBegin(ctx, &transport.DatabaseQuorumBeginRequest{
				Database: "vault", QuorumBeginRequest: transport.QuorumBeginRequest{Op: transport.OpUnseal, Username: "alice"},
			})
			return err
		},
	}
	for name, id := range authtest.SystemDatabaseCallers() {
		t.Run(name, func(t *testing.T) {
			fx := servicetest.NewShamirDatabaseOwnedBy(t, "acme", "vault", 2, 2)
			tr, err := New(newMockKVService(), &mockAdminService{}, WithDatabaseService(fx.Service))
			if err != nil {
				t.Fatal(err)
			}
			ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), id), tenantid.System)
			for verb, call := range calls {
				var notFound *database.NotFoundError
				if err := call(ctx, tr); !errors.As(err, &notFound) {
					t.Errorf("%s on acme's database = %v, want *database.NotFoundError", verb, err)
				}
			}
			if _, err := fx.Service.Get(fx.Name); err != nil {
				t.Fatalf("acme's database was changed: %v", err)
			}
			list, err := tr.ListDatabases(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, info := range list {
				if info.Name == fx.Name {
					t.Errorf("the system tenant's list includes acme's database %q", info.Name)
				}
			}
			// A tenant named in a create request is discarded: the database is
			// the caller's, never acme's.
			if _, err := tr.CreateDatabase(ctx, transport.CreateDatabaseRequest{TenantID: "acme", Name: "smuggled", Backend: "memory"}); err == nil {
				info, err := fx.Service.Get("smuggled")
				if err != nil || !database.OwnedBy(info.TenantID, tenantid.System) {
					t.Errorf("create naming tenant acme made %+v (%v), want a database the system tenant owns", info, err)
				}
				list, err := tr.ListDatabases(ctx)
				if err != nil {
					t.Fatal(err)
				}
				listed := false
				for _, info := range list {
					listed = listed || info.Name == "smuggled"
				}
				if !listed {
					t.Errorf("the system tenant's list omits its own database: %v", list)
				}
			}
		})
	}
}

func TestEmbeddedDatabaseBarrier_NilDatabaseService(t *testing.T) {
	tr := newExtTransport(t)
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"SealDatabase":   func() error { return tr.SealDatabase(ctx, "mydb") },
		"UnsealDatabase": func() error { return tr.UnsealDatabase(ctx, "mydb", "secret") },
		"DatabaseBarrierStatus": func() error {
			_, err := tr.DatabaseBarrierStatus(ctx, "mydb")
			return err
		},
	} {
		var ne *NilServiceError
		if err := call(); !errors.As(err, &ne) {
			t.Errorf("%s: expected NilServiceError, got %T: %v", name, err, err)
		}
	}
}
