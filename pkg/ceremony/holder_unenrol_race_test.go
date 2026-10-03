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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// hookedDistributor is the holder authority as the barrier's distributor,
// running hook once the first PrepareRoster call has returned -- the window
// between the barrier's validation of an initialization's holder set and the
// generation of its key -- and beforeSecond just before the second call, the
// read that splits the key, once the barrier has marked itself initialized.
type hookedDistributor struct {
	seal.CeremonyDistributor
	calls        int
	hook         func()
	beforeSecond func()
}

func (d *hookedDistributor) PrepareRoster(ctx context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy, exclude []string) (*seal.HolderRoster, error) {
	d.calls++
	if d.calls == 2 && d.beforeSecond != nil {
		d.beforeSecond()
	}
	roster, err := d.CeremonyDistributor.PrepareRoster(ctx, domain, threshold, policy, exclude)
	if d.calls == 1 && d.hook != nil {
		d.hook()
	}
	return roster, err
}

// initRaceScopes builds the scopes whose initialization the holder authority
// serializes, a database's and a tenant's, each 2-of-2 with the authority
// installed on its barrier through dist, and returns the authority.
func initRaceScopes(t *testing.T) map[string]func(dist *hookedDistributor) (*ScopedCeremonyService, *seal.StorageBarrier) {
	build := func(newSvc func(*seal.StorageBarrier) (*ScopedCeremonyService, error)) func(dist *hookedDistributor) (*ScopedCeremonyService, *seal.StorageBarrier) {
		return func(dist *hookedDistributor) (*ScopedCeremonyService, *seal.StorageBarrier) {
			barrier := shamirBarrier(t, seal.NewMemoryBackend(), 2, 2)
			svc, err := newSvc(barrier)
			require.NoError(t, err)
			dist.CeremonyDistributor = svc
			barrier.SetCeremonyDistributor(dist)
			barrier.SetHolderVerifier(svc)
			return svc, barrier
		}
	}
	return map[string]func(dist *hookedDistributor) (*ScopedCeremonyService, *seal.StorageBarrier){
		"database": build(func(b *seal.StorageBarrier) (*ScopedCeremonyService, error) {
			return NewDatabaseCeremonyService("acme", "orders", b, HolderDeps{CA: testCA(t)}, testLogger())
		}),
		"tenant": build(func(b *seal.StorageBarrier) (*ScopedCeremonyService, error) {
			return NewTenantCeremonyService("acme", b, HolderDeps{CA: testCA(t)}, testLogger())
		}),
	}
}

// TestRegister_RefusedWhileTheSystemBarrierInitializes is the system scope's
// regression for a registration racing a Shamir initialization: the system
// barrier is initialized through its holder authority
// (CeremonyService.InitializeShamir), so an unseal-key holder registered
// just before the barrier's second read is refused rather than failing the
// split and leaving the barrier to need a restart.
func TestRegister_RefusedWhileTheSystemBarrierInitializes(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	dist := &hookedDistributor{CeremonyDistributor: e.svc}
	e.barrier.SetCeremonyDistributor(dist)
	var registerErr, secondInitErr error
	dist.beforeSecond = func() {
		_, registerErr = e.svc.RegisterOfficer(ctx, seal.DomainUnseal, newHolder(t, "carol", "").request("admin-1"))
		_, secondInitErr = e.svc.InitializeShamir(ctx, "")
	}
	for _, h := range []*testHolder{newHolder(t, "alice", ""), newHolder(t, "bob", "")} {
		_, err := e.svc.RegisterOfficer(ctx, seal.DomainUnseal, h.request("admin-1"))
		require.NoError(t, err)
	}

	params, err := e.svc.InitializeShamir(ctx, "")
	require.NoError(t, err, "the initialization completes")
	assert.Equal(t, 2, params.ShareCount)
	require.GreaterOrEqual(t, dist.calls, 2, "the barrier read the holder set twice, the hook before the second")
	require.ErrorIs(t, registerErr, ErrInitializationInProgress)
	require.ErrorIs(t, secondInitErr, ErrInitializationInProgress)

	list, err := e.svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	assert.Len(t, list, 2, "carol was not enrolled")
}

