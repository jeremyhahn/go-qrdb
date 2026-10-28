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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// TestVectorClose_ConcurrentCallersWaitForJobs holds a background job past
// cancellation and closes the service twice concurrently: the second Close,
// begun after the first has already set closed, must not return until the
// held job has stopped either.
func TestVectorClose_ConcurrentCallersWaitForJobs(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "close-ns", 3, 4096)
	cancelled := make(chan struct{})
	release := make(chan struct{})
	var finished atomic.Bool

	require.NoError(t, svc.startBackground(func(ctx context.Context) {
		<-ctx.Done()
		close(cancelled)
		<-release
		finished.Store(true)
	}))

	firstDone := make(chan error, 1)
	go func() { firstDone <- svc.Close() }()
	// The job observed cancellation, so the first Close has set closed.
	<-cancelled

	go close(release)
	require.NoError(t, svc.Close())
	assert.True(t, finished.Load(), "second Close returned before the held job stopped")

	require.NoError(t, <-firstDone)
	assert.True(t, finished.Load(), "first Close returned before the held job stopped")
}

// TestVectorClose_TwoClosersAfterJobsDrain closes an idle service from two
// goroutines; both return nil and the service stays closed.
func TestVectorClose_TwoClosersAfterJobsDrain(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "close-ns", 3, 4096)
	done := make(chan error, 2)
	go func() { done <- svc.Close() }()
	go func() { done <- svc.Close() }()
	require.NoError(t, <-done)
	require.NoError(t, <-done)

	err := svc.startBackground(func(context.Context) {})
	var closedErr *VectorServiceClosedError
	assert.ErrorAs(t, err, &closedErr)
}

// newTouchTrackingVecSvc returns a closed service over storage that is still
// open and records any access to it, so a test can tell an entry point that
// refused a request from one that reached storage after Close.
func newTouchTrackingVecSvc(t *testing.T) (*vectorService, *atomic.Bool) {
	t.Helper()
	svc, storage := newCoverageVecSvc(t, "close-ns", 3, 4096)
	var touched atomic.Bool
	vec := &vector.VectorValue{ID: 1, Embedding: []float32{1, 2, 3}}
	storage.getVectorFn = func(context.Context, string, uint64) (*vector.VectorValue, error) {
		touched.Store(true)
		return vec, nil
	}
	storage.batchDeleteVectorsFn = func(context.Context, string, []uint64) error {
		touched.Store(true)
		return nil
	}
	storage.deleteVectorFn = func(context.Context, string, uint64) error {
		touched.Store(true)
		return nil
	}
	storage.scanNamespaceFn = func(context.Context, string, *vector.ScanOptions) ([]*vector.VectorValue, error) {
		touched.Store(true)
		return []*vector.VectorValue{vec}, nil
	}
	info := storage.getNamespaceInfoFn
	storage.getNamespaceInfoFn = func(ctx context.Context, ns string) (*vector.NamespaceInfo, error) {
		touched.Store(true)
		return info(ctx, ns)
	}
	require.NoError(t, svc.Close())
	return svc, &touched
}

// TestVectorEntryPoints_RefuseAfterClose drives every entry point that used
// to reach storage before a guarded method after Close, with storage still
// open: each must refuse with VectorServiceClosedError and touch nothing.
func TestVectorEntryPoints_RefuseAfterClose(t *testing.T) {
	calls := map[string]func(*vectorService) error{
		"BatchDeleteVectors": func(svc *vectorService) error {
			_, err := svc.BatchDeleteVectors(context.Background(), &BatchDeleteVectorsRequest{
				Namespace: "close-ns", IDs: []uint64{1, 2},
			})
			return err
		},
		"ListVectors": func(svc *vectorService) error {
			_, err := svc.ListVectors(context.Background(), &ListVectorsRequest{Namespace: "close-ns"})
			return err
		},
		"UpdateMetadata": func(svc *vectorService) error {
			_, err := svc.UpdateMetadata(context.Background(), &UpdateVectorMetadataRequest{
				Namespace: "close-ns", ID: 1, Metadata: map[string]interface{}{"k": "v"},
			})
			return err
		},
		"SearchByID": func(svc *vectorService) error {
			_, err := svc.SearchByID(context.Background(), &SearchByIDRequest{
				Namespace: "close-ns", ID: 1, K: 1,
			})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			svc, touched := newTouchTrackingVecSvc(t)
			err := call(svc)
			var closedErr *VectorServiceClosedError
			require.ErrorAs(t, err, &closedErr)
			assert.False(t, touched.Load(), "%s reached storage after Close", name)
		})
	}
}

// TestVectorAdmit_RunsAccessWhileOpen checks admit's open path: the access
// runs and its error is returned unchanged.
func TestVectorAdmit_RunsAccessWhileOpen(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "close-ns", 3, 4096)
	ran := false
	require.NoError(t, svc.admit(func() error {
		ran = true
		return nil
	}))
	assert.True(t, ran)

	assert.ErrorIs(t, svc.admit(func() error { return vector.ErrVectorNotFound }), vector.ErrVectorNotFound)
}

// TestVectorAdmit_RefusesAfterClose checks admit's closed path: the access
// never runs.
func TestVectorAdmit_RefusesAfterClose(t *testing.T) {
	svc, _ := newCoverageVecSvc(t, "close-ns", 3, 4096)
	require.NoError(t, svc.Close())
	ran := false
	err := svc.admit(func() error {
		ran = true
		return nil
	})
	var closedErr *VectorServiceClosedError
	require.ErrorAs(t, err, &closedErr)
	assert.False(t, ran)
}
