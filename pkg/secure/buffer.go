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
	return &Buffer{buf: memguard.NewBuffer(size)}, nil
}

// NewBufferFromBytes creates a Buffer by copying src into mlocked
// memory. On return, src is wiped — the caller must not reuse it. Nil
// or empty src returns (nil, *InvalidSizeError).
func NewBufferFromBytes(src []byte) (*Buffer, error) {
	if len(src) == 0 {
		return nil, &InvalidSizeError{Size: len(src)}
	}
	// memguard.NewBufferFromBytes copies and wipes the source.
	return &Buffer{buf: memguard.NewBufferFromBytes(src)}, nil
}

// Bytes returns the underlying mlocked byte slice. The caller MUST NOT
// retain this slice beyond the lifetime of the Buffer, MUST NOT copy
// its contents into unprotected memory, and MUST NOT mutate it unless
// explicitly intended. Returns nil after Destroy.
func (b *Buffer) Bytes() []byte {
	if b == nil || b.destroyed.Load() || b.buf == nil {
		return nil
	}
	return b.buf.Bytes()
}

// Size returns the length of the underlying mlocked buffer. Returns 0
// after Destroy or on a zero-value Buffer.
func (b *Buffer) Size() int {
	if b == nil || b.destroyed.Load() || b.buf == nil {
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
