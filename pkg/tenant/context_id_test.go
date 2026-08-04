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

// TestIDFromContextRoundTrip verifies that IDFromContext returns the tenant
// ID bound by WithTenantID with ok=true.
func TestIDFromContextRoundTrip(t *testing.T) {
	ctx := WithTenantID(context.Background(), "acme")

	id, ok := IDFromContext(ctx)
	if !ok {
		t.Fatal("IDFromContext ok = false, want true")
	}
	if id != "acme" {
		t.Errorf("IDFromContext id = %q, want %q", id, "acme")
	}
}

// TestIDFromContextMissing verifies comma-ok semantics when no tenant is
// bound or the bound value is empty — no SystemTenantID fallback.
func TestIDFromContextMissing(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
	}{
		{"unbound context", context.Background()},
		{"empty tenant ID bound", WithTenantID(context.Background(), "")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := IDFromContext(tt.ctx)
			if ok {
				t.Errorf("IDFromContext ok = true, want false")
			}
			if id != "" {
				t.Errorf("IDFromContext id = %q, want empty", id)
			}
		})
	}
}
