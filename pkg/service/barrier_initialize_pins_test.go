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
	// ceremony refuses every standalone initialize (go-qrdb#560 review item
	// 8), so only a plain service reaches the initialized-barrier refusal.
	plain := NewBarrierService(e.barrier)
	_, err := plain.BarrierInitializeShamir(ctx, 2, 2, "")
	require.NoError(t, err)
	pins := &countingPINs{PINBackend: pintest.NewBackend(t)}
	require.NoError(t, pins.SetSOPIN("", svcSOPIN))
	for _, presented := range []string{"wrong-pin-0000", svcSOPIN} {
		require.ErrorIs(t, ProvisionInitializePINs(ctx, plain, pins, presented, ""), seal.ErrAlreadyInitialized)
	}
	require.Zero(t, pins.verifies, "the SO PIN must not be compared on an initialized barrier")
}

// TestProvisionInitializePINs_UninitializedProvisions: before initialization
// a plain barrier service has the PINs provisioned, and an already-set SO PIN
// is verified.
func TestProvisionInitializePINs_UninitializedProvisions(t *testing.T) {
	e := newHolderSvcEnv(t, 2, 2)
	plain := NewBarrierService(e.barrier)
	pins := pintest.NewBackend(t)
	require.NoError(t, ProvisionInitializePINs(context.Background(), plain, pins, svcSOPIN, "user-pin-1234"))
	require.True(t, pins.SOPINSet())
	require.True(t, pins.UserPINSet())
	require.ErrorIs(t, ProvisionInitializePINs(context.Background(), plain, pins, "wrong-pin-0000", ""), pin.ErrPINInvalid)
}

// TestProvisionInitializePINs_CeremonyRefusedBeforePIN: a barrier service
// built with a ceremony -- qrdbd's own system barrier, which `qrdb init`
// alone brings up -- refuses every standalone initialize, so the PIN step
// refuses it too, before any PIN is written or compared. A refused
// initialize leaves the PIN backend exactly as it found it.
func TestProvisionInitializePINs_CeremonyRefusedBeforePIN(t *testing.T) {
	e := newHolderSvcEnv(t, 2, 2)
	ctx := context.Background()

	fresh := pintest.NewBackend(t)
	var refused *SystemBarrierInitRefusedError
	require.ErrorAs(t, ProvisionInitializePINs(ctx, e.svc, fresh, svcSOPIN, "user-pin-1234"), &refused)
	require.False(t, fresh.SOPINSet(), "a refused initialize must not provision the SO PIN")
	require.False(t, fresh.UserPINSet(), "a refused initialize must not provision the User PIN")

	set := &countingPINs{PINBackend: pintest.NewBackend(t)}
	require.NoError(t, set.SetSOPIN("", svcSOPIN))
	require.ErrorAs(t, ProvisionInitializePINs(ctx, e.svc, set, "wrong-pin-0000", ""), &refused)
	require.Zero(t, set.verifies, "the SO PIN must not be compared on a barrier that refuses initialize")
}

// unreadableBarrier cannot answer whether it is initialized.
type unreadableBarrier struct{ BarrierServicer }

func (unreadableBarrier) BarrierInitializePrecondition(context.Context) error {
	return errInitializedRead
}

// TestProvisionInitializePINs_UnreadableFailsClosed: a barrier that cannot
// say whether it is initialized is refused, never passed to the PIN check.
func TestProvisionInitializePINs_UnreadableFailsClosed(t *testing.T) {
	pins := &countingPINs{PINBackend: pintest.NewBackend(t)}
	require.NoError(t, pins.SetSOPIN("", svcSOPIN))
	require.ErrorIs(t, ProvisionInitializePINs(context.Background(), unreadableBarrier{}, pins, svcSOPIN, ""), errInitializedRead)
	require.Zero(t, pins.verifies)
}

// TestBarrierService_BarrierInitializePrecondition: a plain service admits
// an initialize before initialization and refuses it with
// seal.ErrAlreadyInitialized after.
func TestBarrierService_BarrierInitializePrecondition(t *testing.T) {
	e := newHolderSvcEnv(t, 2, 2)
	ctx := adminCtx()
	plain := NewBarrierService(e.barrier)
	require.NoError(t, plain.BarrierInitializePrecondition(ctx))
	e.register(seal.DomainUnseal, newSvcHolder(t, "alice"), newSvcHolder(t, "bob"))
	_, err := plain.BarrierInitializeShamir(ctx, 2, 2, "")
	require.NoError(t, err)
	require.ErrorIs(t, plain.BarrierInitializePrecondition(ctx), seal.ErrAlreadyInitialized)
}

// TestBarrierService_BarrierInitializePrecondition_Ceremony: a service built
// with a ceremony refuses with *SystemBarrierInitRefusedError, before and
// after the barrier is initialized, exactly as its initialize methods do.
func TestBarrierService_BarrierInitializePrecondition_Ceremony(t *testing.T) {
	e := newHolderSvcEnv(t, 2, 2)
	ctx := adminCtx()
	var refused *SystemBarrierInitRefusedError
	require.ErrorAs(t, e.svc.BarrierInitializePrecondition(ctx), &refused)
	e.register(seal.DomainUnseal, newSvcHolder(t, "alice"), newSvcHolder(t, "bob"))
	_, err := NewBarrierService(e.barrier).BarrierInitializeShamir(ctx, 2, 2, "")
	require.NoError(t, err)
	require.ErrorAs(t, e.svc.BarrierInitializePrecondition(ctx), &refused)
}
