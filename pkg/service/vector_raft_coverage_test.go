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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// =============================================================================
// vectorService.proposeToRaft Tests
// =============================================================================

func TestVectorService_ProposeToRaft_NilRaftNode(t *testing.T) {
	vs := &vectorService{
		raftNode: nil,
		shardID:  VectorShardID,
	}

	cmd := &statemachine.VectorCommand{
		Op:        statemachine.OpUpsertVector,
		Namespace: "test-ns",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	}

	err := vs.proposeToRaft(context.Background(), cmd)
	// Nil raft node means local-only mode, should return nil
	require.NoError(t, err)
}

func TestVectorService_ProposeToRaft_Success(t *testing.T) {
	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return nil
		},
	}
	vs := &vectorService{
		raftNode: mockRaft,
		shardID:  VectorShardID,
	}

	cmd := &statemachine.VectorCommand{
		Op:        statemachine.OpUpsertVector,
		Namespace: "test-ns",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	}

	err := vs.proposeToRaft(context.Background(), cmd)
	require.NoError(t, err)
}

func TestVectorService_ProposeToRaft_RaftError(t *testing.T) {
	mockRaft := &mockRaftNode{
		syncProposeFn: func(_ context.Context, _ uint64, _ []byte) error {
			return &vectorTestError{msg: "raft proposal failed"}
		},
	}
	vs := &vectorService{
		raftNode: mockRaft,
		shardID:  VectorShardID,
	}

	cmd := &statemachine.VectorCommand{
		Op:        statemachine.OpUpsertVector,
		Namespace: "test-ns",
		ID:        1,
		Vector:    []float32{1.0, 2.0, 3.0},
	}

	err := vs.proposeToRaft(context.Background(), cmd)
	require.Error(t, err)

	requireErrorAs[*ProposeError](t, err)
}

// =============================================================================
// Batch writes on a Raft-configured service
// =============================================================================

// countingVectorStorage is the service's view of the vector store; it counts
// the batch deletes the service issues against storage directly, which a
// Raft-configured service must never do.
type countingVectorStorage struct {
	*vector.MemoryVectorStorage
	directBatchDeletes int
}

func (s *countingVectorStorage) BatchDeleteVectors(ctx context.Context, namespace string, ids []uint64) error {
	s.directBatchDeletes++
	return s.MemoryVectorStorage.BatchDeleteVectors(ctx, namespace, ids)
}

// raftAppliedVectorService builds a Raft-configured vector service whose
// Raft node applies every proposal into a vector consensus FSM over the
// same store, as a committed entry is applied on each replica. proposeErr,
// when set, fails every proposal before it is applied. It returns the
// service, its storage view and the commands the node was asked to apply.
func raftAppliedVectorService(t *testing.T, proposeErr error) (VectorService, *countingVectorStorage, *[]*statemachine.VectorCommand) {
	t.Helper()
	ctx := context.Background()
	mem := vector.NewMemoryVectorStorage()
	scoped := scopedFromContext(ctx, "docs")
	require.NoError(t, mem.CreateNamespace(ctx, &vector.NamespaceInfo{Name: scoped, Dimension: 2}))

	log, err := logger.New(logger.Default())
	require.NoError(t, err)
	fsm := statemachine.NewVectorConsensusFSMWithStorage(VectorShardID, 1, mem, log)
	t.Cleanup(func() { _ = fsm.Close(context.Background()) })

	var applied []*statemachine.VectorCommand
	var index uint64
	node := &mockRaftNode{
		syncProposeFn: func(ctx context.Context, _ uint64, data []byte) error {
			if proposeErr != nil {
				return proposeErr
			}
			cmd, err := statemachine.DecodeVectorCommand(data)
			if err != nil {
				return err
			}
			applied = append(applied, cmd)
			index++
			results := make([]sm.Result, 1)
			return fsm.Update(ctx, []sm.Entry{{Index: index, Cmd: data}}, results)
		},
	}
	store := &countingVectorStorage{MemoryVectorStorage: mem}
	svc, err := NewVectorService(store, newVecTestLogger(), WithRaftNode(node), WithNodeID("node-1"))
	require.NoError(t, err)
	return svc, store, &applied
}

// TestVectorService_RaftBatchUpsertKeepsMetadata checks that a batch upsert
// on a Raft-configured service stores each vector's metadata: the proposed
// command carries it and the applied record returns it on read.
func TestVectorService_RaftBatchUpsertKeepsMetadata(t *testing.T) {
	svc, _, applied := raftAppliedVectorService(t, nil)
	ctx := context.Background()

	_, err := svc.BatchUpsert(ctx, &BatchUpsertRequest{Namespace: "docs", Vectors: []*VectorData{
		{ID: 1, Vector: []float32{1, 0}, Metadata: map[string]interface{}{"color": "red"}},
		{ID: 2, Vector: []float32{0, 1}, Metadata: map[string]interface{}{"color": "blue"}},
	}})
	require.NoError(t, err)
	require.Len(t, *applied, 1)
	require.Len(t, (*applied)[0].BatchMeta, 2)

	for id, want := range map[uint64]string{1: "red", 2: "blue"} {
		got, err := svc.GetVector(ctx, &GetVectorRequest{Namespace: "docs", ID: id, IncludeMetadata: true})
		require.NoError(t, err)
		require.Equal(t, want, got.Metadata["color"], "vector %d", id)
	}
}

