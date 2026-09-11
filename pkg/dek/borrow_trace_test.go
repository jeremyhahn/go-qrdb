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

package dek

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// The tests below are the EXECUTED half of go-qrdb#74's per-site callee trace.
// Reading a callback and concluding it allocates nothing is a claim about
// everything it can reach through interfaces this package does not own; the
// borrow guard turns that claim into a check that fails loudly instead of a
// daemon that stops at boot.

// Cluster DEK generation must not take a nested borrow.
//
// This is the trace for go-qrdb#74's converted dek sites, executed rather than
// read. fillFromEntropy holds the protected-memory borrow across
// io.ReadFull(crypto/rand.Reader, ...); if anything on that path allocated
// protected memory the daemon would hang here at boot, on the first
// Initialize. The guard turns that into a failure with both stacks.
//
// Initialize AND Rotate are driven, because they are separate call sites and
// only one of them is on the boot path.
func TestClusterDEK_GenerationTakesNoNestedBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	barrier := newMockEncryptionBarrier()
	mgr, err := NewClusterDEKManager(barrier, discardLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })

	require.NoError(t, mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32))
	require.NoError(t, mgr.Rotate(context.Background()))
}

// The DEK read path, converted on PR #82, must stay free of nested borrows
// too. It is re-checked here rather than assumed because withCurrentDEK's
// callback is supplied by WrapKey and UnwrapKey, and a later change to either
// would move the boundary without touching the borrow.
func TestClusterDEK_WrapUnwrapTakeNoNestedBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	barrier := newMockEncryptionBarrier()
	mgr, err := NewClusterDEKManager(barrier, discardLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.Initialize(context.Background(), AlgorithmAES256GCM, 32))

	wrapped, err := mgr.WrapKey([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	unwrapped, err := mgr.UnwrapKey(wrapped)
	require.NoError(t, err)
	require.Len(t, unwrapped, 32)
}

// The guard must catch the conversion this repository already knows is wrong.
//
// Holding a protected-memory borrow across m.barrier.Encrypt is the shape
// review blocked on PR #82 and go-qrdb#145 owns: m.barrier is an injected
// seal.EncryptionBarrier, any conforming implementation may allocate protected
// memory, and the allocation takes exclusively what the borrow holds shared on
// the same goroutine. Production does NOT do this — Initialize and Rotate pass
// dekBuf.Bytes() with no borrow held, deliberately — so the shape is built
// here, against the protectedAllocBarrier double that exists for it.
//
// Without the guard this function never returns. That is the whole claim, and
// it is why this test is the one that proves the detector is worth having: a
// detector that only ever passes is indistinguishable from no detector.
func TestBorrowGuard_CatchesTheBarrierConversionReviewBlocked(t *testing.T) {
	var caught atomic.Pointer[secure.NestedBorrowError]
	restore := secure.EnableBorrowGuard(func(e *secure.NestedBorrowError) {
		caught.CompareAndSwap(nil, e)
		runtime.Goexit()
	})
	defer restore()

	barrier := &protectedAllocBarrier{}
	dekBuf, err := secure.NewBuffer(32)
	require.NoError(t, err)
	defer dekBuf.Destroy()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = dekBuf.WithBytes(func(dek []byte) error {
			_, encErr := barrier.Encrypt(dek)
			return encErr
		})
	}()
	<-done

	e := caught.Load()
	require.NotNil(t, e, "the borrow held across barrier.Encrypt was not reported; "+
		"without the guard this call site hangs the daemon at boot")
	require.Equal(t, "NewBuffer", e.Op,
		"the report must name the allocation the barrier made inside the borrow")
	require.Contains(t, e.BorrowStack, "WithBytes",
		"the report must name the borrow that would have deadlocked")
}
