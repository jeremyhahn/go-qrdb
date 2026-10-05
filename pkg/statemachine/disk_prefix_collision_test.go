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
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	filestore "github.com/jeremyhahn/go-qrdb/pkg/storage/file"
)

// TestDiskAdapter_PrefixKeysNeverHaltApply reproduces go-qrdb#592 at the
// state-machine level: on the disk state machine over sm_storage: file, a Put
// of "dir/child" followed by a Put of "dir" used to come back from Apply as a
// Go error (a rename failure in the file backend), which go-quicraft treats
// as fatal -- it trips the shard's circuit breaker and stalls every later
// linearizable read (see ResolveApplyError and go-qrdb#129/#520 for the
// general shape of that failure mode). Both orders must now apply cleanly:
// Update returns nil, and both keys read back their own value afterwards.
func TestDiskAdapter_PrefixKeysNeverHaltApply(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second string
	}{
		{"child then parent", "dir/child", "dir"},
		{"parent then child", "dir", "dir/child"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stg, err := filestore.New(t.TempDir())
			if err != nil {
				t.Fatalf("file.New: %v", err)
			}
			defer func() { _ = stg.Close() }()

			a := newDiskAdapter(stg)
			ctx := context.Background()

			cmd1, err := kv.EncodePut([]byte(tc.first), []byte("v1"))
			if err != nil {
				t.Fatalf("EncodePut(%q): %v", tc.first, err)
			}
			cmd2, err := kv.EncodePut([]byte(tc.second), []byte("v2"))
			if err != nil {
				t.Fatalf("EncodePut(%q): %v", tc.second, err)
			}

			results := make([]sm.Result, 2)
			if err := a.Update(ctx, []sm.Entry{
				{Index: 1, Cmd: cmd1},
				{Index: 2, Cmd: cmd2},
			}, results); err != nil {
				t.Fatalf("Update halted Apply (this is the circuit-breaker trip): %v", err)
			}

			got1, err := a.Lookup(ctx, []byte(tc.first))
			if err != nil {
				t.Fatalf("Lookup(%q): %v", tc.first, err)
			}
			if string(got1.([]byte)) != "v1" {
				t.Fatalf("Lookup(%q) = %q, want %q", tc.first, got1, "v1")
			}
			got2, err := a.Lookup(ctx, []byte(tc.second))
			if err != nil {
				t.Fatalf("Lookup(%q): %v", tc.second, err)
			}
			if string(got2.([]byte)) != "v2" {
				t.Fatalf("Lookup(%q) = %q, want %q", tc.second, got2, "v2")
			}

			// The shard must still be able to apply further entries -- the
			// property an actual breaker trip would have cost every later
			// proposal on the shard.
			cmd3, err := kv.EncodePut([]byte("unrelated"), []byte("v3"))
			if err != nil {
				t.Fatalf("EncodePut(unrelated): %v", err)
			}
			results3 := make([]sm.Result, 1)
			if err := a.Update(ctx, []sm.Entry{{Index: 3, Cmd: cmd3}}, results3); err != nil {
				t.Fatalf("Update after the prefix pair: %v", err)
			}
		})
	}
}

// An empty-key batch on the disk-over-file state machine is already covered
// end to end by TestInvalidKeysAreRefusedNotFatal (native_txn_test.go,
// go-qrdb#136): its "disk-over-file" machine runs real file.Engine storage,
// and its "batch empty key" case proves Update never returns a Go error for
// one. That is the other half of go-qrdb#592's report -- not reproduced
// again here, since it already has a regression test pinning it green.
