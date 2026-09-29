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

package service

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// newBarrierDBOwnedByTenant builds a DatabaseService with a real barrier
// manager and a barrier-enabled database owned by the given tenant. The
// database is the one shape a per-database barrier protects: a non-raft store
// of its own (an explicit DataDir) under a prefix-isolated tenant, which the
// request resolver serves from that barrier-wrapped store. The tenant-scoped
// create path never accepts a caller DataDir, so the fixture creates the
// record through the Manager with its owner set, as an operator provisioning
// it would. It returns the service and the barrier secret so the
// allowed-path tests can drive Seal/Unseal end to end.
func newBarrierDBOwnedByTenant(t *testing.T, tenantID, dbName string) (*DatabaseService, []byte) {
	t.Helper()
	svc, mgr := newTestDatabaseServiceWithBarrier(t)

	secret := []byte("s3cret-passphrase")
	cfg := testDBConfig(dbName)
	cfg.TenantID = tenantID
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	if _, err := mgr.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials(bytes.Clone(secret))); err != nil {
		t.Fatalf("setup CreateWithCredentials(%q owned by %q) error: %v", dbName, tenantID, err)
	}
	return svc, secret
}

// TestDatabaseService_BarrierOps_CrossTenantDenied proves the server-side
// ownership check on the per-database barrier control surface: a caller
// authenticated as tenant A cannot seal, unseal, or read the barrier status of
// a database owned by tenant B, even addressing it by its global name (the
// SDK-side verifyOwnership check is bypassable over raw gRPC/REST, so this must
// hold at the service layer). Every attempt fails closed with a typed
// *database.NotFoundError -- the same answer a missing database gets -- and never mutates the target's barrier.
func TestDatabaseService_BarrierOps_CrossTenantDenied(t *testing.T) {
	const owner, attacker, dbName = "tenant-b", "tenant-a", "b-db"
	svc, _ := newBarrierDBOwnedByTenant(t, owner, dbName)
	attackerCtx := tenant.WithTenantID(context.Background(), attacker)

	var tade *database.NotFoundError

	t.Run("BarrierStatus", func(t *testing.T) {
		_, err := svc.DatabaseBarrierStatus(attackerCtx, dbName)
		if !errors.As(err, &tade) {
			t.Fatalf("DatabaseBarrierStatus cross-tenant = %v, want *database.NotFoundError", err)
		}
	})

	t.Run("Unseal", func(t *testing.T) {
		err := svc.UnsealDatabase(attackerCtx, dbName, seal.MustCredentials([]byte("s3cret-passphrase")))
		if !errors.As(err, &tade) {
			t.Fatalf("UnsealDatabase cross-tenant = %v, want *database.NotFoundError", err)
		}
	})

	t.Run("Seal_DoesNotMutateTarget", func(t *testing.T) {
		err := svc.SealDatabase(attackerCtx, dbName)
		if !errors.As(err, &tade) {
			t.Fatalf("SealDatabase cross-tenant = %v, want *database.NotFoundError", err)
		}
		// The attacker's seal must NOT have reached the barrier: the owner's
		// database is still open.
		info, gerr := svc.Get(dbName)
		if gerr != nil {
			t.Fatalf("Get(%q) error: %v", dbName, gerr)
		}
		if info.Status == database.StatusSealed {
			t.Fatal("cross-tenant SealDatabase mutated the target barrier: database is sealed")
		}
	})
}

// TestDatabaseService_BarrierOps_OwnerAllowed proves the owning tenant retains
// full control of its own database's barrier through the same guarded methods.
func TestDatabaseService_BarrierOps_OwnerAllowed(t *testing.T) {
	const owner, dbName = "tenant-b", "b-db"
	svc, secret := newBarrierDBOwnedByTenant(t, owner, dbName)
	ownerCtx := tenant.WithTenantID(context.Background(), owner)

	if _, err := svc.DatabaseBarrierStatus(ownerCtx, dbName); err != nil {
		t.Fatalf("owner DatabaseBarrierStatus error: %v", err)
	}
	if err := svc.SealDatabase(ownerCtx, dbName); err != nil {
		t.Fatalf("owner SealDatabase error: %v", err)
	}
	if err := svc.UnsealDatabase(ownerCtx, dbName, seal.MustCredentials(bytes.Clone(secret))); err != nil {
		t.Fatalf("owner UnsealDatabase error: %v", err)
	}
}

// TestDatabaseService_BarrierOps_SystemAllowed proves the system tenant — the
// legitimate store-global admin — may target any tenant's database barrier.
func TestDatabaseService_BarrierOps_SystemAllowed(t *testing.T) {
	const owner, dbName = "tenant-b", "b-db"
	svc, secret := newBarrierDBOwnedByTenant(t, owner, dbName)
	sysCtx := tenant.WithTenantID(context.Background(), tenant.SystemTenantID)

	if _, err := svc.DatabaseBarrierStatus(sysCtx, dbName); err != nil {
		t.Fatalf("system DatabaseBarrierStatus error: %v", err)
	}
	if err := svc.SealDatabase(sysCtx, dbName); err != nil {
		t.Fatalf("system SealDatabase error: %v", err)
	}
	if err := svc.UnsealDatabase(sysCtx, dbName, seal.MustCredentials(bytes.Clone(secret))); err != nil {
		t.Fatalf("system UnsealDatabase error: %v", err)
	}
}

// TestDatabaseService_BarrierOps_UnownedDatabaseSystemOnly proves an
// unowned/legacy database (empty TenantID, created on the system path) is
// reachable only by the system tenant: a non-system caller fails closed, while
// the system tenant is admitted.
func TestDatabaseService_BarrierOps_UnownedDatabaseSystemOnly(t *testing.T) {
	svc, mgr := newTestDatabaseServiceWithBarrier(t)
	_ = mgr

	const dbName = "legacy-db"
	cfg := testDBConfig(dbName)
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	if _, err := svc.CreateWithCredentials(context.Background(), cfg, seal.MustCredentials([]byte("s3cret"))); err != nil {
		t.Fatalf("setup CreateWithCredentials error: %v", err)
	}

	// A non-system caller is denied even though the database has no owning tenant.
	var tade *database.NotFoundError
	nonSystem := tenant.WithTenantID(context.Background(), "some-tenant")
	if _, err := svc.DatabaseBarrierStatus(nonSystem, dbName); !errors.As(err, &tade) {
		t.Fatalf("non-system DatabaseBarrierStatus on unowned db = %v, want *database.NotFoundError", err)
	}

	// The system tenant may read it.
	sysCtx := tenant.WithTenantID(context.Background(), tenant.SystemTenantID)
	if _, err := svc.DatabaseBarrierStatus(sysCtx, dbName); err != nil {
		t.Fatalf("system DatabaseBarrierStatus on unowned db error: %v", err)
	}
}
