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

package sdk

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestTenantContextRoundTrip verifies that TenantFromContext returns the
// tenant ID bound by ContextWithTenant.
func TestTenantContextRoundTrip(t *testing.T) {
	ctx := ContextWithTenant(context.Background(), "acme")

	id, ok := TenantFromContext(ctx)
	if !ok {
		t.Fatal("TenantFromContext ok = false, want true")
	}
	if id != "acme" {
		t.Errorf("TenantFromContext id = %q, want %q", id, "acme")
	}
}

// TestTenantContextOverwrite verifies that a later binding shadows an
// earlier one on the derived context.
func TestTenantContextOverwrite(t *testing.T) {
	ctx := ContextWithTenant(context.Background(), "first")
	ctx = ContextWithTenant(ctx, "second")

	id, ok := TenantFromContext(ctx)
	if !ok {
		t.Fatal("TenantFromContext ok = false, want true")
	}
	if id != "second" {
		t.Errorf("TenantFromContext id = %q, want %q", id, "second")
	}
}

// TestTenantFromContextMissing verifies comma-ok semantics when no tenant
// is bound or the bound value is empty (contexts that resolve to the
// implicit system tenant).
func TestTenantFromContextMissing(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
	}{
		{"unbound context", context.Background()},
		{"empty tenant ID bound", ContextWithTenant(context.Background(), "")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := TenantFromContext(tt.ctx)
			if ok {
				t.Error("TenantFromContext ok = true, want false")
			}
			if id != "" {
				t.Errorf("TenantFromContext id = %q, want empty", id)
			}
		})
	}
}

// TestEffectiveTenantAlias verifies the SDK re-export is pkg/tenant.EffectiveTenant
// itself (same function value), so a module host sees the identical behaviour
// qrdb's own IAM routing relies on: a system identity gets no lift into
// another tenant.
func TestEffectiveTenantAlias(t *testing.T) {
	ctx := ContextWithTenant(context.Background(), "acme")
	got, err := EffectiveTenant(ctx, "", "op")
	if err != nil {
		t.Fatalf("EffectiveTenant unexpected error: %v", err)
	}
	if got != "acme" {
		t.Errorf("EffectiveTenant = %q, want %q (the caller's own tenant)", got, "acme")
	}

	sysCtx := ContextWithTenant(context.Background(), SystemTenantID)
	var xt *CrossTenantDeniedError
	if _, err := EffectiveTenant(sysCtx, "acme", "op"); !errors.As(err, &xt) {
		t.Fatalf("EffectiveTenant for a system identity targeting acme: err = %v, want *CrossTenantDeniedError", err)
	}
}

// TestOwnTenantAlias verifies OwnTenant refuses even a system identity's
// cross-tenant request, unlike EffectiveTenant, through the SDK boundary.
func TestOwnTenantAlias(t *testing.T) {
	ctx := ContextWithTenant(context.Background(), SystemTenantID)
	_, err := OwnTenant(ctx, "acme", "op")
	var xt *CrossTenantDeniedError
	if !errors.As(err, &xt) {
		t.Fatalf("OwnTenant err = %v, want *CrossTenantDeniedError", err)
	}
}

// TestCrossTenantDeniedErrorAlias verifies CrossTenantDeniedError aliases
// pkg/tenant.CrossTenantDeniedError, so a composing service can type-assert
// through the SDK boundary without importing pkg/tenant directly.
func TestCrossTenantDeniedErrorAlias(t *testing.T) {
	var sdkErr error = &CrossTenantDeniedError{IdentityTenantID: "a", RequestedTenantID: "b", Op: "op"}
	var pkgErr *tenant.CrossTenantDeniedError
	if !errors.As(sdkErr, &pkgErr) {
		t.Fatal("CrossTenantDeniedError does not alias pkg/tenant.CrossTenantDeniedError")
	}
}

