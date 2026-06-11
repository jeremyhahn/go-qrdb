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

package ceremony

import (
	"log/slog"
	"strings"
	"testing"
)

func TestScopeSystem_Prefix(t *testing.T) {
	if got := ScopeSystem.StoragePrefix(); got != "ceremony/officers/" {
		t.Errorf("ScopeSystem.StoragePrefix() = %q, want %q", got, "ceremony/officers/")
	}
}

func TestScopeRecovery_Prefix(t *testing.T) {
	if got := ScopeRecovery.StoragePrefix(); got != "ceremony/recovery/officers/" {
		t.Errorf("ScopeRecovery.StoragePrefix() = %q, want %q", got, "ceremony/recovery/officers/")
	}
}

func TestScopeTenant_Prefix(t *testing.T) {
	tests := []struct {
		tenantID string
		want     string
	}{
		{"acme", "ceremony/tenants/acme/officers/"},
		{"tenant-123", "ceremony/tenants/tenant-123/officers/"},
	}
	for _, tt := range tests {
		s := ScopeTenant(tt.tenantID)
		if got := s.StoragePrefix(); got != tt.want {
			t.Errorf("ScopeTenant(%q).StoragePrefix() = %q, want %q", tt.tenantID, got, tt.want)
		}
	}
}

func TestScopeSystem_String(t *testing.T) {
	if got := ScopeSystem.String(); got != "system" {
		t.Errorf("ScopeSystem.String() = %q, want %q", got, "system")
	}
}

func TestScopeRecovery_String(t *testing.T) {
	if got := ScopeRecovery.String(); got != "recovery" {
		t.Errorf("ScopeRecovery.String() = %q, want %q", got, "recovery")
	}
}

func TestScopeTenant_String(t *testing.T) {
	s := ScopeTenant("myorg")
	if got := s.String(); got != "tenant:myorg" {
		t.Errorf("ScopeTenant.String() = %q, want %q", got, "tenant:myorg")
	}
}

func TestBarrierScope_LogValue(t *testing.T) {
	tests := []struct {
		scope   BarrierScope
		wantStr string // substring in log output
	}{
		{ScopeSystem, "system"},
		{ScopeRecovery, "recovery"},
		{ScopeTenant("demo"), "demo"},
	}
	for _, tt := range tests {
		v := tt.scope.LogValue()
		if v.Kind() == slog.KindAny {
			t.Errorf("LogValue() returned KindAny for scope %s", tt.scope)
		}
		// Just verify it produces a non-zero value without panicking.
		if v.String() == "" {
			t.Errorf("LogValue().String() is empty for scope %s", tt.scope)
		}
		if !strings.Contains(v.String(), tt.wantStr) {
			t.Errorf("LogValue() = %q, want substring %q", v.String(), tt.wantStr)
		}
	}
}

func TestScopeTenant_PrefixIsolation(t *testing.T) {
	s1 := ScopeTenant("tenant-a")
	s2 := ScopeTenant("tenant-b")
	if s1.StoragePrefix() == s2.StoragePrefix() {
		t.Error("different tenants must have different storage prefixes")
	}
}
