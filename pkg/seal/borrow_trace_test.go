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

	"github.com/jeremyhahn/go-enclave"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

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

// copyEnclaveToBuffer now nests BOTH enclaves' borrows for the whole copy —
// dst's via its own WithBytes, not dst.Bytes() — and this pins why that is
// both correct and required (go-qrdb#114 review, round 4).
//
// Under go-quicraft pre-v0.11.0-alpha, before it consumed
// github.com/jeremyhahn/go-enclave directly, nesting a second borrow this way
// was forbidden in terms by that package's own WithBytes documentation,
// because the two packages held SEPARATE process-wide purge locks and Go's
// RWMutex admits no new reader once a writer is queued elsewhere — a
// deadlock, not a data race. go-enclave has no such package-wide lock: every
// enclave, in pkg/secure and in go-quicraft's pkg/crypto/shamir alike, carries
// its own independent per-instance lock, so nesting the two borrows here
// cannot deadlock either against the other.
//
// Not nesting them was worse than merely legal-but-unnecessary, though: dst is
// freshly allocated and registered on go-enclave's shutdown ledger the moment
// secure.NewBuffer returns, so a concurrent enclave.WipeAll needs no external
// reference to dst to destroy it — reading it via the unguarded dst.Bytes()
// this test used to exercise could fault against pages WipeAll had already
// unmapped mid-copy, the identical class of bug SecretValue.WithBytes's own
// round-3 fix closed. This test exercises the real function directly, rather
// than a hand-rolled stand-in for its old shape, and TestCopyEnclaveToBuffer_
// SurvivesConcurrentWipeAll below proves the race is actually closed.
func TestCopyEnclaveToBuffer_AllocatesOutsideEveryBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	want := bytes.Repeat([]byte{0x11}, 32)
	enc, err := enclave.NewMemoryEnclaveCopy(want)
	if err != nil {
		t.Fatalf("enclave.NewMemoryEnclaveCopy: %v", err)
	}
	defer enc.Destroy()

	dst, err := copyEnclaveToBuffer(enc)
	if err != nil {
		t.Fatalf("copyEnclaveToBuffer: %v", err)
	}
	defer dst.Destroy()

	if !bytes.Equal(dst.Bytes(), want) {
		t.Error("the copy did not land in the destination")
	}
}
