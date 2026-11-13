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

package embedded

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// descriptiveEmbeddedTransport returns a transport over the REAL
// seal-disabled tenant service holding one tenant, "acme".
func descriptiveEmbeddedTransport(t *testing.T) *Transport {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatal(err)
	}
	return newExtTransport(t, WithTenantService(svc))
}

func TestEmbedded_UpdateTenant_DescriptionAndMetadataRoundTrip(t *testing.T) {
	tr := descriptiveEmbeddedTransport(t)
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

	// The returned map is the caller's copy: editing it does not reach the store.
	got.Tenant.Metadata["env"] = "dev"
	again, err := tr.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(again.Tenant.Metadata, want) {
		t.Fatalf("editing the returned map changed the store: %v", again.Tenant.Metadata)
	}

	// Nil fields leave the stored values unchanged.
	if _, err := tr.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "acme", Name: "Acme Inc"}); err != nil {
		t.Fatal(err)
	}
	again, err = tr.GetTenant(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if again.Tenant.Description != desc || !maps.Equal(again.Tenant.Metadata, want) {
		t.Fatalf("rename changed descriptive fields: %q, %v", again.Tenant.Description, again.Tenant.Metadata)
	}
}

// TestEmbedded_MapTenantInfo_CarriesIsolationPolicy proves the embedded
// transport reports the tenant's isolation policy, and leaves it empty for
// a tenant that inherits the cluster floor. The set itself is gated on
// strict-isolation; cmd/qrdb's TestTenantGetCmd_PrintsIsolationPolicy
// drives UpdateTenant then GetTenant through this transport with the gate
// open.
func TestEmbedded_MapTenantInfo_CarriesIsolationPolicy(t *testing.T) {
	if got := mapTenantInfo(&tenant.TenantInfo{ID: "acme", IsolationPolicy: tenant.TenantIsolationPolicyStrict}).IsolationPolicy; got != "strict" {
		t.Fatalf("IsolationPolicy = %q; want strict", got)
	}
	if got := mapTenantInfo(&tenant.TenantInfo{ID: "acme"}).IsolationPolicy; got != "" {
		t.Fatalf("IsolationPolicy = %q; want empty", got)
	}
}

func TestEmbedded_UpdateTenant_OverLimitIsInvalidArgument(t *testing.T) {
	tr := descriptiveEmbeddedTransport(t)
	_, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{
		TenantID: "acme",
		Metadata: map[string]string{"k": strings.Repeat("v", tenant.MaxMetadataValueBytes+1)},
	})
	var fe *tenant.TenantFieldError
	if !errors.As(err, &fe) || errclass.Classify(err) != errclass.InvalidArgument {
		t.Fatalf("over-limit metadata = %v; want an InvalidArgument *tenant.TenantFieldError", err)
	}
}
