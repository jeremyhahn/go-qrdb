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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// errInitializedRead is a failure to read the initialization record.
var errInitializedRead = errors.New("initialization record unreadable")

// countingPINs counts SO PIN comparisons on the real software backend.
type countingPINs struct {
	pin.PINBackend
	verifies int
}

func (c *countingPINs) VerifySOPIN(p string) error {
	c.verifies++
	return c.PINBackend.VerifySOPIN(p)
}

// TestProvisionInitializePINs_InitializedRefusedBeforePIN: over the real
// barrier service, an initialized barrier is refused without comparing the SO
// PIN, right or wrong.
func TestProvisionInitializePINs_InitializedRefusedBeforePIN(t *testing.T) {
	e := newHolderSvcEnv(t, 2, 2)
	ctx := adminCtx()
	e.register(seal.DomainUnseal, newSvcHolder(t, "alice"), newSvcHolder(t, "bob"))
	// A plain, non-ceremony BarrierService over the same barrier: e.svc's
	// ceremony now refuses BarrierInitializeShamir (go-qrdb#560 review
	// item 8), the same way it already refused BarrierInitialize.
	_, err := NewBarrierService(e.barrier).BarrierInitializeShamir(ctx, 2, 2, "")
	require.NoError(t, err)
	pins := &countingPINs{PINBackend: pintest.NewBackend(t)}
	require.NoError(t, pins.SetSOPIN("", svcSOPIN))
	for _, presented := range []string{"wrong-pin-0000", svcSOPIN} {
		require.ErrorIs(t, ProvisionInitializePINs(ctx, e.svc, pins, presented, ""), seal.ErrAlreadyInitialized)
	}
	require.Zero(t, pins.verifies, "the SO PIN must not be compared on an initialized barrier")
}

// TestProvisionInitializePINs_UninitializedProvisions: before initialization
// the PINs are provisioned, and an already-set SO PIN is verified.
func TestProvisionInitializePINs_UninitializedProvisions(t *testing.T) {
	e := newHolderSvcEnv(t, 2, 2)
	pins := pintest.NewBackend(t)
	require.NoError(t, ProvisionInitializePINs(context.Background(), e.svc, pins, svcSOPIN, "user-pin-1234"))
	require.True(t, pins.SOPINSet())
	require.True(t, pins.UserPINSet())
	require.ErrorIs(t, ProvisionInitializePINs(context.Background(), e.svc, pins, "wrong-pin-0000", ""), pin.ErrPINInvalid)
}

// unreadableBarrier cannot answer whether it is initialized.
type unreadableBarrier struct{ BarrierServicer }

func (unreadableBarrier) BarrierInitialized(context.Context) (bool, error) {
	return false, errInitializedRead
}

// TestProvisionInitializePINs_UnreadableFailsClosed: a barrier that cannot
// say whether it is initialized is refused, never passed to the PIN check.
func TestProvisionInitializePINs_UnreadableFailsClosed(t *testing.T) {
	pins := &countingPINs{PINBackend: pintest.NewBackend(t)}
	require.NoError(t, pins.SetSOPIN("", svcSOPIN))
	require.ErrorIs(t, ProvisionInitializePINs(context.Background(), unreadableBarrier{}, pins, svcSOPIN, ""), errInitializedRead)
	require.Zero(t, pins.verifies)
}

// TestBarrierService_BarrierInitialized reports the barrier's state before
// and after initialization.
func TestBarrierService_BarrierInitialized(t *testing.T) {
	e := newHolderSvcEnv(t, 2, 2)
	ctx := adminCtx()
	initialized, err := e.svc.BarrierInitialized(ctx)
	require.NoError(t, err)
	require.False(t, initialized)
	e.register(seal.DomainUnseal, newSvcHolder(t, "alice"), newSvcHolder(t, "bob"))
	// See TestProvisionInitializePINs_InitializedRefusedBeforePIN above.
	_, err = NewBarrierService(e.barrier).BarrierInitializeShamir(ctx, 2, 2, "")
	require.NoError(t, err)
	initialized, err = e.svc.BarrierInitialized(ctx)
	require.NoError(t, err)
	require.True(t, initialized)
}