// TestErrTenantBarrierNotEnabledAlias verifies the SDK re-export is the same
// sentinel value as pkg/tenant.ErrBarrierNotEnabled so composing services can
// classify the error through the SDK boundary with errors.Is.
func TestErrTenantBarrierNotEnabledAlias(t *testing.T) {
	if ErrTenantBarrierNotEnabled == nil {
		t.Fatal("ErrTenantBarrierNotEnabled is nil")
	}
	if !errors.Is(ErrTenantBarrierNotEnabled, tenant.ErrBarrierNotEnabled) {
		t.Error("ErrTenantBarrierNotEnabled does not alias pkg/tenant.ErrBarrierNotEnabled")
	}
	const wantMsg = "tenant: barrier not enabled (seal subsystem disabled)"
	if got := ErrTenantBarrierNotEnabled.Error(); got != wantMsg {
		t.Errorf("ErrTenantBarrierNotEnabled message = %q, want %q", got, wantMsg)
	}
}

// TestErrTenantBarrierNotEnabledWrapped verifies errors.Is traversal through a
// wrapped chain, matching how the sentinel typically arrives at a composing
// service's transport layer (wrapped by intermediate layers with %w).
func TestErrTenantBarrierNotEnabledWrapped(t *testing.T) {
	wrapped := fmt.Errorf("tenant barrier init: %w", ErrTenantBarrierNotEnabled)
	if !errors.Is(wrapped, ErrTenantBarrierNotEnabled) {
		t.Error("errors.Is failed to match the wrapped sentinel")
	}
	if errors.Is(errors.New("unrelated"), ErrTenantBarrierNotEnabled) {
		t.Error("errors.Is matched an unrelated error")
	}
}

// TestErrTenantStoreSealedAlias verifies the SDK re-export is the same
// sentinel value as pkg/tenant.ErrTenantSealed, the error
// TenantStoreResolver.Resolve (and every service built over a routed store,
// including the ceremony-time auth-factor surface) actually returns for a
// sealed barrier-isolated tenant. Before this alias existed, an SDK consumer
// had no way to recognize that condition through errors.Is without importing
// pkg/tenant directly, crossing the SDK boundary this package exists to hold
// (go-qrdb#464 round 5).
func TestErrTenantStoreSealedAlias(t *testing.T) {
	if ErrTenantStoreSealed == nil {
		t.Fatal("ErrTenantStoreSealed is nil")
	}
	if !errors.Is(ErrTenantStoreSealed, tenant.ErrTenantSealed) {
		t.Error("ErrTenantStoreSealed does not alias pkg/tenant.ErrTenantSealed")
	}
}

// TestErrTenantStoreSealedDistinctFromSealErrTenantSealed pins the reason
// this alias has its own name rather than reusing ErrTenantSealed: the two
// are DIFFERENT sentinels from different packages that happen to describe
// similar-sounding conditions (a store resolution failure vs. a barrier
// operation failure), and a consumer checking the wrong one silently never
// matches.
func TestErrTenantStoreSealedDistinctFromSealErrTenantSealed(t *testing.T) {
	if errors.Is(ErrTenantStoreSealed, ErrTenantSealed) {
		t.Fatal("ErrTenantStoreSealed and ErrTenantSealed are the same sentinel; this test's premise is stale")
	}
}

// TestErrTenantStoreSealedWrapped verifies errors.Is traversal through a
// wrapped chain, matching how the sentinel arrives at a composing service's
// transport layer in practice — e.g. AuthFactorListReadError.Unwrap().
func TestErrTenantStoreSealedWrapped(t *testing.T) {
	wrapped := fmt.Errorf("resolve store: %w", ErrTenantStoreSealed)
	if !errors.Is(wrapped, ErrTenantStoreSealed) {
		t.Error("errors.Is failed to match the wrapped sentinel")
	}
	if errors.Is(errors.New("unrelated"), ErrTenantStoreSealed) {
		t.Error("errors.Is matched an unrelated error")
	}
}
