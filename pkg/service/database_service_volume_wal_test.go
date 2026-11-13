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
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// newVolumeTenantService is a DatabaseService for one volume-isolated
// tenant, "acme", whose data root is dataRoot.
func newVolumeTenantService(t *testing.T, dataRoot string) (DatabaseServicer, *database.Manager, context.Context) {
	t.Helper()
	mgr := databasetest.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	lookup := &fakeTenantInfoLookup{info: &tenant.TenantInfo{
		ID:            "acme",
		IsolationType: tenant.IsolationVolume,
		VolumePool:    "default",
	}}
	svc := NewDatabaseService(mgr, WithTenantInfoLookup(lookup),
		WithTenantDataRootResolver(&fakeDataRootResolver{roots: map[string]string{"acme": dataRoot}}))
	return svc, mgr, tenant.WithTenantID(context.Background(), "acme")
}

// TestDatabaseService_VolumeTenant_RefusesDedicatedWAL is the Codex finding
// on #1517: a volume tenant's databases are served through the tenant's
// shared shard, so a raft database with a dedicated WAL there would persist
// and meter a group that never carries its data. The create is refused with
// a typed error before anything is made.
func TestDatabaseService_VolumeTenant_RefusesDedicatedWAL(t *testing.T) {
	dataRoot := t.TempDir()
	svc, _, ctx := newVolumeTenantService(t, dataRoot)

	cfg := testDBConfig("orders")
	cfg.Backend = database.BackendRaft
	cfg.Raft = &database.DatabaseRaftConfig{ClusterMode: database.RaftLocal}
	cfg.DedicatedWAL = true
	_, err := svc.CreateForTenant(ctx, cfg)
	var offPath *database.DedicatedWALOffDataPathError
	if !errors.As(err, &offPath) {
		t.Fatalf("CreateForTenant(raft, dedicated WAL) error = %v (%T), want *database.DedicatedWALOffDataPathError", err, err)
	}
	if offPath.ServedBy != database.ServedByVolumeTenantStore || offPath.Tenant != "acme" {
		t.Fatalf("refusal = %+v, want the volume tenant's store named", offPath)
	}
	if _, statErr := os.Stat(filepath.Join(dataRoot, "databases", "orders")); !os.IsNotExist(statErr) {
		t.Fatalf("the refused create touched its data dir: %v", statErr)
	}
}

// A volume tenant's database without a dedicated WAL still creates and is
// served from the tenant's store.
func TestDatabaseService_VolumeTenant_CreatesWithoutDedicatedWAL(t *testing.T) {
	dataRoot := t.TempDir()
	svc, mgr, ctx := newVolumeTenantService(t, dataRoot)

	if _, err := svc.CreateForTenant(ctx, testDBConfig("orders")); err != nil {
		t.Fatalf("CreateForTenant(no dedicated WAL) = %v, want nil", err)
	}
	if _, err := mgr.GetForTenant(ctx, "orders"); err != nil {
		t.Fatalf("GetForTenant(orders) = %v, want the created database served", err)
	}
}
