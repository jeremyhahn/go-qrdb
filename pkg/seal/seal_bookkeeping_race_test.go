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

package seal

import (
	"context"
	"errors"
	"testing"
	"time"
)

// concurrentWindow is how long a hook waits for the operation it starts
// concurrently. Under correct locking that operation is blocked on sealMu
// until the hook's caller returns, so the wait expires; under the defect it
// completes inside the window, which is the interleaving under test.
const concurrentWindow = 100 * time.Millisecond

// startInWindow runs op on its own goroutine and waits for it to finish or
// for concurrentWindow to elapse, whichever is first. The returned channel
// closes when op has returned.
func startInWindow(op func()) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		op()
	}()
	select {
	case <-done:
	case <-time.After(concurrentWindow):
	}
	return done
}

// setHook installs fn into *hook for the duration of the test.
func setHook(t *testing.T, hook *func(string), fn func(string)) {
	t.Helper()
	prev := *hook
	*hook = fn
	t.Cleanup(func() { *hook = prev })
}

// TestSealTenant_ConcurrentNodeSealDoesNotUndoIt lands a whole-node seal
// between SealTenant's bookkeeping and its seal. The explicit seal must
// survive the node unseal: the node seal did not move the tenant, its admin
// did.
func TestSealTenant_ConcurrentNodeSealDoesNotUndoIt(t *testing.T) {
	ctx := context.Background()
	reg := registryWithBothTenantKinds(t)

	var nodeSeal <-chan struct{}
	var sealAllErr error
	setHook(t, &sealTenantHook, func(tenantID string) {
		if tenantID != "auto" || nodeSeal != nil {
			return
		}
		nodeSeal = startInWindow(func() { sealAllErr = reg.SealAll() })
	})

	if err := reg.SealTenant("auto"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if nodeSeal == nil {
		t.Fatal("precondition: the hook never ran")
	}
	<-nodeSeal
	if sealAllErr != nil {
		t.Fatalf("SealAll: %v", sealAllErr)
	}
	if err := reg.SystemBarrier().Unseal(ctx, Credentials{}); err != nil {
		t.Fatalf("system Unseal: %v", err)
	}
	if !tenantSealed(t, reg, "auto") {
		t.Fatal("SealTenant reported success, then the node unseal unsealed the tenant")
	}
	if pending := reg.PendingTenantUnseals(); len(pending) != 0 {
		t.Fatalf("PendingTenantUnseals = %v, want none", pending)
	}
}

// TestSealTenant_HoldsSealMuAcrossTheSeal pins the lock discipline the test
// above depends on: SealTenant's bookkeeping and seal are one step under
// sealMu.
func TestSealTenant_HoldsSealMuAcrossTheSeal(t *testing.T) {
	reg := registryWithBothTenantKinds(t)
	held := false
	setHook(t, &sealTenantHook, func(string) {
		if reg.sealMu.TryLock() {
			reg.sealMu.Unlock()
			return
		}
		held = true
	})
	if err := reg.SealTenant("self"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if !held {
		t.Fatal("SealTenant released sealMu between its bookkeeping and the seal")
	}
}

// TestSealAll_ConcurrentTenantUnsealIsConsistent lands a tenant admin's
// unseal of a sealed system-managed tenant between SealAll's check of that
// tenant and its seal. Whatever the unseal reports must hold after the node
// unseal: an unseal that succeeded happened before the node seal, so the
// node unseal brings the tenant back; one that failed leaves it sealed.
func TestSealAll_ConcurrentTenantUnsealIsConsistent(t *testing.T) {
	ctx := context.Background()
	reg := registryWithBothTenantKinds(t)
	if err := reg.SealTenant("auto"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}

	var unseal <-chan struct{}
	var unsealErr error
	setHook(t, &sealAllTenantHook, func(tenantID string) {
		if tenantID != "auto" || unseal != nil {
			return
		}
		unseal = startInWindow(func() { unsealErr = reg.UnsealTenant(ctx, "auto", Credentials{}) })
	})

	if err := reg.SealAll(); err != nil {
		t.Fatalf("SealAll: %v", err)
	}
	if unseal == nil {
		t.Fatal("precondition: the hook never ran")
	}
	<-unseal
	if err := reg.SystemBarrier().Unseal(ctx, Credentials{}); err != nil {
		t.Fatalf("system Unseal: %v", err)
	}
	sealed := tenantSealed(t, reg, "auto")
	switch {
	case unsealErr == nil && sealed:
		t.Fatal("UnsealTenant reported success, then the node seal sealed the tenant and the node unseal left it sealed")
	case unsealErr != nil && !sealed:
		t.Fatalf("UnsealTenant failed (%v), yet the node unseal unsealed the tenant", unsealErr)
	}
}

// TestUnsealTenant_HoldsSealMu pins the lock discipline the test above
// depends on: a tenant unseal through the registry cannot interleave with a
// node seal's per-tenant check and seal.
func TestUnsealTenant_HoldsSealMu(t *testing.T) {
	ctx := context.Background()
	reg := registryWithBothTenantKinds(t)
	if err := reg.SealTenant("auto"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	reg.sealMu.Lock()
	done := startInWindow(func() { _ = reg.UnsealTenant(ctx, "auto", Credentials{}) })
	select {
	case <-done:
		reg.sealMu.Unlock()
		t.Fatal("UnsealTenant completed while sealMu was held")
	default:
	}
	reg.sealMu.Unlock()
	<-done
	if tenantSealed(t, reg, "auto") {
		t.Fatal("UnsealTenant did not unseal once sealMu was released")
	}
}

// TestUnsealTenant_ClearsItsPendingNodeUnseal: a tenant whose re-unseal by
// the node unseal failed is pending (PendingTenantUnseals) until its own
// unseal recovers it, and not after.
func TestUnsealTenant_ClearsItsPendingNodeUnseal(t *testing.T) {
	ctx := context.Background()
	reg := registryWithBothTenantKinds(t)
	if err := reg.SealTenant("auto"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	reg.sealMu.Lock()
	reg.nodeSealed["auto"] = ErrSealed
	reg.sealMu.Unlock()

	if err := reg.UnsealTenant(ctx, "self", Credentials{}); !errors.Is(err, ErrAlreadyUnsealed) {
		t.Fatalf("UnsealTenant(self) = %v, want ErrAlreadyUnsealed", err)
	}
	if _, ok := reg.PendingTenantUnseals()["auto"]; !ok {
		t.Fatal("another tenant's unseal cleared this tenant's pending entry")
	}
	if err := reg.UnsealTenant(ctx, "auto", Credentials{}); err != nil {
		t.Fatalf("UnsealTenant(auto): %v", err)
	}
	if pending := reg.PendingTenantUnseals(); len(pending) != 0 {
		t.Fatalf("PendingTenantUnseals = %v after the tenant's own unseal, want none", pending)
	}
}
