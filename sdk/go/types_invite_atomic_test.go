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

package sdk_test

import (
	"errors"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// TestSDK_InviteAtomicRefusalIsTheCanonicalMarker: the invitation store's
// refusal of a non-atomic backend is the one atomic-refusal marker, not a
// package-local copy — sdk.ErrInviteAtomicWriteUnsupported IS
// sdk.ErrKVAtomicWriteUnsupported, and it proves nothing was written.
func TestSDK_InviteAtomicRefusalIsTheCanonicalMarker(t *testing.T) {
	if sdk.ErrInviteAtomicWriteUnsupported != sdk.ErrKVAtomicWriteUnsupported {
		t.Fatalf("ErrInviteAtomicWriteUnsupported (%v) is not ErrKVAtomicWriteUnsupported", sdk.ErrInviteAtomicWriteUnsupported)
	}

	_, err := sdk.NewInviteStore(nonAtomicKVStore{sdk.NewMemoryKVStore()})
	if !errors.Is(err, sdk.ErrKVAtomicWriteUnsupported) {
		t.Fatalf("NewInviteStore(non-atomic) err = %v, want ErrKVAtomicWriteUnsupported", err)
	}
	if !errors.Is(err, sdk.ErrInviteAtomicWriteUnsupported) {
		t.Fatalf("NewInviteStore(non-atomic) err = %v, want ErrInviteAtomicWriteUnsupported", err)
	}
	if !errors.Is(err, sdk.ErrKVNotApplied) {
		t.Fatalf("NewInviteStore(non-atomic) err = %v, want it to match ErrKVNotApplied", err)
	}
}
