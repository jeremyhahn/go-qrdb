// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package cert

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
)

func testDelegationPolicy(tenantID, principal string) delegation.Policy {
	return delegation.Policy{Tenant: tenantID, Principal: principal, Scope: []string{"kv:get"}, TTL: 20 * time.Minute}
}

func TestDelegationPolicy_RoundTrip(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	if _, found, err := p.DelegationPolicy(ctx, "acme", "billing"); err != nil || found {
		t.Fatalf("DelegationPolicy before a write: found %v, err %v", found, err)
	}
	want := testDelegationPolicy("acme", "billing")
	if err := p.PutDelegationPolicy(ctx, want); err != nil {
		t.Fatalf("PutDelegationPolicy: %v", err)
	}
	if err := p.PutDelegationPolicy(ctx, testDelegationPolicy("acme", "reports")); err != nil {
		t.Fatalf("PutDelegationPolicy reports: %v", err)
	}
	if err := p.PutDelegationPolicy(ctx, testDelegationPolicy("globex", "billing")); err != nil {
		t.Fatalf("PutDelegationPolicy globex: %v", err)
	}
	got, found, err := p.DelegationPolicy(ctx, "acme", "billing")
	if err != nil || !found || got.Tenant != want.Tenant || got.Principal != want.Principal || !slices.Equal(got.Scope, want.Scope) || got.TTL != want.TTL {
		t.Fatalf("DelegationPolicy = %+v, %v, %v; want %+v", got, found, err, want)
	}

	// Replacing it narrows the scope in place.
	want.Scope = []string{"kv:get", "kv:put"}
	if err := p.PutDelegationPolicy(ctx, want); err != nil {
		t.Fatalf("PutDelegationPolicy replace: %v", err)
	}
	list, err := p.ListDelegationPolicies(ctx, "acme")
	if err != nil || len(list) != 2 {
		t.Fatalf("ListDelegationPolicies = %+v, %v; want acme's two", list, err)
	}

	deleted, err := p.DeleteDelegationPolicy(ctx, "acme", "billing")
	if err != nil || !deleted {
		t.Fatalf("DeleteDelegationPolicy = %v, %v", deleted, err)
	}
	if _, found, err := p.DelegationPolicy(ctx, "acme", "billing"); err != nil || found {
		t.Fatalf("a deleted policy still stands: found %v, err %v", found, err)
	}
	if deleted, err := p.DeleteDelegationPolicy(ctx, "acme", "billing"); err != nil || deleted {
		t.Fatalf("deleting it again = %v, %v; want not found", deleted, err)
	}
	if _, found, _ := p.DelegationPolicy(ctx, "globex", "billing"); !found {
		t.Fatal("deleting acme's policy deleted globex's")
	}
	if list, err := p.ListDelegationPolicies(ctx, "initech"); err != nil || len(list) != 0 {
		t.Fatalf("ListDelegationPolicies of a tenant with none = %+v, %v", list, err)
	}
}

func TestDelegationPolicy_RefusesBadInput(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()
	var v *ValidationError

	bad := testDelegationPolicy("acme", "billing")
	bad.TTL = delegation.MaxLifetime + time.Second
	if err := p.PutDelegationPolicy(ctx, bad); !errors.As(err, &v) {
		t.Fatalf("Put past the maximum lifetime: err = %v, want *ValidationError", err)
	}
	if err := p.PutDelegationPolicy(ctx, testDelegationPolicy("acme", "")); !errors.As(err, &v) {
		t.Fatalf("Put with no principal: err = %v, want *ValidationError", err)
	}
	if _, _, err := p.DelegationPolicy(ctx, "", "billing"); !errors.As(err, &v) {
		t.Fatalf("Get with no tenant: err = %v, want *ValidationError", err)
	}
	if _, _, err := p.DelegationPolicy(ctx, "acme", ""); !errors.As(err, &v) {
		t.Fatalf("Get with no principal: err = %v, want *ValidationError", err)
	}
	if _, err := p.ListDelegationPolicies(ctx, ""); !errors.As(err, &v) {
		t.Fatalf("List with no tenant: err = %v, want *ValidationError", err)
	}
	if _, err := p.DeleteDelegationPolicy(ctx, "", "billing"); !errors.As(err, &v) {
		t.Fatalf("Delete with no tenant: err = %v, want *ValidationError", err)
	}
}
