// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
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

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestEnrollmentOpen_FreshCeremony: before anything has happened, anonymous
// holder enrollment is open -- the first-boot state.
func TestEnrollmentOpen_FreshCeremony(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	require.True(t, e.svc.EnrollmentOpen(context.Background()))
}

// TestEnrollmentOpen_ClosedWhenOperationalAndAfterRestart: reaching
// operational closes enrollment, records the marker on the barrier, and the
// marker keeps it closed for a fresh service over the same barrier -- a
// restart -- even though that service's own state starts over.
func TestEnrollmentOpen_ClosedWhenOperationalAndAfterRestart(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	// go-qrdb#548: qrdb init brings the system barrier up itself for the
	// single-admin path below, which newHolderEnv's own Shamir-configured
	// barrier cannot be -- it requires InitializeShamir, never a credential.
	e.barrier = noneStrategyBarrier(t)
	e.svc = e.newService(e.barrier)
	_, err := e.svc.Initialize(ctx, e.ca, initOpts(0))
	require.NoError(t, err)
	require.Equal(t, StateOperational, e.svc.State())
	require.False(t, e.svc.EnrollmentOpen(ctx), "an operational ceremony must close enrollment")

	marked, err := e.barrier.CeremonyOperational(ctx)
	require.NoError(t, err)
	require.True(t, marked, "reaching operational must record the marker on the barrier")

	restarted := e.newService(e.barrier)
	require.Equal(t, StateAwaitingInit, restarted.State())
	require.False(t, restarted.EnrollmentOpen(ctx), "the marker must keep enrollment closed after a restart")
}

// TestEnrollmentOpen_ClosedWhenTheLastEnvelopeIsClaimed: a Shamir ceremony
// becomes operational, and closes enrollment, when its last holder claims.
func TestEnrollmentOpen_ClosedWhenTheLastEnvelopeIsClaimed(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	holders := []*testHolder{newHolder(t, "alice", seal.RoleSecurityOfficer), newHolder(t, "bob", seal.RoleCustodian)}
	res, err := e.svc.Initialize(ctx, e.ca, initOpts(2, holders...))
	require.NoError(t, err)
	for i, h := range holders {
		h.recordCert(t, res.Holders[i].CertPEM)
	}
	e.claim(seal.DomainUnseal, holders[0])
	require.Equal(t, StateEnrolling, e.svc.State())
	e.claim(seal.DomainUnseal, holders[1])
	require.Equal(t, StateOperational, e.svc.State())
	marked, err := e.barrier.CeremonyOperational(ctx)
	require.NoError(t, err)
	require.True(t, marked)
	require.False(t, e.svc.EnrollmentOpen(ctx))
}

// TestEnrollmentOpen_ClosedOnInitializedBarrier: a barrier that is already
// initialized closes enrollment with no marker at all -- the state of a node
// whose ceremony completed before the marker existed.
func TestEnrollmentOpen_ClosedOnInitializedBarrier(t *testing.T) {
	ctx := context.Background()
	barrier := testBarrier(t)
	require.NoError(t, barrier.Initialize(ctx, seal.Credentials{}))
	require.NoError(t, barrier.Seal())

	e := newHolderEnv(t, 2, 2)
	require.False(t, e.newService(barrier).EnrollmentOpen(ctx))
}

// TestEnrollmentOpen_ClosedByMarker: the recorded marker alone closes
// enrollment, whatever the barrier's and the service's state.
func TestEnrollmentOpen_ClosedByMarker(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	require.NoError(t, e.barrier.MarkCeremonyOperational(ctx))
	require.False(t, e.newService(e.barrier).EnrollmentOpen(ctx))
}

// TestEnrollmentOpen_EndedContextFailsClosed: a probe whose context has
// already ended cannot read the barrier and reports enrollment closed.
func TestEnrollmentOpen_EndedContextFailsClosed(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	require.True(t, e.svc.EnrollmentOpen(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, e.svc.EnrollmentOpen(ctx))
}