// TestVectorService_RaftBatchDeleteGoesThroughConsensus checks that a batch
// delete on a Raft-configured service is one proposed delete command
// carrying every ID, that applying it removes the vectors, and that the
// service never deletes from storage itself.
func TestVectorService_RaftBatchDeleteGoesThroughConsensus(t *testing.T) {
	svc, store, applied := raftAppliedVectorService(t, nil)
	ctx := context.Background()

	_, err := svc.BatchUpsert(ctx, &BatchUpsertRequest{Namespace: "docs", Vectors: []*VectorData{
		{ID: 1, Vector: []float32{1, 0}},
		{ID: 2, Vector: []float32{0, 1}},
	}})
	require.NoError(t, err)

	resp, err := svc.BatchDeleteVectors(ctx, &BatchDeleteVectorsRequest{Namespace: "docs", IDs: []uint64{1, 2}})
	require.NoError(t, err)
	require.True(t, resp.Success)
	require.Equal(t, 2, resp.DeletedCount)

	require.Len(t, *applied, 2)
	del := (*applied)[1]
	require.Equal(t, statemachine.OpDeleteVector, del.Op)
	require.Equal(t, []uint64{1, 2}, del.IDs)
	require.Zero(t, store.directBatchDeletes, "a Raft-configured service deleted from storage directly")

	for _, id := range []uint64{1, 2} {
		_, err := svc.GetVector(ctx, &GetVectorRequest{Namespace: "docs", ID: id})
		require.Error(t, err, "vector %d survived the batch delete", id)
	}
}

// TestVectorService_RaftBatchDeleteProposalErrorDeletesNothing checks that a
// failed proposal is the caller's answer and no vector is deleted, neither
// through consensus nor node-locally.
func TestVectorService_RaftBatchDeleteProposalErrorDeletesNothing(t *testing.T) {
	proposeErr := &vectorTestError{msg: "raft proposal failed"}
	svc, store, _ := raftAppliedVectorService(t, proposeErr)
	ctx := context.Background()
	scoped := scopedFromContext(ctx, "docs")
	require.NoError(t, store.MemoryVectorStorage.UpsertVector(ctx, scoped, &vector.VectorValue{ID: 7, Embedding: []float32{1, 0}}))

	resp, err := svc.BatchDeleteVectors(ctx, &BatchDeleteVectorsRequest{Namespace: "docs", IDs: []uint64{7}})
	require.Error(t, err)
	require.Nil(t, resp)
	requireErrorAs[*ProposeError](t, err)
	require.Zero(t, store.directBatchDeletes)

	got, err := store.MemoryVectorStorage.GetVector(ctx, scoped, 7)
	require.NoError(t, err)
	require.NotNil(t, got)
}

// failingBatchDeleteStorage is the state machine's storage with a batch
// delete that fails, as a full or failing disk would.
type failingBatchDeleteStorage struct {
	*vector.MemoryVectorStorage
	err error
}

func (s *failingBatchDeleteStorage) BatchDeleteVectors(context.Context, string, []uint64) error {
	return s.err
}

// TestVectorService_RaftBatchDeleteStorageErrorKeepsTheIndex checks that a
// committed batch delete whose durable delete fails on apply is the caller's
// error: the records survive, so the live index keeps serving them and no ID
// is reported deleted.
func TestVectorService_RaftBatchDeleteStorageErrorKeepsTheIndex(t *testing.T) {
	ctx := context.Background()
	mem := vector.NewMemoryVectorStorage()
	scoped := scopedFromContext(ctx, "docs")
	require.NoError(t, mem.CreateNamespace(ctx, &vector.NamespaceInfo{Name: scoped, Dimension: 2}))

	log, err := logger.New(logger.Default())
	require.NoError(t, err)
	storageErr := &vectorTestError{msg: "batch delete failed on disk"}
	fsm := statemachine.NewVectorConsensusFSMWithStorage(VectorShardID, 1,
		&failingBatchDeleteStorage{MemoryVectorStorage: mem, err: storageErr}, log)
	t.Cleanup(func() { _ = fsm.Close(context.Background()) })

	var index uint64
	node := &mockRaftNode{
		syncProposeFn: func(ctx context.Context, _ uint64, data []byte) error {
			index++
			return fsm.Update(ctx, []sm.Entry{{Index: index, Cmd: data}}, make([]sm.Result, 1))
		},
	}
	store := &countingVectorStorage{MemoryVectorStorage: mem}
	svc, err := NewVectorService(store, newVecTestLogger(), WithRaftNode(node), WithNodeID("node-1"))
	require.NoError(t, err)

	_, err = svc.BatchUpsert(ctx, &BatchUpsertRequest{Namespace: "docs", Vectors: []*VectorData{
		{ID: 1, Vector: []float32{1, 0}},
		{ID: 2, Vector: []float32{0, 1}},
	}})
	require.NoError(t, err)
	vs, ok := svc.(*vectorService)
	require.True(t, ok)
	liveIndex := vs.hnswIndexes[scoped]
	require.NotNil(t, liveIndex)
	require.EqualValues(t, 2, liveIndex.Count())

	resp, err := svc.BatchDeleteVectors(ctx, &BatchDeleteVectorsRequest{Namespace: "docs", IDs: []uint64{1, 2}})
	require.Error(t, err)
	require.Nil(t, resp)
	require.EqualValues(t, 2, liveIndex.Count(), "the live index dropped vectors storage still holds")
	for _, id := range []uint64{1, 2} {
		got, err := mem.GetVector(ctx, scoped, id)
		require.NoError(t, err)
		require.NotNil(t, got)
	}
}

// vectorTestError is a test error type for vector tests.
type vectorTestError struct {
	msg string
}

func (e *vectorTestError) Error() string {
	return e.msg
}
