//go:build raft

package statemachine

import (
	"context"
	"testing"
	"time"

	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/stretchr/testify/require"
)

// newCountTestVSM builds a VectorStateMachine over the mock vector storage
// with one 2-dimensional namespace "ns" registered in both the state machine
// and storage. maxVectors > 0 sets the namespace's vector quota.
func newCountTestVSM(t *testing.T, maxVectors int64) *VectorStateMachine {
	t.Helper()
	adapter := NewMockStorageAdapter()
	adapter.vecStor = NewMockVectorStorageWrapper()
	mockHNSW := NewMockHNSWIndex()
	vsm, err := newTestVectorStateMachine(adapter, mockHNSW, NewMockCache(), newTestLogger())
	require.NoError(t, err)
	setupTestNamespace(vsm, "ns", 2, mockHNSW)
	require.NoError(t, createTestNamespaceInStorage(adapter, "ns", 2))
	if maxVectors > 0 {
		vsm.namespaces["ns"].Quotas = &NamespaceQuotas{MaxVectors: maxVectors}
	}
	return vsm
}

// applyCountCmd applies cmd through Update, as raft does on every replica,
// and returns the entry's result and error.
func applyCountCmd(t *testing.T, vsm *VectorStateMachine, index uint64, cmd *VectorCommand) error {
	t.Helper()
	cmd.Namespace = "ns"
	cmd.Timestamp = time.Now().UnixNano()
	data, err := cmd.Encode()
	require.NoError(t, err)
	return vsm.Update(context.Background(), []sm.Entry{{Index: index, Cmd: data}}, make([]sm.Result, 1))
}

func storedCount(vsm *VectorStateMachine) uint64 {
	ns := vsm.namespaces["ns"]
	ns.mu.RLock()
	defer ns.mu.RUnlock()
	return ns.VectorCount
}

func TestVectorCount_ReUpsertOfStoredIDAddsNoVector(t *testing.T) {
	vsm := newCountTestVSM(t, 0)
	require.NoError(t, applyCountCmd(t, vsm, 1, &VectorCommand{Op: OpUpsertVector, ID: 1, Vector: []float32{1, 0}}))
	require.NoError(t, applyCountCmd(t, vsm, 2, &VectorCommand{Op: OpUpsertVector, ID: 1, Vector: []float32{0, 1}}))
	require.Equal(t, uint64(1), storedCount(vsm), "re-upserting a stored id must leave VectorCount 1")
}

func TestVectorCount_BatchUpsertCountsDistinctNewIDs(t *testing.T) {
	vsm := newCountTestVSM(t, 0)
	require.NoError(t, applyCountCmd(t, vsm, 1, &VectorCommand{Op: OpUpsertVector, ID: 1, Vector: []float32{1, 0}}))
	// id 1 is stored, id 2 appears twice: only id 2 is a new vector.
	require.NoError(t, applyCountCmd(t, vsm, 2, &VectorCommand{
		Op:      OpBatchUpsert,
		IDs:     []uint64{1, 2, 2},
		Vectors: [][]float32{{1, 1}, {0, 1}, {1, 1}},
	}))
	require.Equal(t, uint64(2), storedCount(vsm), "batch must add only distinct ids not stored before")
}

func TestVectorCount_DeleteOfMissingIDLeavesCount(t *testing.T) {
	vsm := newCountTestVSM(t, 0)
	require.NoError(t, applyCountCmd(t, vsm, 1, &VectorCommand{Op: OpUpsertVector, ID: 1, Vector: []float32{1, 0}}))
	require.NoError(t, applyCountCmd(t, vsm, 2, &VectorCommand{Op: OpDeleteVector, ID: 99}))
	require.Equal(t, uint64(1), storedCount(vsm), "deleting a missing id must leave VectorCount unchanged")
	// A batch delete naming the stored id twice and a missing id removes one.
	require.NoError(t, applyCountCmd(t, vsm, 3, &VectorCommand{Op: OpDeleteVector, IDs: []uint64{1, 1, 98}}))
	require.Equal(t, uint64(0), storedCount(vsm))
}

func TestVectorCount_TenantAtQuotaCanReUpsertStoredID(t *testing.T) {
	vsm := newCountTestVSM(t, 2)
	require.NoError(t, applyCountCmd(t, vsm, 1, &VectorCommand{Op: OpUpsertVector, ID: 1, Vector: []float32{1, 0}}))
	require.NoError(t, applyCountCmd(t, vsm, 2, &VectorCommand{Op: OpUpsertVector, ID: 2, Vector: []float32{0, 1}}))
	require.Equal(t, uint64(2), storedCount(vsm))

	require.NoError(t, applyCountCmd(t, vsm, 3, &VectorCommand{Op: OpUpsertVector, ID: 1, Vector: []float32{1, 1}}),
		"a tenant at its vector quota must still re-upsert a stored id")
	require.NoError(t, applyCountCmd(t, vsm, 4, &VectorCommand{
		Op: OpBatchUpsert, IDs: []uint64{1, 2}, Vectors: [][]float32{{1, 1}, {1, 1}},
	}), "a batch of stored ids adds no vector, so the quota admits it")
	require.Error(t, applyCountCmd(t, vsm, 5, &VectorCommand{Op: OpUpsertVector, ID: 3, Vector: []float32{1, 1}}),
		"a new id past the quota is still refused")
	require.Equal(t, uint64(2), storedCount(vsm))
}
