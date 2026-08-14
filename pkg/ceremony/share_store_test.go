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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemguardShareStore_StoreAndClaim(t *testing.T) {
	store := newSealedShareStore()

	shareData := []byte(`{"index":1,"value":"abc123"}`)
	err := store.Store("alice", shareData)
	require.NoError(t, err)

	assert.True(t, store.Exists("alice"))
	assert.False(t, store.Claimed("alice"))

	claimed, err := store.Claim("alice")
	require.NoError(t, err)
	assert.NotEmpty(t, claimed)

	// After claiming, the share no longer exists and is marked claimed.
	assert.False(t, store.Exists("alice"))
	assert.True(t, store.Claimed("alice"))
}

func TestMemguardShareStore_ClaimNonexistent(t *testing.T) {
	store := newSealedShareStore()

	_, err := store.Claim("nobody")
	assert.ErrorIs(t, err, ErrShareNotFound)
}

func TestMemguardShareStore_DoubleClaim(t *testing.T) {
	store := newSealedShareStore()

	err := store.Store("bob", []byte(`{"index":2,"value":"def456"}`))
	require.NoError(t, err)

	_, err = store.Claim("bob")
	require.NoError(t, err)

	_, err = store.Claim("bob")
	assert.ErrorIs(t, err, ErrShareAlreadyClaimed)
}

func TestMemguardShareStore_ExistsAndClaimed(t *testing.T) {
	store := newSealedShareStore()

	// Before storing: not exists, not claimed.
	assert.False(t, store.Exists("charlie"))
	assert.False(t, store.Claimed("charlie"))

	// After storing: exists, not claimed.
	err := store.Store("charlie", []byte(`{"share":"data"}`))
	require.NoError(t, err)
	assert.True(t, store.Exists("charlie"))
	assert.False(t, store.Claimed("charlie"))

	// After claiming: not exists, claimed.
	_, err = store.Claim("charlie")
	require.NoError(t, err)
	assert.False(t, store.Exists("charlie"))
	assert.True(t, store.Claimed("charlie"))
}

func TestMemguardShareStore_DestroyAll(t *testing.T) {
	store := newSealedShareStore()

	err := store.Store("alice", []byte(`share-a`))
	require.NoError(t, err)
	err = store.Store("bob", []byte(`share-b`))
	require.NoError(t, err)
	err = store.Store("charlie", []byte(`share-c`))
	require.NoError(t, err)

	assert.True(t, store.Exists("alice"))
	assert.True(t, store.Exists("bob"))
	assert.True(t, store.Exists("charlie"))

	store.DestroyAll()

	assert.False(t, store.Exists("alice"))
	assert.False(t, store.Exists("bob"))
	assert.False(t, store.Exists("charlie"))
}

func TestMemguardShareStore_Reset_ClearsClaimHistory(t *testing.T) {
	store := newSealedShareStore()

	err := store.Store("alice", []byte(`share-round-1`))
	require.NoError(t, err)

	// Round 1 claim.
	_, err = store.Claim("alice")
	require.NoError(t, err)
	assert.True(t, store.Claimed("alice"))

	// Reset for a new round (rekey): claim history must be cleared so the
	// officer can claim their newly distributed share.
	store.Reset()
	assert.False(t, store.Exists("alice"))
	assert.False(t, store.Claimed("alice"))

	// Round 2: store and claim succeed.
	err = store.Store("alice", []byte(`share-round-2`))
	require.NoError(t, err)
	got, err := store.Claim("alice")
	require.NoError(t, err)
	assert.Equal(t, []byte(`share-round-2`), got)
}

func TestMemguardShareStore_Reset_DestroysUnclaimedShares(t *testing.T) {
	store := newSealedShareStore()

	err := store.Store("alice", []byte(`share-a`))
	require.NoError(t, err)
	err = store.Store("bob", []byte(`share-b`))
	require.NoError(t, err)

	store.Reset()

	assert.False(t, store.Exists("alice"))
	assert.False(t, store.Exists("bob"))

	// Claiming after Reset without a fresh Store reports not-found (NOT
	// already-claimed).
	_, err = store.Claim("alice")
	assert.ErrorIs(t, err, ErrShareNotFound)
}

