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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// servicesDuringCloseBarriers is a MoveBarriers double that, inside the
// move's close (after the Host was evicted, before the barrier closes), runs
// the Services call a concurrent request would make, so the race the move
// hold closes is exercised deterministically.
type servicesDuringCloseBarriers struct {
	hosts *TenantHostRegistry
	reg   *seal.BarrierRegistry
	err   error
}

func (b *servicesDuringCloseBarriers) CloseVolumeTenant(ctx context.Context, tenantID string) error {
	_, b.err = b.hosts.Services(ctx, tenantID)
	return b.reg.CloseVolumeTenant(ctx, tenantID)
}

func (b *servicesDuringCloseBarriers) ReopenVolumeTenant(ctx context.Context, tenantID string) error {
	return b.reg.ReopenVolumeTenant(ctx, tenantID)
}

// TestTenantHostRegistry_MoveHoldRefusesServices: while a move step holds a
// tenant, Services neither creates nor caches a Host and returns the
// retryable ErrTenantMoveInProgress; the move's own reopen (Attach) still
// builds it, and Services serves again once the hold is released.
func TestTenantHostRegistry_MoveHoldRefusesServices(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	ctx := context.Background()
	// A system-managed tenant: the move's reopen unseals it again.
	allocVal, _ := testVolumeAllocators.Load(reg)
	sealtest.ReserveVolumeTenant(t, allocVal.(volume.Allocator), "vt")
	if _, _, err := reg.RegisterVolumeTenant(ctx, "vt", true); err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if err := reg.InitializeTenant(ctx, "vt", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}

	hostReg := NewTenantHostRegistry(reg, &lookupDataRootSource{registry: reg}, volTestLogger())
	t.Cleanup(func() { _ = hostReg.CloseAll() })
	if _, err := hostReg.Services(ctx, "vt"); err != nil {
		t.Fatalf("Services before the move: %v", err)
	}

	barriers := &servicesDuringCloseBarriers{hosts: hostReg, reg: reg}
	closer, err := NewMoveStoreCloser(nil, barriers, nil, 0, WithMoveTenantHosts(hostReg))
	if err != nil {
		t.Fatalf("NewMoveStoreCloser: %v", err)
	}
	hostReg.SetMoveHold(closer)

	if err := closer.CloseTenantStores(ctx, "vt"); err != nil {
		t.Fatalf("CloseTenantStores: %v", err)
	}
	if !errors.Is(barriers.err, ErrTenantMoveInProgress) {
		t.Fatalf("Services during the close = %v, want ErrTenantMoveInProgress", barriers.err)
	}
	if hostReg.Started("vt") {
		t.Fatal("Services during the close created and cached a Host")
	}
	if _, err := hostReg.Services(ctx, "vt"); !errors.Is(err, ErrTenantMoveInProgress) {
		t.Fatalf("Services while held = %v, want ErrTenantMoveInProgress", err)
	}
	if hostReg.Started("vt") {
		t.Fatal("Services while held cached a Host")
	}

	if err := closer.OpenTenantStores(ctx, "vt"); err != nil {
		t.Fatalf("OpenTenantStores: %v", err)
	}
	if !hostReg.Started("vt") {
		t.Fatal("the move's reopen did not attach the Host")
	}
	if closer.HoldsTenant("vt") {
		t.Fatal("the hold outlived the reopen")
	}
	if svcs, err := hostReg.Services(ctx, "vt"); err != nil || svcs == nil {
		t.Fatalf("Services after the move = %v, %v", svcs, err)
	}
}
