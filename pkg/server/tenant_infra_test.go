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

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
)

// TestBuildTenantInfra_HostWithoutManager_ReturnsConfigError verifies that
// passing a non-nil Host with a nil Manager returns TenantInfraConfigError
// instead of panicking inside NewRaftBackendFactoryFromManager or
// reattachRaftTenants. This is the canonical contract violation for BUG 3.
func TestBuildTenantInfra_HostWithoutManager_ReturnsConfigError(t *testing.T) {
	// new(quicraft.Host) is a non-nil pointer to a zeroed Host struct.  No Raft
	// infrastructure is started; the guard fires before any Host method is called.
	_, err := BuildTenantInfra(TenantInfraConfig{
		Ctx:  context.Background(),
		Host: new(quicraft.Host),
		// Manager intentionally omitted — the contract violation under test.
	})

	var cfgErr *TenantInfraConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *TenantInfraConfigError, got %T: %v", err, err)
	}
	if cfgErr.Field != "Manager" {
		t.Errorf("TenantInfraConfigError.Field = %q, want %q", cfgErr.Field, "Manager")
	}
}

// TestBuildTenantInfra_HostWithoutManager_AllOtherFieldsNonNil_ReturnsConfigError
// verifies that TenantInfraConfigError is returned even when BarrierReg,
// TenantSvc, and VolumePool are all non-nil — i.e. the guard fires before
// reattachRaftTenants dereferences the nil Manager.
//
// Without the fix the original code would enter the IsolationRaft block
// and either panic inside NewRaftBackendFactoryFromManager (nil manager
// dereference) or panic inside reattachRaftTenants (manager.List() on nil).
func TestBuildTenantInfra_HostWithoutManager_AllOtherFieldsNonNil_ReturnsConfigError(t *testing.T) {
	_, reg := newRaftReattachFixture(t)
	svc, _ := newRaftReattachFixture(t)

	_, err := BuildTenantInfra(TenantInfraConfig{
		Ctx:          context.Background(),
		BarrierReg:   reg,
		TenantSvc:    svc,
		TenantLookup: svc,
		// Non-nil VolumePool so the IsolationVolume block is entered and
		// ti.Hosts is built — verifying that it is cleaned up before the
		// TenantInfraConfigError is returned.
		VolumePool: &qrdbpool.VolumePoolSubsystem{},
		Host:       new(quicraft.Host),
		// Manager intentionally omitted.
		Logger: reattachTestLogger(),
	})

	var cfgErr *TenantInfraConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *TenantInfraConfigError, got %T: %v", err, err)
	}
	if cfgErr.Field != "Manager" {
		t.Errorf("TenantInfraConfigError.Field = %q, want %q", cfgErr.Field, "Manager")
	}
}
