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

package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestStartLocalLeaseExpiry_DeletesAnExpiredLeasesKeys: the raft-free
// composition sweeps its own leases. Without the sweeper an expired lease's
// key -- a dead holder's lock -- is never deleted.
func TestStartLocalLeaseExpiry_DeletesAnExpiredLeasesKeys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stg, pdb, err := lifecycle.NewLocalStorage(database.BackendMemory, t.TempDir(), nil, qrdbconfig.StorageConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stg.Close() })
	lr := lease.NewLessor()
	eng, err := lifecycle.NewLocalEngine(database.BackendMemory, stg, pdb, nil, lr)
	if err != nil {
		t.Fatal(err)
	}
	host := backend.NewLocalBackend(eng)
	leaseSvc := service.NewLeaseService(host, 1)
	mvccSvc := service.NewMVCCKVService(host, 1)
	kvSvc := service.NewKVService(host, 1)

	l, err := leaseSvc.Grant(ctx, 1)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := mvccSvc.PutWithLease(ctx, "held", []byte("x"), l.ID); err != nil {
		t.Fatalf("put with lease: %v", err)
	}
	stop := lifecycle.StartLocalLeaseExpiry(ctx, lr, leaseSvc, nil)
	defer stop()

	for {
		if _, err := kvSvc.Get(ctx, "held"); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("the key outlived its expired lease: the sweeper did not revoke it")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// TestStartLocalLeaseExpiry_NilIsANoOp: a composition without leases starts
// nothing, and its stop function returns at once.
func TestStartLocalLeaseExpiry_NilIsANoOp(t *testing.T) {
	lifecycle.StartLocalLeaseExpiry(context.Background(), nil, nil, nil)()
	lifecycle.StartLocalLeaseExpiry(context.Background(), lease.NewLessor(), nil, nil)()
}

// TestStartLocalLeaseExpiry_StopWaitsAndStaysStopped: stop returns only once
// the sweeper has exited, is safe to call twice, and nothing is revoked after
// it -- which is what lets a composer close the backend right after it.
func TestStartLocalLeaseExpiry_StopWaitsAndStaysStopped(t *testing.T) {
	ctx := context.Background()
	stg, pdb, err := lifecycle.NewLocalStorage(database.BackendMemory, t.TempDir(), nil, qrdbconfig.StorageConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stg.Close() })
	// The sweeper ticks on the Lessor's clock; a fake one lets the test move
	// time past the lease's TTL without a wall-clock wait.
	fake := clocktest.New()
	lr := lease.NewLessor(lease.WithClock(fake))
	eng, err := lifecycle.NewLocalEngine(database.BackendMemory, stg, pdb, nil, lr)
	if err != nil {
		t.Fatal(err)
	}
	host := backend.NewLocalBackend(eng)
	leaseSvc := service.NewLeaseService(host, 1)
	mvccSvc := service.NewMVCCKVService(host, 1)
	kvSvc := service.NewKVService(host, 1)

	l, err := leaseSvc.Grant(ctx, 1)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := mvccSvc.PutWithLease(ctx, "held", []byte("x"), l.ID); err != nil {
		t.Fatalf("put with lease: %v", err)
	}

	stop := lifecycle.StartLocalLeaseExpiry(ctx, lr, leaseSvc, nil)
	// Stop only once the sweeper has armed its ticker, so stop ends a running
	// sweeper rather than one that has not started yet.
	armCtx, armCancel := context.WithTimeout(ctx, 10*time.Second)
	defer armCancel()
	if err := fake.BlockUntil(armCtx, 1); err != nil {
		t.Fatalf("the sweeper never armed its ticker: %v", err)
	}
	done := make(chan struct{})
	go func() {
		stop()
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stop did not return")
	}

	// A stopped sweeper has released its ticker, so no tick can reach it.
	if n := fake.Waiters(); n != 0 {
		t.Fatalf("after stop %d clock waiters remain armed, want 0", n)
	}
	// Move the clock past the lease's TTL and several sweep intervals: a
	// sweeper that outlived stop would revoke it on one of those ticks.
	fake.Advance(time.Second + 4*lease.DefaultExpiryInterval)
	if _, err := kvSvc.Get(ctx, "held"); err != nil {
		t.Fatalf("a stopped sweeper revoked the lease: %v", err)
	}
	// Closing the backend now races nothing.
	if err := host.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
