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
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// heldFor returns the entry the store currently holds for username, or nil.
func heldFor(s *sealedShareStore, username string) *heldShare {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shares[username]
}

// assertDestroyed fails unless sec has been destroyed.
func assertDestroyed(t *testing.T, sec *secure.Secret, msg string) {
	t.Helper()
	buf, err := sec.Open()
	if buf != nil {
		buf.Destroy()
	}
	var nilSecret *secure.NilSecretError
	assert.True(t, errors.As(err, &nilSecret), "%s; Open returned %v", msg, err)
}

// TestSealedShareStore_OverwriteDestroysThePriorEnclave pins the fix for a
// replaced share that was dropped rather than destroyed: its enclave stayed
// sealed in locked memory, holding a share nobody could claim any more, until
// the collector happened to run.
func TestSealedShareStore_OverwriteDestroysThePriorEnclave(t *testing.T) {
	store := newSealedShareStore(DefaultShareTTL)
	defer store.DestroyAll()

	require.NoError(t, store.Store("alice", []byte(`share-v1`)))
	prior := heldFor(store, "alice")
	require.NotNil(t, prior)

	require.NoError(t, store.Store("alice", []byte(`share-v2`)))

	assertDestroyed(t, prior.secret, "the replaced share's enclave must be destroyed, not merely unreferenced")

	claimed, err := store.Claim("alice")
	require.NoError(t, err)
	assert.Equal(t, []byte(`share-v2`), claimed, "the replacement, not the original, is what the holder claims")
}

// TestSealedShareStore_OverwriteDisarmsThePriorTimer pins that the replaced
// entry's timer cannot wipe its replacement when it fires.
func TestSealedShareStore_OverwriteDisarmsThePriorTimer(t *testing.T) {
	store := newSealedShareStore(DefaultShareTTL)
	defer store.DestroyAll()

	require.NoError(t, store.Store("alice", []byte(`share-v1`)))
	prior := heldFor(store, "alice")
	require.NoError(t, store.Store("alice", []byte(`share-v2`)))

	assert.False(t, prior.timer.Stop(), "Store must stop the replaced entry's timer")
	store.expire("alice", prior)

	assert.True(t, store.Exists("alice"), "a stale timer must not wipe the share that replaced its own")
	claimed, err := store.Claim("alice")
	require.NoError(t, err)
	assert.Equal(t, []byte(`share-v2`), claimed)
}

// TestSealedShareStore_ExpiryWipesAndReportsExpired drives the real timer: a
// share nobody claims is gone at its deadline, and a late claim is told why.
func TestSealedShareStore_ExpiryWipesAndReportsExpired(t *testing.T) {
	store := newSealedShareStore(time.Millisecond)
	defer store.DestroyAll()

	require.NoError(t, store.Store("alice", []byte(`share`)))
	held := heldFor(store, "alice")
	require.NotNil(t, held)

	require.Eventually(t, func() bool { return !store.Exists("alice") },
		2*time.Second, time.Millisecond, "an unclaimed share must be wiped at its deadline")

	assertDestroyed(t, held.secret, "expiry must destroy the enclave, not merely forget it")

	_, err := store.Claim("alice")
	assert.ErrorIs(t, err, ErrShareExpired)
	assert.False(t, store.Claimed("alice"), "an expired share was never claimed")
}

// TestSealedShareStore_ExpiryIsNotNotFound distinguishes a share that expired
// from one that never existed: the remedies differ (rekey vs. wrong holder).
func TestSealedShareStore_ExpiryIsNotNotFound(t *testing.T) {
	store := newSealedShareStore(DefaultShareTTL)
	defer store.DestroyAll()

	require.NoError(t, store.Store("alice", []byte(`share`)))
	store.expire("alice", heldFor(store, "alice"))

	_, err := store.Claim("alice")
	assert.ErrorIs(t, err, ErrShareExpired)
	_, err = store.Claim("bob")
	assert.ErrorIs(t, err, ErrShareNotFound)
}

// TestSealedShareStore_ClaimDisarmsTheTimer pins that a timer firing after a
// successful claim leaves the recorded claim alone.
func TestSealedShareStore_ClaimDisarmsTheTimer(t *testing.T) {
	store := newSealedShareStore(DefaultShareTTL)
	defer store.DestroyAll()

	require.NoError(t, store.Store("alice", []byte(`share`)))
	held := heldFor(store, "alice")
	_, err := store.Claim("alice")
	require.NoError(t, err)

	assert.False(t, held.timer.Stop(), "Claim must have stopped the expiry timer already")
	store.expire("alice", held)

	_, err = store.Claim("alice")
	assert.ErrorIs(t, err, ErrShareAlreadyClaimed, "a claimed share stays claimed, not expired")
}

