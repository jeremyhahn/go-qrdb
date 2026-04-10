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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
)

func TestNamespaceState_CheckQuota_NoQuotas(t *testing.T) {
	ns := &NamespaceState{
		Name:        "test-ns",
		Dimension:   128,
		VectorCount: 100,
		Quotas:      nil, // No quotas set
	}

	// Should always pass when no quotas are configured
	if err := ns.CheckQuota(1000, 1000000); err != nil {
		t.Errorf("CheckQuota() with no quotas = %v, want nil", err)
	}
}

func TestNamespaceState_CheckQuota_VectorLimit(t *testing.T) {
	ns := &NamespaceState{
		Name:        "test-ns",
		Dimension:   128,
		VectorCount: 95,
		TenantID:    "tenant-123",
		Quotas: &NamespaceQuotas{
			MaxVectors: 100,
		},
	}

	tests := []struct {
		name        string
		vectorCount int
		wantErr     bool
	}{
		{
			name:        "within limit",
			vectorCount: 5,
			wantErr:     false,
		},
		{
			name:        "at limit",
			vectorCount: 5,
			wantErr:     false,
		},
		{
			name:        "exceeds limit by 1",
			vectorCount: 6,
			wantErr:     true,
		},
		{
			name:        "exceeds limit significantly",
			vectorCount: 100,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ns.CheckQuota(tt.vectorCount, 0)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckQuota() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				if !errors.IsQuotaExceeded(err) {
					t.Errorf("CheckQuota() error type = %T, want QuotaExceeded", err)
				}
			}
		})
	}
}

func TestNamespaceState_CheckQuota_StorageLimit(t *testing.T) {
	ns := &NamespaceState{
		Name:         "test-ns",
		Dimension:    128,
		VectorCount:  50,
		TenantID:     "tenant-456",
		StorageBytes: 900000, // 900KB used
		Quotas: &NamespaceQuotas{
			MaxStorageBytes: 1000000, // 1MB limit
		},
	}

	tests := []struct {
		name        string
		vectorBytes int64
		wantErr     bool
	}{
		{
			name:        "within limit",
			vectorBytes: 50000, // 50KB
			wantErr:     false,
		},
		{
			name:        "at limit",
			vectorBytes: 100000, // exactly at limit
			wantErr:     false,
		},
		{
			name:        "exceeds limit by 1",
			vectorBytes: 100001,
			wantErr:     true,
		},
		{
			name:        "exceeds limit significantly",
			vectorBytes: 500000,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ns.CheckQuota(0, tt.vectorBytes)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckQuota() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				if !errors.IsQuotaExceeded(err) {
					t.Errorf("CheckQuota() error type = %T, want QuotaExceeded", err)
				}
			}
		})
	}
}

func TestNamespaceState_CheckBatchSizeQuota(t *testing.T) {
	tests := []struct {
		name      string
		quotas    *NamespaceQuotas
		batchSize int
		wantErr   bool
	}{
		{
			name:      "no quotas configured",
			quotas:    nil,
			batchSize: 10000,
			wantErr:   false,
		},
		{
			name:      "batch size limit zero (disabled)",
			quotas:    &NamespaceQuotas{MaxBatchSize: 0},
			batchSize: 10000,
			wantErr:   false,
		},
		{
			name:      "within batch size limit",
			quotas:    &NamespaceQuotas{MaxBatchSize: 100},
			batchSize: 50,
			wantErr:   false,
		},
		{
			name:      "at batch size limit",
			quotas:    &NamespaceQuotas{MaxBatchSize: 100},
			batchSize: 100,
			wantErr:   false,
		},
		{
			name:      "exceeds batch size limit",
			quotas:    &NamespaceQuotas{MaxBatchSize: 100},
			batchSize: 101,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns := &NamespaceState{
				Name:     "test-ns",
				TenantID: "tenant-789",
				Quotas:   tt.quotas,
			}
			err := ns.CheckBatchSizeQuota(tt.batchSize)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckBatchSizeQuota() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				if !errors.IsQuotaExceeded(err) {
					t.Errorf("CheckBatchSizeQuota() error type = %T, want QuotaExceeded", err)
				}
			}
		})
	}
}

