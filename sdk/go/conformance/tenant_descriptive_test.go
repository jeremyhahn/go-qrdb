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
	"maps"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A tenant's Description and Metadata cross every transport identically: each
// cell creates a tenant through the node's own tenant service, then sets,
// leaves alone, overruns and clears both fields over ONE transport, reading
// the record back over the same transport after every step.

// assertTenantDescriptive fails unless info carries exactly desc and md; a nil
// or empty md both mean "no metadata".
func assertTenantDescriptive(t *testing.T, tk transportKind, step string, info transport.TenantInfo, desc string, md map[string]string) {
	t.Helper()
	if info.Description != desc {
		t.Fatalf("%s: %s: Description = %q; want %q", tk, step, info.Description, desc)
	}
	if len(info.Metadata) != len(md) || (len(md) > 0 && !maps.Equal(info.Metadata, md)) {
		t.Fatalf("%s: %s: Metadata = %v; want %v", tk, step, info.Metadata, md)
	}
}

func TestTenantDescriptiveFields_RoundTripOnEveryTransport(t *testing.T) {
	const id = "acme"
	desc := "Acme production"
	md := map[string]string{"env": "prod", "team": "storage"}

	for _, tk := range userNotFoundTransports {
		t.Run(string(tk), func(t *testing.T) {
			// The caller is a system-tenant operator acting on another tenant,
			// the shape a platform composer's tenant management takes.
			c := bootCell(t, tk, permission.TenantsRead, permission.TenantsUpdate,
				permission.TenantsReadCross, permission.TenantsUpdateCross)
			if _, err := c.node.Services().Tenant.CreateTenant(boundedCtx(t), id, "Acme", tenant.IsolationPrefix); err != nil {
				t.Fatalf("CreateTenant(%s): %v", id, err)
			}
			ctx := c.callerCtx(t, tk)
			read := func(step string) transport.TenantInfo {
				t.Helper()
				got, err := c.tenants.GetTenant(ctx, id)
				if err != nil {
					t.Fatalf("%s: %s: GetTenant: %v", tk, step, err)
				}
				return got.Tenant
			}

			assertTenantDescriptive(t, tk, "before any update", read("before any update"), "", nil)

			set, err := c.tenants.UpdateTenant(ctx, &transport.UpdateTenantRequest{
				TenantID: id, Description: &desc, Metadata: md,
			})
			if err != nil {
				t.Fatalf("%s: UpdateTenant (set): %v", tk, err)
			}
			assertTenantDescriptive(t, tk, "set response", set.Tenant, desc, md)
			assertTenantDescriptive(t, tk, "set", read("set"), desc, md)

			if _, err := c.tenants.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: id, Name: "Acme Corp"}); err != nil {
				t.Fatalf("%s: UpdateTenant (name only): %v", tk, err)
			}
			got := read("absent")
			if got.Name != "Acme Corp" {
				t.Fatalf("%s: absent: Name = %q; want the renamed tenant", tk, got.Name)
			}
			assertTenantDescriptive(t, tk, "absent", got, desc, md)

			long := strings.Repeat("d", tenant.MaxDescriptionBytes+1)
			if _, err := c.tenants.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: id, Description: &long}); err == nil {
				t.Fatalf("%s: a description over %d bytes was accepted", tk, tenant.MaxDescriptionBytes)
			}
			if _, err := c.tenants.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: id, Metadata: map[string]string{"": "v"}}); err == nil {
				t.Fatalf("%s: an empty metadata key was accepted", tk)
			}
			assertTenantDescriptive(t, tk, "refused", read("refused"), desc, md)

			empty := ""
			if _, err := c.tenants.UpdateTenant(ctx, &transport.UpdateTenantRequest{
				TenantID: id, Description: &empty, Metadata: map[string]string{},
			}); err != nil {
				t.Fatalf("%s: UpdateTenant (clear): %v", tk, err)
			}
			assertTenantDescriptive(t, tk, "cleared", read("cleared"), "", nil)
		})
	}
}
