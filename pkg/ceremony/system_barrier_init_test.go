// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ceremony

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestInitialize_SystemBarrier_FreshNodeUnsealsIt is the go-qrdb#548 proof
// that qrdb init is the ONLY system bootstrap: against a freshly
// constructed, never-initialized barrier, Initialize brings it up itself
// (no separate `barrier init` call), using the matching passphrase.
func TestInitialize_SystemBarrier_FreshNodeUnsealsIt(t *testing.T) {
	barrier := testBarrier(t)
	require.True(t, barrier.IsSealed())

	svc := newTestCeremonyService(t, defaultConfig(), barrier)

	_, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:   "test-so-pin",
		UserPin: "test-user-pin",
		Secret:  []byte("test-master-password"),
	})
	require.NoError(t, err)
	assert.False(t, barrier.IsSealed(), "Initialize must bring the system barrier up itself")
}

// TestInitialize_SystemBarrier_RetryAfterCrashConverges covers the retry
// path: a barrier already initialized by an earlier, interrupted attempt
// (ceremony.ErrAlreadyInitialized from the barrier) is unsealed with the
// same secret rather than treated as a hard failure.
func TestInitialize_SystemBarrier_RetryAfterCrashConverges(t *testing.T) {
	barrier := testBarrier(t)
	secret := []byte("test-master-password")

	// Simulate the barrier having already been initialized by a prior,
	// interrupted call, then sealed again (a fresh process restart).
	require.NoError(t, barrier.Initialize(context.Background(), seal.MustCredentials(secret)))
	require.NoError(t, barrier.Seal())
	require.True(t, barrier.IsSealed())

	svc := newTestCeremonyService(t, defaultConfig(), barrier)

	_, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:   "test-so-pin",
		UserPin: "test-user-pin",
		Secret:  secret,
	})
	require.NoError(t, err)
	assert.False(t, barrier.IsSealed())
}

// TestInitialize_SystemBarrier_AlreadyUnsealedIsANoOp covers a barrier that
// is already unsealed by the time Initialize runs: nothing to do, no error.
func TestInitialize_SystemBarrier_AlreadyUnsealedIsANoOp(t *testing.T) {
	barrier := unsealedTestBarrier(t)

	svc := newTestCeremonyService(t, defaultConfig(), barrier)

	_, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:   "test-so-pin",
		UserPin: "test-user-pin",
	})
	require.NoError(t, err)
	assert.False(t, barrier.IsSealed())
}

// TestInitialize_SystemBarrier_MofNLeavesItSealed covers the M-of-N
// exception: Initialize does NOT bring the system barrier up through
// initializeSystemBarrierLocked's credential path in that mode -- it goes
// through InitializeShamir instead, which generates and distributes the root
// key without ever accepting (or needing) opts.Secret. InitializeShamir
// leaves the LIVE barrier instance holding the key it just generated (it
// does not need to be reconstructed from shares to use a key it just made),
// but a FRESH barrier instance over the SAME storage -- a restart -- finds
// no credential-initialized root key and stays sealed until officers
// reconstruct it from their shares, exactly as before this command existed.
func TestInitialize_SystemBarrier_MofNLeavesItSealed(t *testing.T) {
	backend := seal.NewMemoryBackend()
	barrier := shamirBarrier(t, backend, 2, 2)
	opts, _ := mofnInitOpts(t, 2, 2)

	svc := newTestCeremonyService(t, defaultConfig(), barrier)

	_, err := svc.Initialize(context.Background(), newMockCA(t), opts)
	require.NoError(t, err)

	restarted := shamirBarrier(t, backend, 2, 2)
	assert.True(t, restarted.IsSealed(), "a restart over the same storage must stay sealed until officers reconstruct the key from their shares")
}
