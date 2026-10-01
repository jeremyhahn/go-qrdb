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

	"github.com/jeremyhahn/go-enclave"
)

// Secret holds long-lived sensitive material at rest, encrypted inside a
// go-enclave SealedEnclave (AES-256-GCM under the process Sealer). The
// plaintext is only materialized transiently via Open, which returns a
// Buffer the caller is responsible for destroying.
//
// Secret is safe to pass between goroutines — the underlying enclave
// pointer is mutated only through atomic swap on Destroy, which is
// idempotent. Multiple concurrent Open calls are permitted; each returns
// an independent Buffer backed by freshly decrypted memory.
//
// The zero value is unusable; callers must use NewSecret or
// NewSecretFromBuffer.
//
// # Why this is not raw memguard (go-qrdb#114, go-qrdb#553)
//
// This type used to wrap memguard.Enclave, sealed with NaCl secretbox and
// opened through a process-wide purge lock. It now wraps
// enclave.SealedEnclave: sealing is AES-256-GCM, opening is admitted against
// the same process-wide locked-memory budget every Buffer uses
// (enclave.ProcessAllocator), and a failed admission or a failed mlock
// returns a typed error instead of purging the process.
type Secret struct {
	// enclave is an atomic.Pointer[enclave.SealedEnclave]. Using
	// atomic.Pointer rather than atomic.Value gives lock-free, type-safe swap
	// + load without boxing. Destroy atomically swaps in nil.
	enclave atomic.Pointer[enclave.SealedEnclave]
}

// newSealed indirects enclave.NewSealed so a test can wrap it with an
// injected delay: enclave.NewSealed itself completes in low microseconds, far
// too fast to reliably race enclave.WipeAll against without instrumentation,
// so a test proving NewSecretFromBuffer's borrow is actually load-bearing
// (rather than merely absent on a path too fast to hit) needs a seam here.
// Production behavior is unchanged — this is a direct copy of the function
// value.
var newSealed = enclave.NewSealed

// NewSecret seals src into a go-enclave sealed enclave and returns a new
// Secret. On return, src is wiped — the caller must not reuse it, on every
// return path including failure. A nil or empty src returns
// (nil, *InvalidSizeError).
func NewSecret(src []byte) (*Secret, error) {
	if len(src) == 0 {
		return nil, &InvalidSizeError{Size: len(src)}
	}
	checkNoBorrow("NewSecret")
	// enclave.NewSealed takes ownership of src and wipes it on every path,
	// including failure.
	sealed, err := newSealed(src)
	if err != nil {
		return nil, err
	}
	s := &Secret{}
	s.enclave.Store(sealed)
	return s, nil
}

// NewSecretFromBuffer seals buf into a new Secret, consuming buf: buf is
// destroyed on every return path once it has passed the nil / destroyed /
// zero-value checks, which return (nil, *NilBufferError) and leave it alone.
//
// The plaintext is sealed straight out of buf's own locked pages. There is no
// intermediate plaintext copy: enclave.NewSealed reads buf's pages inside
// buf's own go-enclave borrow and wipes them as it takes ownership, and buf is
// then destroyed. Because nothing here is admitted against the process
// locked-memory budget (sealing is not, as of go-enclave v0.6.0-alpha), an
// exhausted budget cannot make this fail.
//
// The borrow is held for the whole sealing call, not released after a bare
// read: buf is on go-enclave's shutdown ledger, so a concurrent
// enclave.WipeAll needs no reference to it to unmap its pages, and an
// unguarded alias handed to NewSealed could fault mid-read. Held, a racing
// WipeAll skips buf instead. NewSealed allocates its own, different enclave
// for the key it seals under; go-enclave gives every enclave an independent
// lock, so that nesting cannot deadlock.
//
// A sealing failure (enclave.ErrSealerWiped, enclave.ErrLockedMemoryUnavailable
// for the key's pages, …) is returned unwrapped, and buf is consumed all the
// same: NewSealed has already wiped the pages it was handed.
func NewSecretFromBuffer(buf *Buffer) (*Secret, error) {
	if buf == nil {
		return nil, &NilBufferError{}
	}
	checkNoBorrow("NewSecretFromBuffer")
	if buf.destroyed.Load() || buf.enc.Size() == 0 {
		return nil, &NilBufferError{}
	}
	defer buf.Destroy()

	var sealed *enclave.SealedEnclave
	var opened bool
	withErr := buf.enc.WithBytes(func(plaintext []byte) error {
		opened = true
		var sealErr error
		sealed, sealErr = newSealed(plaintext)
		return sealErr
	})
	if withErr != nil {
		if !opened {
			// buf.enc.WithBytes refused before invoking fn: buf was destroyed
			// or emptied (a racing Destroy or WipeAll) after the checks above.
			return nil, &NilBufferError{}
		}
		return nil, withErr
	}
	s := &Secret{}
	s.enclave.Store(sealed)
	return s, nil
}

// Open decrypts the sealed material into a new Buffer that the caller is
// responsible for destroying, admitted against the process-wide
// locked-memory budget. Returns (nil, *NilSecretError) if the Secret has
// been destroyed or is a zero value. Wraps any underlying go-enclave failure
// in *OpenError.
func (s *Secret) Open() (*Buffer, error) {
	if s == nil {
		return nil, &NilSecretError{}
	}
	checkNoBorrow("Secret.Open")
	sealed := s.enclave.Load()
	if sealed == nil {
		return nil, &NilSecretError{}
	}
	a, err := processAllocator()
	if err != nil {
		return nil, &OpenError{Err: err}
	}
	enc, err := sealed.OpenWith(a)
	if err != nil {
		return nil, &OpenError{Err: err}
	}
	return &Buffer{enc: enc}, nil
}

// Destroy wipes the sealed enclave and releases its resources. Safe to call
// multiple times — subsequent calls are no-ops. Safe to call on a nil
// receiver. Concurrent Destroy calls are serialized via the atomic swap.
func (s *Secret) Destroy() {
	if s == nil {
		return
	}
	checkNoBorrow("Secret.Destroy")
	sealed := s.enclave.Swap(nil)
	if sealed == nil {
		return
	}
	sealed.Destroy()
}
