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
	"errors"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// sealedShareStore provides secure in-memory storage for Shamir key
// shares using *secure.Secret enclaves. Shares are NEVER persisted to
// disk. Once claimed, the Secret is destroyed and the share bytes are
// wiped from memory.
type sealedShareStore struct {
	mu      sync.Mutex
	shares  map[string]*secure.Secret
	claimed map[string]bool
}

// newSealedShareStore creates a new in-memory share store backed by
// *secure.Secret enclaves.
func newSealedShareStore() *sealedShareStore {
	return &sealedShareStore{
		shares:  make(map[string]*secure.Secret),
		claimed: make(map[string]bool),
	}
}

// Store securely stores share bytes for the given username. The input
// slice is consumed — shareJSON is wiped before this function returns.
func (s *sealedShareStore) Store(username string, shareJSON []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sec, err := secure.NewSecret(shareJSON)
	if err != nil {
		// NewSecret wipes shareJSON only on success; defensively wipe on
		// the error path so callers never leak bytes.
		secure.Wipe(shareJSON)
		return err
	}
	s.shares[username] = sec
	return nil
}

// Claim retrieves and returns a copy of the share bytes for the given
// username, then destroys the sealed material. After claiming, the
// share is no longer available. Returns ErrShareAlreadyClaimed if the
// share was previously claimed, or ErrShareNotFound if no share was
// ever stored for the username.
func (s *sealedShareStore) Claim(username string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.claimed[username] {
		return nil, ErrShareAlreadyClaimed
	}

	sec, ok := s.shares[username]
	if !ok {
		return nil, ErrShareNotFound
	}

	buf, err := sec.Open()
	if err != nil {
		return nil, err
	}
	// Copy out before we destroy the buffer & secret, with the copy INSIDE a
	// coordinated borrow: Bytes() covered only the slice-header read, so a
	// memguard failure elsewhere in the process could unmap the share's pages
	// midway through the copy (go-qrdb#74).
	//
	// The copy itself is unavoidable — this function returns []byte — so this
	// is the issue's remedy 1, narrowing the window rather than closing it.
	// Destroy stays OUTSIDE the borrow, and that placement is load-bearing:
	// Destroy is purge-class and takes purgeMu exclusively, so calling it from
	// inside the shared hold would deadlock the caller against itself.
	// The destination is allocated INSIDE the callback from len(src), never
	// from buf.Size() beforehand.
	//
	// Size takes and releases the purge lock, so sizing first leaves a gap
	// before WithBytes reacquires it. A process-wide memguard purge landing in
	// that gap gives a correctly sized destination and an empty source: copy
	// becomes a no-op, and Claim returns a zero-filled slice of the right
	// length while marking the real share consumed. That is fail-open
	// corruption of one-time recovery material — the share is spent and the
	// caller holds zeros (found in review of go-qrdb#74).
	//
	// An empty source is rejected for the same reason: nothing here should
	// destroy the secret and record the claim on material it did not actually
	// read.
	out, err := copyShareOut(buf)
	if err != nil {
		buf.Destroy()
		return nil, err
	}
	buf.Destroy()

	sec.Destroy()
	delete(s.shares, username)
	s.claimed[username] = true

	return out, nil
}

// Exists reports whether a share is currently stored (not yet claimed)
// for the given username.
func (s *sealedShareStore) Exists(username string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.shares[username]
	return ok
}

// Claimed reports whether a share was previously stored and has been
// claimed for the given username. Returns true only if the share was
// stored and subsequently claimed.
func (s *sealedShareStore) Claimed(username string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimed[username]
}

// DestroyAll destroys all remaining sealed shares, wiping share data
// from memory. This should be called during cleanup or when the
// ceremony is abandoned. Claim history is preserved; use Reset to begin
// a fresh distribution round.
func (s *sealedShareStore) DestroyAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for username, sec := range s.shares {
		sec.Destroy()
		delete(s.shares, username)
	}
}

// Reset destroys all remaining sealed shares AND clears the claim
// history, preparing the store for a fresh distribution round. Rekey
// flows must use Reset (not DestroyAll) so officers who claimed shares
// in the prior round can claim their newly distributed shares.
func (s *sealedShareStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for username, sec := range s.shares {
		sec.Destroy()
		delete(s.shares, username)
	}
	for username := range s.claimed {
		delete(s.claimed, username)
	}
}

// copyShareOut copies a share's bytes out of protected memory, refusing to
// return anything when the borrow yields nothing.
//
// The mechanism is secure.CopyOut, which is where this shape now lives: it was
// written here for go-qrdb#74's ceremony half and five other call sites needed
// the identical four lines, so it was hoisted rather than copied. The two
// properties that make it correct are stated on CopyOut and are not restated
// here — the destination is allocated INSIDE the callback from len(src) so no
// purge can land between a size read and the copy, and an empty source is
// refused rather than returned as zeros.
//
// The refusal keeps this package's own error. A caller of Claim distinguishes
// "the share could not be read" from every other ceremony failure by
// ErrShareUnreadable, and a share is one-time material: the wrapper exists so
// that contract does not become secure.EmptyBorrowError at the transport
// boundary.
func copyShareOut(buf secure.Borrower) ([]byte, error) {
	out, err := secure.CopyOut(buf)
	if err != nil {
		var empty *secure.EmptyBorrowError
		if errors.As(err, &empty) {
			return nil, ErrShareUnreadable
		}
		return nil, err
	}
	return out, nil
}
