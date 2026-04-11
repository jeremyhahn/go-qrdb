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

package statemachine

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// TestVectorStateMachine_CrossTenantAccessDenied verifies that cross-tenant access is blocked
func TestVectorStateMachine_CrossTenantAccessDenied(t *testing.T) {
	adapter := NewMockStorageAdapter()
	adapter.vecStor = NewMockVectorStorageWrapper()
	hnswIndex := NewMockHNSWIndex()
	log := newTestLogger()

	vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
	require.NoError(t, err)

	// Create namespace owned by tenant-A
	createCmd := &VectorCommand{
		Op:        OpCreateNamespace,
		Namespace: "tenant_a_ns",
		Dimension: 128,
		Timestamp: time.Now().UnixNano(),
		TenantID:  "tenant-A",
	}
	createData, err := createCmd.Encode()
	require.NoError(t, err)

	err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: createData}}, make([]sm.Result, 1))
	require.NoError(t, err)

	// Add a vector for tenant-A
	upsertCmd := &VectorCommand{
		Op:        OpUpsertVector,
		Namespace: "tenant_a_ns",
		ID:        1,
		Vector:    make([]float32, 128),
		Timestamp: time.Now().UnixNano(),
		Version:   1,
		TenantID:  "tenant-A",
	}
	upsertData, err := upsertCmd.Encode()
	require.NoError(t, err)

	err = vsm.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: upsertData}}, make([]sm.Result, 1))
	require.NoError(t, err)

	// Attempt to access with tenant-B - should be denied
	searchCmd := &VectorCommand{
		Op:        OpSearchSimilar,
		Namespace: "tenant_a_ns",
		Vector:    make([]float32, 128),
		K:         5,
		TenantID:  "tenant-B",
	}
	searchData, err := searchCmd.Encode()
	require.NoError(t, err)

	_, err = vsm.Lookup(context.Background(), searchData)
	require.Error(t, err)

	// Verify it's a cross-tenant access error
	var qrdbErr *errors.QRDBError
	if assert.ErrorAs(t, err, &qrdbErr) {
		assert.Equal(t, errors.ErrCrossTenantAccess, qrdbErr.Code)
	}
}

// TestVectorStateMachine_MissingTenantContext verifies that tenant context is required for tenant-owned namespaces
func TestVectorStateMachine_MissingTenantContext(t *testing.T) {
	adapter := NewMockStorageAdapter()
	adapter.vecStor = NewMockVectorStorageWrapper()
	hnswIndex := NewMockHNSWIndex()
	log := newTestLogger()

	vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
	require.NoError(t, err)

	// Create namespace owned by tenant-A
	createCmd := &VectorCommand{
		Op:        OpCreateNamespace,
		Namespace: "tenant_owned_ns",
		Dimension: 128,
		Timestamp: time.Now().UnixNano(),
		TenantID:  "tenant-A",
	}
	createData, err := createCmd.Encode()
	require.NoError(t, err)

	err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: createData}}, make([]sm.Result, 1))
	require.NoError(t, err)

	// Attempt to access without tenant context - should be denied
	searchCmd := &VectorCommand{
		Op:        OpSearchSimilar,
		Namespace: "tenant_owned_ns",
		Vector:    make([]float32, 128),
		K:         5,
		TenantID:  "", // No tenant context
	}
	searchData, err := searchCmd.Encode()
	require.NoError(t, err)

	_, err = vsm.Lookup(context.Background(), searchData)
	require.Error(t, err)

	// Verify it's a missing tenant context error
	var qrdbErr *errors.QRDBError
	if assert.ErrorAs(t, err, &qrdbErr) {
		assert.Equal(t, errors.ErrMissingTenantContext, qrdbErr.Code)
	}
}

