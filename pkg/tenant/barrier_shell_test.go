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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// joinWithoutBarrier leaves this node as one placement has just assigned to
// tenantID's group: it holds the tenant's record and volume assignment, and
// neither the barrier nor its data.
func joinWithoutBarrier(t *testing.T, reg *seal.BarrierRegistry, tenantID string) {
	t.Helper()
	ctx := context.Background()
	dataRoot, err := reg.LocalTenantDataRoot(ctx, tenantID)
	if err != nil {
		t.Fatalf("LocalTenantDataRoot: %v", err)
	}
	if err := reg.ReleaseTenant(ctx, tenantID); err != nil {
		t.Fatalf("ReleaseTenant: %v", err)
	}
	if err := os.RemoveAll(dataRoot); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := reg.GetTenant(tenantID); err == nil {
		t.Fatal("the joined node still holds the tenant's barrier")
	}
}

// TestRegisterTenantShell_OwnerCustody verifies that a node placement assigns
// to an owner-custody raft tenant registers an uninitialized, sealed shell --
// no key exists until the owner initializes this node -- and that a second
// registration leaves that shell in place.
func TestRegisterTenantShell_OwnerCustody(t *testing.T) {
	svc, reg := newPlatformService(t, &bookDouble{}, nil)
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationRaft); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	joinWithoutBarrier(t, reg, "acme")

	if err := svc.RegisterTenantShell(ctx, "acme"); err != nil {
		t.Fatalf("RegisterTenantShell: %v", err)
	}
	shell, err := reg.GetTenant("acme")
	if err != nil {
		t.Fatalf("registry GetTenant after the shell: %v", err)
	}
	if !shell.IsSealed() {
		t.Fatal("a shell must be sealed")
	}
	if initialized(t, svc, "acme") {
		t.Fatal("a shell must hold no key")
	}

	if err := svc.RegisterTenantShell(ctx, "acme"); err != nil {
		t.Fatalf("RegisterTenantShell again: %v", err)
	}
	again, err := reg.GetTenant("acme")
	if err != nil {
		t.Fatalf("registry GetTenant after the second registration: %v", err)
	}
	if again != shell {
		t.Fatal("a second registration replaced the shell")
	}
}

// TestRegisterTenantShell_PlatformCustodyThenSelfInit verifies that the shell
// of a platform-custody isolated tenant -- every one of them has a home group
// placed across the cluster -- is the barrier the platform's init statement
// then initializes, under this node's own key.
func TestRegisterTenantShell_PlatformCustodyThenSelfInit(t *testing.T) {
	for _, isolation := range isolatedTypes {
		t.Run(string(isolation), func(t *testing.T) {
			svc, reg := newPlatformService(t, &bookDouble{}, &platformSignerDouble{raw: []byte("signed")})
			ctx := context.Background()
			if _, err := svc.CreateTenant(ctx, "acme", "Acme", isolation, WithBarrierAutoUnseal(true)); err != nil {
				t.Fatalf("CreateTenant: %v", err)
			}
			joinWithoutBarrier(t, reg, "acme")

			if err := svc.RegisterTenantShell(ctx, "acme"); err != nil {
				t.Fatalf("RegisterTenantShell: %v", err)
			}
			shell, err := reg.GetTenant("acme")
			if err != nil {
				t.Fatalf("registry GetTenant after the shell: %v", err)
			}
			if !shell.IsSealed() || initialized(t, svc, "acme") {
				t.Fatal("a platform-custody shell must be sealed and hold no key before self-init")
			}

			if err := svc.TenantBarrierSelfInit(ctx, "acme", seal.StrategySystemBarrier, ""); err != nil {
				t.Fatalf("TenantBarrierSelfInit: %v", err)
			}
			served, err := reg.GetTenant("acme")
			if err != nil {
				t.Fatalf("registry GetTenant after self-init: %v", err)
			}
			if served != shell {
				t.Fatal("self-init registered a second barrier instead of initializing the shell")
			}
			if !initialized(t, svc, "acme") || served.IsSealed() {
				t.Fatal("self-init must leave the shell initialized and unsealed")
			}
		})
	}
}

// TestRegisterTenantShell_LeavesOtherTenantsAlone verifies that a tenant this
// node has no record of, or one that already holds a barrier here, registers
// nothing.
func TestRegisterTenantShell_LeavesOtherTenantsAlone(t *testing.T) {
	svc, reg := newPlatformService(t, &bookDouble{}, nil)
	ctx := context.Background()

	if err := svc.RegisterTenantShell(ctx, "globex"); err != nil {
		t.Fatalf("RegisterTenantShell(unknown tenant) = %v, want nil", err)
	}
	if _, err := reg.GetTenant("globex"); !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("an unknown tenant got a barrier: GetTenant = %v", err)
	}

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationRaft); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	created, err := reg.GetTenant("acme")
	if err != nil {
		t.Fatalf("registry GetTenant: %v", err)
	}
	if err := svc.RegisterTenantShell(ctx, "acme"); err != nil {
		t.Fatalf("RegisterTenantShell(registered tenant): %v", err)
	}
	if after, err := reg.GetTenant("acme"); err != nil || after != created {
		t.Fatalf("the registered barrier was replaced: %v", err)
	}
}
