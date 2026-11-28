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

package server

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// recordingVectorLocker stands in for a real service.VectorNamespaceLocker,
// recording the order in which locks are acquired and released so a test
// can assert that a write holds its lock across the check AND the write.
type recordingVectorLocker struct {
	mu     sync.Mutex
	events []string
}

func (l *recordingVectorLocker) record(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *recordingVectorLocker) LockVectorNamespaceWrite(namespace string) func() {
	l.record("write-lock:" + namespace)
	return func() { l.record("write-unlock:" + namespace) }
}

func (l *recordingVectorLocker) LockVectorNamespaceReclaim(namespace string) func() {
	l.record("reclaim-lock:" + namespace)
	return func() { l.record("reclaim-unlock:" + namespace) }
}

// recordingVectorServicer records which mutating call ran and lets the test
// control its outcome. Only the methods a test exercises are overridden;
// every other call panics via the nil embedded VectorServicer.
type recordingVectorServicer struct {
	service.VectorServicer
	called    []string
	upsertErr error
}

func (s *recordingVectorServicer) UpsertVector(ctx context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	s.called = append(s.called, "UpsertVector")
	if s.upsertErr != nil {
		return nil, s.upsertErr
	}
	return &service.UpsertVectorResponse{ID: req.ID, Success: true}, nil
}

// lockingVectorServicer composes a VectorServicer with a VectorNamespaceLocker
// so newNamespacedVectorService's type assertion finds the locker.
type lockingVectorServicer struct {
	service.VectorServicer
	*recordingVectorLocker
}

func TestNamespacedVectorService_WriteLocksAcrossTheCheckAndTheWrite(t *testing.T) {
	locker := &recordingVectorLocker{}
	inner := &recordingVectorServicer{}
	checked := false
	view := &namespacedVectorService{
		dropFence: dropFence{check: func() error { checked = true; return nil }},
		inner:     inner,
		prefix:    "acme/orders/",
		locker:    locker,
	}

	resp, err := view.UpsertVector(context.Background(), &service.UpsertVectorRequest{ID: 1, Namespace: "default"})
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.True(t, checked)
	assert.Equal(t, []string{"UpsertVector"}, inner.called)
	assert.Equal(t, []string{"write-lock:acme/orders/", "write-unlock:acme/orders/"}, locker.events)
}

func TestNamespacedVectorService_RefusalNeverReachesTheInnerWrite(t *testing.T) {
	locker := &recordingVectorLocker{}
	inner := &recordingVectorServicer{}
	refusal := errors.New("dropping")
	view := &namespacedVectorService{
		dropFence: dropFence{check: func() error { return refusal }},
		inner:     inner,
		prefix:    "acme/orders/",
		locker:    locker,
	}

	_, err := view.UpsertVector(context.Background(), &service.UpsertVectorRequest{ID: 1, Namespace: "default"})
	require.ErrorIs(t, err, refusal)
	assert.Empty(t, inner.called)
	// The lock is still released even though the write never ran: a
	// refusal must not leak the namespace lock.
	assert.Equal(t, []string{"write-lock:acme/orders/", "write-unlock:acme/orders/"}, locker.events)
}

func TestNamespacedVectorService_UnlocksEvenWhenTheInnerWriteFails(t *testing.T) {
	locker := &recordingVectorLocker{}
	writeErr := errors.New("storage unavailable")
	inner := &recordingVectorServicer{upsertErr: writeErr}
	view := &namespacedVectorService{
		dropFence: dropFence{check: func() error { return nil }},
		inner:     inner,
		prefix:    "acme/orders/",
		locker:    locker,
	}

	_, err := view.UpsertVector(context.Background(), &service.UpsertVectorRequest{ID: 1, Namespace: "default"})
	require.ErrorIs(t, err, writeErr)
	assert.Equal(t, []string{"write-lock:acme/orders/", "write-unlock:acme/orders/"}, locker.events)
}

func TestNewNamespacedVectorService_DetectsALocker(t *testing.T) {
	locker := &recordingVectorLocker{}
	inner := &lockingVectorServicer{VectorServicer: &recordingVectorServicer{}, recordingVectorLocker: locker}

	view := newNamespacedVectorService(inner, "acme/orders/")
	assert.NotNil(t, view.locker)
}

