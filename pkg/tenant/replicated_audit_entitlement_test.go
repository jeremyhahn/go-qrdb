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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestReplicatedAuditEnabled_DefaultIsFalse asserts the secure default: a
// freshly created tenant with no explicit entitlement grant is NOT entitled
// to the replicated audit chain.
func TestReplicatedAuditEnabled_DefaultIsFalse(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	on, err := svc.ReplicatedAuditEnabled(ctx, "acme")
	if err != nil {
		t.Fatalf("ReplicatedAuditEnabled: %v", err)
	}
	if on {
		t.Fatal("want false by default")
	}
}

// TestReplicatedAuditEnabled_ExplicitGrant asserts a tenant whose
// entitlements explicitly set AllowReplicatedAudit is reported entitled.
func TestReplicatedAuditEnabled_ExplicitGrant(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateEntitlements(ctx, "acme", &quota.Entitlements{AllowReplicatedAudit: true}); err != nil {
		t.Fatalf("UpdateEntitlements: %v", err)
	}

	on, err := svc.ReplicatedAuditEnabled(ctx, "acme")
	if err != nil {
		t.Fatalf("ReplicatedAuditEnabled: %v", err)
	}
	if !on {
		t.Fatal("want true after explicit grant")
	}
}

// TestReplicatedAuditEnabled_SystemTenantNotImplicitlyEntitled asserts the
// system tenant is NOT implicitly entitled here, unlike CheckIsolation and
// CheckBarrierMode: the platform-event trail is governed by a separate
// setting this method does not read.
func TestReplicatedAuditEnabled_SystemTenantNotImplicitlyEntitled(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	on, err := svc.ReplicatedAuditEnabled(ctx, tenantid.System)
	if err != nil {
		t.Fatalf("ReplicatedAuditEnabled: %v", err)
	}
	if on {
		t.Fatal("system tenant must not be implicitly entitled")
	}
}

// TestReplicatedAuditEnabled_UnknownTenantDefaults asserts a tenant with no
// metadata record at all (never created) resolves through the same
// configured-default path as effectiveEntitlements, rather than erroring.
func TestReplicatedAuditEnabled_UnknownTenantDefaults(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	on, err := svc.ReplicatedAuditEnabled(ctx, "never-created")
	if err != nil {
		t.Fatalf("ReplicatedAuditEnabled: %v", err)
	}
	if on {
		t.Fatal("want false for an unregistered tenant under the built-in defaults")
	}
}
