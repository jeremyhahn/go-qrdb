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
	// Copy out before we destroy the buffer & secret.
	src := buf.Bytes()
	out := make([]byte, len(src))
	copy(out, src)
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
// ceremony is abandoned.
func (s *sealedShareStore) DestroyAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for username, sec := range s.shares {
		sec.Destroy()
		delete(s.shares, username)
	}
}
