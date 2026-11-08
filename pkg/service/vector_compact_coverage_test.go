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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// =============================================================================
// Mock HNSW Index for coverage tests
// =============================================================================

type coverageMockHNSWIndex struct {
	insertFn   func(id uint64, vec []float32) error
	deleteFn   func(id uint64) error
	searchFn   func(query []float32, k int) ([]*interfaces.HNSWSearchResult, error)
	countFn    func() int64
	getStatsFn func() *interfaces.HNSWStats
}

func (m *coverageMockHNSWIndex) Insert(id uint64, vec []float32) error {
	if m.insertFn != nil {
		return m.insertFn(id, vec)
	}
	return nil
}

func (m *coverageMockHNSWIndex) Delete(id uint64) error {
	if m.deleteFn != nil {
		return m.deleteFn(id)
	}
	return nil
}

func (m *coverageMockHNSWIndex) Search(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
	if m.searchFn != nil {
		return m.searchFn(query, k)
	}
	return nil, nil
}

func (m *coverageMockHNSWIndex) Count() int64 {
	if m.countFn != nil {
		return m.countFn()
	}
	return 0
}

func (m *coverageMockHNSWIndex) GetStats() *interfaces.HNSWStats {
	if m.getStatsFn != nil {
		return m.getStatsFn()
	}
	return nil
}

// =============================================================================
// Helper: create vectorService with direct access to internal fields
// =============================================================================

// =============================================================================
// performCompaction Tests
// =============================================================================

func TestVectorPerformCompaction_Success(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	// Mock stats returning pre and post compaction sizes
	callCount := 0
	storage.getStatsFn = func(ctx context.Context, namespace string) (*vector.NamespaceStats, error) {
		callCount++
		if callCount == 1 {
			// Pre-compaction: 4096 bytes
			return &vector.NamespaceStats{
				Namespace: namespace,
				SizeBytes: 4096,
			}, nil
		}
		// Post-compaction: 1024 bytes (3072 freed)
		return &vector.NamespaceStats{
			Namespace: namespace,
			SizeBytes: 1024,
		}, nil
	}

	storage.compactFn = func(ctx context.Context, namespace string) error {
		return nil
	}

	// Create the compaction job state manually (normally done by CompactNamespace)
	opID := "test-compact-op"
	job := &CompactionJobState{
		OperationID: opID,
		Namespace:   "compact-ns",
		InProgress:  true,
		TotalBytes:  4096,
		StartedAt:   time.Now(),
	}
	svc.jobMu.Lock()
	svc.compactionJobs[opID] = job
	svc.jobMu.Unlock()

	err := svc.performCompaction(context.Background(), opID, "compact-ns")

	require.NoError(t, err)

	job.mu.RLock()
	defer job.mu.RUnlock()
	assert.False(t, job.InProgress)
	assert.NotNil(t, job.CompletedAt)
	assert.Equal(t, int64(3072), job.BytesCompacted)
	assert.Nil(t, job.Error)
}

func TestVectorPerformCompaction_JobNotFound(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	err := svc.performCompaction(context.Background(), "nonexistent-op", "compact-ns")

	require.Error(t, err)
	requireErrorAs[*VectorJobNotFoundError](t, err)
}

func TestVectorPerformCompaction_StorageCompactError(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	storage.compactFn = func(ctx context.Context, namespace string) error {
		return errors.New("disk full")
	}

	opID := "test-compact-error"
	job := &CompactionJobState{
		OperationID: opID,
		Namespace:   "compact-ns",
		InProgress:  true,
		TotalBytes:  4096,
		StartedAt:   time.Now(),
	}
	svc.jobMu.Lock()
	svc.compactionJobs[opID] = job
	svc.jobMu.Unlock()

	err := svc.performCompaction(context.Background(), opID, "compact-ns")

	require.Error(t, err)
	requireErrorAs[*VectorOperationError](t, err)

	job.mu.RLock()
	defer job.mu.RUnlock()
	assert.False(t, job.InProgress)
	assert.NotNil(t, job.Error)
	assert.Contains(t, job.Error.Error(), "disk full")
}

