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

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// newMemoryIdentityIndex is the real index over an in-process store.
func newMemoryIdentityIndex(t *testing.T) IdentityIndex {
	t.Helper()
	index, err := NewIdentityIndex(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("NewIdentityIndex: %v", err)
	}
	return index
}

// TestReconcileDeletedIdentity_ReleasesOwnClaim proves a deleted record's
// claim is released by its owner, and only once.
func TestReconcileDeletedIdentity_ReleasesOwnClaim(t *testing.T) {
	ctx := context.Background()
	index := newMemoryIdentityIndex(t)
	if _, err := index.Record(ctx, IdentityUser, "7", "acme"); err != nil {
		t.Fatal(err)
	}
	released, err := ReconcileDeletedIdentity(ctx, index, IdentityUser, "7", "acme")
	if err != nil || !released {
		t.Fatalf("ReconcileDeletedIdentity = %t, %v; want the claim released", released, err)
	}
	if _, held, err := index.Lookup(ctx, IdentityUser, "7"); err != nil || held {
		t.Fatalf("Lookup after release = held %t, %v; want no claim", held, err)
	}
	released, err = ReconcileDeletedIdentity(ctx, index, IdentityUser, "7", "acme")
	if err != nil || released {
		t.Fatalf("second ReconcileDeletedIdentity = %t, %v; want nothing left to release", released, err)
	}
}

// TestReconcileDeletedIdentity_DeclinesOthers proves another tenant's claim,
// an empty id and a missing index each release nothing and report no error.
func TestReconcileDeletedIdentity_DeclinesOthers(t *testing.T) {
	ctx := context.Background()
	index := newMemoryIdentityIndex(t)
	if _, err := index.Record(ctx, IdentityRole, "9", "globex"); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() (bool, error){
		"other owner": func() (bool, error) { return ReconcileDeletedIdentity(ctx, index, IdentityRole, "9", "acme") },
		"empty id":    func() (bool, error) { return ReconcileDeletedIdentity(ctx, index, IdentityRole, "", "globex") },
		"no index":    func() (bool, error) { return ReconcileDeletedIdentity(ctx, nil, IdentityRole, "9", "globex") },
	} {
		if released, err := call(); err != nil || released {
			t.Errorf("%s: = %t, %v; want nothing released", name, released, err)
		}
	}
	if _, held, _ := index.Lookup(ctx, IdentityRole, "9"); !held {
		t.Fatal("globex's claim was released by a call that declined")
	}
}

// TestOwningTenant_RoutesToClaim proves a record claimed by the caller's own
// tenant routes there, and an unclaimed one to the caller's tenant.
func TestOwningTenant_RoutesToClaim(t *testing.T) {
	ctx := WithTenantID(context.Background(), "acme")
	index := newMemoryIdentityIndex(t)
	if _, err := index.Record(ctx, IdentityUser, "7", "acme"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"7": "acme", "8": "acme", "": "acme"} {
		if got, err := OwningTenant(ctx, index, IdentityUser, id, "get user"); err != nil || got != want {
			t.Errorf("OwningTenant(%q) = %q, %v; want %q", id, got, err, want)
		}
	}
	if got, err := OwningTenant(ctx, nil, IdentityUser, "7", "get user"); err != nil || got != "acme" {
		t.Errorf("OwningTenant with no index = %q, %v; want the caller's tenant", got, err)
	}
}

// TestOwningTenant_OtherTenantsClaimStaysHome proves a record another tenant
// claims does not route the caller there: the caller's own store answers,
// and finds nothing, rather than a cross-tenant refusal naming the owner.
func TestOwningTenant_OtherTenantsClaimStaysHome(t *testing.T) {
	ctx := WithTenantID(context.Background(), "acme")
	index := newMemoryIdentityIndex(t)
	if _, err := index.Record(ctx, IdentityUser, "7", "globex"); err != nil {
		t.Fatal(err)
	}
	if got, err := OwningTenant(ctx, index, IdentityUser, "7", "get user"); err != nil || got != "acme" {
		t.Fatalf("OwningTenant = %q, %v; want acme, not globex", got, err)
	}
}
