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

package database

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestDatabaseBarrierManager_DoesNotReachATenantBarrier pins that database
// barriers are a namespace of their own. A node's tenant barriers live in the
// seal subsystem's registry keyed by tenant id; a database named like a tenant
// must not resolve to that tenant's barrier, or sealing or reading the status
// of one's own database would seal or describe another tenant's barrier.
func TestDatabaseBarrierManager_DoesNotReachATenantBarrier(t *testing.T) {
	ctx := context.Background()
	system := newTestSystemBarrier(t)
	tenants, err := seal.NewBarrierRegistry(testBarrierLogger(), system)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenants.RegisterTenant(ctx, "acme", seal.NewMemoryBackend(), seal.BarrierConfig{}, seal.NewDeferredSoftwareStrategy()); err != nil {
		t.Fatal(err)
	}
	if err := tenants.InitializeTenant(ctx, "acme", testCreds()); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewDatabaseBarrierManager(testBarrierLogger(), tenants.SystemBarrier())
	if err != nil {
		t.Fatal(err)
	}

	// The database "acme" has no barrier of its own.
	if err := mgr.SealDatabase("acme", ""); err == nil {
		t.Fatal("sealing a database with no barrier succeeded")
	}
	if mgr.GetBarrier("acme", "") != nil {
		t.Fatal("a database with no barrier resolved an encryptor")
	}
	if _, err := mgr.BarrierStatus("acme", ""); err == nil {
		t.Fatal("a database with no barrier reported a barrier status")
	}

	status, err := tenants.TenantStatus("acme")
	if err != nil {
		t.Fatal(err)
	}
	if status.Sealed {
		t.Fatal("the tenant's barrier was sealed through a database of the same name")
	}
}
