//go:build raft

package statemachine

import (
	"context"
	"errors"
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
	return applyCountCmdRule(t, vsm, index, cmd, true)
}

// applyCountCmdRule applies cmd as committed entry index, carrying the
// stored-count rule or not, as a proposer before or after
// featuregate.VectorStoredCount would have sent it.
func applyCountCmdRule(t *testing.T, vsm *VectorStateMachine, index uint64, cmd *VectorCommand, storedCount bool) error {
	t.Helper()
	cmd.Namespace = "ns"
	cmd.Timestamp = time.Now().UnixNano()
	cmd.StoredCount = storedCount
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

// TestVectorCount_ReplayKeepsTheCommandsRule replays one committed sequence
// (MaxVectors=2: insert 1, re-upsert 1, insert 2) twice. Without
// StoredCount, as an older proposer or one before vector-stored-count is
// active sends it, apply keeps the old +1-per-upsert rule: the re-upsert
// counts, so inserting 2 is refused at the quota. With it, the re-upsert
// adds nothing and 2 is admitted. Every replica replaying the same log
// therefore reaches the same count, whichever binary it runs.
func TestVectorCount_ReplayKeepsTheCommandsRule(t *testing.T) {
	replay := func(storedCount bool) (*VectorStateMachine, error) {
		vsm := newCountTestVSM(t, 2)
		require.NoError(t, applyCountCmdRule(t, vsm, 1, &VectorCommand{Op: OpUpsertVector, ID: 1, Vector: []float32{1, 0}}, storedCount))
		require.NoError(t, applyCountCmdRule(t, vsm, 2, &VectorCommand{Op: OpUpsertVector, ID: 1, Vector: []float32{1, 1}}, storedCount))
		return vsm, applyCountCmdRule(t, vsm, 3, &VectorCommand{Op: OpUpsertVector, ID: 2, Vector: []float32{0, 1}}, storedCount)
	}

	old, err := replay(false)
	require.Error(t, err, "the old rule counts the re-upsert, so a second id is past the quota")
	require.Equal(t, uint64(2), storedCount(old))

	stored, err := replay(true)
	require.NoError(t, err, "the stored-count rule admits the second id")
	require.Equal(t, uint64(2), storedCount(stored))
}

// A failed existence read is not "absent": with the backing store still
// writable, an upsert, a batch upsert and a delete whose existence read fails
// are refused before they write, so neither the store nor VectorCount moves.
func TestVectorCount_FailedExistenceReadRefusesTheWrite(t *testing.T) {
	vsm := newCountTestVSM(t, 0)
	require.NoError(t, applyCountCmd(t, vsm, 1, &VectorCommand{Op: OpUpsertVector, ID: 1, Vector: []float32{1, 0}}))
	store := vsm.adapter.(*MockStorageAdapter).vecStor
	readFault := errors.New("vector read fault")
	store.mu.Lock()
	store.getErr = readFault
	store.mu.Unlock()

	err := applyCountCmd(t, vsm, 2, &VectorCommand{Op: OpUpsertVector, ID: 2, Vector: []float32{0, 1}})
	require.ErrorIs(t, err, readFault, "an upsert whose existence read fails must be refused")
	err = applyCountCmd(t, vsm, 3, &VectorCommand{
		Op: OpBatchUpsert, IDs: []uint64{3, 4}, Vectors: [][]float32{{1, 1}, {0, 1}},
	})
	require.ErrorIs(t, err, readFault, "a batch upsert whose existence read fails must be refused")
	err = applyCountCmd(t, vsm, 4, &VectorCommand{Op: OpDeleteVector, IDs: []uint64{1, 5}})
	require.ErrorIs(t, err, readFault, "a delete whose existence read fails must be refused")

	store.mu.Lock()
	store.getErr = nil
	held := len(store.vectors["ns"])
	store.mu.Unlock()
	require.Equal(t, 1, held, "the refused writes must leave the store as it was")
	require.Equal(t, uint64(1), storedCount(vsm), "the refused writes must leave VectorCount as it was")
}
