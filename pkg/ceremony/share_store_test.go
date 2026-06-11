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
