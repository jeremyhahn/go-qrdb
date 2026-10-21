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

package node

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
)

// TestWithClock_ReachesThePINLockoutAndTheClaimChallenge pins that the
// composer's clock reaches the node's two holder time gates: the SO PIN
// lockout over the node's seal store, and the ceremony's claim challenge.
func TestWithClock_ReachesThePINLockoutAndTheClaimChallenge(t *testing.T) {
	fake := clocktest.NewAt(time.Now().Add(-24 * time.Hour))
	n := newSeamNode(t, nil, WithClock(fake))

	b := n.sealPINBackend
	require.NotNil(t, b, "the node opens a PIN backend over its seal store")
	require.NoError(t, b.SetSOPIN("", "so-pin-1234"))
	for i := 0; i < auth.SOPINFreeFailures; i++ {
		require.ErrorIs(t, b.VerifySOPIN("wrong-pin-0000"), pin.ErrPINInvalid)
	}
	status := b.GetLockoutStatus()
	require.True(t, status.IsLocked)
	require.True(t, status.LockoutUntil.Equal(fake.Now().Add(auth.SOPINBaseBackoff)),
		"the lockout runs on the composer's clock: locked until %v, want %v", status.LockoutUntil, fake.Now().Add(auth.SOPINBaseBackoff))
	fake.Advance(auth.SOPINBaseBackoff)
	require.NoError(t, b.VerifySOPIN("so-pin-1234"), "the lockout lifts once the composer's clock passes the backoff")

	core := n.sealedWindowCeremonyCore()
	require.NotNil(t, core, "the node builds a ceremony service")
	require.Same(t, fake, core.HolderDeps().Clock, "the claim challenge runs on the composer's clock")
}

// TestWithClock_NilLeavesTheHolderGatesOnTheRealClock pins the default: a
// node built without a clock runs both gates on the wall clock.
func TestWithClock_NilLeavesTheHolderGatesOnTheRealClock(t *testing.T) {
	n := newSeamNode(t, nil)
	b := n.sealPINBackend
	require.NotNil(t, b)
	require.NoError(t, b.SetSOPIN("", "so-pin-1234"))
	for i := 0; i < auth.SOPINFreeFailures; i++ {
		require.ErrorIs(t, b.VerifySOPIN("wrong-pin-0000"), pin.ErrPINInvalid)
	}
	require.WithinDuration(t, time.Now().Add(auth.SOPINBaseBackoff), b.GetLockoutStatus().LockoutUntil, time.Minute)

	core := n.sealedWindowCeremonyCore()
	require.NotNil(t, core)
	require.Nil(t, core.HolderDeps().Clock)
}
