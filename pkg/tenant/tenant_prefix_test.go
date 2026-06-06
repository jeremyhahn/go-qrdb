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
	"log/slog"
	"strings"
	"testing"
)

// TestMetadataPrefix_HasSysPrefix verifies that tenant metadata keys are
// stored under the "__sys:" prefix so the store-level internal-key filter
// excludes them from user-facing KV list and scan results (Bug 2).
// Without this prefix, tenant records appear in the KV Explorer and can be
// accidentally edited or deleted by users.
func TestMetadataPrefix_HasSysPrefix(t *testing.T) {
	if !strings.HasPrefix(metadataPrefix, "__sys:") {
		t.Fatalf("metadataPrefix = %q; must start with '__sys:' to be excluded from user-facing KV lists",
			metadataPrefix)
	}
}

// TestKVTenantService_CreateTenant_StoresUnderSysPrefix verifies that
// creating a tenant writes its metadata key under the "__sys:" namespace.
// This ensures the store-level filter excludes tenant keys from the
// user-visible KV list returned by /kv/list and the KV Explorer.
func TestKVTenantService_CreateTenant_StoresUnderSysPrefix(t *testing.T) {
	store := newMemKVStore()
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	ctx := context.Background()
	_, err = svc.CreateTenant(ctx, "acme", "Acme Corp", IsolationPrefix)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Inspect the raw KV store to confirm the key starts with "__sys:".
	for key := range store.data {
		if strings.Contains(key, "acme") {
			if !strings.HasPrefix(key, "__sys:") {
				t.Fatalf("tenant metadata key %q does not start with '__sys:'; it will leak into user-facing KV lists",
					key)
			}
		}
	}
}

// TestKVTenantService_ListTenants_NotVisibleWithoutSysPrefix verifies that
// tenant keys are not returned when listing with an empty prefix — which is
// what the KV list endpoint does. The store-level filter removes "__sys:"
// keys from the result when the query prefix does not itself start with
// "__sys:".
//
// This test simulates the filtering by confirming no "tenants/" or
// "__sys:tenants/" key would be returned from a plain-prefix list
// (i.e. filtering is the store's responsibility; this test ensures the key
// format is consistent with the contract).
func TestKVTenantService_TenantKey_ExcludedFromUserKVList(t *testing.T) {
	store := newMemKVStore()
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "tenant1", "Tenant One", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Simulate a user-facing KV list with empty prefix (no __sys: skip-filter).
	// The store-level isInternalKey filter blocks keys starting with "__sys:".
	// Confirm all stored keys for "tenant1" start with "__sys:".
	for key := range store.data {
		if strings.Contains(key, "tenant1") {
			if strings.HasPrefix(key, "tenants/") {
				t.Fatalf("tenant key %q uses legacy 'tenants/' prefix; it will appear in user KV lists", key)
			}
			if !strings.HasPrefix(key, "__sys:") {
				t.Fatalf("tenant key %q does not use '__sys:' prefix; it will appear in user KV lists", key)
			}
		}
	}
}

// TestTenantIDFromKey_SysPrefix verifies that TenantIDFromKey correctly
// strips the updated "__sys:tenants/" prefix.
func TestTenantIDFromKey_SysPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{metadataPrefix + "acme", "acme"},
		{metadataPrefix + "my-tenant-123", "my-tenant-123"},
		{"tenants/old", ""},   // legacy prefix — returns empty (not matched)
		{"__sys:other/x", ""}, // different sys prefix — not a tenant key
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := TenantIDFromKey(tc.input)
			if got != tc.want {
				t.Errorf("TenantIDFromKey(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
