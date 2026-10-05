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

package store_test

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/store"
)

// randomNestedKey returns a byte key built from 1-4 random path segments
// joined by '/'. A small alphabet maximises the chance that two generated
// keys share a '/' prefix relationship with each other -- the shape
// go-qrdb#592 was about.
func randomNestedKey(rng *rand.Rand) []byte {
	segments := rng.Intn(4) + 1
	var b strings.Builder
	alphabet := "abc"
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

// TestPrefixNestingNeverHaltsApply_PebbleAndMVCC is pkg/store's half of
// go-qrdb#592's property requirement: the pebble and mvcc Raft state
// machines always carry their own PebbleDB storage regardless of
// raft.sm_storage (ResolveTypedWithStorage refuses a non-pebble kind for
// them), so a key and any key for which it is a '/' prefix are just two
// ordinary, independent byte-string keys to PebbleDB -- there is no
// filesystem path derived from either. This pins that property never
// regresses: Update must return nil for every entry, on both state
// machines, over the same random Put/Delete sequences that exercise the
// file-backend collision directly in pkg/storage/file and pkg/statemachine.
func TestPrefixNestingNeverHaltsApply_PebbleAndMVCC(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		open func(t *testing.T) interface {
			Open(ctx context.Context, initialDir string, stopper <-chan struct{}) (uint64, error)
			Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error
		}
	}{
		{"pebble", func(t *testing.T) interface {
			Open(ctx context.Context, initialDir string, stopper <-chan struct{}) (uint64, error)
			Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error
		} {
			return store.NewPebbleStateMachine(nil)
		}},
		{"mvcc", func(t *testing.T) interface {
			Open(ctx context.Context, initialDir string, stopper <-chan struct{}) (uint64, error)
			Update(ctx context.Context, entries []sm.Entry, results []sm.Result) error
		} {
			return store.NewMVCCStateMachine(nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.open(t)
			if _, err := s.Open(ctx, t.TempDir(), nil); err != nil {
				t.Fatalf("Open: %v", err)
			}

			rng := rand.New(rand.NewSource(0xC0FFEE))
			for i := 0; i < 300; i++ {
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
				if err := s.Update(ctx, []sm.Entry{{Index: uint64(i + 1), Cmd: cmd}}, results); err != nil {
					t.Fatalf("Update halted Apply at entry %d (key=%q): %v", i, key, err)
				}
			}
		})
	}
}
