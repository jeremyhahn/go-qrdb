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
	"bytes"
	"errors"
	"testing"
	"unsafe"
)

// stubBorrower drives CopyOut's branches that a real *Buffer cannot reach.
type stubBorrower struct {
	src []byte
	err error
}

func (s *stubBorrower) WithBytes(fn func([]byte) error) error {
	if s.err != nil {
		return s.err
	}
	return fn(s.src)
}

var _ Borrower = (*stubBorrower)(nil)

// The ordinary path: the material comes back intact, in a slice the caller
// owns and can wipe.
func TestCopyOut_ReturnsTheMaterial(t *testing.T) {
	const material = "0123456789abcdef"
	buf, err := NewBufferFromBytes([]byte(material))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	out, err := CopyOut(buf)
	if err != nil {
		t.Fatalf("CopyOut: %v", err)
	}
	if !bytes.Equal(out, []byte(material)) {
		t.Error("CopyOut returned different bytes than the buffer holds")
	}
	if len(out) != len(material) {
		t.Errorf("len = %d, want %d", len(out), len(material))
	}
}

// The result must be a COPY, not a view onto the protected pages: the caller
// is expected to hold and wipe it after the buffer is destroyed.
func TestCopyOut_DoesNotAliasProtectedMemory(t *testing.T) {
	buf, err := NewBufferFromBytes([]byte("aliasing would be a bug"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	out, err := CopyOut(buf)
	if err != nil {
		t.Fatalf("CopyOut: %v", err)
	}
	var protected uintptr
	if err := buf.WithBytes(func(b []byte) error {
		// #nosec G103 -- address comparison only; the pointer is never dereferenced.
		protected = uintptr(unsafe.Pointer(&b[0])) //nolint:gosec // address comparison only; the pointer is never dereferenced
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	// #nosec G103 -- address comparison only; the pointer is never dereferenced.
	if uintptr(unsafe.Pointer(&out[0])) == protected { //nolint:gosec // address comparison only
		t.Fatal("CopyOut returned a view onto the protected pages rather than a copy")
	}
}

// An empty borrow must be REFUSED. A correctly sized run of zeros is
// indistinguishable from real material to the caller, and callers of this
// function spend one-time recovery shares and unseal barriers on the result.
func TestCopyOut_RefusesAnEmptyBorrow(t *testing.T) {
	out, err := CopyOut(&stubBorrower{src: nil})
	if out != nil {
		t.Errorf("CopyOut returned %d bytes for an empty borrow", len(out))
	}
	var empty *EmptyBorrowError
	if !errors.As(err, &empty) {
		t.Fatalf("err = %v (%T), want *EmptyBorrowError", err, err)
	}
	if _, err := CopyOut(&stubBorrower{src: []byte{}}); !errors.As(err, &empty) {
		t.Fatalf("zero-length (non-nil) source: err = %v, want *EmptyBorrowError", err)
	}
}

// A borrow that fails is propagated unchanged, so a caller keeps whatever
// contract the underlying accessor promised.
func TestCopyOut_PropagatesTheBorrowError(t *testing.T) {
	sentinel := &NilBufferError{}
	out, err := CopyOut(&stubBorrower{err: sentinel})
	if out != nil {
		t.Errorf("CopyOut returned %d bytes on a failed borrow", len(out))
	}
	if !errors.Is(err, error(sentinel)) {
		t.Fatalf("err = %v, want the borrow's own error", err)
	}
}

// A destroyed buffer must report *NilBufferError rather than an empty copy,
// and a nil Borrower must not panic.
func TestCopyOut_RejectsNilAndDestroyed(t *testing.T) {
	if _, err := CopyOut(nil); err == nil {
		t.Fatal("CopyOut(nil) returned no error")
	}
	buf, err := NewBuffer(16)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	buf.Destroy()
	var nilBuf *NilBufferError
	if _, err := CopyOut(buf); !errors.As(err, &nilBuf) {
		t.Fatalf("destroyed buffer: err = %v, want *NilBufferError", err)
	}
}

// CopyOut's callback must not reach anything in this package, or it would
// deadlock against the borrow it holds. Armed, the guard proves that for the
// implementation as written — which is the check that stops a later "small
// improvement" inside the callback from wedging every caller.
func TestCopyOut_CallbackTakesNoNestedBorrow(t *testing.T) {
	buf, err := NewBufferFromBytes([]byte("checked under the guard"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer buf.Destroy()

	restore := EnableBorrowGuard(func(e *NestedBorrowError) {
		t.Errorf("CopyOut's callback reached %s inside its own borrow", e.Op)
	})
	defer restore()

	if _, err := CopyOut(buf); err != nil {
		t.Fatalf("CopyOut: %v", err)
	}
}
