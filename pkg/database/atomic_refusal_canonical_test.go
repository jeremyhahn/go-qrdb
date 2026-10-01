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

package database

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TestBarrierBackend_AtomicRefusalIsTheCanonicalMarker: a barrier backend over
// an inner backend with no atomic primitives refuses every guarded write with
// kvstore.ErrAtomicWriteUnsupported — the stack's ONE marker for "this store
// cannot write atomically" — so a caller tests one error however many layers
// the refusal crossed.
func TestBarrierBackend_AtomicRefusalIsTheCanonicalMarker(t *testing.T) {
	ctx := context.Background()
	b := NewBarrierBackend(NewRemoteBackend(nil), &xorBarrier{mask: 0x5A})
	if _, ok := any(NewRemoteBackend(nil)).(AtomicBackend); ok {
		t.Fatal("precondition: RemoteBackend must not offer atomic writes")
	}

	calls := map[string]func() (bool, error){
		"PutIfAbsent":      func() (bool, error) { return b.PutIfAbsent(ctx, []byte("k"), []byte("v")) },
		"CompareAndSwap":   func() (bool, error) { return b.CompareAndSwap(ctx, []byte("k"), []byte("a"), []byte("b")) },
		"CompareAndDelete": func() (bool, error) { return b.CompareAndDelete(ctx, []byte("k"), []byte("a")) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			ok, err := call()
			if ok {
				t.Fatal("a refused guarded write reported success")
			}
			if !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
				t.Fatalf("err = %v, want it to match kvstore.ErrAtomicWriteUnsupported", err)
			}
		})
	}
}
