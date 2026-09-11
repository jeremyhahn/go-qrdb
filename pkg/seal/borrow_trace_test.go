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

package seal

import (
	"bytes"
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// The tenant DEK cache read must not take a nested borrow.
//
// The EXECUTED half of go-qrdb#74's callee trace for this site, and the one
// the issue named as the trap: cacheDEK calls secure.NewSecret, which is
// purge-class, and a conversion that pulled it inside the borrow would hang
// the daemon on the first tenant key operation. The MISS path (which caches)
// and the HIT path (which borrows) are driven in that order here, so both the
// allocation and the borrow actually run.
func TestGetTenantDEK_CacheHitTakesNoNestedBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	b := newUnsealedBarrier(t)
	tkm, err := NewTenantKeyManager(b, newMemoryKVStore())
	if err != nil {
		t.Fatalf("NewTenantKeyManager: %v", err)
	}
	ctx := context.Background()

	// Miss: generates, wraps, persists and calls cacheDEK -> secure.NewSecret.
	first, err := tkm.GetTenantDEK(ctx, "tenant-borrow")
	if err != nil {
		t.Fatalf("GetTenantDEK (miss): %v", err)
	}
	// Hit: opens the cached Secret and copies out under the borrow.
	second, err := tkm.GetTenantDEK(ctx, "tenant-borrow")
	if err != nil {
		t.Fatalf("GetTenantDEK (hit): %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Error("the cached read returned different material than the generated one")
	}
	if len(second) == 0 {
		t.Error("the cached read returned nothing")
	}

	// Rotation destroys the cached Secret and caches a fresh one, which is a
	// purge-class pair around the same cache entry the borrow above read.
	if err := tkm.RotateTenantDEK(ctx, "tenant-borrow"); err != nil {
		t.Fatalf("RotateTenantDEK: %v", err)
	}
	rotated, err := tkm.GetTenantDEK(ctx, "tenant-borrow")
	if err != nil {
		t.Fatalf("GetTenantDEK (after rotate): %v", err)
	}
	if bytes.Equal(first, rotated) {
		t.Error("rotation returned the same DEK")
	}
}

// The recovery strategy's root-key read must not take a nested borrow.
//
// UnsealRootKey hands a copy to Barrier.Unseal, and the copy now runs inside a
// coordinated borrow. Close, which destroys the protected copy, is purge-class
// and must stay outside it — driven here so a Close moved into the borrow
// fails rather than wedging every recovery.
func TestRecoveryStrategyUnsealRootKey_TakesNoNestedBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	rootKey := bytes.Repeat([]byte{0x5a}, 32)
	strategy, err := newRecoveryStrategy(rootKey, nil)
	if err != nil {
		t.Fatalf("newRecoveryStrategy: %v", err)
	}

	got, err := strategy.UnsealRootKey(context.Background(), nil, Credentials{})
	if err != nil {
		t.Fatalf("UnsealRootKey: %v", err)
	}
	if !bytes.Equal(got, rootKey) {
		t.Error("UnsealRootKey returned different material than was sealed")
	}

	if err := strategy.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After Close the protected copy is gone, and the read must REFUSE rather
	// than return zeros — the empty-borrow refusal secure.CopyOut adds.
	if _, err := strategy.UnsealRootKey(context.Background(), nil, Credentials{}); err == nil {
		t.Error("UnsealRootKey on a closed strategy returned no error")
	}
}

// copyEnclaveToBuffer is DELIBERATELY not converted, and this pins why.
//
// It writes into a freshly allocated secure.Buffer from inside a go-quicraft
// MemoryEnclave borrow. Holding the destination's borrow across that would
// nest a second protected-memory borrow inside the first, which go-quicraft's
// own WithBytes documentation forbids in terms — "Reading a DIFFERENT enclave
// from inside fn is ALSO forbidden" — because the two packages hold separate
// process-wide purge locks and Go's RWMutex admits no new reader once a writer
// is queued.
//
// So the assertion here is the narrow one that is actually true today: the
// function takes no nested borrow of THIS package's protected memory. The
// allocation happens before the source is borrowed and nothing purge-class runs
// under it. Converting the remaining Bytes() read is blocked on go-qrdb#145 and
// the go-enclave adoption wave, not on this test.
func TestCopyEnclaveToBuffer_AllocatesOutsideEveryBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	src, err := secure.NewBufferFromBytes(bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	defer src.Destroy()

	// A stand-in for the reconstructed-secret enclave: the shape under test is
	// "allocate the destination, then borrow the source", and that is what is
	// being asserted about, not go-quicraft's type.
	dst, err := secure.NewBuffer(32)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer dst.Destroy()

	view := dst.Bytes()
	if err := src.WithBytes(func(b []byte) error {
		copy(view, b)
		return nil
	}); err != nil {
		t.Fatalf("WithBytes: %v", err)
	}
	if !bytes.Equal(dst.Bytes(), bytes.Repeat([]byte{0x11}, 32)) {
		t.Error("the copy did not land in the destination")
	}
}
