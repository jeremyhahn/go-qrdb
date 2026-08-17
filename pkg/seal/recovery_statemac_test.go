// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"testing"

	quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStampNoneOriginSealedState pins the recovery-over-none contract:
// quicraft's Initialize deliberately leaves a StrategyNone blob without a
// metadata MAC, and the recovery unseal -- which runs the full MAC
// verification with the reconstructed root key -- must stamp exactly that
// shape and no other. Without the stamp, every recovery of a
// none-initialized barrier failed closed with
// ErrSealedStateUnauthenticated ("carries no metadata MAC").
func TestStampNoneOriginSealedState(t *testing.T) {
	b := &StorageBarrier{config: BarrierConfig{DeploymentID: 7}}
	rootKey := make([]byte, 32)
	for i := range rootKey {
		rootKey[i] = byte(i)
	}

	t.Run("none-origin unstamped blob is stamped", func(t *testing.T) {
		sealed := &SealedRootKey{Strategy: StrategyNone, Epoch: 1}
		require.NoError(t, b.stampNoneOriginSealedState(rootKey, sealed))
		assert.NotEmpty(t, sealed.StateMAC, "the recovered root key must stamp the MAC")
	})

	t.Run("already-stamped blob untouched", func(t *testing.T) {
		sealed := &SealedRootKey{Strategy: StrategyNone, Epoch: 1}
		require.NoError(t, quicraftseal.StampSealedStateMACWithRootKey(rootKey, 7, sealed))
		before := append([]byte(nil), sealed.StateMAC...)
		require.NoError(t, b.stampNoneOriginSealedState(rootKey, sealed))
		assert.Equal(t, before, sealed.StateMAC)
	})

	t.Run("key-holding strategy blob untouched", func(t *testing.T) {
		// A passphrase-sealed blob with a stripped MAC must NOT be blessed
		// here -- it fails closed inside Unseal instead.
		sealed := &SealedRootKey{Strategy: StrategyPassphrase, Epoch: 1}
		require.NoError(t, b.stampNoneOriginSealedState(rootKey, sealed))
		assert.Empty(t, sealed.StateMAC)
	})

	t.Run("nil blob is a no-op", func(t *testing.T) {
		require.NoError(t, b.stampNoneOriginSealedState(rootKey, nil))
	})
}