// TestVectorStateMachine_SystemNamespaceAccessible verifies system namespaces are accessible without tenant context
func TestVectorStateMachine_SystemNamespaceAccessible(t *testing.T) {
	adapter := NewMockStorageAdapter()
	adapter.vecStor = NewMockVectorStorageWrapper()
	hnswIndex := NewMockHNSWIndex()
	log := newTestLogger()

	vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
	require.NoError(t, err)

	// Create system namespace (no tenant)
	createCmd := &VectorCommand{
		Op:        OpCreateNamespace,
		Namespace: "system_ns",
		Dimension: 128,
		Timestamp: time.Now().UnixNano(),
		TenantID:  "", // System namespace
	}
	createData, err := createCmd.Encode()
	require.NoError(t, err)

	err = vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: createData}}, make([]sm.Result, 1))
	require.NoError(t, err)

	// Add a vector to system namespace
	upsertCmd := &VectorCommand{
		Op:        OpUpsertVector,
		Namespace: "system_ns",
		ID:        1,
		Vector:    make([]float32, 128),
		Timestamp: time.Now().UnixNano(),
		Version:   1,
		TenantID:  "", // No tenant
	}
	upsertData, err := upsertCmd.Encode()
	require.NoError(t, err)

	err = vsm.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: upsertData}}, make([]sm.Result, 1))
	require.NoError(t, err)

	// Access from any tenant or no tenant should work for system namespace
	tests := []struct {
		name     string
		tenantID string
	}{
		{"NoTenant", ""},
		{"TenantA", "tenant-A"},
		{"TenantB", "tenant-B"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getCmd := &VectorCommand{
				Op:        OpGetVector,
				Namespace: "system_ns",
				ID:        1,
				TenantID:  tt.tenantID,
			}
			getData, err := getCmd.Encode()
			require.NoError(t, err)

			result, err := vsm.Lookup(context.Background(), getData)
			require.NoError(t, err)
			require.NotNil(t, result)
		})
	}
}

// TestVectorStateMachine_ListNamespacesFiltered verifies namespace listing is filtered by tenant
func TestVectorStateMachine_ListNamespacesFiltered(t *testing.T) {
	adapter := NewMockStorageAdapter()
	adapter.vecStor = NewMockVectorStorageWrapper()
	hnswIndex := NewMockHNSWIndex()
	log := newTestLogger()

	vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
	require.NoError(t, err)

	// Create system namespace
	systemCmd := &VectorCommand{
		Op:        OpCreateNamespace,
		Namespace: "system_ns",
		Dimension: 128,
		Timestamp: time.Now().UnixNano(),
		TenantID:  "",
	}
	systemData, _ := systemCmd.Encode()
	vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: systemData}}, make([]sm.Result, 1)) //nolint:errcheck
	// Create tenant-A namespace
	tenantACmd := &VectorCommand{
		Op:        OpCreateNamespace,
		Namespace: "tenant_a_ns",
		Dimension: 128,
		Timestamp: time.Now().UnixNano(),
		TenantID:  "tenant-A",
	}
	tenantAData, _ := tenantACmd.Encode()
	vsm.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: tenantAData}}, make([]sm.Result, 1)) //nolint:errcheck
	// Create tenant-B namespace
	tenantBCmd := &VectorCommand{
		Op:        OpCreateNamespace,
		Namespace: "tenant_b_ns",
		Dimension: 128,
		Timestamp: time.Now().UnixNano(),
		TenantID:  "tenant-B",
	}
	tenantBData, _ := tenantBCmd.Encode()
	vsm.Update(context.Background(), []sm.Entry{{Index: 3, Cmd: tenantBData}}, make([]sm.Result, 1)) //nolint:errcheck
	// Tenant-A should see system_ns and tenant_a_ns
	listCmdA := &VectorCommand{
		Op:       OpListNamespaces,
		TenantID: "tenant-A",
	}
	listDataA, _ := listCmdA.Encode()
	resultA, err := vsm.Lookup(context.Background(), listDataA)
	require.NoError(t, err)

	vecResultA := resultA.(*VectorResult)
	assert.Contains(t, vecResultA.Namespaces, "system_ns")
	assert.Contains(t, vecResultA.Namespaces, "tenant_a_ns")
	assert.NotContains(t, vecResultA.Namespaces, "tenant_b_ns")

	// Tenant-B should see system_ns and tenant_b_ns
	listCmdB := &VectorCommand{
		Op:       OpListNamespaces,
		TenantID: "tenant-B",
	}
	listDataB, _ := listCmdB.Encode()
	resultB, err := vsm.Lookup(context.Background(), listDataB)
	require.NoError(t, err)

	vecResultB := resultB.(*VectorResult)
	assert.Contains(t, vecResultB.Namespaces, "system_ns")
	assert.Contains(t, vecResultB.Namespaces, "tenant_b_ns")
	assert.NotContains(t, vecResultB.Namespaces, "tenant_a_ns")
}

