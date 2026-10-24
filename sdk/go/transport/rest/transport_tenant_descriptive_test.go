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

package rest

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// descriptiveRoundTrip connects to a real REST server over the REAL
// seal-disabled tenant service holding one tenant, "acme".
func descriptiveRoundTrip(t *testing.T) *Transport {
	t.Helper()
	svc := realKVTenants(t)
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatal(err)
	}
	return newTenantRoundTrip(t, svc)
}

func TestREST_UpdateTenant_DescriptionAndMetadataRoundTrip(t *testing.T) {
	tr := descriptiveRoundTrip(t)
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

	// Nil fields are omitted from the body, so the stored values stay.
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

func TestREST_UpdateTenant_EmptyMetadataClears(t *testing.T) {
	tr := descriptiveRoundTrip(t)
	ctx := context.Background()
	desc := "to clear"
	if _, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{
		TenantID: "acme", Description: &desc, Metadata: map[string]string{"env": "prod"},
	}); err != nil {
		t.Fatal(err)
	}

	// The empty map survives encoding (omitzero), so it is a clear.
	empty := ""
	resp, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "acme", Description: &empty, Metadata: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Tenant.Description != "" || len(resp.Tenant.Metadata) != 0 {
		t.Fatalf("cleared tenant = %q, %v; want both empty", resp.Tenant.Description, resp.Tenant.Metadata)
	}
}

func TestREST_UpdateTenant_OverLimitIsBadRequest(t *testing.T) {
	tr := descriptiveRoundTrip(t)
	_, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{
		TenantID: "acme",
		Metadata: map[string]string{"k": strings.Repeat("v", tenant.MaxMetadataValueBytes+1)},
	})
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != http.StatusBadRequest {
		t.Fatalf("over-limit metadata = %v; want a RequestError carrying 400", err)
	}
}