func TestNewNamespacedVectorService_NoLockerIsFine(t *testing.T) {
	view := newNamespacedVectorService(&recordingVectorServicer{}, "acme/orders/")
	assert.Nil(t, view.locker)

	// A write still works -- guardedWrite with no locker just skips locking.
	resp, err := view.UpsertVector(context.Background(), &service.UpsertVectorRequest{ID: 1, Namespace: "default"})
	require.NoError(t, err)
	assert.True(t, resp.Success)
}

// guardRecordingVectorServicer records, per method, whether the context it
// was called with carried a write guard.
type guardRecordingVectorServicer struct {
	service.VectorServicer
	guarded map[string]bool
}

func (r *guardRecordingVectorServicer) note(ctx context.Context, method string) {
	r.guarded[method] = !service.WriteGuardFrom(ctx).IsZero()
}

func (r *guardRecordingVectorServicer) CreateNamespace(ctx context.Context, _ *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	r.note(ctx, "CreateNamespace")
	return &service.CreateNamespaceResponse{}, nil
}

func (r *guardRecordingVectorServicer) UpdateNamespace(ctx context.Context, _ *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
	r.note(ctx, "UpdateNamespace")
	return &service.UpdateNamespaceResponse{}, nil
}

func (r *guardRecordingVectorServicer) DeleteNamespace(ctx context.Context, _ *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	r.note(ctx, "DeleteNamespace")
	return &service.DeleteNamespaceResponse{}, nil
}

func (r *guardRecordingVectorServicer) ListNamespaces(ctx context.Context, _ *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	r.note(ctx, "ListNamespaces")
	return &service.ListNamespacesResponse{}, nil
}

func (r *guardRecordingVectorServicer) GetNamespaceInfo(ctx context.Context, _ *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	r.note(ctx, "GetNamespaceInfo")
	return &service.GetNamespaceInfoResponse{}, nil
}

func (r *guardRecordingVectorServicer) UpsertVector(ctx context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	r.note(ctx, "UpsertVector")
	return &service.UpsertVectorResponse{}, nil
}

func (r *guardRecordingVectorServicer) GetVector(ctx context.Context, _ *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	r.note(ctx, "GetVector")
	return &service.GetVectorResponse{}, nil
}

func (r *guardRecordingVectorServicer) DeleteVector(ctx context.Context, _ *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	r.note(ctx, "DeleteVector")
	return &service.DeleteVectorResponse{}, nil
}

func (r *guardRecordingVectorServicer) BatchUpsert(ctx context.Context, _ *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	r.note(ctx, "BatchUpsert")
	return &service.BatchUpsertResponse{}, nil
}

func (r *guardRecordingVectorServicer) BatchDeleteVectors(ctx context.Context, _ *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	r.note(ctx, "BatchDeleteVectors")
	return &service.BatchDeleteVectorsResponse{}, nil
}

func (r *guardRecordingVectorServicer) ListVectors(ctx context.Context, _ *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	r.note(ctx, "ListVectors")
	return &service.ListVectorsResponse{}, nil
}

func (r *guardRecordingVectorServicer) UpdateMetadata(ctx context.Context, _ *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	r.note(ctx, "UpdateMetadata")
	return &service.UpdateVectorMetadataResponse{}, nil
}

func (r *guardRecordingVectorServicer) SearchSimilar(ctx context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	r.note(ctx, "SearchSimilar")
	return &service.SearchSimilarResponse{}, nil
}

func (r *guardRecordingVectorServicer) SearchByMetadata(ctx context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	r.note(ctx, "SearchByMetadata")
	return &service.SearchByMetadataResponse{}, nil
}

func (r *guardRecordingVectorServicer) HybridSearch(ctx context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	r.note(ctx, "HybridSearch")
	return &service.HybridSearchResponse{}, nil
}

func (r *guardRecordingVectorServicer) SearchByID(ctx context.Context, _ *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	r.note(ctx, "SearchByID")
	return &service.SearchByIDResponse{}, nil
}

func (r *guardRecordingVectorServicer) RebuildIndex(ctx context.Context, _ *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	r.note(ctx, "RebuildIndex")
	return &service.RebuildIndexResponse{}, nil
}

func (r *guardRecordingVectorServicer) GetRebuildProgress(ctx context.Context, _ *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	r.note(ctx, "GetRebuildProgress")
	return &service.GetRebuildProgressResponse{}, nil
}

func (r *guardRecordingVectorServicer) GetIndexStats(ctx context.Context, _ *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	r.note(ctx, "GetIndexStats")
	return &service.GetIndexStatsResponse{}, nil
}