// TestVectorStateMachine_TenantOwnershipValidation tests all operations enforce tenant ownership
func TestVectorStateMachine_TenantOwnershipValidation(t *testing.T) {
	adapter := NewMockStorageAdapter()
	adapter.vecStor = NewMockVectorStorageWrapper()
	hnswIndex := NewMockHNSWIndex()
	log := newTestLogger()

	vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
	require.NoError(t, err)

	// Create namespace for tenant-A
	createCmd := &VectorCommand{
		Op:        OpCreateNamespace,
		Namespace: "tenant_a_ns",
		Dimension: 128,
		Timestamp: time.Now().UnixNano(),
		TenantID:  "tenant-A",
	}
	createData, _ := createCmd.Encode()
	vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: createData}}, make([]sm.Result, 1)) //nolint:errcheck
	// Add vector for tenant-A
	upsertCmd := &VectorCommand{
		Op:        OpUpsertVector,
		Namespace: "tenant_a_ns",
		ID:        1,
		Vector:    make([]float32, 128),
		Timestamp: time.Now().UnixNano(),
		Version:   1,
		TenantID:  "tenant-A",
	}
	upsertData, _ := upsertCmd.Encode()
	vsm.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: upsertData}}, make([]sm.Result, 1)) //nolint:errcheck
	// Test all write operations are blocked for tenant-B
	writeOps := []struct {
		name string
		cmd  *VectorCommand
	}{
		{
			name: "Upsert",
			cmd: &VectorCommand{
				Op:        OpUpsertVector,
				Namespace: "tenant_a_ns",
				ID:        2,
				Vector:    make([]float32, 128),
				TenantID:  "tenant-B",
			},
		},
		{
			name: "Delete",
			cmd: &VectorCommand{
				Op:        OpDeleteVector,
				Namespace: "tenant_a_ns",
				ID:        1,
				TenantID:  "tenant-B",
			},
		},
		{
			name: "BatchUpsert",
			cmd: &VectorCommand{
				Op:        OpBatchUpsert,
				Namespace: "tenant_a_ns",
				IDs:       []uint64{3, 4},
				Vectors:   [][]float32{make([]float32, 128), make([]float32, 128)},
				TenantID:  "tenant-B",
			},
		},
		{
			name: "UpdateMetadata",
			cmd: &VectorCommand{
				Op:        OpUpdateMetadata,
				Namespace: "tenant_a_ns",
				ID:        1,
				Metadata:  map[string]interface{}{"key": "value"},
				TenantID:  "tenant-B",
			},
		},
		{
			name: "DeleteNamespace",
			cmd: &VectorCommand{
				Op:        OpDeleteNamespace,
				Namespace: "tenant_a_ns",
				TenantID:  "tenant-B",
			},
		},
		{
			name: "UpdateNamespace",
			cmd: &VectorCommand{
				Op:          OpUpdateNamespace,
				Namespace:   "tenant_a_ns",
				IndexParams: map[string]string{"m": "32"},
				TenantID:    "tenant-B",
			},
		},
		{
			name: "RebuildIndex",
			cmd: &VectorCommand{
				Op:        OpRebuildIndex,
				Namespace: "tenant_a_ns",
				JobID:     "job-123",
				TenantID:  "tenant-B",
			},
		},
	}

	for _, op := range writeOps {
		t.Run(op.name+"_WriteBlocked", func(t *testing.T) {
			data, err := op.cmd.Encode()
			require.NoError(t, err)

			err = vsm.Update(context.Background(), []sm.Entry{{Index: 100, Cmd: data}}, make([]sm.Result, 1))
			require.Error(t, err)

			var qrdbErr *errors.QRDBError
			if assert.ErrorAs(t, err, &qrdbErr) {
				assert.Equal(t, errors.ErrCrossTenantAccess, qrdbErr.Code)
			}
		})
	}

	// Test all read operations are blocked for tenant-B
	readOps := []struct {
		name string
		cmd  *VectorCommand
	}{
		{
			name: "GetVector",
			cmd: &VectorCommand{
				Op:        OpGetVector,
				Namespace: "tenant_a_ns",
				ID:        1,
				TenantID:  "tenant-B",
			},
		},
		{
			name: "Search",
			cmd: &VectorCommand{
				Op:        OpSearchSimilar,
				Namespace: "tenant_a_ns",
				Vector:    make([]float32, 128),
				K:         5,
				TenantID:  "tenant-B",
			},
		},
		{
			name: "SearchByID",
			cmd: &VectorCommand{
				Op:        OpSearchByID,
				Namespace: "tenant_a_ns",
				ID:        1,
				K:         5,
				TenantID:  "tenant-B",
			},
		},
		{
			name: "ListVectors",
			cmd: &VectorCommand{
				Op:        OpListVectors,
				Namespace: "tenant_a_ns",
				Limit:     10,
				TenantID:  "tenant-B",
			},
		},
		{
			name: "GetNamespaceInfo",
			cmd: &VectorCommand{
				Op:        OpGetNamespaceInfo,
				Namespace: "tenant_a_ns",
				TenantID:  "tenant-B",
			},
		},
		{
			name: "GetStats",
			cmd: &VectorCommand{
				Op:        OpGetStats,
				Namespace: "tenant_a_ns",
				TenantID:  "tenant-B",
			},
		},
	}

	for _, op := range readOps {
		t.Run(op.name+"_ReadBlocked", func(t *testing.T) {
			data, err := op.cmd.Encode()
			require.NoError(t, err)

			_, err = vsm.Lookup(context.Background(), data)
			require.Error(t, err)

			var qrdbErr *errors.QRDBError
			if assert.ErrorAs(t, err, &qrdbErr) {
				assert.Equal(t, errors.ErrCrossTenantAccess, qrdbErr.Code)
			}
		})
	}
}

