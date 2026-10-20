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

package server

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// recordingShellRegistrar is a ShellRegistrar that records each tenant it is
// asked to register a shell for, and answers with err.
type recordingShellRegistrar struct {
	mu      sync.Mutex
	tenants []string
	err     error
}

// Compile-time interface check.
var _ ShellRegistrar = (*recordingShellRegistrar)(nil)

func (r *recordingShellRegistrar) RegisterTenantShell(_ context.Context, tenantID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tenants = append(r.tenants, tenantID)
	return r.err
}

func (r *recordingShellRegistrar) registered() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.tenants...)
}

// TestTenantShardReconciler_RegistersShellForAPlacementWithoutABarrier
// verifies that a placement naming this node, for a tenant with no barrier
// here, registers the tenant's shell and still defers the shard start: the
// shell is sealed.
func TestTenantShardReconciler_RegistersShellForAPlacementWithoutABarrier(t *testing.T) {
	factory := newFakeShardLifecycle()
	barriers := newFakeBarrierReadiness()
	barriers.markMissing("acme")
	r, _ := newTestReconciler(t, factory, barriers, placement1("acme", "orders", 1001))
	shells := &recordingShellRegistrar{}
	r.SetShellRegistrar(shells)

	r.ReconcileOnce(context.Background())

	if got := shells.registered(); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("shells registered = %v, want [acme]", got)
	}
	if factory.attachCount() != 0 {
		t.Fatalf("Attach called %d times for a tenant whose barrier is only a shell, want 0", factory.attachCount())
	}
}

// TestTenantShardReconciler_NoShellForAReadyBarrier verifies that a tenant
// whose barrier is registered and unsealed starts its shard and registers no
// shell.
func TestTenantShardReconciler_NoShellForAReadyBarrier(t *testing.T) {
	factory := newFakeShardLifecycle()
	barriers := newFakeBarrierReadiness()
	barriers.markReady("acme")
	r, _ := newTestReconciler(t, factory, barriers, placement1("acme", "orders", 1001))
	shells := &recordingShellRegistrar{}
	r.SetShellRegistrar(shells)

	r.ReconcileOnce(context.Background())

	if got := shells.registered(); len(got) != 0 {
		t.Fatalf("shells registered = %v for a ready barrier, want none", got)
	}
	if factory.attachCount() != 1 {
		t.Fatalf("Attach called %d times, want 1", factory.attachCount())
	}
}

// TestTenantShardReconciler_ShellFailureRetriesNextPass verifies that a shell
// registration that fails defers the shard and is retried on the next pass.
func TestTenantShardReconciler_ShellFailureRetriesNextPass(t *testing.T) {
	factory := newFakeShardLifecycle()
	barriers := newFakeBarrierReadiness()
	barriers.markMissing("acme")
	r, _ := newTestReconciler(t, factory, barriers, placement1("acme", "orders", 1001))
	shells := &recordingShellRegistrar{err: errors.New("volume assignment not replicated yet")}
	r.SetShellRegistrar(shells)

	r.ReconcileOnce(context.Background())
	r.ReconcileOnce(context.Background())

	if got := shells.registered(); len(got) != 2 {
		t.Fatalf("shell registration attempts = %v, want one per pass", got)
	}
	if factory.attachCount() != 0 {
		t.Fatalf("Attach called %d times after failed shell registrations, want 0", factory.attachCount())
	}
}

// TestTenantShardReconciler_SetShellRegistrarNil verifies that clearing the
// registrar stops shell registration without disturbing the deferral.
func TestTenantShardReconciler_SetShellRegistrarNil(t *testing.T) {
	factory := newFakeShardLifecycle()
	barriers := newFakeBarrierReadiness()
	barriers.markMissing("acme")
	r, _ := newTestReconciler(t, factory, barriers, placement1("acme", "orders", 1001))
	shells := &recordingShellRegistrar{}
	r.SetShellRegistrar(shells)
	r.SetShellRegistrar(nil)

	r.ReconcileOnce(context.Background())

	if got := shells.registered(); len(got) != 0 {
		t.Fatalf("shells registered = %v after the registrar was cleared, want none", got)
	}
	if factory.attachCount() != 0 {
		t.Fatalf("Attach called %d times, want 0", factory.attachCount())
	}
}
