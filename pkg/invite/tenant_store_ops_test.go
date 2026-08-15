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

// Per-tenant routing tests for the operations that reach a tenant's own store,
// plus the typed errors this package reports.

package invite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestTenantStoreRedeemConsumesWithinTheTenant covers the routed Redeem. A
// token is redeemable exactly once, and only in the tenant that issued it --
// the routing is what makes the second property hold.
func TestTenantStoreRedeemConsumesWithinTheTenant(t *testing.T) {
	ctx := context.Background()
	store, err := NewTenantStore(newStubResolver())
	if err != nil {
		t.Fatalf("NewTenantStore: %v", err)
	}

	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	entity, err := store.Redeem(ctx, "tenant-a", token)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if entity.UserID != "user-1" {
		t.Errorf("UserID = %q, want %q", entity.UserID, "user-1")
	}

	// Exactly once: the second redemption must fail.
	if _, err := store.Redeem(ctx, "tenant-a", token); err == nil {
		t.Fatal("a token must not be redeemable twice")
	}
}

// TestTenantStoreRedeemDoesNotCrossTenants pins the isolation property on the
// redeem path specifically. A token leaking across tenants would let an
// invitation issued by one tenant enroll a user into another.
func TestTenantStoreRedeemDoesNotCrossTenants(t *testing.T) {
	ctx := context.Background()
	store, err := NewTenantStore(newStubResolver())
	if err != nil {
		t.Fatalf("NewTenantStore: %v", err)
	}

	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := store.Redeem(ctx, "tenant-b", token); err == nil {
		t.Fatal("a token issued for tenant-a must not redeem in tenant-b")
	}

	// And it must still be redeemable where it belongs.
	if _, err := store.Redeem(ctx, "tenant-a", token); err != nil {
		t.Fatalf("the cross-tenant attempt consumed the token: %v", err)
	}
}

// TestTenantStoreGCSweepsOneTenant covers the routed GC. It is per-tenant by
// design -- no store holds every tenant's invitations -- so the count returned
// must reflect only the named tenant.
func TestTenantStoreGCSweepsOneTenant(t *testing.T) {
	ctx := context.Background()
	store, err := NewTenantStore(newStubResolver())
	if err != nil {
		t.Fatalf("NewTenantStore: %v", err)
	}

	if _, _, err := store.Create(ctx, "user-a", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Create in tenant-a: %v", err)
	}
	if _, _, err := store.Create(ctx, "user-b", "tenant-b", time.Hour); err != nil {
		t.Fatalf("Create in tenant-b: %v", err)
	}

	// Sweep well past both expiries; only tenant-a's invitation is in scope.
	removed, err := store.GC(ctx, "tenant-a", time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if removed != 1 {
		t.Fatalf("GC removed %d invitations from tenant-a, want 1", removed)
	}

	// tenant-b's invitation must be untouched.
	removedB, err := store.GC(ctx, "tenant-b", time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("GC tenant-b: %v", err)
	}
	if removedB != 1 {
		t.Fatalf("tenant-b lost %d invitations to tenant-a's sweep", 1-removedB)
	}
}

// TestTenantStoreGCFailsClosedOnASealedTenant covers the resolver-error branch.
// Reporting zero removals for a sealed tenant would look like a clean sweep and
// let a caller conclude there was nothing to collect.
func TestTenantStoreGCFailsClosedOnASealedTenant(t *testing.T) {
	resolver := newStubResolver()
	resolver.fail["tenant-sealed"] = errors.New("tenant is sealed")

	store, err := NewTenantStore(resolver)
	if err != nil {
		t.Fatalf("NewTenantStore: %v", err)
	}

	removed, err := store.GC(context.Background(), "tenant-sealed", time.Now())
	if err == nil {
		t.Fatal("GC on a sealed tenant must fail")
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0 on failure", removed)
	}
}

// TestTenantStoreRedeemFailsClosedOnASealedTenant is the redeem counterpart:
// a sealed tenant must not read as "no such invitation".
func TestTenantStoreRedeemFailsClosedOnASealedTenant(t *testing.T) {
	resolver := newStubResolver()
	resolver.fail["tenant-sealed"] = errors.New("tenant is sealed")

	store, err := NewTenantStore(resolver)
	if err != nil {
		t.Fatalf("NewTenantStore: %v", err)
	}

	if _, err := store.Redeem(context.Background(), "tenant-sealed", "sometoken"); err == nil {
		t.Fatal("Redeem on a sealed tenant must fail")
	}
}

// TestErrorMessagesNameTheirOperation covers the typed errors. Invitation
// failures surface to an operator issuing or redeeming a credential, so the
// message has to say which step failed.
func TestErrorMessagesNameTheirOperation(t *testing.T) {
	cause := errors.New("underlying")

	tests := []struct {
		name     string
		err      error
		contains []string
	}{
		{
			name:     "token generation",
			err:      &TokenGenerationError{Cause: cause},
			contains: []string{"invite", "token generation failed", "underlying"},
		},
		{
			name:     "store names the op",
			err:      &StoreError{Op: "redeem", Cause: cause},
			contains: []string{"invite", "redeem failed", "underlying"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("message %q does not contain %q", got, want)
				}
			}
			if !errors.Is(tc.err, cause) {
				t.Errorf("errors.Is could not reach the cause through %T", tc.err)
			}
		})
	}
}

// TestGenerateTokenProducesDistinctFullLengthTokens covers the success path.
// The token is an unrate-limited bearer credential, so its full entropy is the
// only thing standing between an attacker and enrollment -- a truncated or
// repeating token would be a critical weakness rather than a cosmetic one.
func TestGenerateTokenProducesDistinctFullLengthTokens(t *testing.T) {
	const iterations = 64
	seen := make(map[string]bool, iterations)

	for i := 0; i < iterations; i++ {
		token, err := GenerateToken()
		if err != nil {
			t.Fatalf("GenerateToken: %v", err)
		}
		if len(token) != TokenBytes*2 {
			t.Fatalf("token length = %d hex chars, want %d", len(token), TokenBytes*2)
		}
		if seen[token] {
			t.Fatal("GenerateToken repeated a token")
		}
		seen[token] = true
	}
}

// TestTokenDigestIsStableAndDistinct pins the two properties redemption
// depends on: the same token always keys to the same record, and different
// tokens never collide into one.
func TestTokenDigestIsStableAndDistinct(t *testing.T) {
	first, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	second, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	if TokenDigest(first) != TokenDigest(first) {
		t.Fatal("TokenDigest is not stable for one token")
	}
	if TokenDigest(first) == TokenDigest(second) {
		t.Fatal("TokenDigest collided for two distinct tokens")
	}
	if strings.Contains(TokenDigest(first), first) {
		t.Fatal("the digest embeds the plaintext token")
	}
}