func TestVectorPerformCompaction_PostStatsLargerThanPre(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	// Post size larger than pre size (negative diff clamped to 0)
	callCount := 0
	storage.getStatsFn = func(ctx context.Context, namespace string) (*vector.NamespaceStats, error) {
		callCount++
		if callCount == 1 {
			return &vector.NamespaceStats{SizeBytes: 1024}, nil
		}
		return &vector.NamespaceStats{SizeBytes: 2048}, nil
	}

	opID := "test-compact-neg"
	job := &CompactionJobState{
		OperationID: opID,
		Namespace:   "compact-ns",
		InProgress:  true,
		TotalBytes:  4096,
		StartedAt:   time.Now(),
	}
	svc.jobMu.Lock()
	svc.compactionJobs[opID] = job
	svc.jobMu.Unlock()

	err := svc.performCompaction(context.Background(), opID, "compact-ns")

	require.NoError(t, err)

	job.mu.RLock()
	defer job.mu.RUnlock()
	assert.Equal(t, int64(0), job.BytesCompacted)
}

func TestVectorPerformCompaction_NilPreStats(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	callCount := 0
	storage.getStatsFn = func(ctx context.Context, namespace string) (*vector.NamespaceStats, error) {
		callCount++
		if callCount == 1 {
			return nil, errors.New("stats unavailable")
		}
		return nil, errors.New("stats unavailable")
	}

	opID := "test-compact-nil-stats"
	job := &CompactionJobState{
		OperationID: opID,
		Namespace:   "compact-ns",
		InProgress:  true,
		TotalBytes:  4096,
		StartedAt:   time.Now(),
	}
	svc.jobMu.Lock()
	svc.compactionJobs[opID] = job
	svc.jobMu.Unlock()

	err := svc.performCompaction(context.Background(), opID, "compact-ns")

	require.NoError(t, err)

	job.mu.RLock()
	defer job.mu.RUnlock()
	assert.Equal(t, int64(0), job.BytesCompacted)
}

// =============================================================================
// CompactNamespace Tests
// =============================================================================

