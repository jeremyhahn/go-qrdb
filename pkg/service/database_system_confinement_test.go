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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// systemRoleCtx is a context authenticated as a system-tenant identity
// holding role, as every transport binds it.
func systemRoleCtx(role string) context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject: "sys-" + role, TenantID: tenantid.System, Roles: []string{role},
	})
	return tenant.WithTenantID(ctx, tenantid.System)
}

// databaseVerbs are every per-database verb of the database service, each
// addressing the named database.
func databaseVerbs(svc *DatabaseService) map[string]func(ctx context.Context, name string) error {
	return map[string]func(ctx context.Context, name string) error{
		"get": func(ctx context.Context, name string) error {
			_, err := svc.GetForTenant(ctx, name)
			return err
		},
		"update protocol": func(ctx context.Context, name string) error {
			_, err := svc.UpdateProtocolForTenant(ctx, name, database.ProtocolConfig{})
			return err
		},
		"update etcd": func(ctx context.Context, name string) error {
			_, err := svc.UpdateEtcdForTenant(ctx, name, &database.EtcdConfig{})
			return err
		},
		"update raft": func(ctx context.Context, name string) error {
			_, err := svc.UpdateRaftForTenant(ctx, name, &database.DatabaseRaftConfig{})
			return err
		},
		"update barrier": func(ctx context.Context, name string) error {
			_, err := svc.UpdateBarrierForTenant(ctx, name, &database.DatabaseBarrierConfig{})
			return err
		},
		"update connection": func(ctx context.Context, name string) error {
			_, err := svc.UpdateConnectionForTenant(ctx, name, &database.DatabaseConnectionConfig{})
			return err
		},
		"update sql": func(ctx context.Context, name string) error {
			_, err := svc.UpdateSQLForTenant(ctx, name, &database.SQLConfig{})
			return err
		},
		"seal": func(ctx context.Context, name string) error {
			return svc.SealDatabase(ctx, name)
		},
		"unseal": func(ctx context.Context, name string) error {
			return svc.UnsealDatabase(ctx, name, seal.Credentials{})
		},
		"barrier status": func(ctx context.Context, name string) error {
			_, err := svc.DatabaseBarrierStatus(ctx, name)
			return err
		},
		"holder list": func(ctx context.Context, name string) error {
			_, err := svc.DatabaseBarrierOfficerList(ctx, name)
			return err
		},
		"quorum begin": func(ctx context.Context, name string) error {
			_, err := svc.DatabaseBarrierQuorumBegin(ctx, name, &QuorumBeginRequest{Op: seal.OpUnseal, Username: "alice"})
			return err
		},
		"drop": func(ctx context.Context, name string) error {
			return svc.DropForTenant(ctx, name)
		},
	}
}

// TestDatabaseService_SystemTenantIsConfinedToItsOwnDatabases: there is no
// cross-tenant database access. A system-tenant Admin, Security Officer or
// Reader acts only on the databases the system tenant owns, through the same
// ownership check as every other tenant: another tenant's database answers
// *database.NotFoundError, exactly as a missing one does, on every verb, and
// it is left as it was.
func TestDatabaseService_SystemTenantIsConfinedToItsOwnDatabases(t *testing.T) {
	for _, role := range []string{"admin", "so", "reader"} {
		t.Run(role, func(t *testing.T) {
			svc, mgr := newHolderDatabaseService(t)
			caller := systemRoleCtx(role)
			for verb, call := range databaseVerbs(svc) {
				var notFound *database.NotFoundError
				if err := call(caller, "vault"); !errors.As(err, &notFound) {
					t.Errorf("%s on acme's database = %v, want *database.NotFoundError", verb, err)
				}
				if err := call(caller, "no-such-db"); !errors.As(err, &notFound) {
					t.Errorf("%s on a missing database = %v, want *database.NotFoundError", verb, err)
				}
			}
			if _, err := mgr.Get("vault"); err != nil {
				t.Fatalf("acme's database was changed by a system-tenant caller: %v", err)
			}
			list, err := svc.ListForTenant(caller)
			if err != nil {
				t.Fatal(err)
			}
			for _, info := range list {
				if !tenantid.IsSystem(info.TenantID) {
					t.Errorf("the system tenant's list includes %s's database %q", info.TenantID, info.Name)
				}
			}
		})
	}
}

// TestDatabaseService_SystemTenantReachesItsOwnDatabases is the positive
// control: the system tenant's own databases are reachable to it as any
// tenant's own are.
func TestDatabaseService_SystemTenantReachesItsOwnDatabases(t *testing.T) {
	svc, mgr := newHolderDatabaseService(t)
	if _, err := mgr.Create(database.DatabaseConfig{Name: "platform", Backend: database.BackendMemory}); err != nil {
		t.Fatal(err)
	}
	caller := systemRoleCtx("admin")
	info, err := svc.GetForTenant(caller, "platform")
	if err != nil || info.Name != "platform" {
		t.Fatalf("GetForTenant(platform) = %+v, %v", info, err)
	}
	list, err := svc.ListForTenant(caller)
	if err != nil || len(list) != 1 || list[0].Name != "platform" {
		t.Fatalf("ListForTenant = %+v, %v; want only the system tenant's database", list, err)
	}
	if err := svc.DropForTenant(caller, "platform"); err != nil {
		t.Fatalf("DropForTenant(platform): %v", err)
	}
}

// TestDatabaseService_SystemCreateIsSystemOwned: a system-tenant create
// cannot plant a database in another tenant by naming it in the request; the
// database is the system tenant's, as a tenant's create is that tenant's.
func TestDatabaseService_SystemCreateIsSystemOwned(t *testing.T) {
	svc, mgr := newHolderDatabaseService(t)
	if _, err := svc.CreateForTenant(systemRoleCtx("admin"), database.DatabaseConfig{
		TenantID: "acme", Name: "planted", Backend: database.BackendMemory,
	}); err != nil {
		t.Fatal(err)
	}
	db, err := mgr.Get("planted")
	if err != nil {
		t.Fatal(err)
	}
	if !tenantid.IsSystem(db.TenantID()) {
		t.Fatalf("a system-tenant create made a database owned by %q", db.TenantID())
	}
}
