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

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// hookedDistributor is the holder authority as the barrier's distributor,
// running hook once the first PrepareRoster call has returned: the window
// between the barrier's validation of an initialization's holder set and the
// generation of its key, after which the set is read again to split it.
type hookedDistributor struct {
	seal.CeremonyDistributor
	calls int
	hook  func()
}

func (d *hookedDistributor) PrepareRoster(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy, exclude []string) (*seal.HolderRoster, error) {
	d.calls++
	roster, err := d.CeremonyDistributor.PrepareRoster(ctx, domain, threshold, policy, exclude)
	if d.calls == 1 && d.hook != nil {
		d.hook()
	}
	return roster, err
}

// TestUnenrol_RefusedWhileInitializing is the regression for an unenrolment
// racing the barrier's initialization: the barrier validates the holder set,
// generates the key, then reads the set again to split it, and a holder
// removed between the two left it initialized with no share set, wedged until
// a restart. The unenrolment -- and a second initialization -- is refused
// while the first is in flight, and the initialization succeeds.
func TestUnenrol_RefusedWhileInitializing(t *testing.T) {
	ctx := context.Background()
	barrier := shamirBarrier(t, seal.NewMemoryBackend(), 2, 2)
	svc, err := NewDatabaseCeremonyService("acme", "orders", barrier, HolderDeps{CA: testCA(t)}, testLogger())
	require.NoError(t, err)
	var unenrolErr, secondInitErr error
	dist := &hookedDistributor{CeremonyDistributor: svc}
	dist.hook = func() {
		unenrolErr = svc.UnenrolOfficer(ctx, "bob")
		_, secondInitErr = svc.InitializeShamir(ctx, barrier, "")
	}
	barrier.SetCeremonyDistributor(dist)
	barrier.SetHolderVerifier(svc)
	for _, h := range []*testHolder{newHolder(t, "alice", ""), newHolder(t, "bob", "")} {
		_, err := svc.RegisterOfficer(ctx, h.request("admin-1"))
		require.NoError(t, err)
	}

	params, err := svc.InitializeShamir(ctx, barrier, "")
	require.NoError(t, err, "the initialization completes")
	assert.Equal(t, 2, params.ShareCount)
	require.GreaterOrEqual(t, dist.calls, 2, "the barrier read the holder set twice, the hook between")
	require.ErrorIs(t, unenrolErr, ErrInitializationInProgress)
	require.ErrorIs(t, secondInitErr, ErrInitializationInProgress)

	list, err := svc.ListOfficers(ctx)
	require.NoError(t, err)
	assert.Len(t, list, 2, "bob is still enrolled")
	require.ErrorIs(t, svc.UnenrolOfficer(ctx, "bob"), ErrUnenrolInitialized, "once initialized, removal is a revocation")
}

// TestUnenrol_FollowsTheAuditPosture pins that an unenrolment is recorded
// before the holder is removed: under fail-closed one the trail cannot record
// is refused and the holder stays enrolled; under fail-open it proceeds.
func TestUnenrol_FollowsTheAuditPosture(t *testing.T) {
	ctx := context.Background()
	for _, policy := range []audit.FailurePolicy{audit.FailClosed, audit.FailOpen} {
		t.Run(policy.String(), func(t *testing.T) {
			trail := &switchableTrail{ring: audit.NewRingBuffer(32)}
			barrier := shamirBarrier(t, seal.NewMemoryBackend(), 2, 2)
			svc, err := NewDatabaseCeremonyService("acme", "orders", barrier, HolderDeps{CA: testCA(t), Audit: trail, AuditPolicy: policy}, testLogger())
			require.NoError(t, err)
			_, err = svc.RegisterOfficer(ctx, newHolder(t, "alice", "").request("admin-1"))
			require.NoError(t, err)

			trail.refuse = true
			err = svc.UnenrolOfficer(ctx, "alice")
			trail.refuse = false
			list, lerr := svc.ListOfficers(ctx)
			require.NoError(t, lerr)
			if policy == audit.FailClosed {
				requireGateRefusal(t, err, "unenrol")
				assert.Len(t, list, 1, "the refused unenrolment left alice enrolled")
				return
			}
			require.NoError(t, err)
			assert.Empty(t, list)
		})
	}
}
