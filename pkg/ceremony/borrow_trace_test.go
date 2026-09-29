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

package ceremony

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// Claiming a share must not take a nested borrow.
//
// The share path was converted ahead of go-qrdb#74's remaining sites and its
// copy now runs through secure.CopyOut, so the trace is re-executed here rather
// than inherited: Claim destroys the Secret and the transient buffer either
// side of the borrow, both purge-class, and a Destroy that drifted inside would
// deadlock every custodian claim.
func TestShareStoreClaim_TakesNoNestedBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	store := newSealedShareStore(DefaultShareTTL)
	const share = `{"index":1,"value":"abc123"}`
	if err := store.Store("officer-1", []byte(share)); err != nil {
		t.Fatalf("Store: %v", err)
	}

	out, err := store.Claim("officer-1")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if string(out) != share {
		t.Errorf("Claim returned %q, want %q", out, share)
	}

	// A second claim must refuse, and the refusal path destroys nothing but
	// still runs the same lookup — checked so the guard covers both outcomes.
	if _, err := store.Claim("officer-1"); err == nil {
		t.Error("the second Claim returned no error")
	}
}

// Storing a share allocates protected memory and must do so outside every
// borrow, which is the ordering the whole subsystem depends on.
func TestShareStoreStore_TakesNoNestedBorrow(t *testing.T) {
	defer secure.GuardBorrows(t.Fatalf)()

	store := newSealedShareStore(DefaultShareTTL)
	for _, officer := range []string{"officer-1", "officer-2", "officer-3"} {
		if err := store.Store(officer, []byte(`{"index":1,"value":"abc"}`)); err != nil {
			t.Fatalf("Store(%s): %v", officer, err)
		}
	}
	// Reset destroys every Secret it holds — purge-class, in a loop.
	store.Reset()
}
