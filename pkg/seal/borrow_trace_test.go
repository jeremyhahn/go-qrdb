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

// The recovery strategy's root-key read must not reenter the buffer it borrows.
//
// UnsealRootKey returns the root key as a caller-owned []byte (the
// SealingStrategy contract), so it copies out of the protected buffer. Close
// destroys that buffer, and a Destroy of the buffer from inside its own borrow
// deadlocks (go-qrdb#145) — driven here under the armed guard so a Close moved
// into the read fails rather than wedging every recovery.
func TestRecoveryStrategyUnsealRootKey_TakesNoNestedBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	rootKey := bytes.Repeat([]byte{0x5a}, 32)
	buf, err := secure.NewBufferFromBytes(bytes.Clone(rootKey))
	if err != nil {
		t.Fatalf("NewBufferFromBytes: %v", err)
	}
	strategy, err := newRecoveryStrategyFromBuffer(context.Background(), buf, nil)
	if err != nil {
		t.Fatalf("newRecoveryStrategyFromBuffer: %v", err)
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
