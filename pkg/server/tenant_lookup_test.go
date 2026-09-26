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
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestBuildTenantInfra_VolumeRegistryWithoutLookup_ReturnsConfigError pins
// go-qrdb#383's contract: the IsolationVolume registry outlives a single tenant
// service, so building it requires an explicit use-time lookup. Omitting one is
// refused before any side effect rather than silently capturing TenantSvc.
func TestBuildTenantInfra_VolumeRegistryWithoutLookup_ReturnsConfigError(t *testing.T) {
	svc, reg := newRaftReattachFixture(t)

	ti, err := BuildTenantInfra(TenantInfraConfig{
		Ctx:        context.Background(),
		BarrierReg: reg,
		TenantSvc:  svc,
		VolumePool: &qrdbpool.VolumePoolSubsystem{},
		Logger:     reattachTestLogger(),
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

// swappableLookup is a TenantLookup whose backing service can be replaced,
// modelling a composition that rebuilds its tenant service in place.
type swappableLookup struct {
	current atomic.Pointer[tenantLookupBox]
}

type tenantLookupBox struct{ lookup TenantLookup }

func (s *swappableLookup) GetTenant(ctx context.Context, id string) (*tenant.TenantInfo, error) {
	return s.current.Load().lookup.GetTenant(ctx, id)
}

// fixedDataRoot answers every GetTenant with one DataRoot.
type fixedDataRoot string

func (f fixedDataRoot) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, DataRoot: string(f)}, nil
}

// TestTenantHostRegistry_ResolvesLookupAtUseTime is the unit-level half of
// go-qrdb#383: the registry must consult its lookup on EVERY DataRoot
// resolution, so swapping the service behind the lookup is visible to a
// registry built before the swap. A registry that unwrapped the lookup once at
// construction would keep answering from the first service.
func TestTenantHostRegistry_ResolvesLookupAtUseTime(t *testing.T) {
	sw := &swappableLookup{}
	sw.current.Store(&tenantLookupBox{lookup: fixedDataRoot("/first")})
	reg := NewTenantHostRegistryFromLookup(nil, sw, reattachTestLogger())

	got, err := reg.tenants.dataRootOf(context.Background(), "acme")
	if err != nil || got != "/first" {
		t.Fatalf("dataRootOf before swap = %q, %v; want /first", got, err)
	}
	sw.current.Store(&tenantLookupBox{lookup: fixedDataRoot("/second")})
	got, err = reg.tenants.dataRootOf(context.Background(), "acme")
	if err != nil || got != "/second" {
		t.Fatalf("dataRootOf after swap = %q, %v; want /second (the registry captured the first service)", got, err)
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

// TestLookupDataRootSource_PropagatesLookupError verifies a lookup failure is
// returned to the registry rather than read as an empty DataRoot.
func TestLookupDataRootSource_PropagatesLookupError(t *testing.T) {
	src := &lookupDataRootSource{lookup: failingLookup{}}
	if _, err := src.dataRootOf(context.Background(), "acme"); !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("dataRootOf = %v, want tenant.ErrTenantNotFound", err)
	}
}

type failingLookup struct{}

func (failingLookup) GetTenant(context.Context, string) (*tenant.TenantInfo, error) {
	return nil, tenant.ErrTenantNotFound
}
