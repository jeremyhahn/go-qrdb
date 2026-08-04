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
	"testing"
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
