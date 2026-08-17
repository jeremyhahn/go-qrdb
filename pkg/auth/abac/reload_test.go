// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package abac

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// denyPolicy denies the given action on the given resource for every
// subject.
func denyPolicy(name, tenantID, action, resource string) *Policy {
	return testPolicy(name, tenantID, Rule{
		Name:      "deny-rule",
		Effect:    EffectDeny,
		Actions:   []string{action},
		Resources: []string{resource},
	})
}

// allowPolicy allows the given action on the given resource for every
// subject.
func allowPolicy(name, tenantID, action, resource string) *Policy {
	return testPolicy(name, tenantID, Rule{
		Name:      "allow-rule",
		Effect:    EffectAllow,
		Actions:   []string{action},
		Resources: []string{resource},
	})
}

// TestDecorator_Reload_SwapsDecisions proves Reload atomically replaces the
// active policy set: a request denied under set A is allowed after
// reloading set B, and a request allowed under set B is denied again after
// reloading back to set A.
func TestDecorator_Reload_SwapsDecisions(t *testing.T) {
	inner := &fakeAuthorizer{} // inner would ALLOW everything not matched.
	setA := []*Policy{denyPolicy("set-a", "tenant-a", "delete", "/kv/**")}
	authz, err := Decorate(inner, WithPolicies(setA))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}
	d, ok := authz.(*Decorator)
	if !ok {
		t.Fatalf("authz = %T, want *Decorator", authz)
	}

	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	id := testIdentity("alice", "tenant-a")

	// Under set A, delete is explicitly denied.
	err = d.Authorize(ctx, id, "/kv/orders/1", "delete")
	var denied *PolicyDeniedError
	if !errors.As(err, &denied) || denied.Policy != "set-a" {
		t.Fatalf("under set A, delete should be denied by set-a, got %v", err)
	}

	// Reload to set B: delete is now explicitly allowed.
	setB := []*Policy{allowPolicy("set-b", "tenant-a", "delete", "/kv/**")}
	if err := d.Reload(setB); err != nil {
		t.Fatalf("Reload(setB) error = %v", err)
	}
	if err := d.Authorize(ctx, id, "/kv/orders/1", "delete"); err != nil {
		t.Errorf("under set B, delete should be allowed, got %v", err)
	}

	// Reload back to set A: delete is denied again.
	if err := d.Reload(setA); err != nil {
		t.Fatalf("Reload(setA) error = %v", err)
	}
	err = d.Authorize(ctx, id, "/kv/orders/1", "delete")
	if !errors.As(err, &denied) || denied.Policy != "set-a" {
		t.Fatalf("after reloading back to set A, delete should be denied by set-a, got %v", err)
	}
}

// TestDecorator_Reload_FailureLeavesOldSetInForce proves a Reload with an
// invalid policy returns a typed error and never swaps: the previously
// active policy set keeps deciding every subsequent Authorize call.
func TestDecorator_Reload_FailureLeavesOldSetInForce(t *testing.T) {
	inner := &fakeAuthorizer{}
	setA := []*Policy{denyPolicy("set-a", "tenant-a", "delete", "/kv/**")}
	authz, err := Decorate(inner, WithPolicies(setA))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}
	d := authz.(*Decorator)

	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	id := testIdentity("alice", "tenant-a")

	bad := testPolicy("bad", "tenant-a") // no rules: fails validation.
	err = d.Reload([]*Policy{bad})
	if err == nil {
		t.Fatal("Reload with an invalid policy should fail")
	}
	var compErr *CompilationFailedError
	if !errors.As(err, &compErr) {
		t.Errorf("error = %T, want *CompilationFailedError", err)
	}

	// set-a must still be deciding: the failed reload never took effect.
	err = d.Authorize(ctx, id, "/kv/orders/1", "delete")
	var denied *PolicyDeniedError
	if !errors.As(err, &denied) || denied.Policy != "set-a" {
		t.Fatalf("after a failed reload, set-a should still deny delete, got %v", err)
	}
}

// TestDecorator_Reload_Concurrent runs Authorize in a hot loop across
// several goroutines while another goroutine repeatedly calls Reload,
// alternating between two policy sets. Every Authorize result must be one
// of the two well-defined decisions (never a panic, never a third outcome),
// proving the atomic swap is safe under concurrent read/write access. Run
// with -race (see the abac Makefile target) to catch any data race.
func TestDecorator_Reload_Concurrent(t *testing.T) {
	inner := &fakeAuthorizer{}
	setA := []*Policy{denyPolicy("set-a", "tenant-a", "delete", "/kv/**")}
	authz, err := Decorate(inner, WithPolicies(setA))
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}
	d := authz.(*Decorator)
	setB := []*Policy{allowPolicy("set-b", "tenant-a", "delete", "/kv/**")}

	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	id := testIdentity("alice", "tenant-a")

	const readers = 8
	const iterations = 500
	var wg sync.WaitGroup
	var stop atomic.Bool
	var unexpected atomic.Int64

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				err := d.Authorize(ctx, id, "/kv/orders/1", "delete")
				switch {
				case err == nil:
					// Allowed by set-b: a well-defined outcome.
				case errors.As(err, new(*PolicyDeniedError)):
					// Denied by set-a: a well-defined outcome.
				default:
					unexpected.Add(1)
				}
			}
		}()
	}

	for i := 0; i < iterations; i++ {
		set := setA
		if i%2 == 1 {
			set = setB
		}
		if err := d.Reload(set); err != nil {
			t.Fatalf("Reload() error = %v", err)
		}
	}
	stop.Store(true)
	wg.Wait()

	if n := unexpected.Load(); n != 0 {
		t.Errorf("%d Authorize calls returned an unexpected error type", n)
	}
}

// TestDecorate_WithLiveReload_ZeroPolicies proves WithLiveReload keeps a
// reloadable *Decorator wrapper even when Decorate is called with zero
// policies, so an embedder that starts with no policies can still add its
// first one later via Reload.
func TestDecorate_WithLiveReload_ZeroPolicies(t *testing.T) {
	inner := &fakeAuthorizer{}
	authz, err := Decorate(inner, WithLiveReload())
	if err != nil {
		t.Fatalf("Decorate() error = %v", err)
	}
	d, ok := authz.(*Decorator)
	if !ok {
		t.Fatalf("authz = %T, want *Decorator (WithLiveReload must force wrapping)", authz)
	}

	ctx := tenant.WithTenantID(context.Background(), "tenant-a")
	id := testIdentity("alice", "tenant-a")

	// No policies yet: delegates to inner (allow).
	if err := d.Authorize(ctx, id, "/kv/orders/1", "delete"); err != nil {
		t.Errorf("with zero policies, Authorize should delegate to inner, got %v", err)
	}

	// Reload adds the first policy: delete is now denied.
	if err := d.Reload([]*Policy{denyPolicy("set-a", "tenant-a", "delete", "/kv/**")}); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	var denied *PolicyDeniedError
	err = d.Authorize(ctx, id, "/kv/orders/1", "delete")
	if !errors.As(err, &denied) || denied.Policy != "set-a" {
		t.Fatalf("after reloading the first policy, delete should be denied, got %v", err)
	}
}