// TestVectorStateMachine_ValidateTenantOwnership_DirectCall tests the validateTenantOwnership helper directly
func TestVectorStateMachine_ValidateTenantOwnership_DirectCall(t *testing.T) {
	adapter := NewMockStorageAdapter()
	adapter.vecStor = NewMockVectorStorageWrapper()
	log := newTestLogger()

	vsm, err := newTestVectorStateMachine(adapter, nil, nil, log)
	require.NoError(t, err)

	// Setup namespaces
	vsm.namespaces["system_ns"] = &NamespaceState{
		Name:     "system_ns",
		TenantID: "", // System namespace
	}
	vsm.namespaces["tenant_a_ns"] = &NamespaceState{
		Name:     "tenant_a_ns",
		TenantID: "tenant-A",
	}

	tests := []struct {
		name         string
		namespace    string
		tenantID     string
		expectError  bool
		expectedCode errors.ErrorCode
	}{
		{
			name:        "SystemNamespace_NoTenant",
			namespace:   "system_ns",
			tenantID:    "",
			expectError: false,
		},
		{
			name:        "SystemNamespace_WithTenant",
			namespace:   "system_ns",
			tenantID:    "tenant-A",
			expectError: false,
		},
		{
			name:        "TenantNamespace_CorrectTenant",
			namespace:   "tenant_a_ns",
			tenantID:    "tenant-A",
			expectError: false,
		},
		{
			name:         "TenantNamespace_WrongTenant",
			namespace:    "tenant_a_ns",
			tenantID:     "tenant-B",
			expectError:  true,
			expectedCode: errors.ErrCrossTenantAccess,
		},
		{
			name:         "TenantNamespace_NoTenant",
			namespace:    "tenant_a_ns",
			tenantID:     "",
			expectError:  true,
			expectedCode: errors.ErrMissingTenantContext,
		},
		{
			name:         "NonexistentNamespace",
			namespace:    "nonexistent",
			tenantID:     "tenant-A",
			expectError:  true,
			expectedCode: errors.ErrNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err = vsm.validateTenantOwnership(tt.namespace, tt.tenantID)

			if tt.expectError {
				require.Error(t, err)
				var qrdbErr *errors.QRDBError
				if assert.ErrorAs(t, err, &qrdbErr) {
					assert.Equal(t, tt.expectedCode, qrdbErr.Code)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestVectorStateMachine_CrossTenantDeleteNamespace verifies namespace deletion respects tenant ownership
func TestVectorStateMachine_CrossTenantDeleteNamespace(t *testing.T) {
	adapter := NewMockStorageAdapter()
	adapter.vecStor = NewMockVectorStorageWrapper()
	hnswIndex := NewMockHNSWIndex()
	log := newTestLogger()

	vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
	require.NoError(t, err)

	// Create namespace owned by tenant-A
	createCmd := &VectorCommand{
		Op:        OpCreateNamespace,
		Namespace: "protected_ns",
		Dimension: 128,
		Timestamp: time.Now().UnixNano(),
		TenantID:  "tenant-A",
	}
	createData, _ := createCmd.Encode()
	vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: createData}}, make([]sm.Result, 1)) //nolint:errcheck
	// Verify tenant-B cannot delete it
	deleteCmd := &VectorCommand{
		Op:        OpDeleteNamespace,
		Namespace: "protected_ns",
		TenantID:  "tenant-B",
	}
	deleteData, _ := deleteCmd.Encode()

	err = vsm.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: deleteData}}, make([]sm.Result, 1))
	require.Error(t, err)

	// Verify namespace still exists
	assert.Contains(t, vsm.namespaces, "protected_ns")

	// Verify tenant-A can delete it
	deleteCmd.TenantID = "tenant-A"
	deleteData, _ = deleteCmd.Encode()

	err = vsm.Update(context.Background(), []sm.Entry{{Index: 3, Cmd: deleteData}}, make([]sm.Result, 1))
	require.NoError(t, err)

	// Verify namespace is deleted
	assert.NotContains(t, vsm.namespaces, "protected_ns")
}

