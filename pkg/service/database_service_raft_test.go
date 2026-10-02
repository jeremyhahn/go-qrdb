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
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// fakeTenantInfoLookup returns a fixed TenantInfo for a single tenant.
type fakeTenantInfoLookup struct {
	info *tenant.TenantInfo
}

func (f *fakeTenantInfoLookup) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	if f.info != nil && f.info.ID == id {
		return f.info, nil
	}
	return nil, tenant.ErrTenantNotFound
}

// recordingQuotaUsage records raft-group increments/decrements.
type recordingQuotaUsage struct {
	inc int64
	dec int64
	err error
}

func (r *recordingQuotaUsage) IncrementQuotaUsage(_ context.Context, _ string, res quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	if r.err != nil {
		return nil, r.err
	}
	if res == quota.ResourceRaftGroups {
		r.inc += delta
	}
	return nil, nil
}

func (r *recordingQuotaUsage) DecrementQuotaUsage(_ context.Context, _ string, res quota.Resource, delta int64) (*tenant.TenantInfo, error) {
	if res == quota.ResourceRaftGroups {
		r.dec += delta
	}
	return nil, nil
}

// newRaftTenantDatabaseService builds a DatabaseService whose Manager has a
// shard-ID claimer (so raft databases get a cluster-issued shard ID) and is wired with
// the given tenant-info lookup and raft-group usage recorder.
func newRaftTenantDatabaseService(t *testing.T, info *tenant.TenantInfo, rec QuotaUsageRecorder) (DatabaseServicer, *database.Manager) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir(), database.WithShardIDClaimer(testShardClaimer(t)))
	t.Cleanup(func() { _ = mgr.Close() })
	opts := []DatabaseServiceOption{
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: info}),
	}
	if rec != nil {
		opts = append(opts, WithQuotaUsageRecorder(rec))
	}
	return NewDatabaseService(mgr, opts...), mgr
}

// TestCreateForTenant_Raft_RoutesDataDirAndForcesBackend verifies an
// IsolationRaft tenant's database is forced to the raft backend, gets a Raft
// config (so the claimer issues a shard ID), and has its DataDir routed under
// the tenant's DataRoot. It also verifies the raft-group counter is incremented.
func TestCreateForTenant_Raft_RoutesDataDirAndForcesBackend(t *testing.T) {
	dataRoot := t.TempDir()
	info := &tenant.TenantInfo{ID: "racme", IsolationType: tenant.IsolationRaft, DataRoot: dataRoot}
	rec := &recordingQuotaUsage{}
	svc, _ := newRaftTenantDatabaseService(t, info, rec)

	ctx := tenant.WithTenantID(context.Background(), "racme")
	// The caller did not specify the raft backend; the IsolationRaft routing
	// must force it (testDBConfig defaults to the memory backend).
	got, err := svc.CreateForTenant(ctx, testDBConfig("app"))
	if err != nil {
		t.Fatalf("CreateForTenant(raft tenant): %v", err)
	}
	if got.Backend != database.BackendRaft {
		t.Errorf("Backend = %q, want %q", got.Backend, database.BackendRaft)
	}
	if got.ShardID < shard.UserMin {
		t.Errorf("ShardID = %d, want >= UserMin (%d)", got.ShardID, shard.UserMin)
	}
	wantDir := filepath.Join(dataRoot, "databases", "app")
	if got.DataDir != wantDir {
		t.Errorf("DataDir = %q, want %q", got.DataDir, wantDir)
	}
	if rec.inc != 1 {
		t.Errorf("raft-group increment = %d, want 1", rec.inc)
	}
}

// TestCreateForTenant_Raft_QuotaReservationRollback verifies that when the
// underlying create fails, a reserved raft-group slot is rolled back (decremented).
func TestCreateForTenant_Raft_QuotaReservationRollback(t *testing.T) {
	dataRoot := t.TempDir()
	info := &tenant.TenantInfo{ID: "racme", IsolationType: tenant.IsolationRaft, DataRoot: dataRoot}
	rec := &recordingQuotaUsage{}
	svc, mgr := newRaftTenantDatabaseService(t, info, rec)

	ctx := tenant.WithTenantID(context.Background(), "racme")
	// Pre-create a database with the same name so the second create fails.
	if _, err := svc.CreateForTenant(ctx, testDBConfig("dup")); err != nil {
		t.Fatalf("first CreateForTenant: %v", err)
	}
	rec.inc = 0 // reset to isolate the rollback assertion
	_ = mgr

	if _, err := svc.CreateForTenant(ctx, testDBConfig("dup")); err == nil {
		t.Fatal("duplicate CreateForTenant must fail")
	}
	if rec.inc != 1 || rec.dec != 1 {
		t.Errorf("reservation rollback: inc=%d dec=%d, want inc=1 dec=1", rec.inc, rec.dec)
	}
}

// TestDropForTenant_Raft_ReleasesQuota verifies dropping a dedicated-raft
// database releases (decrements) the tenant's raft-group counter.
func TestDropForTenant_Raft_ReleasesQuota(t *testing.T) {
	dataRoot := t.TempDir()
	info := &tenant.TenantInfo{ID: "racme", IsolationType: tenant.IsolationRaft, DataRoot: dataRoot}
	rec := &recordingQuotaUsage{}
	svc, _ := newRaftTenantDatabaseService(t, info, rec)

	ctx := tenant.WithTenantID(context.Background(), "racme")
	if _, err := svc.CreateForTenant(ctx, testDBConfig("gone")); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if err := svc.DropForTenant(ctx, "gone"); err != nil {
		t.Fatalf("DropForTenant: %v", err)
	}
	if rec.dec != 1 {
		t.Errorf("raft-group decrement after drop = %d, want 1", rec.dec)
	}
}

// TestCreateForTenant_Raft_EntitlementRejected verifies the IsolationRaft routing
// enforces the dedicated-raft entitlement before creating.
func TestCreateForTenant_Raft_EntitlementRejected(t *testing.T) {
	dataRoot := t.TempDir()
	info := &tenant.TenantInfo{ID: "racme", IsolationType: tenant.IsolationRaft, DataRoot: dataRoot}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := database.NewManager(logger, t.TempDir(), database.WithShardIDClaimer(testShardClaimer(t)))
	t.Cleanup(func() { _ = mgr.Close() })

	wantErr := errors.New("no dedicated raft")
	ent := &mockEntitlementChecker{
		featureFn: func(_ context.Context, _, feature string) error {
			if feature == quota.FeatureDedicatedRaft {
				return wantErr
			}
			return nil
		},
	}
	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(ent),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: info}),
	)

	ctx := tenant.WithTenantID(context.Background(), "racme")
	_, err := svc.CreateForTenant(ctx, testDBConfig("blocked"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("CreateForTenant error = %v, want %v", err, wantErr)
	}
}
