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

//go:build qrdb_vector

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qrdbErrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// =============================================================================
// GAP 1: Tenant-partition namespace keys
// =============================================================================

// TestScopedNamespace_Format verifies the scoped key format.
func TestScopedNamespace_Format(t *testing.T) {
	tests := []struct {
		name     string
		tenantID string
		logical  string
		want     string
	}{
		{"explicit tenant", "alice", "docs", "alice\x1fdocs"},
		{"explicit tenant", "bob", "docs", "bob\x1fdocs"},
		{"system tenant", tenant.SystemTenantID, "docs", "\x1fdocs"},
		{"empty string tenant", "", "docs", "\x1fdocs"},
	}
	for _, tc := range tests {
		t.Run(tc.name+" "+tc.tenantID, func(t *testing.T) {
			got := scopedNamespace(tc.tenantID, tc.logical)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestUnscopedNamespace_Roundtrip verifies scopedNamespace and unscopedNamespace
// are mutual inverses for valid inputs.
func TestUnscopedNamespace_Roundtrip(t *testing.T) {
	cases := []struct {
		tenantID string
		logical  string
	}{
		{"alice", "docs"},
		{"bob", "my-vectors"},
		{tenant.SystemTenantID, "global"},
		{"", "legacy"},
	}
	for _, tc := range cases {
		physical := scopedNamespace(tc.tenantID, tc.logical)
		gotTenant, gotLogical := unscopedNamespace(physical)
		expectedTenant := tc.tenantID
		if expectedTenant == "" {
			expectedTenant = tenant.SystemTenantID
		}
		assert.Equal(t, expectedTenant, gotTenant, "tenant mismatch for %q/%q", tc.tenantID, tc.logical)
		assert.Equal(t, tc.logical, gotLogical, "logical mismatch for %q/%q", tc.tenantID, tc.logical)
	}
}

// TestTwoTenantsCreateSameLogicalNamespace verifies that two tenants creating
// the same logical namespace name get independent, non-colliding storage keys.
func TestTwoTenantsCreateSameLogicalNamespace(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctxA := tenant.WithTenantID(context.Background(), "tenant-a")
	ctxB := tenant.WithTenantID(context.Background(), "tenant-b")

	// Both tenants create a namespace with the same logical name.
	respA, err := svc.CreateNamespace(ctxA, &CreateNamespaceRequest{
		Namespace: "docs",
		Dimension: 3,
	})
	require.NoError(t, err)
	assert.Equal(t, "docs", respA.Namespace) // response shows logical name

	respB, err := svc.CreateNamespace(ctxB, &CreateNamespaceRequest{
		Namespace: "docs",
		Dimension: 3,
	})
	require.NoError(t, err)
	assert.Equal(t, "docs", respB.Namespace)

	// Insert a vector for tenant-a only.
	_, err = svc.UpsertVector(ctxA, &UpsertVectorRequest{
		Namespace: "docs",
		ID:        1,
		Vector:    []float32{1.0, 0.0, 0.0},
	})
	require.NoError(t, err)

	// Tenant-a's vector is visible to tenant-a.
	vecA, err := svc.GetVector(ctxA, &GetVectorRequest{
		Namespace:     "docs",
		ID:            1,
		IncludeVector: true,
	})
	require.NoError(t, err)
	assert.True(t, vecA.Found)

	// Tenant-b cannot see tenant-a's vector (different scoped key).
	_, err = svc.GetVector(ctxB, &GetVectorRequest{
		Namespace: "docs",
		ID:        1,
	})
	require.Error(t, err, "tenant-b must not find tenant-a's vector")
}

// TestListNamespacesReturnsOnlyCallersTenant verifies ListNamespaces filters by
// the calling tenant and returns logical (unscoped) names.
func TestListNamespacesReturnsOnlyCallersTenant(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	// Pre-populate storage with scoped namespaces for two different tenants.
	scopedA1 := scopedNamespace("tenant-a", "ns1")
	scopedA2 := scopedNamespace("tenant-a", "ns2")
	scopedB1 := scopedNamespace("tenant-b", "ns1")

	for _, ns := range []string{scopedA1, scopedA2, scopedB1} {
		require.NoError(t, storage.CreateNamespace(context.Background(), &vector.NamespaceInfo{
			Name:      ns,
			Dimension: 3,
		}))
	}

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctxA := tenant.WithTenantID(context.Background(), "tenant-a")
	respA, err := svc.ListNamespaces(ctxA, &ListNamespacesRequest{})
	require.NoError(t, err)
	assert.Equal(t, 2, respA.TotalCount, "tenant-a should see exactly 2 namespaces")

	// All returned namespaces belong to tenant-a and use logical names.
	for _, ns := range respA.Namespaces {
		assert.NotContains(t, ns.Namespace, "\x1f", "response must use logical name, not scoped key")
		assert.True(t, ns.Namespace == "ns1" || ns.Namespace == "ns2")
	}

	ctxB := tenant.WithTenantID(context.Background(), "tenant-b")
	respB, err := svc.ListNamespaces(ctxB, &ListNamespacesRequest{})
	require.NoError(t, err)
	assert.Equal(t, 1, respB.TotalCount, "tenant-b should see exactly 1 namespace")
}

// =============================================================================
// GAP 2: Strict-mode gating
// =============================================================================

// TestStrictTenantIsolation_RejectsEmptyTenant verifies that when strict mode
// is enabled, CreateNamespace rejects requests from the system tenant (i.e.
// requests without an explicit tenant in context).
func TestStrictTenantIsolation_RejectsEmptyTenant(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger, WithStrictTenantIsolation(true))
	require.NoError(t, err)

	// No tenant in context → FromContext returns SystemTenantID → rejected.
	_, err = svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "docs",
		Dimension: 3,
	})
	require.Error(t, err, "strict mode must reject system-tenant creates")

	var qrdbErr *qrdbErrors.QRDBError
	require.True(t, errors.As(err, &qrdbErr))
	assert.Equal(t, qrdbErrors.ErrForbidden, qrdbErr.Code)

	// Underlying error is TenantRequiredError.
	var tenantErr *TenantRequiredError
	require.True(t, errors.As(qrdbErr.Err, &tenantErr))
}

// TestStrictTenantIsolation_AllowsExplicitTenant verifies that strict mode
// permits creates when an explicit tenant is bound in the context.
func TestStrictTenantIsolation_AllowsExplicitTenant(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger, WithStrictTenantIsolation(true))
	require.NoError(t, err)

	ctx := tenant.WithTenantID(context.Background(), "acme-corp")
	resp, err := svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace: "docs",
		Dimension: 3,
	})
	require.NoError(t, err)
	assert.Equal(t, "docs", resp.Namespace)
}

// TestStrictTenantIsolation_DisabledAllowsSystemTenant verifies that when strict
// mode is disabled (default), the system tenant can create namespaces.
func TestStrictTenantIsolation_DisabledAllowsSystemTenant(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	resp, err := svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "docs",
		Dimension: 3,
	})
	require.NoError(t, err)
	assert.Equal(t, "docs", resp.Namespace)
}

// TestCreateNamespaceStampsTenantID verifies that the TenantID field in the
// persisted NamespaceInfo is stamped from the calling context.
func TestCreateNamespaceStampsTenantID(t *testing.T) {
	storage := newMockVectorStorage()
	logger := newMockLogger()

	var captured *vector.NamespaceInfo
	storage.createNamespaceFn = func(ctx context.Context, info *vector.NamespaceInfo) error {
		captured = info
		return nil
	}

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	ctx := tenant.WithTenantID(context.Background(), "my-tenant")
	_, err = svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace: "vectors",
		Dimension: 4,
	})
	require.NoError(t, err)

	require.NotNil(t, captured)
	assert.Equal(t, "my-tenant", captured.TenantID)
	assert.Equal(t, scopedNamespace("my-tenant", "vectors"), captured.Name)
}
