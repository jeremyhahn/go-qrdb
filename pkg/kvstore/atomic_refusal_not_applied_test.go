// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kvstore

import (
	"errors"
	"testing"
)

// A capability refusal is refused before anything can commit, so it PROVES
// nothing was written: the canonical marker matches ErrNotApplied, exactly as
// GuardedBatchUnsupportedError already did. A caller branching on
// ErrNotApplied therefore stops reconciling a write that cannot appear,
// whichever form the refusal took.
func TestErrAtomicWriteUnsupported_IsNotApplied(t *testing.T) {
	if !errors.Is(ErrAtomicWriteUnsupported, ErrNotApplied) {
		t.Fatal("ErrAtomicWriteUnsupported does not match ErrNotApplied")
	}
	if !errors.Is(ErrAtomicWriteUnsupported, ErrAtomicWriteUnsupported) {
		t.Fatal("the marker must still match itself")
	}
	if errors.Is(ErrNotApplied, ErrAtomicWriteUnsupported) {
		t.Fatal("the relation is one way: not every not-applied write is a missing capability")
	}
	if got, want := ErrAtomicWriteUnsupported.Error(), "kvstore: store does not support atomic conditional writes"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
