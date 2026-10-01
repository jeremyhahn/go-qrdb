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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestAuthorizeSOPIN_ProvisionedAndLockedOut drives the governance SO PIN
// gate directly: refused before an SO PIN exists, refused when wrong, and,
// over a backend carrying the node's SO PIN lockout as a composer installs
// it, locked out after the free failures -- the right PIN included.
func TestAuthorizeSOPIN_ProvisionedAndLockedOut(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	e.svc.SetPINBackend(pin.NewLockoutBackend(e.pins))
	require.ErrorIs(t, e.svc.AuthorizeSOPIN("so-pin-1234"), pin.ErrPINNotSet)
	require.NoError(t, e.pins.SetSOPIN("", "so-pin-1234"))
	require.NoError(t, e.svc.AuthorizeSOPIN("so-pin-1234"))
	for i := 0; i < pin.SOPINFreeFailures; i++ {
		require.ErrorIs(t, e.svc.AuthorizeSOPIN("wrong-pin-00"), pin.ErrPINInvalid)
	}
	var locked *pin.SOPINLockedError
	require.ErrorAs(t, e.svc.AuthorizeSOPIN("so-pin-1234"), &locked)
	require.ErrorIs(t, e.svc.AuthorizeSOPIN("so-pin-1234"), pin.ErrPINLocked)

	unwired, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir()}, testBarrier(t), testLogger())
	require.NoError(t, err)
	require.ErrorIs(t, unwired.AuthorizeSOPIN("so-pin-1234"), pin.ErrBackendNotConfigured)
}

// TestShareClaim_EmptyNonceRefused: a claim message is never signed or
// verified over an empty nonce.
func TestShareClaim_EmptyNonceRefused(t *testing.T) {
	h := newHolder(t, "alice", "")
	_, err := SignShareClaim(h.certKey, SystemClaimLabel, "alice", nil)
	require.Error(t, err)
	assert.False(t, verifyShareClaim(h.certKey.Public(), SystemClaimLabel, "alice", nil, []byte("sig")))
}

// TestHolderScope_ProbesOverAPlainBackend: a backend that is not a barrier
// has no initialization state and no roster in force.
func TestHolderScope_ProbesOverAPlainBackend(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	initialized, err := initializedProbe(backend)(ctx)
	require.NoError(t, err)
	assert.False(t, initialized)
	_, _, err = rosterProbe(backend)(ctx, seal.DomainUnseal)
	assert.ErrorIs(t, err, seal.ErrHolderRosterMissing)
}

// TestHolderScope_NameFreeFailsClosed: a sealed scope whose initialization
// cannot be read refuses the registration rather than assuming it may cache.
func TestHolderScope_NameFreeFailsClosed(t *testing.T) {
	probeFault := errors.New("metadata unreadable")
	h, err := newHolderScope(ScopeTenant("acme"), seal.DomainUnseal, "acme", seal.NewMemoryBackend(), DefaultShareTTL, func() bool { return true }, testLogger())
	require.NoError(t, err)
	h.initialized = func(context.Context) (bool, error) { return false, probeFault }
	require.ErrorIs(t, h.nameFree(context.Background(), "alice"), probeFault)

	h.initialized = func(context.Context) (bool, error) { return false, nil }
	require.NoError(t, h.nameFree(context.Background(), "alice"))
	h.registry.cacheOnly(&PersistentOfficer{Username: "alice"})
	require.ErrorIs(t, h.nameFree(context.Background(), "alice"), ErrDuplicateUsername)
}

