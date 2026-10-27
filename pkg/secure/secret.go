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
	"context"
	"sync/atomic"

	"github.com/jeremyhahn/go-enclave"
)

// Secret holds long-lived sensitive material at rest, encrypted inside a
// go-enclave Sealed (AES-256-GCM under the process Sealer). The
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
// enclave.Sealed: sealing is AES-256-GCM, opening is admitted against
// the same process-wide locked-memory budget every Buffer uses
// (enclave.ProcessAllocator), and a failed admission or a failed mlock
// returns a typed error instead of purging the process.
type Secret struct {
	// enclave points at the enclave.Sealed interface value holding the
	// ciphertext. Using atomic.Pointer rather than atomic.Value gives
	// lock-free, type-safe swap + load, and a nil pointer to mean "no
	// enclave", which atomic.Value cannot store. Destroy atomically swaps in
	// nil.
	enclave atomic.Pointer[enclave.Sealed]

	// empty is true while the explicit live zero-length value NewEmptySecret
	// returns is alive. go-enclave refuses to seal nothing, so an empty
	// Secret holds no enclave; Destroy clears this flag, which is what
	// destroys it.
	empty atomic.Bool
}

// NewEmptySecret returns a live Secret that holds zero bytes: the at-rest
// counterpart of NewEmptyBuffer. IsAlive reports true until Destroy, Open
// returns an empty Buffer, and WithBytes calls its callback with an empty
// slice.
//
// NewSecret still refuses an empty src with *InvalidSizeError; the empty
// value is asked for by name. Nothing is sealed, so it cannot fail, and a
// shutdown enclave.WipeAll, which makes every sealed Secret unopenable, leaves
// it openable: there is no key standing between it and its (empty) plaintext.
func NewEmptySecret() *Secret {
	s := &Secret{}
	s.empty.Store(true)
	return s
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
//
// ctx is the real caller context, not context.Background(): go-enclave's
// Sealer may be backed by a provider that blocks, and ctx is checked before
// the seal and passed through to it.
func NewSecret(ctx context.Context, src []byte) (*Secret, error) {
	if len(src) == 0 {
		return nil, &InvalidSizeError{Size: len(src)}
	}
	// enclave.NewSealed takes ownership of src and wipes it on every path,
	// including failure.
	sealed, err := newSealed(ctx, src)
	if err != nil {
		return nil, err
	}
	s := &Secret{}
	s.enclave.Store(&sealed)
	return s, nil
}

// NewSecretFromBuffer seals buf into a new Secret, consuming buf: buf is
// destroyed on every return path once it has passed the nil / destroyed /
// zero-value checks, which return (nil, *NilBufferError) and leave it alone.
// The empty Buffer NewEmptyBuffer returns becomes the empty Secret
// NewEmptySecret returns.
//
// The plaintext is sealed straight out of buf's own locked pages. There is no
// intermediate plaintext copy: enclave.NewSealed reads buf's pages inside
// buf's own go-enclave borrow and wipes them as it takes ownership, and buf is
// then destroyed. Because nothing here is admitted against the process
// locked-memory budget (sealing is not, as of go-enclave v0.10.0-alpha), an
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
//
// ctx is the real caller context, not context.Background(): go-enclave's
// Sealer may be backed by a provider that blocks, and ctx is checked before
// the seal and passed through to it.
func NewSecretFromBuffer(ctx context.Context, buf *Buffer) (*Secret, error) {
	if buf == nil {
		return nil, &NilBufferError{}
	}
	checkNotBorrowed("NewSecretFromBuffer", buf)
	if buf.destroyed.Load() {
		return nil, &NilBufferError{}
	}
	if buf.empty {
		buf.Destroy()
		return NewEmptySecret(), nil
	}
	if buf.hollow() || buf.enc.Size() == 0 {
		return nil, &NilBufferError{}
	}
	defer buf.Destroy()

	var sealed enclave.Sealed
	var opened bool
	withErr := buf.enc.WithBytes(func(plaintext []byte) error {
		opened = true
		var sealErr error
		sealed, sealErr = newSealed(ctx, plaintext)
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
	s.enclave.Store(&sealed)
	return s, nil
}

// Open decrypts the sealed material into a new Buffer that the caller is
// responsible for destroying, admitted against the process-wide
// locked-memory budget. Returns (nil, *NilSecretError) if the Secret has
// been destroyed or is a zero value. Wraps any underlying go-enclave failure
// in *OpenError.
//
// ctx is the real caller context, not context.Background(): in
// [enclave.ModePerOperation] the open may reach a key provider that blocks,
// and ctx bounds that call.
//
// go-enclave v0.7.0-alpha returns the recovered plaintext FROZEN — a write
// through it faults. Buffer's documented contract is uniformly writable
// regardless of constructor, so Open copies the frozen plaintext into a
// fresh, writable, admitted Buffer and destroys the frozen source before
// returning. A non-fatal release report from that destroy (go-qrdb's own
// test suite cannot force this path; see Buffer.DestroyErr) is logged at
// WARN rather than discarded, since Open has nowhere else to hand it.
func (s *Secret) Open(ctx context.Context) (*Buffer, error) {
	if s == nil {
		return nil, &NilSecretError{}
	}
	sealed := s.enclave.Load()
	if sealed == nil {
		if s.empty.Load() {
			return NewEmptyBuffer(), nil
		}
		return nil, &NilSecretError{}
	}
	a, err := processAllocator()
	if err != nil {
		return nil, &OpenError{Err: err}
	}
	opened, err := (*sealed).OpenIn(ctx, a)
	if err != nil {
		return nil, &OpenError{Err: err}
	}
	frozen, ok := opened.(enclave.ByteAccess)
	if !ok {
		LogRelease("opened enclave", opened.Destroy())
		return nil, &OpenError{Err: &enclave.CapabilityError{
			Backend:    opened.Backend().Name(),
			Capability: enclave.CapabilityBytes,
		}}
	}
	buf, copyErr := copyFrozenToWritable(frozen)
	if copyErr != nil {
		return nil, &OpenError{Err: copyErr}
	}
	return buf, nil
}

// WithBytes opens s, calls fn with the plaintext, and destroys the opened
// Buffer before returning: the scoped form of Open, for a caller that only
// needs the plaintext for the length of one call and so never holds a Buffer
// it could forget to destroy.
//
// fn's rules are Buffer.WithBytes's: it MUST NOT retain the slice beyond the
// call, and MUST NOT call into this package (the borrow guard reports it).
// The empty Secret calls fn with a zero-length slice.
//
// Returns *NilSecretError for a nil fn and for a nil, destroyed or zero-value
// Secret, Open's *OpenError when the plaintext cannot be recovered, and fn's
// own error unchanged. ctx is Open's.
func (s *Secret) WithBytes(ctx context.Context, fn func([]byte) error) error {
	if fn == nil {
		return &NilSecretError{}
	}
	buf, err := s.Open(ctx)
	if err != nil {
		return err
	}
	defer buf.Destroy()
	return buf.WithBytes(fn)
}

// IsAlive reports whether s still holds its value: true from construction
// until Destroy, including for the empty Secret NewEmptySecret returns, and
// false for a nil, destroyed or zero-value Secret.
//
// It says nothing about whether Open will succeed: a live Secret becomes
// unopenable, but not destroyed, once a shutdown enclave.WipeAll has wiped
// the process Sealer's keys.
func (s *Secret) IsAlive() bool {
	if s == nil {
		return false
	}
	return s.enclave.Load() != nil || s.empty.Load()
}

// copyFrozenToWritable copies frozen's plaintext into a freshly allocated,
// writable Buffer and destroys frozen, logging any non-fatal release report
// at WARN. frozen is consumed on every return path, including failure.
//
// This is the output direction of the nesting rule (pkg/secure's doc, rule 6):
// dst is allocated BEFORE frozen is borrowed and written in place inside the
// borrow, so the plaintext moves from one protected region to the other and
// never through the heap. The copy holds BOTH enclaves' borrows for its whole
// duration — different objects, independent locks — so a concurrent WipeAll
// cannot unmap either mid-copy.
func copyFrozenToWritable(frozen enclave.ByteAccess) (*Buffer, error) {
	size := frozen.Size()
	buf, err := NewBuffer(size)
	if err != nil {
		LogRelease("frozen enclave", frozen.Destroy())
		return nil, err
	}
	copyErr := buf.WithBytes(func(dst []byte) error {
		return frozen.WithBytes(func(src []byte) error {
			copy(dst, src)
			return nil
		})
	})
	LogRelease("frozen enclave", frozen.Destroy())
	if copyErr != nil {
		buf.Destroy()
		return nil, copyErr
	}
	return buf, nil
}

// Destroy wipes the sealed enclave and releases its resources. Safe to call
// multiple times — subsequent calls are no-ops. Safe to call on a nil
// receiver. Concurrent Destroy calls are serialized via the atomic swap.
func (s *Secret) Destroy() {
	if s == nil {
		return
	}
	s.empty.Store(false)
	sealed := s.enclave.Swap(nil)
	if sealed == nil {
		return
	}
	LogRelease("sealed secret", (*sealed).Destroy())
}
