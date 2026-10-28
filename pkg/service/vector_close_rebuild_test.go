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

// The rebuild half of vector_close_test.go. It is tagged because of what a
// rebuild does, not because of what it compiles against: without
// qrdb_vector, hnsw.NewHNSWIndex is the stub that returns ErrNotCompiled, so
// a rebuild fails before it scans storage and never reaches the gate these
// tests hold it at.

package service

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// TestVectorClose_WaitsForAsyncRebuild holds an async index rebuild inside
// storage.ScanNamespace until its context ends, closes the service, and
// asserts Close cancelled it and returned only after the job had finished.
func TestVectorClose_WaitsForAsyncRebuild(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "rebuild-close-ns", 3, 0)
	guard := &closeGuard{}
	entered := make(chan struct{})
	var returned atomic.Bool

	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		guard.touch()
		close(entered)
		<-ctx.Done()
		returned.Store(true)
		return nil, ctx.Err()
	}

	resp, err := svc.RebuildIndex(context.Background(), &RebuildIndexRequest{
		Namespace: "rebuild-close-ns",
		Async:     true,
	})
	require.NoError(t, err)
	<-entered

	require.NoError(t, svc.Close())
	assert.True(t, returned.Load(), "Close returned before the async rebuild stopped")

	svc.jobMu.RLock()
	job := svc.rebuildJobs[resp.JobID]
	svc.jobMu.RUnlock()
	require.NotNil(t, job)
	job.mu.RLock()
	inProgress, jobErr := job.InProgress, job.Error
	job.mu.RUnlock()
	assert.False(t, inProgress)
	assert.ErrorIs(t, jobErr, context.Canceled)

	guard.closed.Store(true)
	assert.False(t, guard.touched.Load(), "a rebuild touched storage after Close")
}

// TestVectorPerformIndexRebuild_CancelledBeforeScan asserts a rebuild whose
// context has ended stops before scanning storage.
func TestVectorPerformIndexRebuild_CancelledBeforeScan(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "cancel-rebuild-ns", 3, 0)
	var reached atomic.Bool
	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		reached.Store(true)
		return nil, nil
	}

	job := &RebuildJobState{JobID: "job-cancel", InProgress: true}
	svc.rebuildJobs["job-cancel"] = job

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := svc.performIndexRebuild(ctx, "job-cancel", "cancel-rebuild-ns", nil)
	opErr := requireErrorAs[*VectorOperationError](t, err)
	assert.ErrorIs(t, opErr, context.Canceled)
	assert.False(t, reached.Load())
	assert.ErrorIs(t, job.Error, context.Canceled)
	assert.False(t, job.InProgress)
}
