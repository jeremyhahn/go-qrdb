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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// newTestDatabaseServiceWithNoDrainWait is newTestDatabaseServiceWithBarrier
// with a system barrier whose seal does not wait for its stores to drain, so
// a database barrier inheriting it seals at once and reports any store still
// in flight as a *quiesce.DrainTimeoutError.
func newTestDatabaseServiceWithNoDrainWait(t *testing.T) *DatabaseService {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := claimingManager(t, logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	sys, err := seal.NewStorageBarrier(testLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "root-key", DeploymentID: 1, SealDrainTimeout: -1}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("system barrier: %v", err)
	}
	if err := sys.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("init system: %v", err)
	}
	reg, err := seal.NewBarrierRegistry(testLogger(), sys)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	bm, err := database.NewDatabaseBarrierManager(logger, reg.SystemBarrier())
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetBarrierManager(bm)
	svc, ok := NewDatabaseService(mgr, WithBarrierManager(bm)).(*DatabaseService)
	if !ok {
		t.Fatal("NewDatabaseService() did not return *DatabaseService")
	}
	return svc
}

// TestSealDatabase_DrainTimeoutStillMarksSealedAndEndsStreams: a database
// seal whose drain runs out has still sealed the barrier and wiped its key,
// so it marks the database sealed and ends its streams and its tenant's SQL
// sessions, and returns the *quiesce.DrainTimeoutError naming the store left
// in flight.
func TestSealDatabase_DrainTimeoutStillMarksSealedAndEndsStreams(t *testing.T) {
	svc := newTestDatabaseServiceWithNoDrainWait(t)
	deferred := clusterseal.NewDeferred() // inactive: seals are node-local
	svc.sealer = deferred
	ctx := tenant.WithTenantID(context.Background(), tenantid.System)
	cfg := testDBConfig("secure")
	cfg.DataDir = t.TempDir()
	cfg.Barrier = &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase}
	if _, err := svc.CreateForTenantWithCredentials(ctx, cfg, seal.MustCredentials(context.Background(), []byte("s3cret-passphrase"))); err != nil {
		t.Fatal(err)
	}
	streams := deferred.Streams()
	watch, err := streams.SubscribeServing(tenantid.System, "secure", nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := streams.SubscribeTenantServing(tenantid.System, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Hold one operation in flight on the database barrier's quiesce gate,
	// so its drain runs out.
	gated, ok := svc.barrierMgr.GetBarrier("secure", tenantid.System).(interface{ Quiesce() *quiesce.Gate })
	if !ok {
		t.Fatal("the database barrier exposes no quiesce gate")
	}
	held := gated.Quiesce().Register("held-store")
	if err := held.Acquire(); err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	err = svc.SealDatabase(ctx, "secure")
	var timeout *quiesce.DrainTimeoutError
	if !errors.As(err, &timeout) || len(timeout.Stores) != 1 || timeout.Stores[0] != "held-store" {
		t.Fatalf("SealDatabase with a store in flight = %v; want a DrainTimeoutError naming held-store", err)
	}
	info, err := svc.Get("secure")
	if err != nil {
		t.Fatalf("Get() after seal error = %v", err)
	}
	if info.Status != database.StatusSealed {
		t.Fatalf("Status after a drain-timed-out seal = %q, want %q", info.Status, database.StatusSealed)
	}
	for name, sub := range map[string]*clusterseal.Subscription{"watch": watch, "SQL session": session} {
		select {
		case <-sub.Done():
		default:
			t.Fatalf("the database's %s outlived a drain-timed-out seal", name)
		}
		var sealed *clusterseal.SealedError
		if !errors.As(sub.Cause(), &sealed) {
			t.Fatalf("%s ended with %v; want a *clusterseal.SealedError", name, sub.Cause())
		}
	}
}
