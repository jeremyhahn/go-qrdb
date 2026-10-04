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

package seal

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// errTenantRootWrite is the fault tenantRootFaultBackend injects.
var errTenantRootWrite = errors.New("injected tenant-root write failure")

// tenantRootFaultBackend fails every write to a tenant-root record while
// armed, and passes everything else -- the system barrier's own keys, slot
// assignments -- to the wrapped backend.
type tenantRootFaultBackend struct {
	StorageBackend
	armed atomic.Bool
}

func (b *tenantRootFaultBackend) Put(ctx context.Context, key string, data []byte) error {
	if b.armed.Load() && strings.Contains(key, tenantRootStorePrefix) {
		return errTenantRootWrite
	}
	return b.StorageBackend.Put(ctx, key, data)
}

// newTenantRootFaultRegistry returns a pooled registry whose system barrier is
// initialized over a tenantRootFaultBackend.
func newTenantRootFaultRegistry(t *testing.T) (*BarrierRegistry, *tenantRootFaultBackend) {
	t.Helper()
	ctx := context.Background()
	_, opts, _ := newShareableComponents(t)
	backend := &tenantRootFaultBackend{StorageBackend: NewMemoryBackend()}
	system, err := NewStorageBarrier(poolTestLogger(), backend, BarrierConfig{RootKeyPath: "system/root-key"},
		newPoolSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := system.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}
	reg, err := NewBarrierRegistry(poolTestLogger(), system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return reg, backend
}

// TestRegisterVolumeTenant_TenantRootWriteFailureFailsTheCreate: a tenant
// whose tenant-root record did not land would serve now and vanish at the next
// restart. The registration fails with *TenantRootPersistError, nothing stays
// registered, and the same create succeeds once the store recovers -- the
// store the failed attempt opened was released.
func TestRegisterVolumeTenant_TenantRootWriteFailureFailsTheCreate(t *testing.T) {
	ctx := context.Background()
	reg, backend := newTenantRootFaultRegistry(t)
	backend.armed.Store(true)

	_, tb, err := reg.RegisterVolumeTenant(ctx, "vt", true)
	var perr *TenantRootPersistError
	if !errors.As(err, &perr) || perr.TenantID != "vt" || !errors.Is(err, errTenantRootWrite) {
		t.Fatalf("RegisterVolumeTenant = %v; want *TenantRootPersistError wrapping the write failure", err)
	}
	if tb != nil {
		t.Fatal("a failed registration returned a barrier")
	}
	if _, gerr := reg.GetTenant("vt"); gerr == nil {
		t.Fatal("a tenant whose record did not land is still registered")
	}
	if !strings.Contains(perr.Error(), "vt") {
		t.Fatalf("message %q does not name the tenant", perr.Error())
	}

	backend.armed.Store(false)
	if _, _, err := reg.RegisterVolumeTenant(ctx, "vt", true); err != nil {
		t.Fatalf("RegisterVolumeTenant after recovery = %v; the failed attempt left state behind", err)
	}
	rec, found, err := reg.loadTenantRoot(ctx, "vt")
	if err != nil || !found || !rec.AutoUnseal {
		t.Fatalf("tenant-root record after recovery = %+v, %v, %v; want it written", rec, found, err)
	}
}

// TestPersistTenantRoot_SealedSystemBarrierIsAnError: the record cannot be
// written behind a sealed system barrier, and that is a failure, not a skip.
func TestPersistTenantRoot_SealedSystemBarrierIsAnError(t *testing.T) {
	reg, _ := newTenantRootFaultRegistry(t)
	if err := reg.SystemBarrier().Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	err := reg.persistTenantRoot(context.Background(), "vt", slotAssignment{Pool: "default"}, false)
	var perr *TenantRootPersistError
	if !errors.As(err, &perr) || !errors.Is(err, ErrSealed) {
		t.Fatalf("persistTenantRoot on a sealed system barrier = %v; want *TenantRootPersistError wrapping ErrSealed", err)
	}
}
