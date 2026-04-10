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

	"github.com/awnumar/memguard"
)

// memguardShareStore provides secure in-memory storage for Shamir key
// shares using memguard locked buffers. Shares are NEVER persisted to
// disk. Once claimed, the locked buffer is destroyed and the share
// bytes are wiped from memory.
type memguardShareStore struct {
	mu      sync.Mutex
	shares  map[string]*memguard.LockedBuffer
	claimed map[string]bool
}

// newMemguardShareStore creates a new in-memory share store backed
// by memguard locked buffers.
func newMemguardShareStore() *memguardShareStore {
	return &memguardShareStore{
		shares:  make(map[string]*memguard.LockedBuffer),
		claimed: make(map[string]bool),
	}
}

// Store securely stores share bytes for the given username in a
// memguard LockedBuffer. The input bytes are copied into the locked
// buffer and the original bytes are wiped.
func (s *memguardShareStore) Store(username string, shareJSON []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	buf := memguard.NewBufferFromBytes(shareJSON)
	memguard.WipeBytes(shareJSON)
	s.shares[username] = buf
	return nil
}

// Claim retrieves and returns a copy of the share bytes for the given
// username, then destroys the locked buffer. After claiming, the share
// is no longer available. Returns ErrShareAlreadyClaimed if the share
// was previously claimed, or ErrShareNotFound if no share was ever
// stored for the username.
func (s *memguardShareStore) Claim(username string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.claimed[username] {
		return nil, ErrShareAlreadyClaimed
	}

	buf, ok := s.shares[username]
	if !ok {
		return nil, ErrShareNotFound
	}

	// Copy the bytes out before destroying the buffer.
	src := buf.Bytes()
	out := make([]byte, len(src))
	copy(out, src)

	buf.Destroy()
	delete(s.shares, username)
	s.claimed[username] = true

	return out, nil
}

// Exists reports whether a share is currently stored (not yet claimed)
// for the given username.
func (s *memguardShareStore) Exists(username string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.shares[username]
	return ok
}

// Claimed reports whether a share was previously stored and has been
// claimed for the given username. Returns true only if the share was
// stored and subsequently claimed.
func (s *memguardShareStore) Claimed(username string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimed[username]
}

// DestroyAll destroys all remaining locked buffers, wiping share data
// from memory. This should be called during cleanup or when the
// ceremony is abandoned.
func (s *memguardShareStore) DestroyAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for username, buf := range s.shares {
		buf.Destroy()
		delete(s.shares, username)
	}
}
