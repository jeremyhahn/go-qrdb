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

// Buffer is a short-lived, mlocked, canary-guarded working copy of
// sensitive bytes. It wraps memguard.LockedBuffer.
//
// Buffers MUST be destroyed when no longer needed — callers are expected
// to defer Destroy() immediately after construction. The zero value is
// unusable; callers must use one of the provided constructors.
//
// Thread safety: Destroy is safe to call concurrently with itself (it is
// idempotent via atomic CAS). However, Bytes() / Size() / Wipe() while
// another goroutine is calling Destroy() is undefined and must be
// avoided — Buffer is intended to be single-owner for its working life.
type Buffer struct {
	buf       *memguard.LockedBuffer
	destroyed atomic.Bool
}

// NewBuffer allocates a zero-filled Buffer of size bytes. The memory is
// locked (mlock), guard-paged on both sides, and zero-wiped on destroy.
// Returns an *InvalidSizeError when size <= 0.
func NewBuffer(size int) (*Buffer, error) {
	if size <= 0 {
		return nil, &InvalidSizeError{Size: size}
	}
	// Purge-class: memguard purges the whole process if this allocation fails.
	// See purgeMu.
	purgeMu.Lock()
	defer purgeMu.Unlock()
	return &Buffer{buf: memguard.NewBuffer(size)}, nil
}

// NewBufferFromBytes creates a Buffer by copying src into mlocked
// memory. On return, src is wiped — the caller must not reuse it. Nil
// or empty src returns (nil, *InvalidSizeError).
func NewBufferFromBytes(src []byte) (*Buffer, error) {
	if len(src) == 0 {
		return nil, &InvalidSizeError{Size: len(src)}
	}
	// Purge-class, as NewBuffer. See purgeMu.
	purgeMu.Lock()
	defer purgeMu.Unlock()
	// memguard.NewBufferFromBytes copies and wipes the source.
	return &Buffer{buf: memguard.NewBufferFromBytes(src)}, nil
}

// Bytes returns the underlying mlocked byte slice. The caller MUST NOT
// retain this slice beyond the lifetime of the Buffer, MUST NOT copy
// its contents into unprotected memory, and MUST NOT mutate it unless
// explicitly intended. Returns nil after Destroy.
//
// # This method cannot protect the slice it returns
//
// The purge coordination described on purgeMu covers the slice-header read
// made HERE, and stops there. Once Bytes has returned, the caller holds a
// slice into mlocked pages with no lock held, and a memguard failure anywhere
// in the process can unmap those pages while the caller reads them — the
// hazard purgeMu exists for, on the one path that cannot be closed from inside
// this package (go-qrdb#71).
//
// Use WithBytes for anything that must be safe against that. It holds the
// borrow for the whole read, which is the only shape a lock can cover. Bytes
// remains because a large number of call sites take a slice and pass it
// onward, and converting them is a separate change from adding the guarantee.
func (b *Buffer) Bytes() []byte {
	if b == nil {
		return nil
	}
	purgeMu.RLock()
	defer purgeMu.RUnlock()
	if b.destroyed.Load() || b.buf == nil {
		return nil
	}
	return b.buf.Bytes()
}

// WithBytes calls fn with the buffer's mlocked bytes, holding the borrow for
// the whole call so no purge can run underneath it.
//
// This is the borrow with an actual guarantee, and the reason to prefer it to
// Bytes: a memguard failure anywhere in the process purges every live enclave,
// and only a borrow held across the READ — not merely across the accessor —
// keeps the pages mapped while fn touches them.
//
// fn MUST NOT retain the slice beyond the call, and MUST NOT call back into
// this package: a nested borrow deadlocks against any purge-class operation
// that queued in between. See purgeMu.
//
// Returns *NilBufferError when the buffer is nil, destroyed, or a zero value.
// Any error fn returns is passed through unchanged.
func (b *Buffer) WithBytes(fn func([]byte) error) error {
	if b == nil || fn == nil {
		return &NilBufferError{}
	}
	purgeMu.RLock()
	defer purgeMu.RUnlock()
	if b.destroyed.Load() || b.buf == nil {
		return &NilBufferError{}
	}
	return fn(b.buf.Bytes())
}

// Size returns the length of the underlying mlocked buffer. Returns 0
// after Destroy or on a zero-value Buffer.
func (b *Buffer) Size() int {
	if b == nil {
		return 0
	}
	// Borrow-class despite looking like a scalar read: Size is
	// len(buffer.Bytes()), a slice header a concurrent purge rewrites.
	purgeMu.RLock()
	defer purgeMu.RUnlock()
	if b.destroyed.Load() || b.buf == nil {
		return 0
	}
	return b.buf.Size()
}

// Destroy wipes and releases the mlocked memory. Safe to call multiple
// times — subsequent calls are no-ops. Safe to call on a nil receiver.
func (b *Buffer) Destroy() {
	if b == nil {
		return
	}
	if !b.destroyed.CompareAndSwap(false, true) {
		return
	}
	// PURGE-class, not a read. memguard's destroy calls core.Panic when
	// releasing pages fails, which purges every live enclave in the process.
	// See purgeMu.
	purgeMu.Lock()
	defer purgeMu.Unlock()
	if b.buf != nil {
		b.buf.Destroy()
		b.buf = nil
	}
}

// Wipe zeroes a caller-owned []byte using memguard's
// compiler-optimization-resistant wipe. Useful at the boundary where a
// caller holds a raw slice that must be cleared before returning (e.g.,
// after copying its contents into a Buffer). Nil or empty slices are
// no-ops.
func Wipe(b []byte) {
	if len(b) == 0 {
		return
	}
	memguard.WipeBytes(b)
}
