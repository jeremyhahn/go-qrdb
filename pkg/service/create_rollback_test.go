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
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// failingFenceReclaimer refuses the store fence of a drop while fail is set:
// the drop has claimed the catalog record and fenced the database, then
// fails before it reclaims anything.
type failingFenceReclaimer struct {
	fail atomic.Bool
}

func (r *failingFenceReclaimer) FenceDatabaseKeyspace(context.Context, string, string, string, string) error {
	if r.fail.Load() {
		return errors.New("injected: store fence refused")
	}
	return nil
}

func (r *failingFenceReclaimer) ReclaimDatabaseKeyspace(context.Context, string, string, string, string) error {
	return nil
}

func (r *failingFenceReclaimer) UnfenceDatabaseKeyspace(context.Context, string, string, string, string) error {
	return nil
}

// TestCreateForTenant_StrictRefusedCreateStaysFencedWhenItsRollbackDropFails
// pins Codex P1 (fx1576i): strict commits while a create races it, the
// create is refused by the tightening handshake, and its rollback drop
// fails. The database must stay unservable -- reads and writes refused, the
// catalog record still marked dropping -- with its reservations held, until
// the retried drop lands.
func TestCreateForTenant_StrictRefusedCreateStaysFencedWhenItsRollbackDropFails(t *testing.T) {
	strict := looseRaftTenant()
	strict.IsolationPolicy = tenant.TenantIsolationPolicyStrict
	l := &tighteningLookup{info: looseRaftTenant(), afterFirst: strict}

	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, dir)
	t.Cleanup(func() { _ = mgr.Close() })
	reclaimer := &failingFenceReclaimer{}
	reclaimer.fail.Store(true)
	mgr.SetKeyspaceReclaimer(reclaimer)
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := NewDatabaseService(mgr,
		WithEntitlementChecker(&mockEntitlementChecker{}),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: l.info}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
	).(*DatabaseService)
	svc.tenantInfo = l
	ctx := tenant.WithTenantID(context.Background(), "rt")

	_, err := svc.CreateForTenant(ctx, testDBConfig("app"))
	var incomplete *database.CreateRollbackIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("CreateForTenant with a failing rollback drop = %v, want *CreateRollbackIncompleteError", err)
	}
	var refused *database.StrictTenantDedicatedWALRequiredError
	if !errors.As(err, &refused) {
		t.Fatalf("CreateForTenant = %v, want it to carry the strict refusal", err)
	}

	var dropping *database.DroppingError
	if _, gerr := mgr.GetForTenant(ctx, "app"); !errors.As(gerr, &dropping) {
		t.Fatalf("read of the fenced database = %v, want *DroppingError", gerr)
	}
	if werr := mgr.UpdateProtocolForTenant(ctx, "app", database.ProtocolConfig{}); werr == nil {
		t.Fatal("write to the fenced database succeeded, want it refused")
	}
	meta, err := databasetest.Catalog(dir).Get(context.Background(), "rt", "app")
	if err != nil {
		t.Fatalf("catalog Get: %v", err)
	}
	if !meta.Dropping || meta.DropClaim == "" {
		t.Fatalf("catalog record = dropping %v claim %q, want it still marked dropping", meta.Dropping, meta.DropClaim)
	}
	if svc.PendingCreateRollbacks() != 1 {
		t.Fatalf("pending create rollbacks = %d, want 1", svc.PendingCreateRollbacks())
	}
	releasedBefore := f.decTotal + f.decRaft + f.decWAL

	// Still failing: the retry keeps the fence and the reservations.
	if err := svc.RetryCreateRollbacks(context.Background()); err == nil {
		t.Fatal("RetryCreateRollbacks with the fence still refused succeeded")
	}
	if _, gerr := mgr.GetForTenant(ctx, "app"); !errors.As(gerr, &dropping) {
		t.Fatalf("read after a failed retry = %v, want *DroppingError", gerr)
	}
	if got := f.decTotal + f.decRaft + f.decWAL; got != releasedBefore {
		t.Fatalf("a failed retry released reservations (%d -> %d)", releasedBefore, got)
	}

	reclaimer.fail.Store(false)
	if err := svc.RetryCreateRollbacks(context.Background()); err != nil {
		t.Fatalf("RetryCreateRollbacks: %v", err)
	}
	if svc.PendingCreateRollbacks() != 0 {
		t.Fatalf("pending create rollbacks after the drop landed = %d, want 0", svc.PendingCreateRollbacks())
	}
	if _, err := databasetest.Catalog(dir).Get(context.Background(), "rt", "app"); err == nil {
		t.Fatal("catalog record survived the landed drop")
	}
	if got := f.decTotal + f.decRaft + f.decWAL; got <= releasedBefore {
		t.Fatalf("reservations not released after the drop landed (%d -> %d)", releasedBefore, got)
	}
}
