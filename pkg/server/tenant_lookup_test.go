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
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// TestBuildTenantInfra_VolumeRegistryWithoutLookup_ReturnsConfigError pins
// go-qrdb#383's contract: the IsolationVolume registry outlives a single tenant
// service, so building it requires an explicit use-time lookup. Omitting one is
// refused before any side effect rather than silently capturing TenantSvc.
func TestBuildTenantInfra_VolumeRegistryWithoutLookup_ReturnsConfigError(t *testing.T) {
	svc, reg := newRaftReattachFixture(t)

	ti, err := BuildTenantInfra(TenantInfraConfig{
		Ctx:                  context.Background(),
		BarrierReg:           reg,
		TenantSvc:            svc,
		VolumePoolConfigured: true,
		Logger:               reattachTestLogger(),
		// TenantLookup intentionally omitted -- the contract violation under test.
	})

	var cfgErr *TenantInfraConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("expected *TenantInfraConfigError, got %T: %v", err, err)
	}
	if cfgErr.Field != "TenantLookup" {
		t.Errorf("TenantInfraConfigError.Field = %q, want %q", cfgErr.Field, "TenantLookup")
	}
	if ti != nil {
		t.Errorf("BuildTenantInfra returned a TenantInfra alongside a config error: %+v", ti)
	}
}

// TestNewTenantHostRegistryFromLookup_NilLookupIsUnavailable verifies a nil
// lookup yields a registry that refuses with ErrTenantHostUnavailable rather
// than one that dereferences nil on first use.
func TestNewTenantHostRegistryFromLookup_NilLookupIsUnavailable(t *testing.T) {
	reg := NewTenantHostRegistryFromLookup(&seal.BarrierRegistry{}, nil, reattachTestLogger())
	_, err := reg.Services(context.Background(), "acme")
	if !errors.Is(err, ErrTenantHostUnavailable) {
		t.Fatalf("Services with nil lookup = %v, want ErrTenantHostUnavailable", err)
	}
}

// TestLookupDataRootSource_ResolvesThroughTheRegistryAtUseTime is the
// unit-level half of go-qrdb#383, re-expressed for the catalog-backed
// resolution path: lookupDataRootSource.dataRootOf delegates to
// BarrierRegistry.LocalTenantDataRoot on EVERY call rather than caching a
// resolved path, so a tenant reserved and registered AFTER the
// TenantHostRegistry was built still resolves correctly on first use.
func TestLookupDataRootSource_ResolvesThroughTheRegistryAtUseTime(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	src := &lookupDataRootSource{registry: reg}

	// Not yet reserved/registered: resolution fails.
	if _, err := src.dataRootOf(context.Background(), "acme"); err == nil {
		t.Fatal("dataRootOf must fail before the tenant has a catalog Assignment")
	}

	dataRoot, _ := registerVolumeTenant(t, reg, "acme")

	got, err := src.dataRootOf(context.Background(), "acme")
	if err != nil || got != dataRoot {
		t.Fatalf("dataRootOf after registration = %q, %v; want %q", got, err, dataRoot)
	}
}

// TestLookupDataRootSource_PropagatesRegistryError verifies that a
// resolution failure (here: an unregistered tenant) is returned to the
// caller rather than read as an empty DataRoot.
func TestLookupDataRootSource_PropagatesRegistryError(t *testing.T) {
	reg := newVolumePooledRegistry(t)
	src := &lookupDataRootSource{registry: reg}

	_, err := src.dataRootOf(context.Background(), "ghost")
	var notFound *volume.AssignmentNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("dataRootOf = %v, want *volume.AssignmentNotFoundError", err)
	}
}