func TestMemguardShareStore_DestroyAll_PreservesClaimHistory(t *testing.T) {
	store := newSealedShareStore()

	err := store.Store("alice", []byte(`share-a`))
	require.NoError(t, err)
	_, err = store.Claim("alice")
	require.NoError(t, err)

	store.DestroyAll()

	// DestroyAll is a cleanup, not a new round: claim history survives.
	assert.True(t, store.Claimed("alice"))
}

func TestMemguardShareStore_StoreMultipleClaimOne(t *testing.T) {
	store := newSealedShareStore()

	err := store.Store("alice", []byte(`share-alice`))
	require.NoError(t, err)
	err = store.Store("bob", []byte(`share-bob`))
	require.NoError(t, err)
	err = store.Store("charlie", []byte(`share-charlie`))
	require.NoError(t, err)

	// Claim only alice.
	_, err = store.Claim("alice")
	require.NoError(t, err)

	// Alice claimed, bob and charlie still exist.
	assert.False(t, store.Exists("alice"))
	assert.True(t, store.Claimed("alice"))
	assert.True(t, store.Exists("bob"))
	assert.False(t, store.Claimed("bob"))
	assert.True(t, store.Exists("charlie"))
	assert.False(t, store.Claimed("charlie"))
}

func TestMemguardShareStore_StoreOverwrite(t *testing.T) {
	store := newSealedShareStore()

	err := store.Store("alice", []byte(`share-v1`))
	require.NoError(t, err)

	// Overwrite with a new share.
	err = store.Store("alice", []byte(`share-v2`))
	require.NoError(t, err)

	assert.True(t, store.Exists("alice"))

	claimed, err := store.Claim("alice")
	require.NoError(t, err)
	assert.NotEmpty(t, claimed)
}

// TestMemguardShareStore_ConsumesTheCallerSlice pins the wiping contract that
// makes "shares are never stored" true in memory as well as on disk.
//
// Store documents that the input slice is consumed. If it merely copied the
// bytes and left the caller's slice intact, the plaintext share would survive
// on the heap after the ceremony believed it had been enclaved -- readable in a
// core dump or a swapped page, which is exactly what the memguard enclave
// exists to prevent. The share bytes are the root key: anything that lets a
// threshold of them be recovered from a crashed process defeats the scheme.
func TestMemguardShareStore_ConsumesTheCallerSlice(t *testing.T) {
	store := newSealedShareStore()
	defer store.DestroyAll()

	shareJSON := []byte(`{"index":1,"value":"deadbeefdeadbeef"}`)
	original := make([]byte, len(shareJSON))
	copy(original, shareJSON)

	require.NoError(t, store.Store("alice", shareJSON))

	assert.NotEqual(t, original, shareJSON,
		"Store must wipe the caller's slice; leaving the plaintext share on the heap "+
			"defeats the enclave it was just placed in")

	// And the enclaved copy must still be intact and correct, or the wipe above
	// would be destroying the share rather than protecting it.
	claimed, err := store.Claim("alice")
	require.NoError(t, err)
	assert.Equal(t, original, claimed,
		"the claimed share must equal what the caller supplied")
}

// TestMemguardShareStore_ClaimDestroysTheEnclave pins the other half of the
// invariant: a share is claimable exactly once, and nothing is retained
// afterwards for a second reader.
//
// The custodian is responsible for storing their share from the moment they
// retrieve it. If the server kept a copy, that responsibility would be a
// fiction and the server would hold enough material to rebuild the root key on
// its own -- which is the precise property an M-of-N ceremony is chosen to
// avoid.
func TestMemguardShareStore_ClaimDestroysTheEnclave(t *testing.T) {
	store := newSealedShareStore()
	defer store.DestroyAll()

	require.NoError(t, store.Store("alice", []byte(`{"index":1,"value":"abc123"}`)))
	require.True(t, store.Exists("alice"))

	if _, err := store.Claim("alice"); err != nil {
		t.Fatalf("first Claim: %v", err)
	}

	assert.False(t, store.Exists("alice"),
		"the enclave must be destroyed on claim, not merely marked")
	assert.True(t, store.Claimed("alice"),
		"the claim must still be recorded so a second attempt is refused rather than served")

	_, err := store.Claim("alice")
	assert.ErrorIs(t, err, ErrShareAlreadyClaimed)
}
