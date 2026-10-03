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
	"errors"
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
		SOPin:         "test-so-pin",
		UserPin:       "test-user-pin",
		Secret:        []byte("test-master-password"),
		AdminGenerate: true,
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
		SOPin:         "test-so-pin",
		UserPin:       "test-user-pin",
		Secret:        secret,
		AdminGenerate: true,
	})
	require.NoError(t, err)
	assert.False(t, barrier.IsSealed())
}

// TestInitialize_SystemBarrier_WrongSecretOnRetryFails is
// RetryAfterCrashConverges' failure half: a barrier already initialized by
// an earlier attempt falls through to Unseal on retry (the SAME path that
// converges when the secret matches), but a retry with the WRONG secret
// fails that Unseal and is refused with *SystemBarrierInitError, naming the
// barrier's own decryption failure as the cause, not silently treated as a
// fresh initialization.
func TestInitialize_SystemBarrier_WrongSecretOnRetryFails(t *testing.T) {
	// A DEFERRED strategy, unlike testBarrier's bound one, actually
	// validates Credentials.Secret against the stored encrypted key on
	// every Unseal rather than ignoring it (the passphrase was never bound
	// at construction to ignore) -- the one strategy shape where "wrong
	// secret" is a real, checkable failure rather than a no-op parameter.
	barrier, err := seal.NewStorageBarrier(testLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "test/root-key"}, seal.NewDeferredSoftwareStrategy())
	require.NoError(t, err)
	secret := []byte("test-master-password")

	require.NoError(t, barrier.Initialize(context.Background(), seal.MustCredentials(secret)))
	require.NoError(t, barrier.Seal())
	require.True(t, barrier.IsSealed())

	svc := newTestCeremonyService(t, defaultConfig(), barrier)

	_, err = svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:         "test-so-pin",
		UserPin:       "test-user-pin",
		Secret:        []byte("a-completely-different-password"),
		AdminGenerate: true,
	})
	var barrierErr *SystemBarrierInitError
	require.ErrorAs(t, err, &barrierErr)
	assert.Contains(t, barrierErr.Error(), "system barrier initialization failed")
	assert.NotNil(t, errors.Unwrap(barrierErr))
	assert.True(t, barrier.IsSealed(), "a failed retry must leave the barrier exactly as it found it")
}

// TestInitialize_SystemBarrier_AlreadyUnsealedIsANoOp covers a barrier that
// is already unsealed by the time Initialize runs: nothing to do, no error.
func TestInitialize_SystemBarrier_AlreadyUnsealedIsANoOp(t *testing.T) {
	barrier := unsealedTestBarrier(t)

	svc := newTestCeremonyService(t, defaultConfig(), barrier)

	_, err := svc.Initialize(context.Background(), newMockCA(t), &InitializeOptions{
		SOPin:         "test-so-pin",
		UserPin:       "test-user-pin",
		AdminGenerate: true,
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

// TestInitialize_RefusedAdminCredential_NoSideEffects proves B3 (go-qrdb#548
// review: "init is atomic"): a request whose admin-credential selection is
// invalid -- conflicting, none selected, or a BYOC certificate that does not
// chain -- is refused before Initialize provisions the SO/user PINs or
// brings the system barrier up. Before this fix, all three ran only AFTER
// those side effects (and after issuing the TLS certificate), so a
// corrected retry was merely tolerated by their own idempotence rather than
// never having needed it: this test pins that the barrier and the PINs are
// untouched, not just that a retry eventually converges.
func TestInitialize_RefusedAdminCredential_NoSideEffects(t *testing.T) {
	cases := map[string]struct {
		opts      *InitializeOptions
		want      any
		wantMsg   string
		wantWraps bool
	}{
		"conflicting": {
			&InitializeOptions{
				SOPin: "test-so-pin", UserPin: "test-user-pin",
				AdminCSRPEM: []byte("csr"), AdminGenerate: true,
			},
			&ConflictingAdminCredentialInputsError{},
			"mutually exclusive",
			false,
		},
		"none selected": {
			&InitializeOptions{SOPin: "test-so-pin", UserPin: "test-user-pin"},
			&NoAdminCredentialSelectedError{},
			"no admin credential path selected",
			false,
		},
		"byoc untrusted": {
			&InitializeOptions{
				SOPin: "test-so-pin", UserPin: "test-user-pin",
				AdminCertPEM: []byte("not-a-cert"),
			},
			&AdminCredentialRejectedError{},
			"bootstrap admin credential rejected",
			true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			barrier := testBarrier(t)
			require.True(t, barrier.IsSealed())
			svc := newTestCeremonyService(t, defaultConfig(), barrier)

			_, err := svc.Initialize(context.Background(), newMockCA(t), tc.opts)
			require.Error(t, err)
			assert.IsType(t, tc.want, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
			if tc.wantWraps {
				assert.NotNil(t, errors.Unwrap(err), "%s must wrap its cause", name)
			}
			assert.True(t, barrier.IsSealed(),
				"a refused admin-credential request must not bring the system barrier up")
			assert.False(t, svc.pins.SOPINSet(),
				"a refused admin-credential request must not provision the SO PIN")
			assert.False(t, svc.pins.UserPINSet(),
				"a refused admin-credential request must not provision the user PIN")
			assert.Equal(t, StateAwaitingInit, svc.State(),
				"a refused admin-credential request must leave the ceremony retriable from scratch")
		})
	}
}