// TestVectorStateMachine_TenantSnapshotPreservation verifies tenant info is preserved in snapshots
func TestVectorStateMachine_TenantSnapshotPreservation(t *testing.T) {
	adapter := NewMockStorageAdapter()
	adapter.vecStor = NewMockVectorStorageWrapper()
	hnswIndex := NewMockHNSWIndex()
	log := newTestLogger()

	vsm, err := newTestVectorStateMachine(adapter, hnswIndex, nil, log)
	require.NoError(t, err)

	// Create namespace with tenant info
	createCmd := &VectorCommand{
		Op:          OpCreateNamespace,
		Namespace:   "tenant_ns",
		Dimension:   128,
		Timestamp:   time.Now().UnixNano(),
		TenantID:    "tenant-A",
		OwnerUserID: "user-123",
		Labels:      map[string]string{"env": "prod"},
	}
	createData, _ := createCmd.Encode()
	vsm.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: createData}}, make([]sm.Result, 1)) //nolint:errcheck
	// Prepare snapshot
	snapshot, err := vsm.PrepareSnapshot()
	require.NoError(t, err)

	// Verify tenant info is in snapshot
	vecSnapshot := snapshot.(*VectorSnapshot)
	ns := vecSnapshot.Namespaces["tenant_ns"]
	require.NotNil(t, ns)
	assert.Equal(t, "tenant-A", ns.TenantID)
	assert.Equal(t, "user-123", ns.OwnerUserID)
	assert.Equal(t, map[string]string{"env": "prod"}, ns.Labels)
}

// suppress unused import warning for vector package
var _ = vector.Storage(nil)
