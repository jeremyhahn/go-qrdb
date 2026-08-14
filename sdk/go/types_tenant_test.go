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