// TestSealedShareStore_RedistributionClearsExpiry pins that a fresh share
// (rekey) replaces the expired marker, and that Reset clears it too.
func TestSealedShareStore_RedistributionClearsExpiry(t *testing.T) {
	store := newSealedShareStore(DefaultShareTTL)
	defer store.DestroyAll()

	require.NoError(t, store.Store("alice", []byte(`old`)))
	store.expire("alice", heldFor(store, "alice"))
	require.NoError(t, store.Store("alice", []byte(`new`)))
	claimed, err := store.Claim("alice")
	require.NoError(t, err)
	assert.Equal(t, []byte(`new`), claimed)

	require.NoError(t, store.Store("bob", []byte(`old`)))
	store.expire("bob", heldFor(store, "bob"))
	store.Reset()
	_, err = store.Claim("bob")
	assert.ErrorIs(t, err, ErrShareNotFound, "Reset starts a fresh round with no expiry history")
}

// TestSealedShareStore_DestroyAllDisarmsTimers pins that DestroyAll leaves no
// armed timer behind to fire on a destroyed entry.
func TestSealedShareStore_DestroyAllDisarmsTimers(t *testing.T) {
	store := newSealedShareStore(DefaultShareTTL)
	require.NoError(t, store.Store("alice", []byte(`share`)))
	held := heldFor(store, "alice")

	store.DestroyAll()

	assert.False(t, held.timer.Stop(), "DestroyAll must stop every entry's timer")
	assertDestroyed(t, held.secret, "DestroyAll must destroy every enclave")
}

// TestNewSealedShareStore_NonPositiveTTLSelectsDefault pins the default.
func TestNewSealedShareStore_NonPositiveTTLSelectsDefault(t *testing.T) {
	assert.Equal(t, DefaultShareTTL, newSealedShareStore(0).ttl)
	assert.Equal(t, DefaultShareTTL, newSealedShareStore(-time.Second).ttl)
	assert.Equal(t, time.Minute, newSealedShareStore(time.Minute).ttl)
}

// TestCeremonyService_ShareTTLFromConfig pins that the configured TTL reaches
// the unseal and recovery holder stores.
func TestCeremonyService_ShareTTLFromConfig(t *testing.T) {
	svc, err := NewCeremonyService(&CeremonyConfig{DataDir: t.TempDir(), ShareTTL: 90 * time.Minute},
		shamirBarrier(t, seal.NewMemoryBackend(), 2, 2), testLogger())
	require.NoError(t, err)
	assert.Equal(t, 90*time.Minute, svc.system.store.ttl)
	assert.Equal(t, 90*time.Minute, svc.recovery.store.ttl)
}

// TestClaimShare_ExpiredShareIsReportedAtBegin drives the claim flow: a
// holder whose envelope outlived its TTL is told it expired, not that none
// was ever distributed, and cannot complete a claim with a nonce issued
// before the expiry.
func TestClaimShare_ExpiredShareIsReportedAtBegin(t *testing.T) {
	ctx := context.Background()
	e := newHolderEnv(t, 2, 2)
	a, b := newHolder(t, "alice", ""), newHolder(t, "bob", "")
	e.register(seal.DomainUnseal, a, b)
	_, err := e.barrier.InitializeShamir(ctx, "")
	require.NoError(t, err)

	cr, err := e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", a.thumb)
	require.NoError(t, err)

	store := e.svc.system.store
	store.expire("alice", heldFor(store, "alice"))

	_, err = e.svc.BeginClaimShare(ctx, seal.DomainUnseal, "alice", a.thumb)
	assert.ErrorIs(t, err, ErrShareExpired)

	sig, err := SignShareClaim(a.certKey, ScopeSystem.ClaimLabel(), "alice", cr.Nonce)
	require.NoError(t, err)
	_, err = e.svc.CompleteClaimShare(ctx, seal.DomainUnseal, "alice", hex.EncodeToString(cr.Nonce), sig, a.thumb)
	assert.ErrorIs(t, err, ErrShareExpired, "a nonce issued before expiry must not reach a wiped envelope")

	// The other holder is unaffected.
	e.claim(seal.DomainUnseal, b)
	assert.NotEmpty(t, b.envelope)
}
