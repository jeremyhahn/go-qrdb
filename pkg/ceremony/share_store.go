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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// DefaultShareTTL is how long a distributed share is held for its holder to
// claim before it is wiped, when the configuration does not say otherwise.
const DefaultShareTTL = 24 * time.Hour

// sealedShareStore provides secure in-memory storage for Shamir key
// shares using *secure.Secret enclaves. Shares are NEVER persisted to
// disk. Once claimed, the Secret is destroyed and the share bytes are
// wiped from memory.
//
// Every held share carries its own expiry timer, so a share nobody claims is
// wiped at its deadline whether or not anything else touches the store; a lazy
// sweep on access would leave an abandoned ceremony's shares resident
// indefinitely. A timer holds no goroutine until it fires.
type sealedShareStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	shares  map[string]*heldShare
	claimed map[string]bool
	expired map[string]bool
}

// heldShare is one sealed share and the timer that wipes it at its deadline.
type heldShare struct {
	secret *secure.Secret
	timer  *time.Timer
}

// newSealedShareStore creates a new in-memory share store backed by
// *secure.Secret enclaves. A non-positive ttl selects DefaultShareTTL.
func newSealedShareStore(ttl time.Duration) *sealedShareStore {
	if ttl <= 0 {
		ttl = DefaultShareTTL
	}
	return &sealedShareStore{
		ttl:     ttl,
		shares:  make(map[string]*heldShare),
		claimed: make(map[string]bool),
		expired: make(map[string]bool),
	}
}

// Store securely stores share bytes for the given username. The input
// slice is consumed — shareJSON is wiped before this function returns.
//
// A share already held for username is destroyed before it is replaced: a
// holder has one share at a time, and dropping the old enclave without
// destroying it would leave its pages locked and its plaintext recoverable
// until the collector ran.
func (s *sealedShareStore) Store(username string, shareJSON []byte) error {
	sec, err := secure.NewSecret(shareJSON)
	if err != nil {
		// NewSecret wipes shareJSON only on success; defensively wipe on
		// the error path so callers never leak bytes.
		secure.Wipe(shareJSON)
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if prior, ok := s.shares[username]; ok {
		prior.timer.Stop()
		prior.secret.Destroy()
	}
	held := &heldShare{secret: sec}
	held.timer = time.AfterFunc(s.ttl, func() { s.expire(username, held) })
	s.shares[username] = held
	delete(s.expired, username)
	return nil
}

// expire wipes held if it is still the share held for username. A timer that
// fires after its share was claimed or replaced finds a different entry (or
// none) and does nothing.
func (s *sealedShareStore) expire(username string, held *heldShare) {
	s.mu.Lock()
	if s.shares[username] != held {
		s.mu.Unlock()
		return
	}
	delete(s.shares, username)
	s.expired[username] = true
	s.mu.Unlock()
	held.secret.Destroy()
}

// Claim retrieves and returns a copy of the share bytes for the given
// username, then destroys the sealed material. After claiming, the
// share is no longer available. Returns ErrShareAlreadyClaimed if the
// share was previously claimed, ErrShareExpired if it was wiped unclaimed at
// the end of its time-to-live, or ErrShareNotFound if no share was ever
// stored for the username.
func (s *sealedShareStore) Claim(username string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.claimed[username] {
		return nil, ErrShareAlreadyClaimed
	}

	held, ok := s.shares[username]
	if !ok {
		if s.expired[username] {
			return nil, ErrShareExpired
		}
		return nil, ErrShareNotFound
	}
	sec := held.secret

	buf, err := sec.Open()
	if err != nil {
		return nil, err
	}
	// Copy out before we destroy the buffer & secret, with the copy INSIDE a
	// coordinated borrow: Bytes() covers only the slice-header read and
	// releases go-enclave's lock the instant it returns, so a concurrent
	// Destroy of THIS SAME buffer — under memguard, a process-wide purge
	// anywhere in the process; under go-enclave, any other caller with a
	// handle to buf — could unmap the share's pages midway through the copy
	// (go-qrdb#74; see also SecretValue.WithBytes's identical fix, go-qrdb#114
	// review round 3).
	//
	// The copy itself is unavoidable — this function returns []byte — so this
	// is the issue's remedy 1, narrowing the window rather than closing it.
	// Destroy stays OUTSIDE the borrow, and that placement is load-bearing:
	// go-enclave's per-object lock is not reentrant, so calling buf.Destroy()
	// from inside buf's own WithBytes callback would deadlock this buffer
	// against itself (it would NOT affect any other Buffer or Secret in the
	// process — go-enclave gives every enclave its own independent lock,
	// unlike memguard's process-wide purgeMu this replaced).
	// The destination is allocated INSIDE the callback from len(src), never
	// from buf.Size() beforehand.
	//
	// Sizing via buf.Size() and then reading via buf.Bytes() as two separate
	// calls would leave a gap between them in which the buffer could be
	// destroyed — under memguard, by a process-wide purge landing in that gap;
	// under go-enclave, by any concurrent Destroy with a handle to buf. Either
	// way that gives a correctly sized destination and an empty source: copy
	// becomes a no-op, and Claim returns a zero-filled slice of the right
	// length while marking the real share consumed. That is fail-open
	// corruption of one-time recovery material — the share is spent and the
	// caller holds zeros (found in review of go-qrdb#74). copyShareOut's
	// single held borrow removes the gap entirely rather than narrowing it.
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

	held.timer.Stop()
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

// Expired reports whether a share distributed to username was wiped unclaimed
// at the end of its time-to-live and has not been redistributed since.
func (s *sealedShareStore) Expired(username string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expired[username]
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
	for username, held := range s.shares {
		held.timer.Stop()
		held.secret.Destroy()
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
	for username, held := range s.shares {
		held.timer.Stop()
		held.secret.Destroy()
		delete(s.shares, username)
	}
	clear(s.claimed)
	clear(s.expired)
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
