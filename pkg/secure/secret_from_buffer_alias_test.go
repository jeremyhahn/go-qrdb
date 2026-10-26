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
	"context"
	"testing"
	"unsafe"

	"github.com/jeremyhahn/go-enclave"
)

// TestNewSecretFromBuffer_SealsTheBuffersOwnPages pins go-qrdb#145 shape 1 on
// the sealing path pkg/secret's NewSecretValueFromBuffer (and through it the
// credential resolver) relies on: the plaintext handed to the sealer IS buf's
// locked pages, not a heap copy taken on the way in. The address the sealer
// receives is compared with the address of buf's own borrow; a transient
// make-and-copy before newSealed changes it and fails here.
func TestNewSecretFromBuffer_SealsTheBuffersOwnPages(t *testing.T) {
	want := []byte("sealed straight out of locked pages")
	buf, err := NewBufferFromBytes(append([]byte(nil), want...))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	var bufAddr *byte
	if err := buf.WithBytes(func(b []byte) error {
		bufAddr = unsafe.SliceData(b) // #nosec G103 -- address comparison only; nothing is read through it.
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}

	var sealedAddr *byte
	prevNewSealed := newSealed
	newSealed = func(ctx context.Context, src []byte) (enclave.Sealed, error) {
		sealedAddr = unsafe.SliceData(src) // #nosec G103 -- address comparison only; nothing is read through it.
		return prevNewSealed(ctx, src)
	}
	defer func() { newSealed = prevNewSealed }()

	defer GuardBorrows(t.Fatalf)()
	sec, err := NewSecretFromBuffer(context.Background(), buf)
	if err != nil {
		t.Fatalf("NewSecretFromBuffer: %v", err)
	}
	defer sec.Destroy()

	if sealedAddr == nil {
		t.Fatal("the sealer was never called")
	}
	if sealedAddr != bufAddr {
		t.Fatalf("the sealer received %p, not buf's locked pages at %p: the plaintext "+
			"crossed the heap on its way into the seal", sealedAddr, bufAddr)
	}
	opened, err := sec.Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer opened.Destroy()
	if got := contents(opened); !bytes.Equal(got, want) {
		t.Fatalf("sealed content = %q, want %q", got, want)
	}
}

// TestNewSecretFromBuffer_SealerMayUseProtectedMemory is the tolerance half:
// the sealer runs inside buf's borrow, and a sealer that allocates, opens and
// destroys protected memory of its own there must complete with the armed
// guard silent, because none of it reenters buf.
func TestNewSecretFromBuffer_SealerMayUseProtectedMemory(t *testing.T) {
	buf, err := NewBufferFromBytes([]byte("sealed beside other protected memory"))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	// Sealed before the seam is swapped: NewSecret seals through newSealed too,
	// and must not recurse into the wrapper below.
	inner, err := NewSecret(context.Background(), []byte("the sealer's own secret"))
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	defer inner.Destroy()

	prevNewSealed := newSealed
	newSealed = func(ctx context.Context, src []byte) (enclave.Sealed, error) {
		scratch, err := NewBuffer(len(src))
		if err != nil {
			return nil, err
		}
		defer scratch.Destroy()
		opened, err := inner.Open(ctx)
		if err != nil {
			return nil, err
		}
		opened.Destroy()
		return prevNewSealed(ctx, src)
	}
	defer func() { newSealed = prevNewSealed }()

	defer GuardBorrows(t.Fatalf)()
	sec, err := NewSecretFromBuffer(context.Background(), buf)
	if err != nil {
		t.Fatalf("NewSecretFromBuffer: %v", err)
	}
	sec.Destroy()
}
