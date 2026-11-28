//go:build raft

package statemachine

import (
	"context"
	"testing"

	sm "github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/stretchr/testify/require"
)

// TestVectorConsensusFSM_ResultReportsAppliedChange pins Result.Value of
// the consensus FSM to the change each apply made to the stored vector
// count, as VectorStateMachine reports it, so a service can move its gauge
// by the committed mutation.
func TestVectorConsensusFSM_ResultReportsAppliedChange(t *testing.T) {
	fsm := NewVectorConsensusFSMWithStorage(VectorShardID, 1, newMockVectorStorage(), createTestVectorLogger(t))
	defer func() { require.NoError(t, fsm.Close(context.Background())) }()

	steps := []struct {
		name string
		cmd  *VectorCommand
		want uint64
	}{
		{"create namespace", &VectorCommand{Op: OpCreateNamespace, Dimension: 2}, 1},
		{"fresh upsert", &VectorCommand{Op: OpUpsertVector, ID: 1, Vector: []float32{1, 0}}, 1},
		{"re-upsert", &VectorCommand{Op: OpUpsertVector, ID: 1, Vector: []float32{0, 1}}, 0},
		{"batch with id 1 stored", &VectorCommand{
			Op:      OpBatchUpsert,
			IDs:     []uint64{1, 2, 2},
			Vectors: [][]float32{{1, 1}, {0, 1}, {1, 1}},
		}, 1},
		{"delete of a missing id", &VectorCommand{Op: OpDeleteVector, ID: 9}, 0},
		{"delete of a stored id", &VectorCommand{Op: OpDeleteVector, ID: 1}, 1},
		{"batch delete of stored, missing and repeated ids", &VectorCommand{Op: OpDeleteVector, IDs: []uint64{2, 2, 9}}, 1},
	}
	for i, step := range steps {
		step.cmd.Namespace = "count-ns"
		results := make([]sm.Result, 1)
		err := fsm.Update(context.Background(), []sm.Entry{{Index: uint64(i + 1), Cmd: encodeVectorCmd(t, step.cmd)}}, results)
		require.NoError(t, err, step.name)
		if step.cmd.Op == OpCreateNamespace {
			continue
		}
		require.Equal(t, step.want, results[0].Value, step.name)
	}
}