// TestRecoveryHolders_RefusedWhileTheRecoveryKeyIsGenerated pins the
// recovery domain's initialization: generating the recovery key reads the
// recovery holder set to build the roster, then reads the holders' records
// again to seal each one's share. A recovery holder registered or unenrolled
// between the two reads is refused with ErrInitializationInProgress, so the
// set the key is split to is the set the roster names, and the generation
// completes.
func TestRecoveryHolders_RefusedWhileTheRecoveryKeyIsGenerated(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	e.initShamir("", newHolder(t, "so-1", ""), newHolder(t, "so-2", ""))
	e.register(seal.DomainRecovery, newHolder(t, "rita", ""), newHolder(t, "rob", ""))
	dist := &hookedDistributor{CeremonyDistributor: e.svc}
	e.barrier.SetCeremonyDistributor(dist)
	var registerErr, unenrolErr, secondErr error
	dist.hook = func() {
		_, registerErr = e.svc.RegisterOfficer(ctx, seal.DomainRecovery, newHolder(t, "ruth", "").request("admin-1"))
		unenrolErr = e.svc.recovery.unenrol(ctx, &e.svc.deps, "rob")
		// Bounded: without the refusal a second generation waits for the
		// first's redistribution slot, which the first holds until it returns.
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_, secondErr = e.svc.GenerateRecoveryKeys(bounded, 2, "")
	}

	res, err := e.svc.GenerateRecoveryKeys(ctx, 2, "")
	require.NoError(t, err, "the generation completes")
	assert.Equal(t, 2, res.ShareCount)
	require.Equal(t, 1, dist.calls, "the recovery roster was read once, the hook after it")
	require.ErrorIs(t, registerErr, ErrInitializationInProgress)
	require.ErrorIs(t, unenrolErr, ErrInitializationInProgress)
	require.ErrorIs(t, secondErr, ErrInitializationInProgress)

	list, err := e.svc.ListOfficers(ctx, seal.DomainRecovery)
	require.NoError(t, err)
	assert.Len(t, list, 2, "ruth was not enrolled and rob was not removed")
}

// TestRegister_RefusedWhileInitializing is the regression for a registration
// racing the barrier's initialization: the barrier marks itself initialized,
// then reads the holder set again to split the key, and a holder enrolled
// just before that second read -- admitted, the barrier now reading as
// initialized -- made the split fail with more holders than shares, every
// retry included. A registration is refused while the initialization is in
// flight, in every scope that serializes it, and the initialization succeeds.
func TestRegister_RefusedWhileInitializing(t *testing.T) {
	for name, build := range initRaceScopes(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dist := &hookedDistributor{}
			svc, barrier := build(dist)
			var registerErr error
			dist.beforeSecond = func() {
				_, registerErr = svc.RegisterOfficer(ctx, newHolder(t, "carol", "").request("admin-1"))
			}
			for _, h := range []*testHolder{newHolder(t, "alice", ""), newHolder(t, "bob", "")} {
				_, err := svc.RegisterOfficer(ctx, h.request("admin-1"))
				require.NoError(t, err)
			}

			params, err := svc.InitializeShamir(ctx, barrier, "")
			require.NoError(t, err, "the initialization completes")
			assert.Equal(t, 2, params.ShareCount)
			require.GreaterOrEqual(t, dist.calls, 2, "the barrier read the holder set twice, the hook before the second")
			require.ErrorIs(t, registerErr, ErrInitializationInProgress)

			list, err := svc.ListOfficers(ctx)
			require.NoError(t, err)
			assert.Len(t, list, 2, "carol was not enrolled")
		})
	}
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
