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

package statemachine

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
)

// updater is the Update method shape shared, with identical signatures, by
// sm.StateMachine, sm.ConcurrentStateMachine and sm.DiskStateMachine
// (verified against go-quicraft/pkg/sm/interfaces.go). A property test that
// only needs to apply entries can target this instead of picking the right
// one of the three per sm_type.
type updater interface {
	Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error
}

// opener is satisfied by sm.DiskStateMachine alone: a storage-backed state
// machine must be opened at a directory before Update is ever called.
type opener interface {
	Open(ctx context.Context, initialDir string, stopper <-chan struct{}) (uint64, error)
}

// closer is the Close(ctx) shape shared by all three sm.*StateMachine kinds.
// FastBackend writes through to storage asynchronously, off the apply
// goroutine (registry.go: "ordered asynchronous write-through"); its Close
// waits for every in-flight write to finish. Without calling it before the
// test's t.TempDir() cleanup runs, that cleanup can delete the directory out
// from under a write still in flight -- a test-harness race, not the
// production one this test is otherwise checking for.
type closer interface {
	Close(ctx context.Context) error
}

// randomNestedKey returns a byte key built from 1-4 random path segments
// joined by '/', covering the prefix relationships go-qrdb#592 was about:
// successive calls over the same rng frequently produce one key that is a
// '/' prefix of another, in either order of generation.
func randomNestedKey(rng *rand.Rand) []byte {
	segments := rng.Intn(4) + 1
	var b strings.Builder
	alphabet := "abc" // a small alphabet maximises the chance of two keys sharing a prefix
	for i := 0; i < segments; i++ {
		if i > 0 {
			b.WriteByte('/')
		}
		segLen := rng.Intn(3) + 1
		for j := 0; j < segLen; j++ {
			b.WriteByte(alphabet[rng.Intn(len(alphabet))])
		}
	}
	return []byte(b.String())
}

// runPrefixNestingProperty drives n random Put/Delete entries, one Update
// call per entry, through sm. Update returning a non-nil error is a failure:
// on every sm_type this package or pkg/store serves, Apply may fail ONLY on
// a genuine infrastructure fault, never as a deterministic consequence of
// which keys a well-formed client happened to choose (go-qrdb#592).
func runPrefixNestingProperty(t *testing.T, sm_ interface{}, seed int64, n int) {
	t.Helper()
	ctx := context.Background()
	if o, ok := sm_.(opener); ok {
		if _, err := o.Open(ctx, t.TempDir(), nil); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	u, ok := sm_.(updater)
	if !ok {
		t.Fatalf("%T implements neither sm.StateMachine, sm.ConcurrentStateMachine nor sm.DiskStateMachine's Update", sm_)
	}
	if c, ok := sm_.(closer); ok {
		t.Cleanup(func() {
			if err := c.Close(ctx); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}

	rng := rand.New(rand.NewSource(seed))
	for i := 0; i < n; i++ {
		key := randomNestedKey(rng)
		var cmd []byte
		var err error
		if rng.Intn(4) == 0 {
			cmd, err = kv.EncodeDelete(key)
		} else {
			cmd, err = kv.EncodePut(key, []byte(fmt.Sprintf("v%d", i)))
		}
		if err != nil {
			t.Fatalf("encode entry %d (key=%q): %v", i, key, err)
		}
		results := make([]sm.Result, 1)
		if err := u.Update(ctx, []sm.Entry{{Index: uint64(i + 1), Cmd: cmd}}, results); err != nil {
			t.Fatalf("Update halted Apply at entry %d (key=%q): %v -- this is the circuit-breaker trip go-qrdb#592 describes", i, key, err)
		}
	}
}

// TestPrefixNestingNeverHaltsApply_StorageGeneric runs the property above
// over every state machine this package registers that can compose
// raft.sm_storage, on every storage backend that composes it ("pebble", the
// default, and "file", where go-qrdb#592 was found) -- plus the two pure
// in-memory sm_types, which carry their own storage and so take no
// sm_storage.
func TestPrefixNestingNeverHaltsApply_StorageGeneric(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(t *testing.T) interface{}
	}{
		{"memory", func(t *testing.T) interface{} {
			fn, err := ResolveTyped("memory", "")
			if err != nil {
				t.Fatal(err)
			}
			return fn(1, 1)
		}},
		{"highperf", func(t *testing.T) interface{} {
			fn, err := ResolveTyped("highperf", "")
			if err != nil {
				t.Fatal(err)
			}
			return fn(1, 1)
		}},
		{"disk-over-pebble", func(t *testing.T) interface{} {
			fn, err := ResolveTypedWithStorage("disk", "", StorageConfig{Root: t.TempDir(), Kind: "pebble"})
			if err != nil {
				t.Fatal(err)
			}
			return fn(1, 1)
		}},
		{"disk-over-file", func(t *testing.T) interface{} {
			fn, err := ResolveTypedWithStorage("disk", "", StorageConfig{Root: t.TempDir(), Kind: "file"})
			if err != nil {
				t.Fatal(err)
			}
			return fn(1, 1)
		}},
		{"fast-over-file", func(t *testing.T) interface{} {
			fn, err := ResolveTypedWithStorage("fast", "", StorageConfig{Root: t.TempDir(), Kind: "file"})
			if err != nil {
				t.Fatal(err)
			}
			return fn(1, 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runPrefixNestingProperty(t, tc.build(t), 0xC0FFEE, 300)
		})
	}
}
