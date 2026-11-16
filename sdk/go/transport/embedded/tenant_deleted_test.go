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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// retiredEmbeddedTenants returns the REAL seal-disabled tenant service with
// the named tenants created and deleted through it.
func retiredEmbeddedTenants(t *testing.T, ids ...string) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range ids {
		if _, err := svc.CreateTenant(ctx, id, "Tenant "+id, tenant.IsolationPrefix); err != nil {
			t.Fatal(err)
		}
		if err := svc.DeleteTenant(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

func TestEmbedded_ListDeletedTenants_PagesThroughRetiredIds(t *testing.T) {
	tr := newExtTransport(t, WithTenantService(retiredEmbeddedTenants(t, "bravo", "alpha", "charlie")))
	ctx := context.Background()

	first, err := tr.ListDeletedTenants(ctx, &transport.ListDeletedTenantsRequest{PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Tenants) != 2 || first.Tenants[0].ID != "alpha" || first.Tenants[0].DeletedAt.IsZero() || first.NextPageToken == "" {
		t.Fatalf("first page = %+v", first)
	}
	second, err := tr.ListDeletedTenants(ctx, &transport.ListDeletedTenantsRequest{PageSize: 2, PageToken: first.NextPageToken})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Tenants) != 1 || second.Tenants[0].ID != "charlie" {
		t.Fatalf("second page = %+v", second)
	}
	// A nil request is the first page at the default size.
	all, err := tr.ListDeletedTenants(ctx, nil)
	if err != nil || len(all.Tenants) != 3 {
		t.Fatalf("nil request = %+v, %v", all, err)
	}
}

func TestEmbedded_DeletedTenantVerbs_NilServiceAndClosed(t *testing.T) {
	tr := newExtTransport(t)
	var nse *NilServiceError
	if _, err := tr.ListDeletedTenants(context.Background(), nil); !errors.As(err, &nse) {
		t.Fatalf("list with no tenant service = %v", err)
	}
	if _, err := tr.ReconcileDeletedTenants(context.Background()); !errors.As(err, &nse) {
		t.Fatalf("reconcile with no tenant service = %v", err)
	}

	tr = newExtTransport(t, WithTenantService(retiredEmbeddedTenants(t)))
	if _, err := tr.ListDeletedTenants(context.Background(), &transport.ListDeletedTenantsRequest{PageSize: -1}); err == nil {
		t.Fatal("a negative page size was accepted")
	}
	_ = tr.Close()
	var closed *transport.ClosedError
	if _, err := tr.ListDeletedTenants(context.Background(), nil); !errors.As(err, &closed) {
		t.Fatalf("list after close = %v", err)
	}
	if _, err := tr.ReconcileDeletedTenants(context.Background()); !errors.As(err, &closed) {
		t.Fatalf("reconcile after close = %v", err)
	}
}
