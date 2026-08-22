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

// borrower is the borrow half of *secure.Buffer.
//
// Taken as an interface so copyShareOut's empty-source branch is reachable in
// a test. It cannot be reached through a real *secure.Buffer: NewBuffer and
// NewBufferFromBytes both reject a zero length, and WithBytes returns
// *NilBufferError for a destroyed buffer without calling the callback at all.
// The branch's condition — a LIVE buffer whose pages a process-wide memguard
// purge has already taken — is not one a caller can construct, which is
// exactly why it needed a seam rather than a cleverer test (go-qrdb#74).
type borrower interface {
	WithBytes(fn func([]byte) error) error
}

// copyShareOut copies a share's bytes out of protected memory, refusing to
// return anything when the borrow yields nothing.
//
// The destination is allocated INSIDE the callback from len(src), never from
// a size read beforehand. Size takes and releases the purge lock, so sizing
// first leaves a gap before WithBytes reacquires it; a purge landing in that
// gap gives a correctly sized destination and an empty source, and the copy
// becomes a no-op. Claim would then return a zero-filled slice of the right
// length while destroying the secret and marking the share consumed — one-time
// recovery material spent, the caller holding zeros, nothing reporting a
// fault.
//
// An empty source is therefore refused rather than copied: nothing here should
// destroy the secret and record the claim on material it did not read.
func copyShareOut(buf borrower) ([]byte, error) {
	var out []byte
	if err := buf.WithBytes(func(src []byte) error {
		if len(src) == 0 {
			return ErrShareUnreadable
		}
		out = make([]byte, len(src))
		copy(out, src)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}
