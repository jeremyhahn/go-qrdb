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

package conformance

import (
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Retired tenant ids are a Tier-1 capability: listing them and reconciling
// their residue must behave identically on every transport (go-qrdb#470,
// go-qrdb#469). Each cell retires tenants through the node's own tenant
// service -- the production DeleteTenant -- and then reads the retirements
// and runs the reconcile over ONE transport.

// retireThroughNode creates and deletes each id through the node's real
// tenant service.
func retireThroughNode(t *testing.T, c userClients, ids ...string) time.Time {
	t.Helper()
	svc := c.node.Services().Tenant
	ctx := boundedCtx(t)
	before := time.Now().UTC().Truncate(time.Second)
	for _, id := range ids {
		if _, err := svc.CreateTenant(ctx, id, "Tenant "+id, tenant.IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
		if err := svc.DeleteTenant(ctx, id); err != nil {
			t.Fatalf("DeleteTenant(%s): %v", id, err)
		}
	}
	return before
}

func TestListDeletedTenants_OnEveryTransport(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootCell(t, tk, permission.TenantsListDeleted, permission.TenantsReconcileDeleted)
			before := retireThroughNode(t, c, "charlie", "alpha", "bravo")
			ctx := boundedCtx(t)

			first, err := c.tenants.ListDeletedTenants(ctx, &transport.ListDeletedTenantsRequest{PageSize: 2})
			if err != nil {
				t.Fatalf("%s: ListDeletedTenants: %v", tk, err)
			}
			if len(first.Tenants) != 2 || first.Tenants[0].ID != "alpha" || first.Tenants[1].ID != "bravo" {
				t.Fatalf("%s: first page = %+v; want alpha, bravo", tk, first.Tenants)
			}
			if first.NextPageToken == "" {
				t.Fatalf("%s: a first page of two out of three carried no next page token", tk)
			}
			got := first.Tenants[0]
			if got.Name != "Tenant alpha" || got.IsolationType != transport.TenantIsolationPrefix {
				t.Fatalf("%s: record = %+v; want the deleted tenant's name and isolation", tk, got)
			}
			if got.DeletedAt.Before(before) {
				t.Fatalf("%s: DeletedAt = %v; want the deletion time (after %v)", tk, got.DeletedAt, before)
			}

			second, err := c.tenants.ListDeletedTenants(ctx, &transport.ListDeletedTenantsRequest{
				PageSize:  2,
				PageToken: first.NextPageToken,
			})
			if err != nil {
				t.Fatalf("%s: ListDeletedTenants (page 2): %v", tk, err)
			}
			if len(second.Tenants) != 1 || second.Tenants[0].ID != "charlie" || second.NextPageToken != "" {
				t.Fatalf("%s: second page = %+v (token %q); want only charlie and no token", tk, second.Tenants, second.NextPageToken)
			}

			if _, err := c.tenants.ListDeletedTenants(ctx, &transport.ListDeletedTenantsRequest{PageSize: -1}); err == nil {
				t.Fatalf("%s: a negative page size was accepted", tk)
			}
		})
	}
}

func TestReconcileDeletedTenants_OnEveryTransport(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			c := bootCell(t, tk, permission.TenantsListDeleted, permission.TenantsReconcileDeleted)
			retireThroughNode(t, c, "alpha", "bravo")

			report, err := c.tenants.ReconcileDeletedTenants(boundedCtx(t))
			if err != nil {
				t.Fatalf("%s: ReconcileDeletedTenants: %v", tk, err)
			}
			if report.Inspected != 2 {
				t.Fatalf("%s: inspected %d retired ids; want 2", tk, report.Inspected)
			}
			// Prefix tenants own no directory, so nothing is reclaimed; the
			// directory semantics are pinned in pkg/tenant against a real
			// registry. What this cell pins is that the verb crosses the
			// transport and reports what it did.
			if len(report.Reclaimed) != 0 {
				t.Fatalf("%s: reclaimed %+v for prefix tenants that own no directory", tk, report.Reclaimed)
			}
		})
	}
}

// TestDeletedTenantVerbs_RequireTheirPermission: an identity without the two
// cross-scope permissions is refused on every remote transport, so neither
// verb is reachable by a caller merely able to list tenants.
func TestDeletedTenantVerbs_RequireTheirPermission(t *testing.T) {
	for _, tk := range userNotFoundTransports {
		if !tk.remote() {
			continue
		}
		t.Run(string(tk), func(t *testing.T) {
			c := bootCell(t, tk)
			ctx := boundedCtx(t)
			if _, err := c.tenants.ListDeletedTenants(ctx, &transport.ListDeletedTenantsRequest{}); err == nil {
				t.Fatalf("%s: ListDeletedTenants succeeded without %s", tk, permission.TenantsListDeleted)
			}
			if _, err := c.tenants.ReconcileDeletedTenants(ctx); err == nil {
				t.Fatalf("%s: ReconcileDeletedTenants succeeded without %s", tk, permission.TenantsReconcileDeleted)
			}
		})
	}
}
