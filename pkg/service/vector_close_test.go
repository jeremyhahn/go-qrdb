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

package service

import (
	"context"
	"io"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// closeGuard stands in for the storage owner's Close: once closed is set,
// any storage call the service makes is the #1171 defect, a background job
// touching storage its owner has closed.
type closeGuard struct {
	closed  atomic.Bool
	touched atomic.Bool
}

func (g *closeGuard) touch() {
	if g.closed.Load() {
		g.touched.Store(true)
	}
}

// TestVectorClose_WaitsForAsyncCompaction holds an async compaction inside
// storage.Compact until its context ends, closes the service, and asserts
// Close cancelled it and returned only after the job had finished.
func TestVectorClose_WaitsForAsyncCompaction(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "close-ns", 3, 4096)
	guard := &closeGuard{}
	entered := make(chan struct{})
	var returned atomic.Bool

	storage.getStatsFn = func(ctx context.Context, namespace string) (*vector.NamespaceStats, error) {
		guard.touch()
		return &vector.NamespaceStats{SizeBytes: 4096}, nil
	}
	storage.compactFn = func(ctx context.Context, namespace string) error {
		guard.touch()
		close(entered)
		<-ctx.Done()
		returned.Store(true)
		return ctx.Err()
	}

	resp, err := svc.CompactNamespace(context.Background(), &CompactNamespaceRequest{
		Namespace: "close-ns",
		Async:     true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.OperationID)
	<-entered

	require.NoError(t, svc.Close())
	assert.True(t, returned.Load(), "Close returned before the async compaction stopped")

	svc.jobMu.RLock()
	job := svc.compactionJobs[resp.OperationID]
	svc.jobMu.RUnlock()
	require.NotNil(t, job)
	job.mu.RLock()
	inProgress, jobErr := job.InProgress, job.Error
	job.mu.RUnlock()
	assert.False(t, inProgress)
	assert.ErrorIs(t, jobErr, context.Canceled)

	guard.closed.Store(true)
	require.NoError(t, storage.Close())
	assert.False(t, guard.touched.Load(), "a compaction touched storage after Close")
}

// TestVectorClose_RefusesAsyncJobsAfterClose asserts a compaction or a
// rebuild requested after Close is refused with the typed closed error,
// registers no job and never reaches storage.
func TestVectorClose_RefusesAsyncJobsAfterClose(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "after-close-ns", 3, 4096)
	var reached atomic.Bool
	storage.compactFn = func(ctx context.Context, namespace string) error {
		reached.Store(true)
		return nil
	}
	storage.scanNamespaceFn = func(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
		reached.Store(true)
		return nil, nil
	}

	require.NoError(t, svc.Close())

	for _, async := range []bool{true, false} {
		_, err := svc.CompactNamespace(context.Background(), &CompactNamespaceRequest{
			Namespace: "after-close-ns",
			Async:     async,
		})
		requireErrorAs[*VectorServiceClosedError](t, err)

		_, err = svc.RebuildIndex(context.Background(), &RebuildIndexRequest{
			Namespace: "after-close-ns",
			Async:     async,
		})
		requireErrorAs[*VectorServiceClosedError](t, err)
	}

	assert.False(t, reached.Load())
	svc.jobMu.RLock()
	defer svc.jobMu.RUnlock()
	assert.Empty(t, svc.compactionJobs)
	assert.Empty(t, svc.rebuildJobs)
}

// TestVectorStartBackground_RefusesAfterClose covers the refusal inside
// startBackground itself, which a request reaching it after its own closed
// check -- Close landing between the two -- meets, and the job-map cleanup
// each async path does on that refusal.
func TestVectorStartBackground_RefusesAfterClose(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "", 3, 0)
	require.NoError(t, svc.Close())

	ran := false
	err := svc.startBackground(func(context.Context) { ran = true })
	requireErrorAs[*VectorServiceClosedError](t, err)
	assert.False(t, ran)
}

// TestVectorStartBackground_RunsOnBackgroundContext asserts a job started
// before Close runs on the service's context, live until Close cancels it.
func TestVectorStartBackground_RunsOnBackgroundContext(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "", 3, 0)
	got := make(chan context.Context, 1)
	require.NoError(t, svc.startBackground(func(ctx context.Context) { got <- ctx }))

	ctx := <-got
	require.NoError(t, svc.Close())
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}

// TestVectorClose_Idempotent asserts a second Close is a no-op, and that the
// service is the io.Closer the lifecycle's assertion relies on.
func TestVectorClose_Idempotent(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "", 3, 0)
	var closer io.Closer = svc
	require.NoError(t, closer.Close())
	require.NoError(t, closer.Close())
}

// TestVectorPerformCompaction_CancelledBeforeStart asserts a compaction whose
// context Close has already cancelled records the cancellation and does not
// reach storage.
func TestVectorPerformCompaction_CancelledBeforeStart(t *testing.T) {
	svc, storage := newCoverageVecSvc(t, "cancel-ns", 3, 4096)
	var reached atomic.Bool
	storage.getStatsFn = func(ctx context.Context, namespace string) (*vector.NamespaceStats, error) {
		reached.Store(true)
		return nil, nil
	}
	storage.compactFn = func(ctx context.Context, namespace string) error {
		reached.Store(true)
		return nil
	}

	job := &CompactionJobState{OperationID: "op-cancel", InProgress: true}
	svc.compactionJobs["op-cancel"] = job

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := svc.performCompaction(ctx, "op-cancel", "cancel-ns")
	opErr := requireErrorAs[*VectorOperationError](t, err)
	assert.ErrorIs(t, opErr, context.Canceled)
	assert.False(t, reached.Load())
	assert.ErrorIs(t, job.Error, context.Canceled)
	assert.False(t, job.InProgress)
}
