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
// officer enrollment is open -- the first-boot state.
func TestEnrollmentOpen_FreshCeremony(t *testing.T) {
	svc, err := NewCeremonyService(defaultConfig(), testBarrier(t), newTestCredentialService(), testLogger())
	require.NoError(t, err)
	require.True(t, svc.EnrollmentOpen(context.Background()))
}

// TestEnrollmentOpen_ClosedWhenOperationalAndAfterRestart: reaching
// operational closes enrollment, records the marker on the barrier, and the
// marker keeps it closed for a fresh service over the same barrier -- a
// restart -- even though that service's own state starts over.
func TestEnrollmentOpen_ClosedWhenOperationalAndAfterRestart(t *testing.T) {
	ctx := context.Background()
	barrier := testBarrier(t)
	svc, err := NewCeremonyService(defaultConfig(), barrier, newTestCredentialService(), testLogger())
	require.NoError(t, err)
	_, err = svc.Initialize(ctx, newMockCA(t), nil)
	require.NoError(t, err)
	require.Equal(t, StateOperational, svc.State())
	require.False(t, svc.EnrollmentOpen(ctx), "an operational ceremony must close enrollment")

	marked, err := barrier.CeremonyOperational(ctx)
	require.NoError(t, err)
	require.True(t, marked, "reaching operational must record the marker on the barrier")

	restarted, err := NewCeremonyService(defaultConfig(), barrier, newTestCredentialService(), testLogger())
	require.NoError(t, err)
	require.Equal(t, StateAwaitingInit, restarted.State())
	require.False(t, restarted.EnrollmentOpen(ctx), "the marker must keep enrollment closed after a restart")
}

// TestEnrollmentOpen_ClosedOnInitializedBarrier: a barrier that is already
// initialized closes enrollment with no marker at all -- the state of a node
// whose ceremony completed before the marker existed.
func TestEnrollmentOpen_ClosedOnInitializedBarrier(t *testing.T) {
	ctx := context.Background()
	barrier := testBarrier(t)
	require.NoError(t, barrier.Initialize(ctx, seal.Credentials{}))
	require.NoError(t, barrier.Seal())

	svc, err := NewCeremonyService(defaultConfig(), barrier, newTestCredentialService(), testLogger())
	require.NoError(t, err)
	require.False(t, svc.EnrollmentOpen(ctx))
}

// TestEnrollmentOpen_ClosedByMarker: the recorded marker alone closes
// enrollment, whatever the barrier's and the service's state.
func TestEnrollmentOpen_ClosedByMarker(t *testing.T) {
	ctx := context.Background()
	barrier := testBarrier(t)
	require.NoError(t, barrier.MarkCeremonyOperational(ctx))
	svc, err := NewCeremonyService(defaultConfig(), barrier, newTestCredentialService(), testLogger())
	require.NoError(t, err)
	require.False(t, svc.EnrollmentOpen(ctx))
}
