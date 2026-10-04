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

package tenant

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// raftAndVolumeEntitlements permits prefix, volume, and raft isolation plus
// the dedicated-storage features each requires, for WithDataRoot tests that
// exercise both dedicated-isolation types.
func raftAndVolumeEntitlements() *quota.Entitlements {
	return &quota.Entitlements{
		AllowedIsolationModes: []string{
			quota.IsolationModePrefix,
			quota.IsolationModeVolume,
			quota.IsolationModeRaft,
		},
		AllowVolumeStorage: true,
		AllowDedicatedRaft: true,
	}
}

// newDataRootService builds a TenantService whose default entitlements permit
// both volume and raft isolation, backed by a pooled barrier registry.
func newDataRootService(t *testing.T) *TenantService {
	t.Helper()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(seal.NewMemoryBackend(), reg, testLogger(),
		WithDefaultEntitlements(raftAndVolumeEntitlements()))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc
}

// newDataRootServiceOpts builds a volume/raft-entitled TenantService and
// returns it alongside its barrier registry, threading any extra options
// (e.g. WithReservedDataRoots) after the default entitlements.
func newDataRootServiceOpts(t *testing.T, opts ...TenantServiceOption) (*TenantService, *seal.BarrierRegistry) {
	t.Helper()
	reg := newTestBarrierRegistry(t)
	all := append([]TenantServiceOption{WithDefaultEntitlements(raftAndVolumeEntitlements())}, opts...)
	svc, err := NewTenantService(seal.NewMemoryBackend(), reg, testLogger(), all...)
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc, reg
}

// newDataRootServiceOptsUnsealed is newDataRootServiceOpts with its registry's
// system barrier already initialized (and so unsealed):
// BarrierRegistry.RegisterVolumeTenant/ReattachVolumeTenant -- which
// CreateTenant(IsolationVolume/IsolationRaft, WithDataRoot(...)) reaches --
// refuse with ErrSealed otherwise, and these WithDataRoot tests are about the
// DataRoot-overlap guard, not the system barrier's own seal state.
func newDataRootServiceOptsUnsealed(t *testing.T, opts ...TenantServiceOption) (*TenantService, *seal.BarrierRegistry) {
	t.Helper()
	svc, reg := newDataRootServiceOpts(t, opts...)
	if err := reg.SystemBarrier().Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	return svc, reg
}