func (r *guardRecordingVectorServicer) CompactNamespace(ctx context.Context, _ *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	r.note(ctx, "CompactNamespace")
	return &service.CompactNamespaceResponse{}, nil
}

func (r *guardRecordingVectorServicer) GetCompactionProgress(ctx context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	r.note(ctx, "GetCompactionProgress")
	return &service.GetCompactionProgressResponse{}, nil
}

// TestNamespacedVectorService_WritesCarryTheDropGuard pins that every write
// the vector view delegates carries the database's write guard on its
// context -- so a write the inner servicer proposes through Raft (a
// materialized database's) is refused at apply time once Drop fences the
// database -- and that no read does.
func TestNamespacedVectorService_WritesCarryTheDropGuard(t *testing.T) {
	guard, err := service.DropFenceGuard("acme", "orders", "")
	require.NoError(t, err)
	require.False(t, guard.IsZero())

	inner := &guardRecordingVectorServicer{guarded: map[string]bool{}}
	view := newNamespacedVectorService(inner, "acme/orders/")
	view.dbGuard = dbGuard{guard: guard}

	ctx := context.Background()
	calls := []func() error{
		func() error { _, e := view.CreateNamespace(ctx, &service.CreateNamespaceRequest{}); return e },
		func() error { _, e := view.UpdateNamespace(ctx, &service.UpdateNamespaceRequest{}); return e },
		func() error { _, e := view.DeleteNamespace(ctx, &service.DeleteNamespaceRequest{}); return e },
		func() error { _, e := view.ListNamespaces(ctx, &service.ListNamespacesRequest{}); return e },
		func() error { _, e := view.GetNamespaceInfo(ctx, &service.GetNamespaceInfoRequest{}); return e },
		func() error { _, e := view.UpsertVector(ctx, &service.UpsertVectorRequest{}); return e },
		func() error { _, e := view.GetVector(ctx, &service.GetVectorRequest{}); return e },
		func() error { _, e := view.DeleteVector(ctx, &service.DeleteVectorRequest{}); return e },
		func() error { _, e := view.BatchUpsert(ctx, &service.BatchUpsertRequest{}); return e },
		func() error { _, e := view.BatchDeleteVectors(ctx, &service.BatchDeleteVectorsRequest{}); return e },
		func() error { _, e := view.ListVectors(ctx, &service.ListVectorsRequest{}); return e },
		func() error { _, e := view.UpdateMetadata(ctx, &service.UpdateVectorMetadataRequest{}); return e },
		func() error { _, e := view.SearchSimilar(ctx, &service.SearchSimilarRequest{}); return e },
		func() error { _, e := view.SearchByMetadata(ctx, &service.SearchByMetadataRequest{}); return e },
		func() error { _, e := view.HybridSearch(ctx, &service.HybridSearchRequest{}); return e },
		func() error { _, e := view.SearchByID(ctx, &service.SearchByIDRequest{}); return e },
		func() error { _, e := view.RebuildIndex(ctx, &service.RebuildIndexRequest{}); return e },
		func() error { _, e := view.GetRebuildProgress(ctx, &service.GetRebuildProgressRequest{}); return e },
		func() error { _, e := view.GetIndexStats(ctx, &service.GetIndexStatsRequest{}); return e },
		func() error { _, e := view.CompactNamespace(ctx, &service.CompactNamespaceRequest{}); return e },
		func() error {
			_, e := view.GetCompactionProgress(ctx, &service.GetCompactionProgressRequest{})
			return e
		},
	}
	for _, call := range calls {
		require.NoError(t, call())
	}

	assert.Equal(t, map[string]bool{
		"CreateNamespace":       true,
		"UpdateNamespace":       true,
		"DeleteNamespace":       true,
		"UpsertVector":          true,
		"DeleteVector":          true,
		"BatchUpsert":           true,
		"BatchDeleteVectors":    true,
		"UpdateMetadata":        true,
		"RebuildIndex":          true,
		"CompactNamespace":      true,
		"ListNamespaces":        false,
		"GetNamespaceInfo":      false,
		"GetVector":             false,
		"ListVectors":           false,
		"SearchSimilar":         false,
		"SearchByMetadata":      false,
		"HybridSearch":          false,
		"SearchByID":            false,
		"GetRebuildProgress":    false,
		"GetIndexStats":         false,
		"GetCompactionProgress": false,
	}, inner.guarded)
}
