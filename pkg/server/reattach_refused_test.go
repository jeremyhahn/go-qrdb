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
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// putLegacyTenant writes the records a tenant created before tenant locations
// were resolved per node left behind -- tenant metadata and a tenant-root
// record both naming a path -- through the stores the services read, and
// returns that path.
func putLegacyTenant(t *testing.T, store *seal.MemoryBackend, reg *seal.BarrierRegistry, id string, iso tenant.IsolationType) string {
	t.Helper()
	ctx := context.Background()
	recorded := filepath.Join(t.TempDir(), "another-node", id)
	meta := `{"id":"` + id + `","name":"` + id + `","isolation_type":"` + string(iso) + `","data_root":"` + recorded + `"}`
	if err := store.Put(ctx, "__sys:tenants/"+id, []byte(meta)); err != nil {
		t.Fatalf("put legacy metadata: %v", err)
	}
	rec := `{"pool":"default","slot":0,"data_root":"` + recorded + `","auto_unseal":true}`
	if err := reg.SystemBarrier().Put(ctx, "__registry:tenant_root/"+id, []byte(rec)); err != nil {
		t.Fatalf("put legacy tenant-root record: %v", err)
	}
	return recorded
}

// TestBoot_ALegacyTenantDoesNotBrickTheNode: a cluster holding one tenant
// whose records name a path boots. Restore and both re-attach passes skip it
// with an ERROR naming it, nothing is opened at the path it names, and every
// other tenant is served.
func TestBoot_ALegacyTenantDoesNotBrickTheNode(t *testing.T) {
	svc, reg, store := newReattachFixtureStore(t)
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "vauto", "Auto", tenant.IsolationVolume, tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant(vauto): %v", err)
	}
	if err := reg.SealTenant("vauto"); err != nil {
		t.Fatalf("SealTenant(vauto): %v", err)
	}
	legacyVolume := putLegacyTenant(t, store, reg, "legacyvol", tenant.IsolationVolume)
	legacyRaft := putLegacyTenant(t, store, reg, "legacyraft", tenant.IsolationRaft)

	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}
	hostReg := NewTenantHostRegistryFromLookup(reg, svc, logger)
	t.Cleanup(func() { _ = hostReg.CloseAll() })

	if err := reattachVolumeTenants(ctx, svc, reg, hostReg, logger); err != nil {
		t.Fatalf("reattachVolumeTenants = %v; a legacy tenant must not fail boot", err)
	}
	if err := reattachRaftTenants(ctx, svc, reg, nil, nil, logger); err != nil {
		t.Fatalf("reattachRaftTenants = %v; a legacy tenant must not fail boot", err)
	}

	if _, err := hostReg.Services(ctx, "vauto"); err != nil {
		t.Fatalf("the healthy tenant is not served after boot: %v", err)
	}
	for _, id := range []string{"legacyvol", "legacyraft"} {
		if _, err := reg.GetTenant(id); err == nil {
			t.Errorf("%s: a refused tenant's barrier was registered", id)
		}
		if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "tenant="+id) {
			t.Errorf("%s: boot did not log the refusal at ERROR:\n%s", id, logs.String())
		}
	}
	for _, p := range []string{legacyVolume, legacyRaft} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("boot opened or created the path a refused record names: %s (%v)", p, err)
		}
	}
}