// TestRegistry_ReconcileFailuresAndClaimCarry covers reconcile's remaining
// branches: a read fault and a corrupt record are errors, and a claim
// recorded while sealed is carried onto the same holder's persisted record.
func TestRegistry_ReconcileFailuresAndClaimCarry(t *testing.T) {
	ctx := context.Background()
	fault := errors.New("read fault")
	broken, err := NewScopedOfficerRegistry(failingGetBackend{StorageBackend: seal.NewMemoryBackend(), err: fault}, ScopeTenant("acme"))
	require.NoError(t, err)
	require.ErrorIs(t, broken.reconcile(ctx, &PersistentOfficer{Username: "alice"}), fault)

	backend := seal.NewMemoryBackend()
	reg, err := NewScopedOfficerRegistry(backend, ScopeTenant("acme"))
	require.NoError(t, err)
	require.NoError(t, backend.Put(ctx, reg.officerKey("bob"), []byte("{not json")))
	var corrupt *OfficerRegistryError
	require.ErrorAs(t, reg.reconcile(ctx, &PersistentOfficer{Username: "bob"}), &corrupt)

	persisted := &PersistentOfficer{Username: "alice", CertThumbprint: "aa", PubKeyPEM: "k", Distribution: "d1"}
	require.NoError(t, reg.Save(ctx, persisted))
	claimed := *persisted
	claimed.ClaimedAt = time.Now().UTC()
	require.NoError(t, reg.reconcile(ctx, &claimed))
	fresh, err := NewScopedOfficerRegistry(backend, ScopeTenant("acme"))
	require.NoError(t, err)
	got, err := fresh.Load(ctx, "alice")
	require.NoError(t, err)
	assert.False(t, got.ClaimedAt.IsZero(), "the claim recorded while sealed was carried over")

	// A claim for another distribution is not carried.
	other := claimed
	other.Distribution, other.ClaimedAt = "d0", time.Now().UTC().Add(time.Hour)
	require.NoError(t, reg.reconcile(ctx, &other))
}

// TestHolderScope_StagedSetLifecycle: a staged set is promoted only for its
// own distribution, and a set that never commits is destroyed.
func TestHolderScope_StagedSetLifecycle(t *testing.T) {
	h, err := newHolderScope(ScopeTenant("acme"), seal.DomainUnseal, "acme", seal.NewMemoryBackend(), DefaultShareTTL, func() bool { return false }, testLogger())
	require.NoError(t, err)
	staged := newSealedShareStore(DefaultShareTTL)
	require.NoError(t, staged.Store("alice", []byte("envelope")))
	h.staged, h.stagedDistribution = staged, "d2"

	assert.False(t, h.promoteStaged("d1"), "another distribution's commit promotes nothing")
	assert.False(t, h.store.Exists("alice"))
	h.discardStaged()
	assert.Nil(t, h.staged)
	assert.False(t, staged.Exists("alice"), "the discarded set is destroyed")
	h.discardStaged()
}

// TestHolderScope_AdmitDropsChallengesForAReplacedRoster: a challenge issued
// against a roster the barrier no longer holds can never verify, so admitting
// a new one drops it, and the scope cap is what the roster can use.
func TestHolderScope_AdmitDropsChallengesForAReplacedRoster(t *testing.T) {
	h, err := newHolderScope(ScopeTenant("acme"), seal.DomainUnseal, "acme", seal.NewMemoryBackend(), DefaultShareTTL, func() bool { return false }, testLogger())
	require.NoError(t, err)
	live := &authfactors.Challenge{ExpiresAt: time.Now().Add(time.Minute)}
	h.quorum["stale"] = &pendingQuorumChallenge{holder: "bob", op: seal.OpSeal, digest: []byte("old"), challenge: live}
	roster := &seal.HolderRoster{Total: 1}
	require.NoError(t, h.admitQuorumChallenge(roster, []byte("new"), seal.OpSeal, "alice", false))
	assert.NotContains(t, h.quorum, "stale")

	// One holder, the domain's operations, two each: the cap.
	limit := seal.QuorumOpCount(seal.DomainUnseal) * maxPendingPerHolderOp
	for i := 0; i < limit; i++ {
		h.quorum[string(rune('a'+i))] = &pendingQuorumChallenge{holder: "carol", op: seal.QuorumOp(string(rune('a' + i))), digest: []byte("new"), challenge: live}
	}
	require.ErrorIs(t, h.admitQuorumChallenge(roster, []byte("new"), seal.OpSeal, "alice", false), ErrTooManyPendingChallenges)
}
