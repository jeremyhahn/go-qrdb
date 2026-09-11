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

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
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

// A claim that FAILS must not consume the share.
//
// Claim destroys the sealed material and records the claim as its last acts,
// and both must stay behind the successful read. A one-time recovery share
// marked consumed by a claim that returned an error is unrecoverable: the
// custodian holds nothing and the store will refuse to hand it over again.
//
// The failure is provoked with a real purge rather than a stub, because the
// purge IS the condition — it is what a memguard allocation failure anywhere
// in the process does to every live enclave, including this share's
// (go-qrdb#74). A test that faked the error would prove the error path
// returns; only the purge proves the store survives the thing that causes it.
//
// It is driven through secure.Purge, NOT memguard.Purge. Importing memguard
// here was a live rule-1 violation — the containment boundary the whole
// pkg/secure design rests on, unreported for as long as it was because
// nothing ran the analyzer (go-qrdb#429). secure.Purge reaches the same
// memguard.Purge and additionally takes the package's purge lock
// exclusively, so it waits for any in-flight borrow instead of unmapping
// pages underneath one — which is what this package's callers are entitled
// to assume happens. EnablePurge is the arming half of the lifecycle a
// binary's main() performs; Purge refuses to run without it.
func TestMemguardShareStore_FailedClaimDoesNotConsumeTheShare(t *testing.T) {
	store := newSealedShareStore()

	require.NoError(t, store.Store("alice", []byte(`{"index":1,"value":"abc123"}`)))
	require.True(t, store.Exists("alice"))

	// Destroys every live enclave in the process, this share's included.
	secure.EnablePurge()
	require.NoError(t, secure.Purge())

	_, err := store.Claim("alice")
	require.Error(t, err, "a claim on purged material must not report success")

	assert.False(t, store.Claimed("alice"),
		"a failed claim marked the share consumed — the custodian holds nothing "+
			"and the store will never hand it over again")
}

// emptyBorrow is a borrow that yields no bytes — the shape a process-wide
// memguard purge leaves a live buffer in.
//
// A fake is used because the condition cannot be built from a real
// *secure.Buffer: both constructors reject a zero length, and WithBytes
// refuses a destroyed buffer without calling the callback. The branch guards a
// state only a purge produces, mid-Claim, between Open and the borrow.
type emptyBorrow struct{ called bool }

func (b *emptyBorrow) WithBytes(fn func([]byte) error) error {
	b.called = true
	return fn(nil)
}

// fullBorrow yields real bytes, so the same seam covers the success path.
type fullBorrow struct{ data []byte }

func (b *fullBorrow) WithBytes(fn func([]byte) error) error { return fn(b.data) }

// A borrow that yields nothing is REFUSED, not copied.
//
// The failure this pins is silent and unrecoverable: copying zero bytes into a
// correctly sized destination returns a zero-filled share that looks valid,
// while Claim goes on to destroy the secret and mark the share consumed. The
// custodian holds zeros and the store will never hand the real share over
// again (go-qrdb#74).
func TestCopyShareOut_RefusesAnEmptyBorrow(t *testing.T) {
	src := &emptyBorrow{}

	out, err := copyShareOut(src)

	require.True(t, src.called, "the borrow was never entered")
	assert.ErrorIs(t, err, ErrShareUnreadable)
	assert.Nil(t, out, "a refused copy must return nothing, not a zero-filled slice")
}

// The success path returns an independent copy of the borrowed bytes.
//
// Independence matters as much as the contents: the source lives in protected
// memory that is destroyed immediately after, so a returned slice aliasing it
// would be freed under the caller.
func TestCopyShareOut_CopiesTheBorrowedBytes(t *testing.T) {
	data := []byte(`{"index":1,"value":"abc123"}`)
	src := &fullBorrow{data: data}

	out, err := copyShareOut(src)

	require.NoError(t, err)
	assert.Equal(t, data, out)

	// Mutating the source must not change what the caller holds.
	data[0] = 'X'
	assert.NotEqual(t, data, out, "copyShareOut returned a slice aliasing the borrow")
}
