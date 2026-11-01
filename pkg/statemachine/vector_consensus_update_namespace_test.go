//go:build raft

package statemachine

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vectorStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
)

// updateVectorFSM applies one command to fsm and returns its result and error.
func updateVectorFSM(t *testing.T, fsm *VectorConsensusFSM, index uint64, cmd *VectorCommand) (sm.Result, error) {
	t.Helper()
	results := make([]sm.Result, 1)
	err := fsm.Update(context.Background(), []sm.Entry{{Index: index, Cmd: encodeVectorCmd(t, cmd)}}, results)
	return results[0], err
}

func TestVectorConsensusFSM_UpdateNamespace(t *testing.T) {
	t.Run("merges index params and stringifies metadata", func(t *testing.T) {
		fsm, storage := installingFSM(t, nil)
		defer func() { require.NoError(t, fsm.Close(context.Background())) }()

		res, err := updateVectorFSM(t, fsm, 1, &VectorCommand{
			Op:          OpUpdateNamespace,
			Namespace:   "ns",
			Dimension:   2,
			IndexParams: map[string]string{"M": "32"},
			Metadata:    map[string]interface{}{"owner": "team", "tier": 7},
		})
		require.NoError(t, err)
		assert.Equal(t, uint64(1), res.Value)

		var decoded UpdateNamespaceResult
		require.NoError(t, decoded.Decode(res.Data))
		assert.True(t, decoded.Updated)

		info, err := storage.GetNamespaceInfo(context.Background(), "ns")
		require.NoError(t, err)
		assert.Equal(t, "team", info.Metadata["owner"])
		assert.Equal(t, "7", info.Metadata["tier"], "metadata values are stored in their %v form")
		assert.Equal(t, uint32(2), info.Dimension, "an update never changes the dimension")
	})

	t.Run("dimension zero means unchanged", func(t *testing.T) {
		fsm, _ := installingFSM(t, nil)
		defer func() { require.NoError(t, fsm.Close(context.Background())) }()

		res, err := updateVectorFSM(t, fsm, 1, &VectorCommand{
			Op:        OpUpdateNamespace,
			Namespace: "ns",
			Metadata:  map[string]interface{}{"k": "v"},
		})
		require.NoError(t, err)
		assert.Equal(t, uint64(1), res.Value)
	})

	t.Run("a different dimension is refused", func(t *testing.T) {
		fsm, storage := installingFSM(t, nil)
		defer func() { require.NoError(t, fsm.Close(context.Background())) }()

		res, err := updateVectorFSM(t, fsm, 1, &VectorCommand{
			Op:        OpUpdateNamespace,
			Namespace: "ns",
			Dimension: 3,
			Metadata:  map[string]interface{}{"k": "v"},
		})
		require.NoError(t, err, "a refusal must not halt the apply loop")
		want, stop, _ := ResolveApplyError(nil, 1, sm.Result{}, &vector.DimensionMismatchError{Namespace: "ns", Got: 3, Want: 2})
		require.False(t, stop)
		assert.Equal(t, want, res)

		info, err := storage.GetNamespaceInfo(context.Background(), "ns")
		require.NoError(t, err)
		assert.NotContains(t, info.Metadata, "k", "a refused update must not write")
	})

	t.Run("an absent namespace is NamespaceNotFound", func(t *testing.T) {
		fsm, storage := installingFSM(t, nil)
		defer func() { require.NoError(t, fsm.Close(context.Background())) }()
		storage.getNSInfoErr = vectorStorage.ErrNamespaceNotFound

		res, err := updateVectorFSM(t, fsm, 1, &VectorCommand{Op: OpUpdateNamespace, Namespace: "missing"})
		require.NoError(t, err, "a refusal must not halt the apply loop")
		want, stop, _ := ResolveApplyError(nil, 1, sm.Result{}, &vector.NamespaceNotFoundError{Namespace: "missing"})
		require.False(t, stop)
		assert.Equal(t, want, res)
	})

	t.Run("any other storage error is returned as is", func(t *testing.T) {
		fsm, storage := installingFSM(t, nil)
		defer func() { require.NoError(t, fsm.Close(context.Background())) }()
		storage.getNSInfoErr = errStorageUnavailable

		_, err := updateVectorFSM(t, fsm, 1, &VectorCommand{Op: OpUpdateNamespace, Namespace: "ns"})
		require.Error(t, err)
		assert.True(t, errors.Is(err, errStorageUnavailable))
	})

	t.Run("without storage the update is acknowledged", func(t *testing.T) {
		fsm := NewVectorConsensusFSM(VectorShardID, 1, createTestVectorLogger(t)).(*VectorConsensusFSM)
		defer func() { require.NoError(t, fsm.Close(context.Background())) }()

		res, err := updateVectorFSM(t, fsm, 1, &VectorCommand{Op: OpUpdateNamespace, Namespace: "ns"})
		require.NoError(t, err)
		assert.Equal(t, uint64(1), res.Value)
		assert.Empty(t, res.Data)
	})
}
