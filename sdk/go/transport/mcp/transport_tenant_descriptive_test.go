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

package mcp

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// connectDescriptiveTenants connects to an in-process MCP server over the
// REAL seal-disabled tenant service holding one tenant, "acme".
func connectDescriptiveTenants(t *testing.T) *Transport {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatal(err)
	}
	tr, cleanup := connectTenantInProcess(t, svc)
	t.Cleanup(cleanup)
	return tr
}

func TestMCP_UpdateTenant_DescriptionAndMetadataRoundTrip(t *testing.T) {
	tr := connectDescriptiveTenants(t)
	ctx := context.Background()
	desc := "Acme production"
	want := map[string]string{"env": "prod", "team": "storage"}

	if _, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "acme", Description: &desc, Metadata: want}); err != nil {
		t.Fatal(err)
	}
	got, err := tr.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tenant.Description != desc || !maps.Equal(got.Tenant.Metadata, want) {
		t.Fatalf("get = %q, %v; want %q, %v", got.Tenant.Description, got.Tenant.Metadata, desc, want)
	}

	// Nil fields are not sent, so the stored values stay.
	if _, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "acme", Name: "Acme Inc"}); err != nil {
		t.Fatal(err)
	}
	got, err = tr.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tenant.Description != desc || !maps.Equal(got.Tenant.Metadata, want) {
		t.Fatalf("rename changed descriptive fields: %q, %v", got.Tenant.Description, got.Tenant.Metadata)
	}
}

func TestMCP_UpdateTenant_EmptyMetadataClears(t *testing.T) {
	tr := connectDescriptiveTenants(t)
	ctx := context.Background()
	desc := "to clear"
	if _, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{
		TenantID: "acme", Description: &desc, Metadata: map[string]string{"env": "prod"},
	}); err != nil {
		t.Fatal(err)
	}

	empty := ""
	resp, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "acme", Description: &empty, Metadata: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Tenant.Description != "" || len(resp.Tenant.Metadata) != 0 {
		t.Fatalf("cleared tenant = %q, %v; want both empty", resp.Tenant.Description, resp.Tenant.Metadata)
	}
}

func TestMCP_UpdateTenant_OverLimitIsInvalidArgument(t *testing.T) {
	tr := connectDescriptiveTenants(t)
	long := strings.Repeat("d", tenant.MaxDescriptionBytes+1)
	_, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{TenantID: "acme", Description: &long})
	// UpdateTenant's RequestError wraps the tool call's, which carries the code.
	var outer, inner *transport.RequestError
	if !errors.As(err, &outer) || !errors.As(outer.Err, &inner) || inner.StatusCode != int(wire.CodeInvalidArgument) {
		t.Fatalf("over-limit description = %v; want a RequestError carrying InvalidArgument", err)
	}
}