func TestNamespaceState_CheckDimensionQuota(t *testing.T) {
	tests := []struct {
		name       string
		quotas     *NamespaceQuotas
		dimensions int
		wantErr    bool
	}{
		{
			name:       "no quotas configured",
			quotas:     nil,
			dimensions: 4096,
			wantErr:    false,
		},
		{
			name:       "dimension limit zero (disabled)",
			quotas:     &NamespaceQuotas{MaxDimensions: 0},
			dimensions: 4096,
			wantErr:    false,
		},
		{
			name:       "within dimension limit",
			quotas:     &NamespaceQuotas{MaxDimensions: 1536},
			dimensions: 768,
			wantErr:    false,
		},
		{
			name:       "at dimension limit",
			quotas:     &NamespaceQuotas{MaxDimensions: 1536},
			dimensions: 1536,
			wantErr:    false,
		},
		{
			name:       "exceeds dimension limit",
			quotas:     &NamespaceQuotas{MaxDimensions: 1536},
			dimensions: 1537,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns := &NamespaceState{
				Name:      "test-ns",
				Dimension: 128,
				TenantID:  "tenant-dim",
				Quotas:    tt.quotas,
			}
			err := ns.CheckDimensionQuota(tt.dimensions)
			if (err != nil) != tt.wantErr {
				t.Errorf("CheckDimensionQuota() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				if !errors.IsQuotaExceeded(err) {
					t.Errorf("CheckDimensionQuota() error type = %T, want QuotaExceeded", err)
				}
			}
		})
	}
}

func TestNamespaceState_UpdateStorageBytes(t *testing.T) {
	ns := &NamespaceState{
		Name:         "test-ns",
		StorageBytes: 1000,
	}

	// Increase storage
	ns.UpdateStorageBytes(500)
	if ns.StorageBytes != 1500 {
		t.Errorf("UpdateStorageBytes(500) StorageBytes = %d, want 1500", ns.StorageBytes)
	}

	// Decrease storage
	ns.UpdateStorageBytes(-300)
	if ns.StorageBytes != 1200 {
		t.Errorf("UpdateStorageBytes(-300) StorageBytes = %d, want 1200", ns.StorageBytes)
	}

	// Decrease below zero should floor at 0
	ns.UpdateStorageBytes(-2000)
	if ns.StorageBytes != 0 {
		t.Errorf("UpdateStorageBytes(-2000) StorageBytes = %d, want 0", ns.StorageBytes)
	}
}

func TestEstimateVectorBytes(t *testing.T) {
	tests := []struct {
		name          string
		vectorLen     int
		metadataCount int
		minExpected   int64
	}{
		{
			name:          "small vector no metadata",
			vectorLen:     128,
			metadataCount: 0,
			minExpected:   500, // At least base + 128*4
		},
		{
			name:          "large vector with metadata",
			vectorLen:     1536,
			metadataCount: 10,
			minExpected:   6000, // At least base + 1536*4 + 10*64
		},
		{
			name:          "empty vector",
			vectorLen:     0,
			metadataCount: 0,
			minExpected:   30, // At least base overhead
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EstimateVectorBytes(tt.vectorLen, tt.metadataCount)
			if result < tt.minExpected {
				t.Errorf("EstimateVectorBytes(%d, %d) = %d, want >= %d",
					tt.vectorLen, tt.metadataCount, result, tt.minExpected)
			}
		})
	}
}

func TestNamespaceState_CombinedQuotaChecks(t *testing.T) {
	ns := &NamespaceState{
		Name:         "combined-test",
		Dimension:    128,
		VectorCount:  90,
		TenantID:     "tenant-combined",
		StorageBytes: 900000,
		Quotas: &NamespaceQuotas{
			MaxVectors:      100,
			MaxStorageBytes: 1000000,
			MaxBatchSize:    50,
		},
	}

	// Check that vector limit is enforced first
	err := ns.CheckQuota(15, 50000)
	if err == nil {
		t.Error("CheckQuota() should fail - vector limit exceeded")
	}

	// Check that storage limit is enforced
	ns.VectorCount = 50 // Reset to allow vector check to pass
	err = ns.CheckQuota(10, 200000)
	if err == nil {
		t.Error("CheckQuota() should fail - storage limit exceeded")
	}

	// Check that batch size is enforced
	err = ns.CheckBatchSizeQuota(100)
	if err == nil {
		t.Error("CheckBatchSizeQuota() should fail - batch size exceeded")
	}

	// All within limits should pass
	err = ns.CheckQuota(5, 50000)
	if err != nil {
		t.Errorf("CheckQuota() should pass: %v", err)
	}
	err = ns.CheckBatchSizeQuota(30)
	if err != nil {
		t.Errorf("CheckBatchSizeQuota() should pass: %v", err)
	}
}
