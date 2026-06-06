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

package secure

import (
	"sync/atomic"

	"github.com/awnumar/memguard"
)

// Secret holds long-lived sensitive material at rest, encrypted inside a
// memguard.Enclave. The plaintext is only materialized transiently via
// Open, which returns a Buffer the caller is responsible for destroying.
//
// Secret is safe to pass between goroutines — the underlying enclave
// pointer is mutated only through atomic swap on Destroy, which is
// idempotent. Multiple concurrent Open calls are permitted; each returns
// an independent Buffer backed by freshly decrypted memory.
//
// The zero value is unusable; callers must use NewSecret or
// NewSecretFromBuffer.
type Secret struct {
	// enclave is an atomic.Pointer[memguard.Enclave]. Using atomic.Pointer
	// rather than atomic.Value gives lock-free, type-safe swap + load
	// without boxing. Destroy atomically swaps in nil.
	enclave atomic.Pointer[memguard.Enclave]
}

// NewSecret seals src into a memguard enclave and returns a new Secret.
// On return, src is wiped — the caller must not reuse it. A nil or empty
// src returns (nil, *InvalidSizeError).
func NewSecret(src []byte) (*Secret, error) {
	if len(src) == 0 {
		return nil, &InvalidSizeError{Size: len(src)}
	}
	s := &Secret{}
	// memguard.NewEnclave copies src into an enclave and wipes the source.
	enc := memguard.NewEnclave(src)
	s.enclave.Store(enc)
	return s, nil
}

// NewSecretFromBuffer seals buf into a new Secret, consuming buf. On
// success, buf is destroyed. Returns (nil, *NilBufferError) if buf is
// nil or already destroyed.
func NewSecretFromBuffer(buf *Buffer) (*Secret, error) {
	if buf == nil || buf.destroyed.Load() || buf.buf == nil {
		return nil, &NilBufferError{}
	}
	// memguard.LockedBuffer.Seal consumes the buffer and produces an
	// enclave; the LockedBuffer is destroyed as part of the seal.
	enc := buf.buf.Seal()
	// Mark our wrapper destroyed so repeated Destroy is a no-op and
	// Bytes/Size return zero.
	buf.destroyed.Store(true)
	buf.buf = nil
	s := &Secret{}
	s.enclave.Store(enc)
	return s, nil
}

// Open decrypts the sealed material into a new Buffer that the caller is
// responsible for destroying. Returns (nil, *NilSecretError) if the
// Secret has been destroyed or is a zero value. Wraps any underlying
// memguard failure in *OpenError.
func (s *Secret) Open() (*Buffer, error) {
	if s == nil {
		return nil, &NilSecretError{}
	}
	enc := s.enclave.Load()
	if enc == nil {
		return nil, &NilSecretError{}
	}
	lb, err := enc.Open()
	if err != nil {
		return nil, &OpenError{Err: err}
	}
	return &Buffer{buf: lb}, nil
}

// Destroy wipes the enclave and releases its resources. Safe to call
// multiple times — subsequent calls are no-ops. Safe to call on a nil
// receiver. Concurrent Destroy calls are serialized via the atomic swap.
func (s *Secret) Destroy() {
	if s == nil {
		return
	}
	enc := s.enclave.Swap(nil)
	if enc == nil {
		return
	}
	// memguard.Enclave has no direct Destroy — the canonical way to
	// actively zero it is to Open into a LockedBuffer and destroy that
	// buffer. If Open fails (e.g., the enclave was already reaped),
	// we silently drop; the Swap above already cleared our reference.
	lb, err := enc.Open()
	if err == nil && lb != nil {
		lb.Destroy()
	}
}
