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

// Borrower is the borrow half of *Buffer.
//
// Taken as an interface for two reasons, both load-bearing. It lets CopyOut's
// empty-source branch be reached by a test — through a real *Buffer it cannot
// be, since NewBuffer and NewBufferFromBytes reject a zero length and WithBytes
// refuses a destroyed buffer without calling the callback at all. And it is the
// narrowest thing CopyOut needs, which keeps the accessor a caller must reach
// for down to the ONE that holds the borrow across the read.
type Borrower interface {
	WithBytes(fn func([]byte) error) error
}

// CopyOut copies protected bytes into a fresh caller-owned slice, holding the
// borrow across the whole copy.
//
// This is the canonical shape for the copy that a []byte-returning API forces,
// and it is remedy 1 of go-qrdb#74: it narrows the window rather than closing
// it, because the copy itself is unavoidable where the caller's signature says
// []byte. Callers that can take a *Buffer or a *Secret should do that instead
// and never reach this function.
//
// Two properties are the reason this is one function rather than five copies of
// four lines, and both were found in review rather than designed in:
//
// The destination is allocated INSIDE the callback from len(src), never from a
// Size call beforehand. Size takes and releases its own borrow, so sizing first
// leaves a gap before the copy's borrow is taken; a concurrent destroy (a
// shutdown enclave.WipeAll) landing in that gap yields a correctly sized
// destination and an empty source, the copy becomes a no-op, and the caller
// receives a run of zeros it has no way to tell from the real material.
//
// An empty source is therefore REFUSED (*EmptyBorrowError) rather than copied.
// A caller that acts on the result — spending a one-time recovery share,
// unsealing with a key — must not act on zeros.
//
// fn-free by construction: the callback allocates ordinary heap memory and
// calls copy, and reaches nothing in this package, so it cannot reenter the
// buffer it is borrowing (see "Nesting" in the package doc).
func CopyOut(b Borrower) ([]byte, error) {
	if b == nil {
		return nil, &NilBufferError{}
	}
	var out []byte
	if err := b.WithBytes(func(src []byte) error {
		if len(src) == 0 {
			return &EmptyBorrowError{}
		}
		out = make([]byte, len(src))
		copy(out, src)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// Compile-time assertion that the type this interface was extracted from still
// satisfies it. Placed here, beside the interface, so an accidental change to
// WithBytes fails at the definition rather than at some call site elsewhere.
var _ Borrower = (*Buffer)(nil)