// TestDataRootsOverlap covers the component-aware containment helper directly,
// including the /data/foo vs /data/foo2 guard that a naive strings.HasPrefix
// would get wrong.
func TestDataRootsOverlap(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"equal", "/data/foo", "/data/foo", true},
		{"equal trailing slash normalized", "/data/foo/", "/data/foo", true},
		{"a parent of b", "/data/foo", "/data/foo/bar", true},
		{"b parent of a", "/data/foo/bar", "/data/foo", true},
		{"deep child", "/data", "/data/a/b/c", true},
		{"sibling foo vs foo2", "/data/foo", "/data/foo2", false},
		{"sibling foo2 vs foo", "/data/foo2", "/data/foo", false},
		{"unrelated", "/data/foo", "/other/bar", false},
		{"prefix-string but not path", "/data/foobar", "/data/foo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dataRootsOverlap(tt.a, tt.b); got != tt.want {
				t.Errorf("dataRootsOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestCreateTenant_WithDataRoot_IsRefused verifies an explicit data root is
// refused with *DataRootRetiredError for every isolation type, before any side
// effect: no tenant record, no barrier, and nothing created at the path.
func TestCreateTenant_WithDataRoot_IsRefused(t *testing.T) {
	svc, reg := newDataRootServiceOptsUnsealed(t)
	ctx := context.Background()

	for _, iso := range []IsolationType{IsolationVolume, IsolationRaft, IsolationPrefix} {
		t.Run(string(iso), func(t *testing.T) {
			id := "explicit-" + string(iso)
			root := filepath.Join(t.TempDir(), "dedicated-disk", id)
			_, err := svc.CreateTenant(ctx, id, "Explicit", iso, WithDataRoot(root))
			var retired *DataRootRetiredError
			if !errors.As(err, &retired) || retired.TenantID != id || retired.DataRoot != root {
				t.Fatalf("CreateTenant(%s, WithDataRoot) = %v, want *DataRootRetiredError", iso, err)
			}
			if retired.Error() == "" {
				t.Fatal("empty message")
			}
			if _, gerr := svc.GetTenant(ctx, id); !errors.Is(gerr, ErrTenantNotFound) {
				t.Errorf("GetTenant after the refusal = %v, want ErrTenantNotFound", gerr)
			}
			if _, rerr := reg.GetTenant(id); rerr == nil {
				t.Error("a barrier was registered for a refused tenant")
			}
			if _, serr := os.Stat(root); !os.IsNotExist(serr) {
				t.Errorf("the refused path was created: %v", serr)
			}
		})
	}
}

// TestCreateTenant_PersistsNoDataRoot verifies a volume tenant's stored record
// carries no DataRoot -- the record is replicated and a path names one node's
// disk -- while GetTenant and ListTenants report this node's own directory,
// resolved from the tenant's pool and slot.
func TestCreateTenant_PersistsNoDataRoot(t *testing.T) {
	svc, reg := newDataRootServiceOptsUnsealed(t)
	ctx := context.Background()

	created, err := svc.CreateTenant(ctx, "vol-local", "Volume", IsolationVolume)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	raw, err := svc.store.Get(ctx, metadataPrefix+"vol-local")
	if err != nil {
		t.Fatalf("read stored record: %v", err)
	}
	if strings.Contains(string(raw), "data_root") {
		t.Fatalf("stored tenant record %s carries a data root", raw)
	}

	local, err := reg.LocalTenantDataRoot(ctx, "vol-local")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot: %v", err)
	}
	if created.DataRoot != local {
		t.Errorf("CreateTenant DataRoot = %q, want this node's %q", created.DataRoot, local)
	}
	got, err := svc.GetTenant(ctx, "vol-local")
	if err != nil || got.DataRoot != local {
		t.Fatalf("GetTenant = %+v, %v; want DataRoot %q", got, err, local)
	}
	list, err := svc.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	found := false
	for _, ti := range list {
		if ti.ID == "vol-local" {
			found = ti.DataRoot == local
		}
	}
	if !found {
		t.Fatalf("ListTenants did not report vol-local at %q: %+v", local, list)
	}
}

// TestGetTenant_RefusesAStoredDataRoot verifies a stored tenant record that
// carries a DataRoot is refused with *PersistedDataRootError -- by GetTenant
// and by ListTenants -- rather than handing out another node's path.
func TestGetTenant_RefusesAStoredDataRoot(t *testing.T) {
	svc, _ := newDataRootServiceOptsUnsealed(t)
	ctx := context.Background()
	foreign := filepath.Join(t.TempDir(), "another-node", "legacy")
	record := `{"id":"legacy","name":"Legacy","isolation_type":"volume","data_root":"` + foreign + `"}`
	if err := svc.store.Put(ctx, metadataPrefix+"legacy", []byte(record)); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	var persisted *PersistedDataRootError
	if _, err := svc.GetTenant(ctx, "legacy"); !errors.As(err, &persisted) || persisted.DataRoot != foreign {
		t.Fatalf("GetTenant = %v, want *PersistedDataRootError naming %s", err, foreign)
	}
	if persisted.Error() == "" {
		t.Fatal("empty message")
	}
	tenants, err := svc.ListTenants(ctx)
	if err != nil || len(tenants) != 1 || !errors.As(tenants[0].Refusal, &persisted) || tenants[0].DataRoot != "" {
		t.Fatalf("ListTenants = %+v, %v; want legacy listed as refused with no DataRoot", tenants, err)
	}
}
