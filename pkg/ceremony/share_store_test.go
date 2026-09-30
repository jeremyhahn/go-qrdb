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
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-enclave"
)

func TestSealedShareStore_StoreAndClaim(t *testing.T) {
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

func TestSealedShareStore_ClaimNonexistent(t *testing.T) {
	store := newSealedShareStore()

	_, err := store.Claim("nobody")
	assert.ErrorIs(t, err, ErrShareNotFound)
}

func TestSealedShareStore_DoubleClaim(t *testing.T) {
	store := newSealedShareStore()

	err := store.Store("bob", []byte(`{"index":2,"value":"def456"}`))
	require.NoError(t, err)

	_, err = store.Claim("bob")
	require.NoError(t, err)

	_, err = store.Claim("bob")
	assert.ErrorIs(t, err, ErrShareAlreadyClaimed)
}

func TestSealedShareStore_ExistsAndClaimed(t *testing.T) {
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

func TestSealedShareStore_DestroyAll(t *testing.T) {
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

func TestSealedShareStore_Reset_ClearsClaimHistory(t *testing.T) {
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

func TestSealedShareStore_Reset_DestroysUnclaimedShares(t *testing.T) {
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

func TestSealedShareStore_DestroyAll_PreservesClaimHistory(t *testing.T) {
	store := newSealedShareStore()

	err := store.Store("alice", []byte(`share-a`))
	require.NoError(t, err)
	_, err = store.Claim("alice")
	require.NoError(t, err)

	store.DestroyAll()

	// DestroyAll is a cleanup, not a new round: claim history survives.
	assert.True(t, store.Claimed("alice"))
}

func TestSealedShareStore_StoreMultipleClaimOne(t *testing.T) {
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

func TestSealedShareStore_StoreOverwrite(t *testing.T) {
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

// TestSealedShareStore_ConsumesTheCallerSlice pins the wiping contract that
// makes "shares are never stored" true in memory as well as on disk.
//
// Store documents that the input slice is consumed. If it merely copied the
// bytes and left the caller's slice intact, the plaintext share would survive
// on the heap after the ceremony believed it had been enclaved -- readable in a
// core dump or a swapped page, which is exactly what the enclave
// exists to prevent. The share bytes are the root key: anything that lets a
// threshold of them be recovered from a crashed process defeats the scheme.
func TestSealedShareStore_ConsumesTheCallerSlice(t *testing.T) {
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

// TestSealedShareStore_ClaimDestroysTheEnclave pins the other half of the
// invariant: a share is claimable exactly once, and nothing is retained
// afterwards for a second reader.
//
// The custodian is responsible for storing their share from the moment they
// retrieve it. If the server kept a copy, that responsibility would be a
// fiction and the server would hold enough material to rebuild the root key on
// its own -- which is the precise property an M-of-N ceremony is chosen to
// avoid.
func TestSealedShareStore_ClaimDestroysTheEnclave(t *testing.T) {
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
// The failure is provoked with a real process-wide sweep rather than a stub,
// because the sweep IS the condition this pins: go-enclave's WipeAll (which
// replaced memguard's process-wide purge, go-qrdb#114) destroys every live
// enclave in the process, including this share's, on the same shutdown path
// a binary's main() drives.
//
// It runs in a SUBPROCESS, unlike the rest of this file. WipeAll additionally
// destroys the process Sealer's own keys (per its documentation, "sweeps the
// process SEALING KEY instead"), which is a ONE-SHOT, TERMINAL event: nothing
// in this package can install a fresh Sealer afterward, so every OTHER test in
// this binary that seals a share would start failing with "this sealer's keys
// were destroyed by a shutdown sweep" if this one ran WipeAll in-process. A
// binary calls WipeAll exactly once, on its way out — this test plays that
// binary's role in a process of its own, which is the only place doing so is
// safe.
func TestShareStore_FailedClaimDoesNotConsumeTheShare(t *testing.T) {
	const childEnv = "QRDB_CEREMONY_SHARESTORE_WIPEALL_CHILD"
	if os.Getenv(childEnv) != "" {
		runShareStoreWipeAllChild()
		return
	}

	args := []string{"-test.run=^TestShareStore_FailedClaimDoesNotConsumeTheShare$", "-test.timeout=20s"}
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		// Forward the coverage directory so this subprocess's execution —
		// otherwise invisible to `go tool cover` — is merged into the gate's
		// profile by `make coverage`'s `go tool covdata textfmt` step.
		args = append(args, "-test.gocoverdir="+dir)
	}
	// #nosec G204,G702 -- re-exec of this test binary: argv[0] is os.Args[0], the path `go test` invoked, and the arguments are compile-time literals plus the GOCOVERDIR the go tool exported for this run. No shell is involved.
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), childEnv+"=1")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "subprocess failed: %s", out)
	require.Contains(t, string(out), "PASS: claim after WipeAll refused and did not consume the share")
}

// runShareStoreWipeAllChild is the subprocess body for
// TestShareStore_FailedClaimDoesNotConsumeTheShare. Assertion failures exit
// the process nonzero after printing why, rather than using *testing.T: this
// runs as a plain function in a subprocess whose one dispatched test is the
// caller above, so a t.Fatal here would only end that same single goroutine,
// not signal failure to the parent the way a nonzero exit does.
func runShareStoreWipeAllChild() {
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		os.Exit(1)
	}

	store := newSealedShareStore()
	if err := store.Store("alice", []byte(`{"index":1,"value":"abc123"}`)); err != nil {
		fail("Store: %v", err)
	}
	if !store.Exists("alice") {
		fail("share does not exist after Store")
	}

	// Destroys every live enclave in the process, this share's included, and
	// the process Sealer's keys along with it — terminal for this process,
	// which is exactly why it runs here rather than in the parent binary.
	if _, err := enclave.WipeAll(); err != nil {
		fail("enclave.WipeAll: %v", err)
	}

	if _, err := store.Claim("alice"); err == nil {
		fail("a claim on swept material must not report success")
	}
	if store.Claimed("alice") {
		fail("a failed claim marked the share consumed — the custodian holds " +
			"nothing and the store will never hand it over again")
	}
	fmt.Println("PASS: claim after WipeAll refused and did not consume the share")
}

// emptyBorrow is a borrow that yields no bytes — the shape a process-wide
// enclave.WipeAll sweep leaves a live buffer in.
//
// A fake is used because the condition cannot be built from a real
// *secure.Buffer: both constructors reject a zero length, and WithBytes
// refuses a destroyed buffer without calling the callback. The branch guards a
// state only a sweep produces, mid-Claim, between Open and the borrow.
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
