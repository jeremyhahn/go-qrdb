// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package cert

import (
	"context"
	"errors"
	"slices"
	"strings"
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

// policyFaultKVStore fails one operation, and only on a delegation policy
// key, so the tenant routing beneath it still resolves.
type policyFaultKVStore struct {
	*mockKVStore
	op string
}

var errPolicyFault = errors.New("policy store fault")

func (s *policyFaultKVStore) fails(op, key string) bool {
	return s.op == op && strings.HasPrefix(key, prefixDelegationPolicy)
}

func (s *policyFaultKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if s.fails("get", key) {
		return nil, errPolicyFault
	}
	return s.mockKVStore.Get(ctx, key)
}

func (s *policyFaultKVStore) Put(ctx context.Context, key string, value []byte) error {
	if s.fails("put", key) {
		return errPolicyFault
	}
	return s.mockKVStore.Put(ctx, key, value)
}

func (s *policyFaultKVStore) Delete(ctx context.Context, key string) error {
	if s.fails("delete", key) {
		return errPolicyFault
	}
	return s.mockKVStore.Delete(ctx, key)
}

func (s *policyFaultKVStore) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	if s.fails("scan", prefix) {
		return errPolicyFault
	}
	return s.mockKVStore.Scan(ctx, prefix, fn)
}

// delegationPolicyCalls runs every DelegationPolicyStore method for
// acme/billing and returns each one's error.
func delegationPolicyCalls(ctx context.Context, p *DefaultCertProvider) map[string]error {
	_, _, getErr := p.DelegationPolicy(ctx, "acme", "billing")
	_, listErr := p.ListDelegationPolicies(ctx, "acme")
	_, deleteErr := p.DeleteDelegationPolicy(ctx, "acme", "billing")
	return map[string]error{
		"put":    p.PutDelegationPolicy(ctx, testDelegationPolicy("acme", "billing")),
		"get":    getErr,
		"list":   listErr,
		"delete": deleteErr,
	}
}

func TestDelegationPolicy_RefusesWhenSealed(t *testing.T) {
	p, _, barrier := initializeProvider(t)
	barrier.seal()
	for name, err := range delegationPolicyCalls(context.Background(), p) {
		var sealed *BarrierSealedError
		if !errors.As(err, &sealed) {
			t.Errorf("%s while sealed: err = %v, want *BarrierSealedError", name, err)
		}
	}
}

func TestDelegationPolicy_UnavailableHomeRefuses(t *testing.T) {
	p, _, _, r := newHomeRoutedProvider(t)
	r.sealHome()
	ctx := context.Background()
	if err := p.PutDelegationPolicy(ctx, testDelegationPolicy(homedTenant, "billing")); err == nil {
		t.Error("Put into an unavailable home store succeeded")
	}
	if _, found, err := p.DelegationPolicy(ctx, homedTenant, "billing"); err == nil || found {
		t.Errorf("Get from an unavailable home store = found %v, err %v; want the home-store error", found, err)
	}
	if list, err := p.ListDelegationPolicies(ctx, homedTenant); err == nil || list != nil {
		t.Errorf("List from an unavailable home store = %v, %v; want the home-store error", list, err)
	}
	if deleted, err := p.DeleteDelegationPolicy(ctx, homedTenant, "billing"); err == nil || deleted {
		t.Errorf("Delete from an unavailable home store = %v, %v; want the home-store error", deleted, err)
	}
}

func TestDelegationPolicy_StoreFailures(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		op   string
		call func(p *DefaultCertProvider) error
	}{
		{"put", func(p *DefaultCertProvider) error {
			return p.PutDelegationPolicy(ctx, testDelegationPolicy("acme", "billing"))
		}},
		{"get", func(p *DefaultCertProvider) error {
			_, _, err := p.DelegationPolicy(ctx, "acme", "billing")
			return err
		}},
		{"scan", func(p *DefaultCertProvider) error {
			_, err := p.ListDelegationPolicies(ctx, "acme")
			return err
		}},
		{"delete", func(p *DefaultCertProvider) error {
			_, err := p.DeleteDelegationPolicy(ctx, "acme", "billing")
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.op, func(t *testing.T) {
			p, kv, _ := initializeProvider(t)
			if err := p.PutDelegationPolicy(ctx, testDelegationPolicy("acme", "billing")); err != nil {
				t.Fatalf("PutDelegationPolicy: %v", err)
			}
			p.kvStore = &policyFaultKVStore{mockKVStore: kv, op: tt.op}
			err := tt.call(p)
			var se *StoreError
			if !errors.As(err, &se) || se.Op != tt.op || !errors.Is(err, errPolicyFault) {
				t.Fatalf("%s failure: err = %v, want *StoreError{Op: %q} wrapping the fault", tt.op, err, tt.op)
			}
		})
	}
}

func TestDelegationPolicy_CorruptRecord(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()
	if err := kv.Put(ctx, delegationPolicyKey("acme", "billing"), []byte("{not json")); err != nil {
		t.Fatalf("seed corrupt record: %v", err)
	}
	var se *StoreError
	if _, found, err := p.DelegationPolicy(ctx, "acme", "billing"); !errors.As(err, &se) || se.Op != "unmarshal" || found {
		t.Fatalf("DelegationPolicy of a corrupt record = found %v, err %v; want an unmarshal *StoreError", found, err)
	}
	if _, err := p.ListDelegationPolicies(ctx, "acme"); !errors.As(err, &se) || se.Op != "scan" {
		t.Fatalf("ListDelegationPolicies over a corrupt record: err = %v, want a scan *StoreError", err)
	}
	if deleted, err := p.DeleteDelegationPolicy(ctx, "acme", "billing"); !errors.As(err, &se) || deleted {
		t.Fatalf("DeleteDelegationPolicy of a corrupt record = %v, %v; want the read's *StoreError", deleted, err)
	}
}
