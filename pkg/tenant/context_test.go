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
)

func TestWithTenantIDAndFromContextRoundTrip(t *testing.T) {
	ctx := WithTenantID(context.Background(), "tenant-abc")
	got := FromContext(ctx)
	if got != "tenant-abc" {
		t.Errorf("FromContext() = %q, want %q", got, "tenant-abc")
	}
}

func TestFromContextNoTenantReturnsDefault(t *testing.T) {
	got := FromContext(context.Background())
	if got != SystemTenantID {
		t.Errorf("FromContext() = %q, want %q", got, SystemTenantID)
	}
}

func TestFromContextEmptyStringReturnsDefault(t *testing.T) {
	ctx := WithTenantID(context.Background(), "")
	got := FromContext(ctx)
	if got != SystemTenantID {
		t.Errorf("FromContext() = %q, want %q", got, SystemTenantID)
	}
}

func TestNestedContextsInnerTenantWins(t *testing.T) {
	ctx := WithTenantID(context.Background(), "outer-tenant")
	ctx = WithTenantID(ctx, "inner-tenant")
	got := FromContext(ctx)
	if got != "inner-tenant" {
		t.Errorf("FromContext() = %q, want %q", got, "inner-tenant")
	}
}

func TestWithTenantIDPreservesParentValues(t *testing.T) {
	type otherKey struct{}
	parent := context.WithValue(context.Background(), otherKey{}, "preserved")
	ctx := WithTenantID(parent, "my-tenant")

	// Tenant is set correctly.
	got := FromContext(ctx)
	if got != "my-tenant" {
		t.Errorf("FromContext() = %q, want %q", got, "my-tenant")
	}

	// Parent value is preserved.
	v, ok := ctx.Value(otherKey{}).(string)
	if !ok || v != "preserved" {
		t.Errorf("parent context value = %q, want %q", v, "preserved")
	}
}

func TestFromContextWithCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = WithTenantID(ctx, "cancelled-tenant")
	cancel()

	got := FromContext(ctx)
	if got != "cancelled-tenant" {
		t.Errorf("FromContext() = %q, want %q", got, "cancelled-tenant")
	}
}