func TestVectorCompactNamespace_SyncSuccess(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	storage.compactFn = func(ctx context.Context, namespace string) error {
		return nil
	}

	callCount := 0
	storage.getStatsFn = func(ctx context.Context, namespace string) (*vector.NamespaceStats, error) {
		callCount++
		if callCount == 1 {
			return &vector.NamespaceStats{SizeBytes: 4096}, nil
		}
		return &vector.NamespaceStats{SizeBytes: 2048}, nil
	}

	resp, err := svc.CompactNamespace(context.Background(), &CompactNamespaceRequest{
		Namespace: "compact-ns",
		Async:     false,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.Success)
	assert.NotEmpty(t, resp.OperationID)
	assert.Greater(t, resp.StartedAt, int64(0))
	assert.NotNil(t, resp.CompletedAt)
	assert.Equal(t, int64(2048), resp.BytesCompacted)
}

func TestVectorCompactNamespace_NamespaceNotFound(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	resp, err := svc.CompactNamespace(context.Background(), &CompactNamespaceRequest{
		Namespace: "nonexistent",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*NamespaceNotFoundError](t, err)
}

func TestVectorCompactNamespace_ServiceClosed(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	svc.mu.Lock()
	svc.closed = true
	svc.mu.Unlock()

	resp, err := svc.CompactNamespace(context.Background(), &CompactNamespaceRequest{
		Namespace: "compact-ns",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*VectorServiceClosedError](t, err)
}

func TestVectorCompactNamespace_OtherTenantGetsNotFound(t *testing.T) {
	// Scoped namespaces ensure a different tenant's CompactNamespace call
	// resolves to their own (non-existent) scoped key → ErrNotFound.
	svc, _ := newCoverageVecSvc(t, "tenant-ns", 128, 4096)

	ctx := tenant.WithTenantID(context.Background(), "tenant-other")

	resp, err := svc.CompactNamespace(ctx, &CompactNamespaceRequest{
		Namespace: "tenant-ns",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*NamespaceNotFoundError](t, err)
}

func TestVectorCompactNamespace_SyncStorageError(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	storage.compactFn = func(ctx context.Context, namespace string) error {
		return errors.New("io error during compaction")
	}

	resp, err := svc.CompactNamespace(context.Background(), &CompactNamespaceRequest{
		Namespace: "compact-ns",
		Async:     false,
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*VectorOperationError](t, err)
}

// =============================================================================
// GetCompactionProgress Tests
// =============================================================================

func TestVectorGetCompactionProgress_ByOperationID_Completed(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	completedAt := time.Now()
	opID := "completed-op"
	job := &CompactionJobState{
		OperationID:    opID,
		Namespace:      "compact-ns",
		InProgress:     false,
		BytesCompacted: 2048,
		TotalBytes:     4096,
		StartedAt:      completedAt.Add(-5 * time.Second),
		CompletedAt:    &completedAt,
		Error:          nil,
	}
	svc.jobMu.Lock()
	svc.compactionJobs[opID] = job
	svc.jobMu.Unlock()

	resp, err := svc.GetCompactionProgress(context.Background(), &GetCompactionProgressRequest{
		OperationID: opID,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, opID, resp.OperationID)
	assert.Equal(t, "compact-ns", resp.Namespace)
	assert.Equal(t, int64(2048), resp.BytesCompacted)
	assert.Equal(t, int64(4096), resp.TotalBytes)
	assert.Equal(t, float64(100), resp.PercentComplete)
	assert.False(t, resp.InProgress)
	assert.NotNil(t, resp.CompletedAt)
	assert.Empty(t, resp.Error)
}

func TestVectorGetCompactionProgress_ByOperationID_WithError(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	completedAt := time.Now()
	opID := "failed-op"
	job := &CompactionJobState{
		OperationID:    opID,
		Namespace:      "compact-ns",
		InProgress:     false,
		BytesCompacted: 0,
		TotalBytes:     4096,
		StartedAt:      completedAt.Add(-3 * time.Second),
		CompletedAt:    &completedAt,
		Error:          errors.New("compaction failed: disk full"),
	}
	svc.jobMu.Lock()
	svc.compactionJobs[opID] = job
	svc.jobMu.Unlock()

	resp, err := svc.GetCompactionProgress(context.Background(), &GetCompactionProgressRequest{
		OperationID: opID,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, opID, resp.OperationID)
	assert.Contains(t, resp.Error, "compaction failed")
	// percentComplete should NOT be 100 because job has an error
	assert.Equal(t, float64(0), resp.PercentComplete)
}

func TestVectorGetCompactionProgress_ByNamespace(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	completedAt := time.Now()
	// Insert two jobs for the same namespace; the latest should be returned
	job1 := &CompactionJobState{
		OperationID: "old-op",
		Namespace:   "compact-ns",
		InProgress:  false,
		TotalBytes:  1024,
		StartedAt:   completedAt.Add(-10 * time.Second),
		CompletedAt: &completedAt,
	}
	job2 := &CompactionJobState{
		OperationID:    "latest-op",
		Namespace:      "compact-ns",
		InProgress:     false,
		BytesCompacted: 512,
		TotalBytes:     2048,
		StartedAt:      completedAt.Add(-2 * time.Second),
		CompletedAt:    &completedAt,
	}
	svc.jobMu.Lock()
	svc.compactionJobs["old-op"] = job1
	svc.compactionJobs["latest-op"] = job2
	svc.jobMu.Unlock()

	resp, err := svc.GetCompactionProgress(context.Background(), &GetCompactionProgressRequest{
		Namespace: "compact-ns",
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "latest-op", resp.OperationID)
	assert.Equal(t, "compact-ns", resp.Namespace)
}

func TestVectorGetCompactionProgress_ByNamespace_NotFound(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	resp, err := svc.GetCompactionProgress(context.Background(), &GetCompactionProgressRequest{
		Namespace: "nonexistent-ns",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*VectorJobNotFoundError](t, err)
}

func TestVectorGetCompactionProgress_ServiceClosed(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	svc.mu.Lock()
	svc.closed = true
	svc.mu.Unlock()

	resp, err := svc.GetCompactionProgress(context.Background(), &GetCompactionProgressRequest{
		OperationID: "any-op",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*VectorServiceClosedError](t, err)
}

func TestVectorGetCompactionProgress_OperationIDNotFound(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	resp, err := svc.GetCompactionProgress(context.Background(), &GetCompactionProgressRequest{
		OperationID: "nonexistent-op-id",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*VectorJobNotFoundError](t, err)
}

func TestVectorGetCompactionProgress_InProgressWithPartialBytes(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "compact-ns", 128, 4096)

	opID := "in-progress-op"
	job := &CompactionJobState{
		OperationID:    opID,
		Namespace:      "compact-ns",
		InProgress:     true,
		BytesCompacted: 1024,
		TotalBytes:     4096,
		StartedAt:      time.Now().Add(-2 * time.Second),
	}
	svc.jobMu.Lock()
	svc.compactionJobs[opID] = job
	svc.jobMu.Unlock()

	resp, err := svc.GetCompactionProgress(context.Background(), &GetCompactionProgressRequest{
		OperationID: opID,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.True(t, resp.InProgress)
	assert.Equal(t, float64(25), resp.PercentComplete)
	assert.Nil(t, resp.CompletedAt)
}

// =============================================================================
// performIndexRebuild Tests
// =============================================================================

func TestVectorPerformIndexRebuild_Success(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	// Return vectors for the scan
	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		if opts.Offset == 0 {
			return []*vector.VectorValue{
				{ID: 1, Embedding: []float32{1.0, 2.0, 3.0}},
				{ID: 2, Embedding: []float32{4.0, 5.0, 6.0}},
			}, nil
		}
		return nil, nil
	}

	jobID := "rebuild-job-1"
	job := &RebuildJobState{
		JobID:        jobID,
		Namespace:    "rebuild-ns",
		InProgress:   true,
		TotalVectors: 2,
		StartedAt:    time.Now(),
	}
	svc.jobMu.Lock()
	svc.rebuildJobs[jobID] = job
	svc.jobMu.Unlock()

	err := svc.performIndexRebuild(context.Background(), jobID, "rebuild-ns", &vector.NamespaceInfo{Name: "rebuild-ns", Dimension: 3}, nil)

	require.NoError(t, err)

	job.mu.RLock()
	defer job.mu.RUnlock()
	assert.False(t, job.InProgress)
	assert.NotNil(t, job.CompletedAt)
	assert.Equal(t, uint64(2), job.VectorsProcessed)
	assert.Nil(t, job.Error)

	// Verify the index was stored
	svc.indexMu.RLock()
	defer svc.indexMu.RUnlock()
	_, indexExists := svc.hnswIndexes[scopedNamespace(tenantid.System, "rebuild-ns")]
	assert.True(t, indexExists)
}

func TestVectorPerformIndexRebuild_JobNotFound(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	err := svc.performIndexRebuild(context.Background(), "nonexistent-job", "rebuild-ns", &vector.NamespaceInfo{Name: "rebuild-ns", Dimension: 3}, nil)

	require.Error(t, err)
	requireErrorAs[*VectorJobNotFoundError](t, err)
}

func TestVectorPerformIndexRebuild_ScanError(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return nil, errors.New("storage scan failure")
	}

	jobID := "rebuild-scan-error"
	job := &RebuildJobState{
		JobID:        jobID,
		Namespace:    "rebuild-ns",
		InProgress:   true,
		TotalVectors: 10,
		StartedAt:    time.Now(),
	}
	svc.jobMu.Lock()
	svc.rebuildJobs[jobID] = job
	svc.jobMu.Unlock()

	err := svc.performIndexRebuild(context.Background(), jobID, "rebuild-ns", &vector.NamespaceInfo{Name: "rebuild-ns", Dimension: 3}, nil)

	require.Error(t, err)
	requireErrorAs[*VectorOperationError](t, err)

	job.mu.RLock()
	defer job.mu.RUnlock()
	assert.NotNil(t, job.Error)
	assert.False(t, job.InProgress)
}

func TestVectorPerformIndexRebuild_WithCustomConfig(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return nil, nil // No vectors
	}

	jobID := "rebuild-custom-config"
	job := &RebuildJobState{
		JobID:        jobID,
		Namespace:    "rebuild-ns",
		InProgress:   true,
		TotalVectors: 0,
		StartedAt:    time.Now(),
	}
	svc.jobMu.Lock()
	svc.rebuildJobs[jobID] = job
	svc.jobMu.Unlock()

	indexConfig := map[string]string{
		"m":               "32",
		"ef_construction": "400",
		"ef_search":       "100",
	}

	err := svc.performIndexRebuild(context.Background(), jobID, "rebuild-ns", &vector.NamespaceInfo{Name: "rebuild-ns", Dimension: 3}, indexConfig)

	require.NoError(t, err)

	job.mu.RLock()
	defer job.mu.RUnlock()
	assert.False(t, job.InProgress)
	assert.NotNil(t, job.CompletedAt)
}

func TestVectorPerformIndexRebuild_InvalidConfigValues(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return nil, nil
	}

	jobID := "rebuild-bad-config"
	job := &RebuildJobState{
		JobID:        jobID,
		Namespace:    "rebuild-ns",
		InProgress:   true,
		TotalVectors: 0,
		StartedAt:    time.Now(),
	}
	svc.jobMu.Lock()
	svc.rebuildJobs[jobID] = job
	svc.jobMu.Unlock()

	// Invalid config values should be logged as warnings but not fail the rebuild
	indexConfig := map[string]string{
		"m":               "not-a-number",
		"ef_construction": "also-invalid",
		"ef_search":       "bad-value",
	}

	err := svc.performIndexRebuild(context.Background(), jobID, "rebuild-ns", &vector.NamespaceInfo{Name: "rebuild-ns", Dimension: 3}, indexConfig)

	require.NoError(t, err)
}

func TestVectorPerformIndexRebuild_MultipleBatches(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "rebuild-ns", 2, 0)

	// Return exactly 1000 vectors on first call (batch size limit),
	// then return fewer on second call to trigger the break
	callCount := 0
	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		callCount++
		if callCount == 1 {
			vecs := make([]*vector.VectorValue, 1000)
			for i := range vecs {
				vecs[i] = &vector.VectorValue{
					ID:        uint64(i + 1),
					Embedding: []float32{float32(i), float32(i + 1)},
				}
			}
			return vecs, nil
		}
		// Second call: return fewer than limit (3 vectors)
		return []*vector.VectorValue{
			{ID: 1001, Embedding: []float32{0.1, 0.2}},
			{ID: 1002, Embedding: []float32{0.3, 0.4}},
			{ID: 1003, Embedding: []float32{0.5, 0.6}},
		}, nil
	}

	jobID := "rebuild-multi-batch"
	job := &RebuildJobState{
		JobID:        jobID,
		Namespace:    "rebuild-ns",
		InProgress:   true,
		TotalVectors: 1003,
		StartedAt:    time.Now(),
	}
	svc.jobMu.Lock()
	svc.rebuildJobs[jobID] = job
	svc.jobMu.Unlock()

	err := svc.performIndexRebuild(context.Background(), jobID, "rebuild-ns", &vector.NamespaceInfo{Name: "rebuild-ns", Dimension: 3}, nil)

	require.NoError(t, err)

	job.mu.RLock()
	defer job.mu.RUnlock()
	assert.Equal(t, uint64(1003), job.VectorsProcessed)
}

// =============================================================================
// RebuildIndex Tests
// =============================================================================

func TestVectorRebuildIndex_SyncWithVectors(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	// Update namespace info to show vector count. Use the scoped name
	// since RebuildIndex passes the scoped key to storage.
	scoped := scopedNamespace(tenantid.System, "rebuild-ns")
	storage.getNamespaceInfoFn = func(ctx context.Context, ns string) (*vector.NamespaceInfo, error) {
		if ns == scoped {
			return &vector.NamespaceInfo{
				Name:        scoped,
				Dimension:   3,
				VectorCount: 2,
			}, nil
		}
		return nil, vector.ErrNamespaceNotFound
	}

	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		if opts.Offset == 0 {
			return []*vector.VectorValue{
				{ID: 1, Embedding: []float32{1.0, 2.0, 3.0}},
				{ID: 2, Embedding: []float32{4.0, 5.0, 6.0}},
			}, nil
		}
		return nil, nil
	}

	resp, err := svc.RebuildIndex(context.Background(), &RebuildIndexRequest{
		Namespace: "rebuild-ns",
		Async:     false,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "completed", resp.Status)
	assert.True(t, resp.Success)
	assert.NotEmpty(t, resp.JobID)
	assert.Equal(t, uint64(2), resp.TotalVectors)
	assert.Equal(t, float64(100), resp.PercentComplete)
}

func TestVectorRebuildIndex_AsyncReturnsImmediately(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	// Use a channel to block the scan until we verify the response
	scanBlocked := make(chan struct{})
	scanDone := make(chan struct{})

	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		<-scanBlocked
		close(scanDone)
		return nil, nil
	}

	resp, err := svc.RebuildIndex(context.Background(), &RebuildIndexRequest{
		Namespace: "rebuild-ns",
		Async:     true,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "in_progress", resp.Status)
	assert.False(t, resp.Success)
	assert.NotEmpty(t, resp.JobID)

	// Unblock the background goroutine and wait for it to complete
	close(scanBlocked)
	<-scanDone
}

func TestVectorRebuildIndex_NamespaceNotFound(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "", 0, 0) // No namespace pre-populated

	resp, err := svc.RebuildIndex(context.Background(), &RebuildIndexRequest{
		Namespace: "nonexistent",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*NamespaceNotFoundError](t, err)
}

func TestVectorRebuildIndex_SyncScanError(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		return nil, errors.New("disk read error")
	}

	resp, err := svc.RebuildIndex(context.Background(), &RebuildIndexRequest{
		Namespace: "rebuild-ns",
		Async:     false,
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*VectorOperationError](t, err)
}

// =============================================================================
// GetRebuildProgress Tests
// =============================================================================

func TestVectorGetRebuildProgress_CompletedJob(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	completedAt := time.Now()
	jobID := "completed-rebuild"
	job := &RebuildJobState{
		JobID:            jobID,
		Namespace:        "rebuild-ns",
		InProgress:       false,
		VectorsProcessed: 500,
		TotalVectors:     500,
		StartedAt:        completedAt.Add(-10 * time.Second),
		CompletedAt:      &completedAt,
		Error:            nil,
	}
	svc.jobMu.Lock()
	svc.rebuildJobs[jobID] = job
	svc.jobMu.Unlock()

	resp, err := svc.GetRebuildProgress(context.Background(), &GetRebuildProgressRequest{
		JobID: jobID,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, jobID, resp.JobID)
	assert.Equal(t, "rebuild-ns", resp.Namespace)
	assert.Equal(t, "completed", resp.Status)
	assert.Equal(t, float64(100), resp.PercentComplete)
	assert.Equal(t, uint64(500), resp.VectorsProcessed)
	assert.NotZero(t, resp.CompletedAt)
	assert.Empty(t, resp.Error)
}

func TestVectorGetRebuildProgress_FailedJob(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	completedAt := time.Now()
	jobID := "failed-rebuild"
	job := &RebuildJobState{
		JobID:            jobID,
		Namespace:        "rebuild-ns",
		InProgress:       false,
		VectorsProcessed: 100,
		TotalVectors:     500,
		StartedAt:        completedAt.Add(-5 * time.Second),
		CompletedAt:      &completedAt,
		Error:            errors.New("scan vectors failed"),
	}
	svc.jobMu.Lock()
	svc.rebuildJobs[jobID] = job
	svc.jobMu.Unlock()

	resp, err := svc.GetRebuildProgress(context.Background(), &GetRebuildProgressRequest{
		JobID: jobID,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "failed", resp.Status)
	assert.Contains(t, resp.Error, "scan vectors failed")
	assert.Equal(t, uint64(100), resp.VectorsProcessed)
}

func TestVectorGetRebuildProgress_InProgressWithEstimatedTime(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	jobID := "in-progress-rebuild"
	job := &RebuildJobState{
		JobID:            jobID,
		Namespace:        "rebuild-ns",
		InProgress:       true,
		VectorsProcessed: 250,
		TotalVectors:     1000,
		StartedAt:        time.Now().Add(-5 * time.Second),
	}
	svc.jobMu.Lock()
	svc.rebuildJobs[jobID] = job
	svc.jobMu.Unlock()

	resp, err := svc.GetRebuildProgress(context.Background(), &GetRebuildProgressRequest{
		JobID: jobID,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "running", resp.Status)
	assert.Equal(t, float64(25), resp.PercentComplete)
	assert.Greater(t, resp.EstimatedTimeRemainingMs, int64(0))
	assert.Greater(t, resp.ElapsedTimeMs, int64(0))
}

func TestVectorGetRebuildProgress_ByNamespace(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	completedAt := time.Now()
	// Add two jobs for the same namespace; lookup by namespace returns the latest
	job1 := &RebuildJobState{
		JobID:            "old-rebuild",
		Namespace:        "rebuild-ns",
		InProgress:       false,
		VectorsProcessed: 10,
		TotalVectors:     10,
		StartedAt:        completedAt.Add(-30 * time.Second),
		CompletedAt:      &completedAt,
	}
	job2 := &RebuildJobState{
		JobID:            "latest-rebuild",
		Namespace:        "rebuild-ns",
		InProgress:       false,
		VectorsProcessed: 20,
		TotalVectors:     20,
		StartedAt:        completedAt.Add(-5 * time.Second),
		CompletedAt:      &completedAt,
	}
	svc.jobMu.Lock()
	svc.rebuildJobs["old-rebuild"] = job1
	svc.rebuildJobs["latest-rebuild"] = job2
	svc.jobMu.Unlock()

	resp, err := svc.GetRebuildProgress(context.Background(), &GetRebuildProgressRequest{
		Namespace: "rebuild-ns",
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "latest-rebuild", resp.JobID)
}

func TestVectorGetRebuildProgress_NotFoundByJobID(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	resp, err := svc.GetRebuildProgress(context.Background(), &GetRebuildProgressRequest{
		JobID: "nonexistent-job",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*VectorJobNotFoundError](t, err)
}

func TestVectorGetRebuildProgress_NotFoundByNamespace(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	resp, err := svc.GetRebuildProgress(context.Background(), &GetRebuildProgressRequest{
		Namespace: "nonexistent-ns",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*VectorJobNotFoundError](t, err)
}

func TestVectorGetRebuildProgress_PendingJob(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "rebuild-ns", 3, 0)

	jobID := "pending-rebuild"
	job := &RebuildJobState{
		JobID:            jobID,
		Namespace:        "rebuild-ns",
		InProgress:       false, // Not yet started
		VectorsProcessed: 0,
		TotalVectors:     100,
		StartedAt:        time.Now(),
	}
	svc.jobMu.Lock()
	svc.rebuildJobs[jobID] = job
	svc.jobMu.Unlock()

	resp, err := svc.GetRebuildProgress(context.Background(), &GetRebuildProgressRequest{
		JobID: jobID,
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "pending", resp.Status)
	assert.Equal(t, float64(0), resp.PercentComplete)
}

// =============================================================================
// GetIndexStats Tests
// =============================================================================

func TestVectorGetIndexStats_WithHNSWIndex(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "stats-ns", 128, 0)

	// Inject a mock HNSW index into the service
	mockIndex := &coverageMockHNSWIndex{
		countFn: func() int64 { return 500 },
		getStatsFn: func() *interfaces.HNSWStats {
			return &interfaces.HNSWStats{
				NodeCount:      500,
				MaxLayer:       3,
				AvgConnections: 16.0,
				Dimensions:     128,
			}
		},
	}
	svc.indexMu.Lock()
	svc.hnswIndexes[scopedNamespace(tenantid.System, "stats-ns")] = mockIndex
	svc.indexMu.Unlock()

	resp, err := svc.GetIndexStats(context.Background(), &GetIndexStatsRequest{
		Namespace: "stats-ns",
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "stats-ns", resp.Namespace)
	assert.Equal(t, "HNSW", resp.IndexType)
	assert.Equal(t, uint64(500), resp.VectorCount)
	assert.Equal(t, uint32(128), resp.Dimension)
	assert.Greater(t, resp.IndexSizeBytes, uint64(0))
	assert.Greater(t, resp.MemoryUsageBytes, uint64(0))
	assert.NotNil(t, resp.SearchPerformance)
	assert.NotZero(t, resp.Timestamp)
}

func TestVectorGetIndexStats_WithoutIndex(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "stats-ns", 128, 0)

	resp, err := svc.GetIndexStats(context.Background(), &GetIndexStatsRequest{
		Namespace: "stats-ns",
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "stats-ns", resp.Namespace)
	assert.Equal(t, uint64(0), resp.VectorCount)
	assert.Equal(t, uint64(0), resp.IndexSizeBytes)
	assert.Equal(t, uint64(0), resp.MemoryUsageBytes)
}

func TestVectorGetIndexStats_NamespaceNotFound(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "stats-ns", 128, 0)

	resp, err := svc.GetIndexStats(context.Background(), &GetIndexStatsRequest{
		Namespace: "nonexistent",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*NamespaceNotFoundError](t, err)
}

func TestVectorGetIndexStats_ServiceClosed(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "stats-ns", 128, 0)

	svc.mu.Lock()
	svc.closed = true
	svc.mu.Unlock()

	resp, err := svc.GetIndexStats(context.Background(), &GetIndexStatsRequest{
		Namespace: "stats-ns",
	})

	require.Error(t, err)
	assert.Nil(t, resp)
	requireErrorAs[*VectorServiceClosedError](t, err)
}

func TestVectorGetIndexStats_NilStats(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "stats-ns", 128, 0)

	// Index that returns nil stats
	mockIndex := &coverageMockHNSWIndex{
		countFn:    func() int64 { return 10 },
		getStatsFn: func() *interfaces.HNSWStats { return nil },
	}
	svc.indexMu.Lock()
	svc.hnswIndexes[scopedNamespace(tenantid.System, "stats-ns")] = mockIndex
	svc.indexMu.Unlock()

	resp, err := svc.GetIndexStats(context.Background(), &GetIndexStatsRequest{
		Namespace: "stats-ns",
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, uint64(10), resp.VectorCount)
	assert.Equal(t, uint64(0), resp.IndexSizeBytes)
	assert.Equal(t, uint64(0), resp.MemoryUsageBytes)
}

func TestVectorGetIndexStats_ZeroNodeCountStats(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "stats-ns", 128, 0)

	// Index that returns stats with zero node count
	mockIndex := &coverageMockHNSWIndex{
		countFn: func() int64 { return 0 },
		getStatsFn: func() *interfaces.HNSWStats {
			return &interfaces.HNSWStats{
				NodeCount:      0,
				MaxLayer:       0,
				AvgConnections: 0,
				Dimensions:     128,
			}
		},
	}
	svc.indexMu.Lock()
	svc.hnswIndexes[scopedNamespace(tenantid.System, "stats-ns")] = mockIndex
	svc.indexMu.Unlock()

	resp, err := svc.GetIndexStats(context.Background(), &GetIndexStatsRequest{
		Namespace: "stats-ns",
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, uint64(0), resp.VectorCount)
	assert.Equal(t, uint64(0), resp.IndexSizeBytes)
}

func TestVectorGetIndexStats_MaxLayerZero(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "stats-ns", 64, 0)

	// Stats with MaxLayer=0 should use maxLayers=1 in the calculation
	mockIndex := &coverageMockHNSWIndex{
		countFn: func() int64 { return 100 },
		getStatsFn: func() *interfaces.HNSWStats {
			return &interfaces.HNSWStats{
				NodeCount:      100,
				MaxLayer:       0,
				AvgConnections: 16.0,
				Dimensions:     64,
			}
		},
	}
	svc.indexMu.Lock()
	svc.hnswIndexes[scopedNamespace(tenantid.System, "stats-ns")] = mockIndex
	svc.indexMu.Unlock()

	resp, err := svc.GetIndexStats(context.Background(), &GetIndexStatsRequest{
		Namespace: "stats-ns",
	})

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, uint64(100), resp.VectorCount)
	assert.Greater(t, resp.IndexSizeBytes, uint64(0))
	// memoryUsageBytes = indexSizeBytes + (indexSizeBytes / 5)
	assert.Greater(t, resp.MemoryUsageBytes, resp.IndexSizeBytes)
}
